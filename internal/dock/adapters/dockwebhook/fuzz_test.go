package dockwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// FuzzParseBody: any request body is either refused or becomes at most
// MaxEvents JSON objects whose raw bytes are valid JSON.
func FuzzParseBody(f *testing.F) {
	for _, s := range []string{
		`{"id":42,"msg":"disk full"}`, `[{"a":1},{"b":[1,2,{"c":null}]}]`, `[]`, `{}`, `[1]`, `"x"`, ``, ` {"a":1e999} `,
		`[{"a":1},`, `{"a":"\ud800"}`, strings.Repeat("[", 500), `{"a":{"b":{"c":{"d":1}}}}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		events, err := parseBody([]byte(body))
		if err != nil {
			return
		}
		if len(events) == 0 || len(events) > MaxEvents {
			t.Fatalf("%d events", len(events))
		}
		for _, e := range events {
			if _, ok := e.value.(map[string]any); !ok || len(e.raw) == 0 || e.raw[0] != '{' {
				t.Fatalf("not an object: %q", e.raw)
			}
		}
	})
}

// FuzzTemplate: a template is refused at compile time or renders, for any
// body, to a message within the limits the ship domain checks, in bounded
// time and without panicking.
func FuzzTemplate(f *testing.F) {
	for _, s := range []string{
		"{{.issue.id}}", "{{lower .issue.title}} ({{index .seaport.headers \"x-event\"}})", "{{default \"nobody\" .user}}",
		"{{join \", \" .issue.tags}} {{json .issue.tags}} {{trunc 3 .issue.title}}", "{{range .issue.tags}}{{.}},{{end}}",
		"{{if .a}}x{{else}}y{{end}}", "{{printf \"%1000000d\" 1}}", "{{template \"x\"}}", "{{range 5}}x{{end}}",
		"{{range .a}}{{range .b}}x{{end}}{{end}}", "{{.seaport.raw}}", "{{with .a}}{{.}}{{end}}", "{{", "{{.a | upper | lower}}",
		"@PM!", "{{ .x.y.z }}", "\x00{{.a}}", strings.Repeat("{{.a}}", 100),
	} {
		f.Add(s, `{"issue":{"id":"A-1","title":"Boom","tags":["db","prod"]},"user":null,"a":[{"b":[1]}],"count":3}`)
	}
	f.Add("{{.issue.id}}", `{"issue":{"id":"`+strings.Repeat("é", 1000)+`"}}`)
	f.Fuzz(func(t *testing.T, tpl, body string) {
		events, err := parseBody([]byte(body))
		if err != nil {
			return
		}
		src := Template{ChatID: tpl, ChatTitle: tpl, ExternalID: tpl, SenderID: tpl, SenderName: tpl, Text: tpl, Tags: tpl, Agent: tpl}
		if len(tpl) > MaxTemplateField {
			return
		}
		prog, err := src.Compile()
		if err != nil {
			return
		}
		in := Input{DockName: "sentry", Body: events[0].value, Raw: events[0].raw, Headers: map[string]string{"x-event": "e"},
			At: time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC)}
		start := time.Now()
		m := prog.Render(in)
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("render took %v", d)
		}
		for name, v := range map[string]string{"chat_id": m.ChatID, "chat_title": m.ChatTitle, "sender_name": m.SenderName} {
			if v == "" || utf8.RuneCountInString(v) > text.MaxName && name != "chat_id" || !utf8.ValidString(v) || text.HasForbiddenControl(v) {
				t.Fatalf("%s: %q", name, v)
			}
		}
		if utf8.RuneCountInString(m.ChatID) > conversation.MaxExternalID || utf8.RuneCountInString(m.ExternalID) > conversation.MaxExternalID ||
			utf8.RuneCountInString(m.SenderID) > ship.MaxSenderID || utf8.RuneCountInString(m.Text) > text.MaxMessage ||
			len(m.Tags) > MaxTags || m.ExternalID == "" || m.SenderID == "" || m.Text == "" {
			t.Fatalf("message out of limits: %+v", m)
		}
		if !utf8.ValidString(m.Text) || text.HasForbiddenControl(m.Text) {
			t.Fatalf("text: %q", m.Text)
		}
	})
}

// FuzzSignature: with a fixed secret, a header that is not exactly the
// HMAC of the body never validates; the right one always does.
func FuzzSignature(f *testing.F) {
	f.Add([]byte(`{"a":1}`), "sha256=00")
	f.Add([]byte(``), "")
	f.Add([]byte(`x`), "sha256="+strings.Repeat("0", 64))
	f.Add([]byte(`x`), " sha256="+strings.Repeat("A", 64))
	f.Fuzz(func(t *testing.T, body []byte, header string) {
		const secret = "fuzz-secret"
		good := signHeader(secret, body)
		if !validSignature(secret, body, good) {
			t.Fatal("the right signature was refused")
		}
		if header != good && strings.TrimSpace(header) != good && !strings.EqualFold(strings.TrimSpace(header), good) {
			if validSignature(secret, body, header) {
				t.Fatalf("accepted %q for %q", header, body)
			}
		}
		_ = validToken(secret, header)
		_ = validBearer(secret, header)
	})
}

func signHeader(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
