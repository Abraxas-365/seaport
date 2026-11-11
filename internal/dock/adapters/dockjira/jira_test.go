package dockjira

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSignature(t *testing.T) {
	body := []byte(`{"webhookEvent":"comment_created"}`)
	good := sign("s3cret", body)
	for _, c := range []struct {
		name, secret, header string
		body                 []byte
		want                 bool
	}{
		{"good", "s3cret", good, body, true},
		{"wrong secret", "other", good, body, false},
		{"tampered body", "s3cret", good, []byte(`{}`), false},
		{"no header", "s3cret", "", body, false},
		{"no prefix", "s3cret", strings.TrimPrefix(good, "sha256="), body, false},
		{"sha1 prefix", "s3cret", "sha1=" + strings.TrimPrefix(good, "sha256="), body, false},
		{"not hex", "s3cret", "sha256=zz", body, false},
	} {
		if got := validSignature(c.secret, c.body, c.header); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	for header, want := range map[string]bool{"Bearer s3cret": true, "Bearer  s3cret": true, "Bearer nope": false, "Bearer ": false, "s3cret": false, "": false} {
		if got := validBearer("s3cret", header); got != want {
			t.Errorf("bearer %q: %v, want %v", header, got, want)
		}
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestADFToMarkdown(t *testing.T) {
	adf := `{"type":"doc","version":1,"content":[
	 {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Steps"}]},
	 {"type":"paragraph","content":[
	   {"type":"mention","attrs":{"id":"5b10bot","text":"@Seaport Bot"}},
	   {"type":"text","text":" see "},
	   {"type":"text","text":"this","marks":[{"type":"link","attrs":{"href":"https://example.com/x"}}]},
	   {"type":"text","text":" and "},
	   {"type":"text","text":"bold ","marks":[{"type":"strong"}]},
	   {"type":"text","text":"code","marks":[{"type":"code"}]}]},
	 {"type":"bulletList","content":[
	   {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},
	     {"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nested"}]}]}]}]},
	   {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]},
	 {"type":"orderedList","attrs":{"order":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"third"}]}]}]},
	 {"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(1)\nreturn"}]},
	 {"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"quoted"}]}]},
	 {"type":"rule"},
	 {"type":"table","content":[
	   {"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]},
	   {"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"1"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"2"}]}]}]}]}
	]}`
	r := &reader{botID: "5b10bot", botName: "Seaport"}
	got := r.markdown(raw(adf))
	want := strings.Join([]string{
		"## Steps",
		"@Seaport see [this](https://example.com/x) and **bold** `code`",
		"- one\n  - nested\n- two",
		"3. third",
		"```go\nfmt.Println(1)\nreturn\n```",
		"> quoted",
		"---",
		"| a | b |\n| --- | --- |\n| 1 | 2 |",
	}, "\n\n")
	if got != want {
		t.Errorf("markdown:\n%s\n--- want\n%s", got, want)
	}
	if !r.mentioned {
		t.Error("bot mention not noticed")
	}
	other := &reader{botID: "someone-else"}
	other.markdown(raw(adf))
	if other.mentioned {
		t.Error("a mention of another account counted")
	}
}

func TestWikiToMarkdown(t *testing.T) {
	r := &reader{botID: "5b10bot", botName: "Seaport"}
	in := "h2. Plan\nHello [~accountid:5b10bot], see [the docs|https://example.com/d] and *bold* _it_ {{code}}.\n" +
		"* one\n** nested\n# first\n{code:java}\nint a = *2*;\n{code}\n||h1||h2||\n|c1|c2|\n{quote}\nsaid\n{quote}"
	got := r.markdown(raw(`"` + strings.NewReplacer("\n", `\n`).Replace(in) + `"`))
	want := "## Plan\nHello @Seaport, see [the docs](https://example.com/d) and **bold** *it* `code`.\n" +
		"- one\n  - nested\n1. first\n\n```java\nint a = *2*;\n```\n\n| h1 | h2 |\n| --- | --- |\n| c1 | c2 |\n\n> said"
	if got != want {
		t.Errorf("wiki:\n%s\n--- want\n%s", got, want)
	}
	if !r.mentioned {
		t.Error("[~accountid:…] not noticed")
	}
	if (&reader{botID: "x"}).markdown(raw(`"hi [~accountid:5b10bot]"`)) == "" {
		t.Error("empty")
	}
}

// adfJSON renders toADF's result for substring checks.
func adfJSON(md string) string {
	b, _ := json.Marshal(toADF(md))
	return string(b)
}

func TestMarkdownToADF(t *testing.T) {
	md := "## Title\n\nSome **bold**, *it*, ~~gone~~ and `code` with [a link](https://example.com/a).\n\n" +
		"- one\n  - nested\n    1. deep\n- two\n\n```go\nfmt.Println(\"<x>\")\n```\n\n> quote\n\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	got := adfJSON(md)
	for _, want := range []string{
		`"type":"doc","version":1`,
		`"type":"heading","attrs":{"level":2}`,
		`{"type":"text","text":"bold","marks":[{"type":"strong"}]}`,
		`"marks":[{"type":"em"}]`,
		`"marks":[{"type":"strike"}]`,
		`{"type":"text","text":"code","marks":[{"type":"code"}]}`,
		`"marks":[{"type":"link","attrs":{"href":"https://example.com/a"}}]`,
		`"type":"bulletList"`,
		`"type":"orderedList"`,
		`"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(\"\u003cx\u003e\")"}]`,
		`"type":"blockquote"`,
		`"type":"rule"`,
		`"type":"table"`, `"type":"tableHeader"`, `"type":"tableCell"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ADF lacks %s:\n%s", want, got)
		}
	}
	// A nested list sits inside its item, after the item's paragraph.
	if !strings.Contains(got, `"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"bulletList"`) {
		t.Errorf("nested list is not inside its item:\n%s", got)
	}
	// What ADF can't hold degrades to text: unsafe links keep their text,
	// a heading in a list is bold, empty input is one empty paragraph.
	if got := adfJSON("[x](javascript:alert(1))"); strings.Contains(got, "link") || !strings.Contains(got, `"text":"x"`) {
		t.Errorf("unsafe link: %s", got)
	}
	if got := adfJSON("- # head\n"); !strings.Contains(got, `"marks":[{"type":"strong"}]`) || strings.Contains(got, `"heading"`) {
		t.Errorf("heading in a list: %s", got)
	}
	if got := adfJSON("  "); got != `{"type":"doc","version":1,"content":[{"type":"paragraph"}]}` {
		t.Errorf("empty: %s", got)
	}
	if got := adfJSON("<b>raw</b>"); !strings.Contains(got, "raw") {
		t.Errorf("raw html lost its text: %s", got)
	}
}

// ADF round trip: what we write reads back as the same Markdown.
func TestRoundTrip(t *testing.T) {
	md := "## Title\n\nSome **bold** and `code` with [a link](https://example.com/a).\n\n- one\n  - nested\n- two\n\n```go\nfmt.Println(1)\n```\n\n> quote\n\n| a | b |\n| --- | --- |\n| 1 | 2 |"
	b, _ := json.Marshal(toADF(md))
	r := &reader{}
	if got := r.markdown(b); got != md {
		t.Errorf("round trip:\n%s\n--- want\n%s", got, md)
	}
}

func TestSplit(t *testing.T) {
	if got := split("  ", 100); got != nil {
		t.Errorf("blank: %q", got)
	}
	if got := split("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short: %q", got)
	}
	para := strings.Repeat("word ", 20) // 100 bytes
	long := para + "\n\n" + para + "\n\n" + para
	chunks := split(long, 250)
	if len(chunks) != 2 || strings.Count(chunks[0], "word") != 40 {
		t.Errorf("by paragraphs: %d chunks %q", len(chunks), chunks)
	}
	// A code block cut in the middle is closed and opened again.
	code := "```go\n" + strings.Repeat("line of code\n", 50) + "```"
	chunks = split("intro\n\n"+code, 200)
	if len(chunks) < 3 {
		t.Fatalf("code: %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 200 {
			t.Errorf("chunk %d has %d bytes", i, len(c))
		}
		if i > 0 && !strings.HasPrefix(c, "```go\n") || i > 0 && !strings.HasSuffix(c, "\n```") {
			t.Errorf("chunk %d is not fenced: %q", i, c)
		}
	}
	// One huge line is cut at rune boundaries.
	huge := strings.Repeat("é", 300)
	for _, c := range split(huge, 100) {
		if len(c) > 100 || strings.ContainsRune(c, '\uFFFD') {
			t.Errorf("huge line chunk: %d bytes", len(c))
		}
	}
	total := 0
	for _, c := range split(huge, 100) {
		total += strings.Count(c, "é")
	}
	if total != 300 {
		t.Errorf("split lost text: %d", total)
	}
}

func TestSiteAllowList(t *testing.T) {
	g := &Gateway{cfg: Config{Hosts: []string{"*.atlassian.net", "jira.corp.example"}}}
	for site, want := range map[string]string{
		"example.atlassian.net":                      "https://example.atlassian.net",
		"https://Example.atlassian.net/":             "https://example.atlassian.net",
		"https://jira.corp.example":                  "https://jira.corp.example",
		"https://a.b.atlassian.net":                  "https://a.b.atlassian.net",
		"https://atlassian.net":                      "",
		"https://evilatlassian.net":                  "",
		"https://example.atlassian.net.evil.example": "",
		"http://example.atlassian.net":               "",
		"https://user@example.atlassian.net":         "",
		"https://example.atlassian.net/jira":         "",
		"https://127.0.0.1":                          "",
		"":                                           "",
	} {
		got, err := g.checkSite(site)
		if (err == nil) != (want != "") || got != want {
			t.Errorf("%q → %q, %v (want %q)", site, got, err, want)
		}
	}
	// A wildcard or a bare host means the https default port; another port
	// needs its own host:port entry.
	ports := &Gateway{cfg: Config{Hosts: []string{"*.atlassian.net", "jira.corp.example", "other.corp.example:8443"}}}
	for site, want := range map[string]string{
		"https://x.atlassian.net:443":      "https://x.atlassian.net:443",
		"https://x.atlassian.net:8080":     "",
		"https://jira.corp.example:8080":   "",
		"https://jira.corp.example:443":    "https://jira.corp.example:443",
		"https://other.corp.example:8443":  "https://other.corp.example:8443",
		"https://other.corp.example":       "",
		"https://other.corp.example:9443":  "",
		"https://x.atlassian.net.evil:443": "",
	} {
		got, err := ports.checkSite(site)
		if (err == nil) != (want != "") || got != want {
			t.Errorf("%q → %q, %v (want %q)", site, got, err, want)
		}
	}
	local := &Gateway{cfg: Config{Hosts: []string{"127.0.0.1:4000"}}}
	if got, err := local.checkSite("http://127.0.0.1:4000"); err != nil || got != "http://127.0.0.1:4000" {
		t.Errorf("loopback: %q %v", got, err)
	}
}

func TestStoredSiteIsCheckedBeforeCredentialsGo(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"accountId":"5b10bot"}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	g := &Gateway{cfg: Config{Hosts: []string{host}}}
	g.client = newClient(g.stillAllowed)
	if _, _, err := g.client.myself(context.Background(), srv.URL, "bot@example.com", "tok"); err != nil || hits != 1 {
		t.Fatalf("allowed site: %v (hits %d)", err, hits)
	}
	// The operator removes the host: nothing is sent, the error says why.
	g.cfg.Hosts = []string{"*.atlassian.net"}
	s := Site{URL: srv.URL, Email: "bot@example.com", token: "tok"}
	for name, call := range map[string]func() error{
		"myself":  func() error { _, _, err := g.client.myself(context.Background(), s.URL, s.Email, s.token); return err },
		"comment": func() error { _, err := g.client.comment(context.Background(), s, "PROJ-1", doc{}, "m1"); return err },
		"stamp":   func() error { _, err := g.client.stamp(context.Background(), s, "10"); return err },
	} {
		err := call()
		if !errx.IsType(err, errx.TypeAuthorization) {
			t.Errorf("%s: %v, want forbidden", name, err)
		}
	}
	if hits != 1 {
		t.Errorf("credentials sent to a removed host: %d requests", hits)
	}
}

func team(names ...string) []agent.Member {
	var out []agent.Member
	for _, n := range names {
		out = append(out, agent.Member{Agent: identity.NewAgentID(), Name: n})
	}
	return out
}

func TestMentionDetection(t *testing.T) {
	site := Site{AccountID: "5b10bot", DisplayName: "Seaport"}
	d := identity.NewDockID()
	comment := func(body, author, kind string) []byte {
		b, _ := json.Marshal(map[string]any{
			"webhookEvent": "comment_created",
			"comment":      map[string]any{"id": "1", "body": body, "author": map[string]any{"accountId": author, "accountType": kind, "emailAddress": "Ana@Example.com", "displayName": "Ana"}},
			"issue":        map[string]any{"id": "7", "key": "PROJ-7", "fields": map[string]any{"summary": "S"}},
		})
		return b
	}
	for _, c := range []struct {
		name, body, author, kind string
		want                     bool
	}{
		{"agent mention", "@qa please look", "ana", "atlassian", true},
		{"wiki account mention", "[~accountid:5b10bot] hi", "ana", "atlassian", true},
		{"other account mention", "[~accountid:other] hi", "ana", "atlassian", false},
		{"plain @name of the account", "thanks @Seaport!", "ana", "atlassian", true},
		{"nobody", "lgtm", "ana", "atlassian", false},
		{"email is not a mention", "mail me@seaport.example", "ana", "atlassian", false},
		{"other bot", "@qa please", "auto", "app", false},
		{"empty", " ", "ana", "atlassian", false},
	} {
		a, ok := parse(comment(c.body, c.author, c.kind), d, site, team("qa"))
		if ok != c.want {
			t.Errorf("%s: %v, want %v", c.name, ok, c.want)
			continue
		}
		if ok {
			in := a.in
			if in.ChatID != "PROJ-7" || in.ExternalID != "comment-1" || !in.Addressed || in.Own || in.Channel != dock.ChannelJira ||
				in.SenderID != "ana" || in.ChatTitle != "PROJ-7: S" {
				t.Errorf("%s: inbound = %+v", c.name, in)
			}
			if !contains(in.ChatHandles, "PROJ-7") || !contains(in.ChatHandles, "PROJ") || !contains(in.SenderHandles, "ana@example.com") {
				t.Errorf("%s: handles = %v / %v", c.name, in.ChatHandles, in.SenderHandles)
			}
		}
	}
	// The account's own comments are own, whatever they say; its own
	// issues are not messages.
	if a, ok := parse(comment("no mention here", "5b10bot", "app"), d, site, team("qa")); !ok || !a.in.Own || a.commentID != "1" {
		t.Errorf("own comment: %+v %v", a, ok)
	}
	issue, _ := json.Marshal(map[string]any{"webhookEvent": "jira:issue_created",
		"issue": map[string]any{"id": "8", "key": "PROJ-8", "fields": map[string]any{"summary": "mine @qa", "reporter": map[string]any{"accountId": "5b10bot"}}}})
	if _, ok := parse(issue, d, site, team("qa")); ok {
		t.Error("an issue created by the account is a message")
	}
	// Garbage never panics.
	for _, junk := range []string{``, `[]`, `{}`, `{"issue":{"key":"nope"}}`, `{"webhookEvent":"comment_created","comment":{"id":"1"},"issue":{"key":"PROJ-1"}}`} {
		if _, ok := parse([]byte(junk), d, site, nil); ok {
			t.Errorf("parsed %q", junk)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestIssueKey(t *testing.T) {
	for k, want := range map[string]bool{"PROJ-1": true, "A1_B-123": true, "proj-1": false, "PROJ-0": false, "PROJ": false, "-1": false, "PROJ-1x": false, "1A-1": false} {
		if dock.ValidIssueKey(k) != want {
			t.Errorf("%q: want %v", k, want)
		}
	}
}
