package dockwebhook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Limits of a template and of what it yields.
const (
	// MaxTemplateField bounds one template string.
	MaxTemplateField = 4096
	// MaxTags bounds the extra chat handles a template may yield.
	MaxTags = 10
)

// DefaultChat is the conversation of a dock whose template names none.
const DefaultChat = "webhook"

// DefaultSender is the sender of a dock whose template names none.
const DefaultSender = "webhook"

// Template maps a webhook's JSON body to the fields of a message. Every
// field is a Go text/template over the body (`.` is the object; `.headers`,
// `.raw` and `.body` are added); an empty field uses its default.
type Template struct {
	// ChatID names the conversation (default "webhook": one per dock).
	ChatID string `json:"chat_id,omitempty"`
	// ChatTitle is its title (default: the chat id).
	ChatTitle string `json:"chat_title,omitempty"`
	// ExternalID dedups provider retries (default: hash of the body).
	ExternalID string `json:"external_id,omitempty"`
	// SenderID / SenderName say who sent it (default "webhook" / dock name).
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	// Text is the message (default: the body as a fenced JSON block).
	Text string `json:"text,omitempty"`
	// Tags are comma separated extra handles customs rules can match.
	Tags string `json:"tags,omitempty"`
	// Agent names the agent to call (the text is prefixed with @name).
	Agent string `json:"agent,omitempty"`
}

// fields lists the template's strings by name, in a fixed order.
func (t Template) fields() []namedField {
	return []namedField{
		{"chat_id", t.ChatID}, {"chat_title", t.ChatTitle}, {"external_id", t.ExternalID},
		{"sender_id", t.SenderID}, {"sender_name", t.SenderName}, {"text", t.Text},
		{"tags", t.Tags}, {"agent", t.Agent},
	}
}

type namedField struct{ name, src string }

// Program is a Template parsed.
type Program struct {
	parsed map[string]*template.Template
}

// Compile parses every field; a bad one is an InvalidField on
// "template.<field>".
func (t Template) Compile() (*Program, error) {
	p := &Program{parsed: map[string]*template.Template{}}
	for _, f := range t.fields() {
		if err := text.Optional("template."+f.name, f.src, MaxTemplateField); err != nil {
			return nil, err
		}
		if strings.TrimSpace(f.src) == "" {
			continue
		}
		tpl, err := template.New(f.name).Funcs(funcs).Option("missingkey=zero").Parse(f.src)
		if err != nil {
			return nil, errx.InvalidField("template."+f.name, "template."+f.name+": "+firstLine(err.Error()))
		}
		p.parsed[f.name] = tpl
	}
	return p, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// Input is one event to render: Body is the decoded JSON element (numbers
// as json.Number), Raw its bytes.
type Input struct {
	DockName string
	Body     any
	Raw      []byte
	// Headers are the request's safe headers, lowercased.
	Headers map[string]string
}

// Message is a rendered event, every field already clean and within the
// limits the ship domain checks.
type Message struct {
	ChatID     string   `json:"chat_id"`
	ChatTitle  string   `json:"chat_title"`
	ExternalID string   `json:"external_id"`
	SenderID   string   `json:"sender_id"`
	SenderName string   `json:"sender_name"`
	Text       string   `json:"text"`
	Tags       []string `json:"tags,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	// Errors: fields whose template failed to run (their default was used).
	Errors map[string]string `json:"errors,omitempty"`
}

// root is what templates see: the body's own fields (when it is an
// object) plus headers, raw and body.
func (in Input) root() map[string]any {
	root := map[string]any{}
	if obj, ok := in.Body.(map[string]any); ok {
		for k, v := range obj {
			root[k] = v
		}
	} else {
		root["value"] = in.Body
	}
	h := make(map[string]any, len(in.Headers))
	for k, v := range in.Headers {
		h[k] = v
	}
	root["headers"], root["raw"], root["body"] = h, string(in.Raw), in.Body
	return root
}

// Render runs the program over one event. A field that fails to run (or
// yields nothing) takes its default: a message is never dropped for its
// template.
func (p *Program) Render(in Input) Message {
	root := in.root()
	var m Message
	run := func(name string) string {
		tpl := p.parsed[name]
		if tpl == nil {
			return ""
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, root); err != nil {
			if m.Errors == nil {
				m.Errors = map[string]string{}
			}
			m.Errors[name] = firstLine(err.Error())
			return ""
		}
		return strings.TrimSpace(strings.ReplaceAll(buf.String(), "<no value>", ""))
	}
	pick := func(name, def string, max int) string {
		v := clip(text.StripControl(run(name)), max)
		if strings.TrimSpace(v) == "" {
			return clip(text.StripControl(def), max)
		}
		return strings.TrimSpace(v)
	}
	m.ChatID = pick("chat_id", DefaultChat, conversation.MaxExternalID)
	m.ChatTitle = pick("chat_title", m.ChatID, text.MaxName)
	sum := sha256.Sum256(in.Raw)
	m.ExternalID = pick("external_id", "hook-"+hex.EncodeToString(sum[:]), conversation.MaxExternalID)
	m.SenderID = pick("sender_id", DefaultSender, ship.MaxSenderID)
	m.SenderName = pick("sender_name", in.DockName, text.MaxName)
	m.Text = clip(pick("text", defaultText(in.Raw), text.MaxMessage), text.MaxMessage)
	seen := map[string]bool{m.ChatID: true}
	for _, tag := range strings.Split(clip(text.StripControl(run("tags")), 4096), ",") {
		tag = clip(strings.TrimSpace(tag), conversation.MaxExternalID)
		if tag != "" && !seen[tag] && len(m.Tags) < MaxTags {
			seen[tag] = true
			m.Tags = append(m.Tags, tag)
		}
	}
	m.Agent = handle(run("agent"))
	if m.Agent != "" {
		m.Text = clip("@"+m.Agent+" "+m.Text, text.MaxMessage)
	}
	return m
}

// defaultText is the body as a fenced, indented JSON block, cut to fit a
// message.
func defaultText(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		buf.Reset()
		buf.Write(raw)
	}
	const fence = "```"
	room := text.MaxMessage - 2*len(fence) - 2 - 16 // fences, newlines, a margin
	return fence + "json\n" + clip(buf.String(), room) + "\n" + fence
}

// handle turns what a template yielded into an agent handle: lowercase
// letters, digits, "_" and "-" (the characters of an @mention).
func handle(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			break
		}
	}
	return b.String()
}

// clip cuts s to at most max runes.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// funcs is the template function map: a few helpers, no I/O.
var funcs = template.FuncMap{
	// json re-serializes a value (a sub-object) as compact JSON.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	// join: {{join ", " .tags}}
	"join": func(sep string, v any) string {
		rv := reflect.ValueOf(v)
		if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
			return str(v)
		}
		parts := make([]string, rv.Len())
		for i := range parts {
			parts[i] = str(rv.Index(i).Interface())
		}
		return strings.Join(parts, sep)
	},
	"lower": func(v any) string { return strings.ToLower(str(v)) },
	// trunc keeps the first n characters: {{trunc 80 .title}}
	"trunc": func(n int, v any) string {
		if n < 0 {
			n = 0
		}
		return clip(str(v), n)
	},
	// default: {{default "unknown" .user}} or {{.user | default "unknown"}}
	"default": func(def any, v ...any) any {
		if len(v) == 0 || empty(v[len(v)-1]) {
			return def
		}
		return v[len(v)-1]
	},
}

// str is a value as text: maps and lists as JSON, nil as "".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func empty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	}
	if n, ok := v.(json.Number); ok {
		f, err := n.Float64()
		return err == nil && f == 0
	}
	return rv.IsZero()
}
