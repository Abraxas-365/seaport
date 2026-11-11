package bootstrap_test

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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeGitHub plays GitHub's REST API for one App (id 4242, slug
// seaport-qa) installed once (installation 9) on the account acme.
type fakeGitHub struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	secret string

	mu       sync.Mutex
	calls    []string
	comments []string
	removed  bool
}

const ghToken = "ghs_fake_installation_token"

func newFakeGitHub(t *testing.T) *fakeGitHub {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, key: key, secret: "whsec-fake"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// appAuthed: the bearer is a JWT of the App signed with its key.
func (f *fakeGitHub) appAuthed(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	return json.Unmarshal(raw, &c) == nil && c.Iss == "4242" && c.Exp > time.Now().Unix()
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	answer := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	inst := map[string]any{"id": 9, "account": map[string]any{"login": "acme", "type": "Organization"}}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app-manifests/good-code/conversions":
		pemText := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(f.key)})
		answer(http.StatusCreated, map[string]any{"id": 4242, "slug": "seaport-qa", "name": "Seaport QA",
			"owner": map[string]any{"login": "acme", "type": "Organization"}, "html_url": f.srv.URL + "/apps/seaport-qa",
			"client_id": "Iv1.fake", "client_secret": "cs-fake", "webhook_secret": f.secret, "pem": string(pemText)})
	case r.URL.Path == "/app/installations/9" && r.Method == http.MethodGet && f.appAuthed(r):
		answer(http.StatusOK, inst)
	case r.URL.Path == "/app/installations/9" && r.Method == http.MethodDelete && f.appAuthed(r):
		f.mu.Lock()
		f.removed = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/repos/acme/web/installation" && f.appAuthed(r):
		answer(http.StatusOK, inst)
	case r.URL.Path == "/app/installations/9/access_tokens" && r.Method == http.MethodPost && f.appAuthed(r):
		answer(http.StatusCreated, map[string]any{"token": ghToken, "expires_at": time.Now().Add(time.Hour)})
	case r.Header.Get("Authorization") != "Bearer "+ghToken:
		answer(http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/web/issues/7/comments":
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.comments = append(f.comments, body.Body)
		f.mu.Unlock()
		answer(http.StatusCreated, map[string]any{"id": 1001})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reactions"):
		answer(http.StatusCreated, map[string]any{"id": 1})
	default:
		answer(http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func (f *fakeGitHub) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

// noRedirect is a client that shows redirects instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func redirected(t *testing.T, u string) *url.URL {
	t.Helper()
	res, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s: %d %s", u, res.StatusCode, raw)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// webhook posts a GitHub event to the dock, signed with secret.
func (p *port) webhook(d dock.Dock, secret, kind string, payload any) (int, map[string]any) {
	p.t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/github/webhook/"+d.ID.String(), strings.NewReader(string(raw)))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", kind)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func ghComment(id int, body, login, kind string) map[string]any {
	return map[string]any{
		"action": "created", "installation": map[string]any{"id": 9},
		"repository": map[string]any{"full_name": "acme/web"},
		"sender":     map[string]any{"login": login, "type": kind},
		"issue": map[string]any{"number": 7, "title": "Checkout v2", "pull_request": map[string]any{"url": "x"},
			"user": map[string]any{"login": "alice", "type": "User"}},
		"comment": map[string]any{"id": id, "body": body, "user": map[string]any{"login": login, "type": kind},
			"author_association": "MEMBER", "created_at": time.Now().UTC().Format(time.RFC3339)},
	}
}

type ghInfo struct {
	App *struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Installations []struct {
		ID      int64  `json:"id"`
		Account string `json:"account"`
	} `json:"installations"`
	WebhookURL string `json:"webhook_url"`
	InstallURL string `json:"install_url"`
}

// A GitHub dock: the App is registered from a manifest and installed
// through GitHub's redirects, webhooks that mention an agent of the team
// reach it (signed, deduplicated, bots ignored), its reply is a comment
// posted with an installation token, and unlinking uninstalls the App.
func TestGitHubDock(t *testing.T) {
	gh := newFakeGitHub(t)
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "github-test-secret-key-0123456789abcdef"
		c.GitHubURL, c.GitHubAPIURL = gh.srv.URL, gh.srv.URL
	}, bootstrap.Options{})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "github", "name": "QA"}, &d); code != http.StatusCreated {
		t.Fatalf("create github dock: %d", code)
	}
	if d.Status != dock.StatusLinking {
		t.Fatalf("new github dock is %s", d.Status)
	}
	var info ghInfo
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String()+"/github", nil, &info); code != http.StatusOK || info.App != nil ||
		info.WebhookURL != p.srv.URL+"/github/webhook/"+d.ID.String() {
		t.Fatalf("github info: %d %+v", code, info)
	}
	// Not a GitHub dock: no GitHub side.
	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/github", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("github info of an api dock: %d", code)
	}

	// The manifest form goes to GitHub with a sealed state.
	var m struct {
		ActionURL string `json:"action_url"`
		Manifest  string `json:"manifest"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/github/manifest", map[string]string{"organization": "acme"}, &m); code != http.StatusOK {
		t.Fatalf("manifest: %d", code)
	}
	action, _ := url.Parse(m.ActionURL)
	if !strings.HasPrefix(m.ActionURL, gh.srv.URL+"/organizations/acme/settings/apps/new?") || action.Query().Get("state") == "" {
		t.Fatalf("action url = %s", m.ActionURL)
	}
	var manifest struct {
		Name     string `json:"name"`
		Redirect string `json:"redirect_url"`
		Hook     struct {
			URL string `json:"url"`
		} `json:"hook_attributes"`
		Perms map[string]string `json:"default_permissions"`
	}
	if err := json.Unmarshal([]byte(m.Manifest), &manifest); err != nil || manifest.Hook.URL != info.WebhookURL ||
		manifest.Redirect != p.srv.URL+"/github/apps/created" || manifest.Perms["pull_requests"] != "write" || manifest.Name != "Seaport QA" {
		t.Fatalf("manifest = %s (%v)", m.Manifest, err)
	}
	state := action.Query().Get("state")

	// A forged or mismatched state is refused.
	for _, bad := range []string{"forged", ""} {
		if res, err := noRedirect.Get(p.srv.URL + "/github/apps/created?code=good-code&state=" + url.QueryEscape(bad)); err != nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("forged state: %v %v", err, res.StatusCode)
		}
	}
	if res, _ := noRedirect.Get(p.srv.URL + "/github/apps/installed?installation_id=9&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("manifest state used to install: %d", res.StatusCode)
	}

	// GitHub created the App: Seaport trades the code, then sends the
	// browser to install it.
	install := redirected(t, p.srv.URL+"/github/apps/created?code=good-code&state="+url.QueryEscape(state))
	if install.Path != "/apps/seaport-qa/installations/new" || install.Query().Get("state") == "" {
		t.Fatalf("install redirect = %s", install)
	}
	back := redirected(t, p.srv.URL+"/github/apps/installed?installation_id=9&setup_action=install&state="+url.QueryEscape(install.Query().Get("state")))
	if back.Path != "/docks/"+d.ID.String() || back.Query().Get("github") != "installed" {
		t.Fatalf("back = %s", back)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked || d.Address != "seaport-qa" {
		t.Fatalf("installed dock: %s %q", d.Status, d.Address)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String()+"/github", nil, &info)
	if info.App == nil || info.App.Slug != "seaport-qa" || len(info.Installations) != 1 || info.Installations[0].Account != "acme" || info.InstallURL == "" {
		t.Fatalf("github info = %+v", info)
	}
	if strings.Contains(fmtJSON(info), "whsec") || strings.Contains(fmtJSON(info), "PRIVATE KEY") {
		t.Fatal("github info leaks a secret")
	}

	// Webhooks: signed with the App's secret only.
	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)
	if code, _ := p.webhook(d, "wrong", "issue_comment", ghComment(555, "@test please QA this", "bob", "User")); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code, out := p.webhook(d, gh.secret, "ping", map[string]any{"zen": "x"}); code != http.StatusOK || out["status"] != "pong" {
		t.Fatalf("ping: %d %v", code, out)
	}
	// Bots (our own replies) and messages calling no one are ignored.
	for _, c := range []map[string]any{ghComment(556, "@test done", "seaport-qa[bot]", "Bot"), ghComment(557, "lgtm", "bob", "User")} {
		if code, out := p.webhook(d, gh.secret, "issue_comment", c); code != http.StatusAccepted || out["status"] != "ignored" {
			t.Fatalf("ignored webhook: %d %v", code, out)
		}
	}
	code, out := p.webhook(d, gh.secret, "issue_comment", ghComment(555, "@test please QA this", "bob", "User"))
	if code != http.StatusAccepted || out["status"] != "received" || out["created"] != true {
		t.Fatalf("webhook: %d %v", code, out)
	}
	// A redelivery is the same ship.
	if code, again := p.webhook(d, gh.secret, "issue_comment", ghComment(555, "@test please QA this", "bob", "User")); code != http.StatusAccepted ||
		again["created"] != false || again["ship_id"] != out["ship_id"] {
		t.Fatalf("redelivery: %d %v", code, again)
	}

	// The agent gets it in the PR's conversation and replies: a comment.
	c := p.dialCrew(1)
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Text != "@test please QA this" {
		t.Fatalf("delivery = %+v", f)
	}
	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	if len(convs.Items) != 1 || convs.Items[0].ExternalID != "acme/web#7" || convs.Items[0].Title != "PR acme/web#7: Checkout v2" {
		t.Fatalf("conversations = %+v", convs.Items)
	}
	waitFor(t, func() bool { return gh.called("POST /repos/acme/web/issues/comments/555/reactions") })
	session := mcpSession(t, p, p.crewToken())
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": convs.Items[0].ID.String(), "text": "QA passed: checkout works."}})
	if err != nil || res.IsError {
		t.Fatalf("reply: %v %+v", err, res)
	}
	gh.mu.Lock()
	comments := append([]string(nil), gh.comments...)
	gh.mu.Unlock()
	if len(comments) != 1 || comments[0] != "QA passed: checkout works." {
		t.Fatalf("comments = %v", comments)
	}
	ack(c, f)

	// Uninstalled on GitHub: logged out; installed again: linked.
	gone := map[string]any{"action": "deleted", "installation": map[string]any{"id": 9, "account": map[string]any{"login": "acme", "type": "Organization"}}}
	if code, _ := p.webhook(d, gh.secret, "installation", gone); code != http.StatusOK {
		t.Fatalf("installation deleted: %d", code)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLoggedOut {
		t.Fatalf("uninstalled dock is %s", d.Status)
	}
	gone["action"] = "created"
	if code, _ := p.webhook(d, gh.secret, "installation", gone); code != http.StatusOK {
		t.Fatalf("installation created: %d", code)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked {
		t.Fatalf("reinstalled dock is %s", d.Status)
	}

	// Unlink uninstalls the App and forgets it: webhooks are unknown.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %s", code, d.Status)
	}
	gh.mu.Lock()
	removed := gh.removed
	gh.mu.Unlock()
	if !removed {
		t.Fatal("unlink did not uninstall the app")
	}
	if code, _ := p.webhook(d, gh.secret, "ping", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("webhook after unlink: %d", code)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String()+"/github", nil, &info)
	if info.App != nil {
		t.Fatalf("app kept after unlink: %+v", info.App)
	}
}

func fmtJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
