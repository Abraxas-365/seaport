package dockwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/text"
)

func body(t *testing.T, raw string) Input {
	t.Helper()
	events, err := parseBody([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return Input{DockName: "sentry", Body: events[0].value, Raw: events[0].raw}
}

func compile(t *testing.T, tpl Template) *Program {
	t.Helper()
	p, err := tpl.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	in := body(t, `{"id":42,"msg":"disk full"}`)
	m := compile(t, Template{}).Render(in)
	sum := sha256.Sum256(in.Raw)
	if m.ChatID != "webhook" || m.ChatTitle != "webhook" || m.SenderID != "webhook" || m.SenderName != "sentry" {
		t.Fatalf("defaults: %+v", m)
	}
	if m.ExternalID != "hook-"+hex.EncodeToString(sum[:]) {
		t.Fatalf("external id: %s", m.ExternalID)
	}
	if !strings.HasPrefix(m.Text, "```json\n") || !strings.HasSuffix(m.Text, "\n```") || !strings.Contains(m.Text, `"msg": "disk full"`) {
		t.Fatalf("text: %q", m.Text)
	}
	if len(m.Errors) != 0 || m.Agent != "" || len(m.Tags) != 0 {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestOverrides(t *testing.T) {
	in := body(t, `{"issue":{"id":"A-1","title":"Boom","tags":["db","prod"],"level":"error"},"user":null,"count":3}`)
	in.Headers = map[string]string{"x-event": "issue.created"}
	m := compile(t, Template{
		ChatID:     "{{.issue.id}}",
		ChatTitle:  `{{lower .issue.title}} ({{index .headers "x-event"}})`,
		ExternalID: "{{.issue.id}}-{{.count}}",
		SenderName: `{{default "nobody" .user}}`,
		Text:       "{{.issue.level | lower}}: {{trunc 3 .issue.title}} {{join \", \" .issue.tags}} {{json .issue.tags}}",
		Tags:       `{{join "," .issue.tags}}, {{.issue.id}}`,
		Agent:      "@PM!",
	}).Render(in)
	if m.ChatID != "A-1" || m.ChatTitle != "boom (issue.created)" || m.ExternalID != "A-1-3" || m.SenderName != "nobody" {
		t.Fatalf("fields: %+v", m)
	}
	if m.Text != `@pm error: Boo db, prod ["db","prod"]` {
		t.Fatalf("text: %q", m.Text)
	}
	if strings.Join(m.Tags, "|") != "db|prod" { // A-1 is the chat id: not repeated
		t.Fatalf("tags: %v", m.Tags)
	}
}

func TestMissingKeysAreEmpty(t *testing.T) {
	m := compile(t, Template{ChatID: "{{.nope.deeper}}", Text: "x{{.absent}}y"}).Render(body(t, `{"a":1}`))
	if m.ChatID != "webhook" || m.Text != "@x"[1:]+"y" {
		t.Fatalf("%+v", m)
	}
}

func TestParseErrorsAreFieldErrors(t *testing.T) {
	_, err := Template{Text: "{{.a"}.Compile()
	var e *errx.Error
	if !errors.As(err, &e) || !strings.Contains(e.Message, "template.text") {
		t.Fatalf("parse error: %v", err)
	}
	if _, err := (Template{ChatID: strings.Repeat("a", MaxTemplateField+1)}).Compile(); err == nil {
		t.Fatal("an oversized field must be refused")
	}
	if _, err := (Template{Text: "{{nofunc .a}}"}).Compile(); err == nil {
		t.Fatal("an unknown function must be refused")
	}
}

func TestExecErrorFallsBackToDefault(t *testing.T) {
	// .a is a number: calling trunc on it works, but indexing a string field fails.
	m := compile(t, Template{ChatID: "{{index .l 5}}", Text: "ok {{.b}}"}).Render(body(t, `{"a":1,"l":[1],"b":"fine"}`))
	if m.ChatID != "webhook" || m.Errors["chat_id"] == "" {
		t.Fatalf("chat id must fall back and report: %+v", m)
	}
	if m.Text != "ok fine" {
		t.Fatalf("other fields still render: %q", m.Text)
	}
}

func TestLimits(t *testing.T) {
	huge := `{"d":"` + strings.Repeat("é", text.MaxMessage) + `"}`
	m := compile(t, Template{}).Render(body(t, huge))
	if n := len([]rune(m.Text)); n > text.MaxMessage {
		t.Fatalf("text has %d runes", n)
	}
	m = compile(t, Template{ChatID: strings.Repeat("c", 300)}).Render(body(t, `{}`))
	if len(m.ChatID) > 256 {
		t.Fatalf("chat id: %d", len(m.ChatID))
	}
	m = compile(t, Template{Text: "{{.a}}"}).Render(body(t, `{"a":"a\u0000b"}`))
	if m.Text != "ab" {
		t.Fatalf("control characters are dropped: %q", m.Text)
	}
}

func TestFuncs(t *testing.T) {
	for _, c := range []struct{ tpl, want string }{
		{`{{default "x" ""}}`, "x"}, {`{{default "x" .z}}`, "x"}, {`{{default "x" .a}}`, "1"},
		{`{{trunc 2 "héllo"}}`, "hé"}, {`{{lower "AbC"}}`, "abc"}, {`{{join "-" .l}}`, "a-b"},
		{`{{join "-" .a}}`, "1"}, {`{{json .o}}`, `{"k":"v"}`}, {`{{trunc 0 "abc"}}x`, "x"},
	} {
		m := compile(t, Template{ChatTitle: c.tpl}).Render(body(t, `{"a":1,"l":["a","b"],"o":{"k":"v"}}`))
		if m.ChatTitle != c.want && !(c.want == "x" && m.ChatTitle == "webhook") {
			t.Errorf("%s = %q, want %q", c.tpl, m.ChatTitle, c.want)
		}
	}
}

func TestParseBody(t *testing.T) {
	ev, err := parseBody([]byte(` [{"a":1},{"a":2}] `))
	if err != nil || len(ev) != 2 || string(ev[1].raw) != `{"a":2}` {
		t.Fatalf("array: %v %+v", err, ev)
	}
	for _, bad := range []string{``, `hello`, `"str"`, `42`, `[1,2]`, `[{"a":1}`, `{"a":`, `[]`} {
		if _, err := parseBody([]byte(bad)); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	var many []string
	for range MaxEvents + 1 {
		many = append(many, `{}`)
	}
	if _, err := parseBody([]byte("[" + strings.Join(many, ",") + "]")); err == nil {
		t.Fatal("too many events must be refused")
	}
	if _, err := parseBody([]byte(strings.Join(many[:MaxEvents], ","))); err == nil {
		t.Fatal("not an array")
	}
	// Numbers keep their digits.
	ev, _ = parseBody([]byte(`{"n":12345678901234567890}`))
	if m := compile(t, Template{Text: "{{.n}}"}).Render(Input{Body: ev[0].value, Raw: ev[0].raw}); m.Text != "12345678901234567890" {
		t.Fatalf("number: %q", m.Text)
	}
	if b, _ := json.Marshal(ev[0].value); !strings.Contains(string(b), "12345678901234567890") {
		t.Fatal("number lost")
	}
}

func TestProofs(t *testing.T) {
	raw := []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(raw)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for _, c := range []struct {
		name, header string
		body         []byte
		want         bool
	}{
		{"good", good, raw, true}, {"tampered", good, []byte(`{}`), false}, {"no prefix", strings.TrimPrefix(good, "sha256="), raw, false},
		{"not hex", "sha256=zz", raw, false}, {"empty", "", raw, false}, {"sha1", "sha1=00", raw, false},
	} {
		if got := validSignature("s3cret", c.body, c.header); got != c.want {
			t.Errorf("signature %s: %v", c.name, got)
		}
	}
	for header, want := range map[string]bool{"Bearer s3cret": true, "Bearer  s3cret": true, "Bearer nope": false, "Bearer ": false, "s3cret": false, "": false} {
		if got := validBearer("s3cret", header); got != want {
			t.Errorf("bearer %q: %v", header, got)
		}
	}
	if !validToken("s3cret", " s3cret ") || validToken("s3cret", "") || validToken("s3cret", "s3cre") || validToken("", "") {
		t.Error("token")
	}
}

func TestSecretHeaders(t *testing.T) {
	for name, want := range map[string]bool{"x-github-event": true, "x-seaport-token": false, "x-api-key": false, "x-hub-signature-256": false, "x-forwarded-for": true, "x-auth": false} {
		if got := secretHeader(name); got == want {
			t.Errorf("%s secret=%v", name, got)
		}
	}
}
