package dockwebhook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
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
	// MaxOutput bounds what one field may write before it counts as failed.
	MaxOutput = 64 << 10
	// MaxRanges bounds the {{range}} actions of one field (never nested).
	MaxRanges = 3
)

// fieldBudget is the time one field may run. A var so tests can shorten it.
var fieldBudget = 200 * time.Millisecond

// Namespace is the key under which templates see what is not the body:
// .seaport.headers and .seaport.raw. A body field of the same name is
// shadowed.
const Namespace = "seaport"

// DefaultChat is the conversation of a dock whose template names none.
const DefaultChat = "webhook"

// DefaultSender is the sender of a dock whose template names none.
const DefaultSender = "webhook"

// Template maps a webhook's JSON body to the fields of a message. Every
// field is a Go text/template over the body (`.` is the object;
// `.seaport.headers` and `.seaport.raw` are added); an empty field uses
// its default.
type Template struct {
	// ChatID names the conversation (default "webhook": one per dock).
	ChatID string `json:"chat_id,omitempty"`
	// ChatTitle is its title (default: the chat id).
	ChatTitle string `json:"chat_title,omitempty"`
	// ExternalID dedups provider retries (default: hash of the body and the
	// UTC minute, so an identical alert a minute later still arrives).
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
		if err := check(tpl.Tree.Root, 0, new(int)); err != nil {
			return nil, errx.InvalidField("template."+f.name, "template."+f.name+": "+err.Error())
		}
		p.parsed[f.name] = tpl
	}
	return p, nil
}

// banned are the builtins a template may not call: printf and its kin can
// build huge strings from a number, call runs arbitrary values, and html,
// js, urlquery and slice have no use here.
var banned = map[string]bool{
	"printf": true, "print": true, "println": true, "call": true,
	"html": true, "js": true, "urlquery": true, "slice": true,
}

// check walks a parsed template and refuses what could use unbounded time
// or memory: banned functions, {{template}} (recursion), {{range}} over
// anything but a field or variable (no literals or computed counts),
// nested ranges and more than MaxRanges of them.
func check(n parse.Node, depth int, ranges *int) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := check(c, depth, ranges); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return check(n.Pipe, depth, ranges)
	case *parse.IfNode:
		return branch(&n.BranchNode, depth, ranges)
	case *parse.WithNode:
		return branch(&n.BranchNode, depth, ranges)
	case *parse.RangeNode:
		if depth > 0 {
			return errors.New("{{range}} can't be nested")
		}
		if *ranges++; *ranges > MaxRanges {
			return fmt.Errorf("at most %d {{range}} actions", MaxRanges)
		}
		if p := n.Pipe; p == nil || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
			return errors.New("{{range}} must go over a field or variable")
		} else {
			switch p.Cmds[0].Args[0].(type) {
			case *parse.FieldNode, *parse.VariableNode, *parse.DotNode:
			default:
				return errors.New("{{range}} must go over a field or variable")
			}
		}
		if err := check(n.Pipe, depth, ranges); err != nil {
			return err
		}
		if err := check(n.List, depth+1, ranges); err != nil {
			return err
		}
		return check(n.ElseList, depth+1, ranges)
	case *parse.TemplateNode:
		return errors.New("{{template}} and {{block}} are not allowed")
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Cmds {
			if err := check(c, depth, ranges); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			if err := check(a, depth, ranges); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		return check(n.Node, depth, ranges)
	case *parse.IdentifierNode:
		if banned[n.Ident] {
			return fmt.Errorf("%s is not allowed", n.Ident)
		}
	}
	return nil
}

func branch(b *parse.BranchNode, depth int, ranges *int) error {
	if err := check(b.Pipe, depth, ranges); err != nil {
		return err
	}
	if err := check(b.List, depth, ranges); err != nil {
		return err
	}
	return check(b.ElseList, depth, ranges)
}

// capWriter is where a field's output goes: it fails once more than max
// bytes were written or the deadline passed (every write checks the
// clock), which stops a template at its next output.
type capWriter struct {
	buf      bytes.Buffer
	max      int
	deadline time.Time
}

var (
	errTooLong  = errors.New("the output is too long")
	errTooSlow  = errors.New("the template took too long")
	errTemplate = errors.New("the template failed")
)

func (w *capWriter) Write(b []byte) (int, error) {
	if time.Now().After(w.deadline) {
		return 0, errTooSlow
	}
	if w.buf.Len()+len(b) > w.max {
		return 0, errTooLong
	}
	return w.buf.Write(b)
}

// exec runs one field within its output and time budget. A goroutine can't
// be stopped, so on timeout it is left to finish (its work is bounded by
// check) and its output is dropped.
func exec(tpl *template.Template, root map[string]any) (string, error) {
	w := &capWriter{max: MaxOutput, deadline: time.Now().Add(fieldBudget)}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- errTemplate
			}
		}()
		done <- tpl.Execute(w, root)
	}()
	timer := time.NewTimer(fieldBudget)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return "", err
		}
		return w.buf.String(), nil
	case <-timer.C:
		return "", errTooSlow
	}
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
	// At is when the event arrived (default now); the default external id
	// carries its UTC minute.
	At time.Time
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
// object) plus .seaport.headers and .seaport.raw.
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
	root[Namespace] = map[string]any{"headers": h, "raw": string(in.Raw)}
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
		out, err := exec(tpl, root)
		if err != nil {
			if m.Errors == nil {
				m.Errors = map[string]string{}
			}
			m.Errors[name] = firstLine(err.Error())
			return ""
		}
		return strings.TrimSpace(strings.ReplaceAll(out, "<no value>", ""))
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
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	m.ExternalID = pick("external_id", "hook-"+hex.EncodeToString(sum[:])+"-"+at.UTC().Format("200601021504"), conversation.MaxExternalID)
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
