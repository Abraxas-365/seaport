package dockgithub

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidSignature(t *testing.T) {
	secret, body := []byte("s3cret"), []byte(`{"a":1}`)
	if !validSignature(secret, body, sign(secret, body)) {
		t.Fatal("good signature refused")
	}
	for name, h := range map[string]string{
		"other secret": sign([]byte("other"), body),
		"other body":   sign(secret, []byte(`{"a":2}`)),
		"no prefix":    strings.TrimPrefix(sign(secret, body), "sha256="),
		"sha1":         "sha1=" + strings.TrimPrefix(sign(secret, body), "sha256="),
		"not hex":      "sha256=zz",
		"empty":        "",
	} {
		if validSignature(secret, body, h) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAppJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	tok, err := appJWT(42, key, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	sig, _ := enc.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal("signature does not verify:", err)
	}
	raw, _ := enc.DecodeString(parts[1])
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "42" || claims.Iat != now.Add(-time.Minute).Unix() || claims.Exp-now.Unix() != 540 {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestParseKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for _, p := range [][]byte{pkcs1, pkcs8} {
		if _, err := parseKey(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := parseKey([]byte("nope")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestMentions(t *testing.T) {
	for text, want := range map[string]bool{
		"@seaport-qa please review":         true,
		"hey @Seaport-QA.":                  true,
		"(@seaport-qa)":                     true,
		"@seaport-qa[bot] look":             true,
		"@seaport-qa-other look":            false,
		"mail me at x@seaport-qa":           false,
		"no mention":                        false,
		"@seaport-qaz then @seaport-qa now": true,
	} {
		if got := mentions(strings.ToLower(text), "seaport-qa"); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}

func TestSplitChat(t *testing.T) {
	repo, n, ok := splitChat("acme/web#12")
	if !ok || repo != "acme/web" || n != 12 {
		t.Fatalf("got %q %d %v", repo, n, ok)
	}
	for _, bad := range []string{"acme/web", "acme#1", "/web#1", "acme/#1", "a/b/c#1", "acme/web#0", "acme/web#x"} {
		if _, _, ok := splitChat(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

var team = []agent.Member{{Name: "qa"}, {Name: "docs"}}

func payload(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func comment(body, login, kind, association string, pr bool) map[string]any {
	issue := map[string]any{"number": 7, "title": "Add login", "user": map[string]any{"login": "alice", "type": "User"}}
	if pr {
		issue["pull_request"] = map[string]any{"url": "x"}
	}
	return map[string]any{
		"action": "created", "installation": map[string]any{"id": 9},
		"repository": map[string]any{"full_name": "acme/web"},
		"sender":     map[string]any{"login": login, "type": kind},
		"issue":      issue,
		"comment": map[string]any{"id": 555, "body": body, "user": map[string]any{"login": login, "type": kind},
			"author_association": association, "created_at": "2026-11-10T10:00:00Z"},
	}
}

func TestParseIssueComment(t *testing.T) {
	dock := identity.NewDockID()
	at, ok := parse("issue_comment", payload(t, comment("@qa please test the login flow", "bob", "User", "MEMBER", true)), dock, "seaport-x", team)
	if !ok {
		t.Fatal("ignored")
	}
	in := at.in
	if in.ChatID != "acme/web#7" || in.ChatKind != conversation.KindGroup || in.ExternalID != "comment-555" ||
		in.SenderID != "@bob" || in.ChatTitle != "PR acme/web#7: Add login" || !in.Addressed || in.Dock != dock {
		t.Fatalf("inbound = %+v", in)
	}
	if strings.Join(in.ChatHandles, ",") != "acme/web#7,acme/web,acme" {
		t.Fatalf("chat handles = %v", in.ChatHandles)
	}
	if strings.Join(in.SenderHandles, ",") != "@bob,github:member,association:member" {
		t.Fatalf("sender handles = %v", in.SenderHandles)
	}
	if at.reactPath != "/issues/comments/555" || at.repo != "acme/web" {
		t.Fatalf("react = %q %q", at.reactPath, at.repo)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseIgnores(t *testing.T) {
	dock := identity.NewDockID()
	cases := map[string]struct {
		kind string
		body []byte
	}{
		"no mention":  {"issue_comment", payload(t, comment("looks good", "bob", "User", "MEMBER", true))},
		"other agent": {"issue_comment", payload(t, comment("@ops look", "bob", "User", "MEMBER", true))},
		"other bot":   {"issue_comment", payload(t, comment("@qa done", "dependabot[bot]", "Bot", "NONE", true))},
		"edited":      {"issue_comment", payload(t, map[string]any{"action": "edited", "repository": map[string]any{"full_name": "a/b"}})},
		"push":        {"push", []byte(`{"repository":{"full_name":"a/b"}}`)},
		"garbage":     {"issue_comment", []byte(`not json`)},
	}
	for name, c := range cases {
		if _, ok := parse(c.kind, c.body, dock, "seaport-x", team); ok {
			t.Errorf("%s: forwarded", name)
		}
	}
}

// The App's own comments come back as its own messages: Seaport's send
// (matched by its hidden message id) or an agent's gh comment.
func TestParseOwnComments(t *testing.T) {
	sent := "QA passed." + marker("gh-0b9c")
	at, ok := parse("issue_comment", payload(t, comment(sent, "seaport-x[bot]", "Bot", "NONE", true)), identity.NewDockID(), "seaport-x", team)
	if !ok || !at.in.Own || at.in.ExternalID != "gh-0b9c" || at.in.Text != "QA passed." || at.reactPath != "" ||
		strings.Join(at.in.SenderHandles, ",") != "@seaport-x[bot]" {
		t.Fatalf("seaport's send = %v %+v", ok, at)
	}
	if err := at.in.Validate(); err != nil {
		t.Fatal(err)
	}
	at, ok = parse("issue_comment", payload(t, comment("## QA review\nAll green, no @mention needed", "Seaport-X[bot]", "Bot", "NONE", true)), identity.NewDockID(), "seaport-x", team)
	if !ok || !at.in.Own || at.in.ExternalID != "comment-555" || !strings.HasPrefix(at.in.Text, "## QA review") {
		t.Fatalf("agent's gh comment = %v %+v", ok, at)
	}
	// A forged marker can't name an arbitrary id.
	if body, id := unmark("x <!-- seaport-message ../../etc -->"); id != "" || body == "" {
		t.Fatalf("unmark forged = %q %q", body, id)
	}
}

func TestParseOutsiderAndAppMention(t *testing.T) {
	at, ok := parse("issue_comment", payload(t, comment("@Seaport-X can you check?", "eve", "User", "NONE", false)), identity.NewDockID(), "seaport-x", team)
	if !ok {
		t.Fatal("app mention ignored")
	}
	if at.in.ChatTitle != "Issue acme/web#7: Add login" {
		t.Fatalf("title = %q", at.in.ChatTitle)
	}
	if strings.Join(at.in.SenderHandles, ",") != "@eve,github:outsider,association:none" {
		t.Fatalf("handles = %v", at.in.SenderHandles)
	}
}

func TestParseOpenedAndReviews(t *testing.T) {
	dock := identity.NewDockID()
	opened := map[string]any{
		"action": "opened", "repository": map[string]any{"full_name": "acme/web"},
		"sender": map[string]any{"login": "bob", "type": "User"},
		"pull_request": map[string]any{"id": 77, "number": 8, "title": "New checkout", "body": "@qa run the checkout QA",
			"user": map[string]any{"login": "bob", "type": "User"}, "author_association": "COLLABORATOR"},
	}
	at, ok := parse("pull_request", payload(t, opened), dock, "seaport-x", team)
	if !ok || at.in.ExternalID != "pr-77" || at.in.ChatID != "acme/web#8" || at.in.Text != "New checkout\n\n@qa run the checkout QA" || at.reactPath != "/issues/8" {
		t.Fatalf("opened = %+v %v", at, ok)
	}
	review := map[string]any{
		"action": "submitted", "repository": map[string]any{"full_name": "acme/web"},
		"sender":       map[string]any{"login": "bob", "type": "User"},
		"pull_request": map[string]any{"id": 77, "number": 8, "title": "New checkout"},
		"review": map[string]any{"id": 99, "body": "@docs update the guide", "state": "COMMENTED",
			"user": map[string]any{"login": "bob", "type": "User"}, "author_association": "OWNER", "submitted_at": "2026-11-10T10:00:00Z"},
	}
	at, ok = parse("pull_request_review", payload(t, review), dock, "seaport-x", team)
	if !ok || at.in.ExternalID != "review-99" || at.in.Text != "(review: commented) @docs update the guide" || at.reactPath != "" {
		t.Fatalf("review = %+v %v", at, ok)
	}
	inline := map[string]any{
		"action": "created", "repository": map[string]any{"full_name": "acme/web"},
		"sender":       map[string]any{"login": "bob", "type": "User"},
		"pull_request": map[string]any{"id": 77, "number": 8, "title": "New checkout"},
		"comment": map[string]any{"id": 31, "body": "@qa is this right?", "path": "src/pay.ts", "line": 12,
			"user": map[string]any{"login": "bob", "type": "User"}, "author_association": "OWNER"},
	}
	at, ok = parse("pull_request_review_comment", payload(t, inline), dock, "seaport-x", team)
	if !ok || at.in.ExternalID != "review-comment-31" || at.in.Text != "(on src/pay.ts:12) @qa is this right?" || at.reactPath != "/pulls/comments/31" {
		t.Fatalf("inline = %+v %v", at, ok)
	}
}

func TestReachable(t *testing.T) {
	for u, want := range map[string]bool{
		"https://port.example.com": true,
		"http://port.example.com":  false,
		"https://localhost:8740":   false,
		"https://127.0.0.1":        false,
		"http://127.0.0.1:8740":    false,
	} {
		if reachable(u) != want {
			t.Errorf("%s: want %v", u, want)
		}
	}
}

func TestFilesBody(t *testing.T) {
	files := []dock.File{{Name: "shot.png", MIME: "image/png"}, {Name: "re[port].md", MIME: "text/plain; charset=utf-8"}}
	links := []string{"https://github.com/o/r/blob/c1/pr-1/gh-1/shot.png", "https://github.com/o/r/blob/c1/pr-1/gh-1/re%5Bport%5D.md"}
	got := filesBody("Done.", files, links)
	want := "Done.\n\n![shot.png](" + links[0] + "?raw=true)\n📎 [report.md](" + links[1] + ")"
	if got != want {
		t.Fatalf("body = %q\nwant %q", got, want)
	}
	if got := filesBody("", files[:1], links[:1]); got != "![shot.png]("+links[0]+"?raw=true)" {
		t.Fatalf("no text: %q", got)
	}
}
