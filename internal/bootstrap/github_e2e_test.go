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
	"math/big"
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
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeGitHub plays GitHub's REST API for one App (id 4242, slug
// seaport-qa) installed once (installation 9) on the account acme.
type fakeGitHub struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	secret string
	// oidcKey signs GitHub Actions' OIDC tokens (issuer srv/oidc).
	oidcKey *rsa.PrivateKey

	mu       sync.Mutex
	calls    []string
	comments []string
	removed  bool
	scoped   [][]string // repositories each scoped token was asked for
	// git is a tiny Git Data API: objects by sha and the files branch.
	git     map[string]map[string]any
	gitHead string
	gitRace int // ref updates to refuse (another reply moved the branch)
}

const (
	ghToken  = "ghs_fake_installation_token"
	ghScoped = "ghs_fake_scoped_token" // limited to one repository (agents')
)

func newFakeGitHub(t *testing.T) *fakeGitHub {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	oidcKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, key: key, secret: "whsec-fake", oidcKey: oidcKey}
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
	case r.URL.Path == "/oidc/.well-known/openid-configuration":
		answer(http.StatusOK, map[string]string{"issuer": f.srv.URL + "/oidc", "jwks_uri": f.srv.URL + "/oidc/jwks"})
	case r.URL.Path == "/oidc/jwks":
		pub := f.oidcKey.PublicKey
		answer(http.StatusOK, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}}})
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
	case r.URL.Path == "/repos/acme/other/installation" && f.appAuthed(r):
		answer(http.StatusNotFound, map[string]string{"message": "Not Found"})
	case r.URL.Path == "/app/installations/9/access_tokens" && r.Method == http.MethodPost && f.appAuthed(r):
		var body struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Repositories) > 0 {
			f.mu.Lock()
			f.scoped = append(f.scoped, body.Repositories)
			f.mu.Unlock()
			answer(http.StatusCreated, map[string]any{"token": ghScoped, "expires_at": time.Now().Add(time.Hour)})
			return
		}
		answer(http.StatusCreated, map[string]any{"token": ghToken, "expires_at": time.Now().Add(time.Hour)})
	case r.Header.Get("Authorization") != "Bearer "+ghToken:
		answer(http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
	case strings.HasPrefix(r.URL.Path, "/repos/acme/web/git/"):
		f.gitAPI(w, r, answer)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/web/issues/7/comments":
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.comments = append(f.comments, body.Body)
		f.mu.Unlock()
		answer(http.StatusCreated, map[string]any{"id": 1001})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/web/issues/7":
		answer(http.StatusOK, map[string]any{"number": 7, "title": "Checkout v2", "pull_request": map[string]any{}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reactions"):
		answer(http.StatusCreated, map[string]any{"id": 1})
	default:
		answer(http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

// actionToken is a GitHub Actions OIDC token of a run in repo, signed
// with key (the issuer's, or a forged one), with extra claims on top.
func (f *fakeGitHub) actionToken(key *rsa.PrivateKey, aud, repo string, extra map[string]any) string {
	now := time.Now()
	claims := map[string]any{"iss": f.srv.URL + "/oidc", "aud": aud, "exp": now.Add(5 * time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(),
		"iat": now.Unix(), "repository": repo, "repository_owner": "acme", "actor": "carol", "ref": "refs/pull/7/merge",
		"event_name": "pull_request", "workflow": "QA", "run_id": "777", "run_attempt": "1", "sha": "abc"}
	for k, v := range extra {
		claims[k] = v
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
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
		c.GitHubOIDCURL = gh.srv.URL + "/oidc"
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
	// Other bots and messages calling no one are ignored.
	for _, c := range []map[string]any{ghComment(556, "@test done", "dependabot[bot]", "Bot"), ghComment(557, "lgtm", "bob", "User")} {
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
	sent, id, _ := strings.Cut(comments[0], "\n\n<!-- seaport-message ")
	if len(comments) != 1 || sent != "QA passed: checkout works." || !strings.HasPrefix(id, "gh-") {
		t.Fatalf("comments = %v", comments)
	}
	// GitHub tells us about our own comment: the echo of the stored
	// reply, no second ship. A comment the agent posted itself with gh
	// is recorded as sent by it and wakes nobody.
	if code, echo := p.webhook(d, gh.secret, "issue_comment", ghComment(560, comments[0], "seaport-qa[bot]", "Bot")); code != http.StatusAccepted ||
		echo["status"] != "recorded" || echo["created"] != false {
		t.Fatalf("echo of our reply: %d %v", code, echo)
	}
	code, rec := p.webhook(d, gh.secret, "issue_comment", ghComment(561, "## QA review\n- checkout: ok\n- @test coupon: ok", "seaport-qa[bot]", "Bot"))
	if code != http.StatusAccepted || rec["status"] != "recorded" || rec["created"] != true {
		t.Fatalf("agent's gh comment: %d %v", code, rec)
	}
	var history query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+convs.Items[0].ID.String()+"/ships", nil, &history)
	if len(history.Items) != 3 {
		t.Fatalf("history = %+v", history.Items)
	}
	byID := map[string]ship.Ship{}
	for _, h := range history.Items {
		byID[h.ExternalID] = h
	}
	own := byID["comment-561"]
	if own.Direction != ship.DirectionOut || own.Status != ship.StatusSent || own.ExternalID != "comment-561" || own.SenderID != "@seaport-qa[bot]" ||
		own.Agent == nil || *own.Agent != p.agent(testAgent).ID {
		t.Fatalf("recorded comment = %+v", own)
	}
	if reply := byID[id[:len(id)-len(" -->")]]; reply.Direction != ship.DirectionOut || reply.Text != "QA passed: checkout works." {
		t.Fatalf("reply = %+v (history %+v)", reply, history.Items)
	}
	ack(c, f)
	c.quiet(300 * time.Millisecond)

	// The agent gets App tokens for its docks' repos only, scoped to one
	// repository; admins, other agents and other repos get none.
	var tok dock.GitHubToken
	if code := p.call(http.MethodPost, dock.GitHubTokenPath, p.crewToken(), map[string]string{"repository": "acme/web"}, &tok); code != http.StatusOK ||
		tok.Token != ghScoped || tok.Repository != "acme/web" || tok.App != "seaport-qa" || tok.APIURL != gh.srv.URL || tok.ExpiresAt.IsZero() {
		t.Fatalf("github token: %d %+v", code, tok)
	}
	gh.mu.Lock()
	scoped := fmtJSON(gh.scoped)
	gh.mu.Unlock()
	if scoped != `[["web"]]` {
		t.Fatalf("scoped tokens asked = %s", scoped)
	}
	ghHost := strings.TrimPrefix(gh.srv.URL, "http://")
	for _, c := range []struct {
		token string
		body  map[string]string
		want  int
	}{
		{p.crewToken(), map[string]string{"repository": "acme/other"}, http.StatusNotFound},                     // App not there
		{p.crewToken(), map[string]string{"repository": "acme/web", "host": "gitlab.com"}, http.StatusNotFound}, // another host
		{p.crewToken(), map[string]string{"repository": "acme/web", "host": ghHost}, http.StatusOK},
		{p.crewToken(), map[string]string{"repository": "../etc"}, http.StatusBadRequest},
		{p.issueAs(adminToken, p.agent("outsider").ID, "outsider").Value, map[string]string{"repository": "acme/web"}, http.StatusNotFound},
		{adminToken, map[string]string{"repository": "acme/web"}, http.StatusUnauthorized},
	} {
		if code := p.call(http.MethodPost, dock.GitHubTokenPath, c.token, c.body, nil); code != c.want {
			t.Fatalf("github token %v: %d, want %d", c.body, code, c.want)
		}
	}

	// A GitHub Actions workflow of a repo the App serves sends a message
	// with its OIDC token: it lands in the PR's conversation, for the
	// agent it names, once per run, step and attempt.
	actions := p.srv.URL + "/github/actions/" + d.ID.String()
	good := gh.actionToken(gh.oidcKey, p.srv.URL, "acme/web", nil)
	post := func(bearer string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, actions, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	forged, _ := rsa.GenerateKey(rand.Reader, 2048)
	for _, c := range []struct {
		name   string
		bearer string
		body   map[string]any
		want   int
	}{
		{"no token", "", map[string]any{"text": "QA"}, http.StatusUnauthorized},
		{"forged", gh.actionToken(forged, p.srv.URL, "acme/web", nil), map[string]any{"text": "QA"}, http.StatusUnauthorized},
		{"other audience", gh.actionToken(gh.oidcKey, "https://elsewhere.example", "acme/web", nil), map[string]any{"text": "QA"}, http.StatusUnauthorized},
		{"expired", gh.actionToken(gh.oidcKey, p.srv.URL, "acme/web", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), map[string]any{"text": "QA"}, http.StatusUnauthorized},
		{"other issuer", gh.actionToken(gh.oidcKey, p.srv.URL, "acme/web", map[string]any{"iss": "https://token.example"}), map[string]any{"text": "QA"}, http.StatusUnauthorized},
		{"repo without the App", gh.actionToken(gh.oidcKey, p.srv.URL, "acme/other", nil), map[string]any{"text": "QA"}, http.StatusForbidden},
		{"no PR", gh.actionToken(gh.oidcKey, p.srv.URL, "acme/web", map[string]any{"ref": "refs/heads/main"}), map[string]any{"text": "QA"}, http.StatusBadRequest},
		{"unknown agent", good, map[string]any{"text": "QA", "agent": "nobody"}, http.StatusBadRequest},
		{"empty", good, map[string]any{"text": " "}, http.StatusBadRequest},
	} {
		if code, out := post(c.bearer, c.body); code != c.want {
			t.Fatalf("action %s: %d %v, want %d", c.name, code, out, c.want)
		}
	}
	code, out = post(good, map[string]any{"text": "Run the functional QA of this PR on the dev env.", "agent": "test", "id": "qa"})
	if code != http.StatusAccepted || out["created"] != true || out["chat"] != "acme/web#7" || out["status"] != "queued" {
		t.Fatalf("action: %d %v", code, out)
	}
	if code, again := post(good, map[string]any{"text": "Run the functional QA of this PR on the dev env.", "agent": "test", "id": "qa"}); code != http.StatusAccepted ||
		again["created"] != false || again["ship_id"] != out["ship_id"] {
		t.Fatalf("retried action step: %d %v", code, again)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Conversation.ExternalID != "acme/web#7" ||
		!strings.Contains(f.Delivery.Ships[0].Text, "@test Run the functional QA") || !strings.Contains(f.Delivery.Ships[0].Text, "GitHub Actions: QA, run 777") ||
		f.Delivery.Ships[0].SenderID != "@carol" || f.Delivery.Ships[0].ExternalID != "action-777-1-qa" {
		t.Fatalf("action delivery = %+v", f.Delivery)
	}
	ack(c, f)
	// Customs can shut workflows out as a whole.
	var rule struct {
		ID string `json:"id"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/customs/rules", map[string]string{"effect": "deny", "pattern": "github:action"}, &rule); code != http.StatusCreated {
		t.Fatalf("deny github:action: %d", code)
	}
	if code, out := post(gh.actionToken(gh.oidcKey, p.srv.URL, "acme/web", map[string]any{"run_id": "778"}), map[string]any{"text": "again"}); code != http.StatusAccepted || out["status"] != "held" {
		t.Fatalf("denied action: %d %v", code, out)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/customs/rules/"+rule.ID, nil, nil); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("remove rule: %d", code)
	}

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

// githubDock makes a GitHub dock whose App (the fake's) is created and
// installed on acme (installation 9).
func (p *port) githubDock(gh *fakeGitHub) dock.Dock {
	p.t.Helper()
	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "github", "name": "QA"}, &d); code != http.StatusCreated {
		p.t.Fatalf("create github dock: %d", code)
	}
	var m struct {
		ActionURL string `json:"action_url"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/github/manifest", map[string]string{"organization": "acme"}, &m); code != http.StatusOK {
		p.t.Fatalf("manifest: %d", code)
	}
	action, _ := url.Parse(m.ActionURL)
	install := redirected(p.t, p.srv.URL+"/github/apps/created?code=good-code&state="+url.QueryEscape(action.Query().Get("state")))
	redirected(p.t, p.srv.URL+"/github/apps/installed?installation_id=9&setup_action=install&state="+url.QueryEscape(install.Query().Get("state")))
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked {
		p.t.Fatalf("github dock is %s", d.Status)
	}
	return d
}

// gitAPI answers the Git Data calls a reply with files makes.
func (f *fakeGitHub) gitAPI(w http.ResponseWriter, r *http.Request, answer func(int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.git == nil {
		f.git = map[string]map[string]any{}
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	store := func(v map[string]any) string {
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		sha := hex.EncodeToString(sum[:20])
		f.git[sha] = v
		return sha
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/acme/web/git/")
	switch {
	case r.Method == http.MethodPost && path == "blobs":
		answer(http.StatusCreated, map[string]string{"sha": store(body)})
	case r.Method == http.MethodPost && path == "trees":
		answer(http.StatusCreated, map[string]string{"sha": store(body)})
	case r.Method == http.MethodPost && path == "commits":
		answer(http.StatusCreated, map[string]string{"sha": store(body)})
	case r.Method == http.MethodGet && path == "ref/heads/seaport-files":
		if f.gitHead == "" {
			answer(http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		answer(http.StatusOK, map[string]any{"object": map[string]string{"sha": f.gitHead}})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "commits/"):
		c := f.git[strings.TrimPrefix(path, "commits/")]
		answer(http.StatusOK, map[string]any{"tree": map[string]any{"sha": c["tree"]}})
	case r.Method == http.MethodPost && path == "refs" && f.gitHead == "" && body["ref"] == "refs/heads/seaport-files":
		f.gitHead = body["sha"].(string)
		answer(http.StatusCreated, map[string]any{})
	case r.Method == http.MethodPatch && path == "refs/heads/seaport-files" && f.gitHead != "":
		parents, _ := f.git[body["sha"].(string)]["parents"].([]any)
		if f.gitRace > 0 || len(parents) != 1 || parents[0] != f.gitHead {
			if f.gitRace > 0 {
				f.gitRace--
				// Someone else's commit lands meanwhile.
				f.gitHead = store(map[string]any{"tree": f.git[f.gitHead]["tree"], "parents": []any{f.gitHead}})
			}
			answer(http.StatusUnprocessableEntity, map[string]string{"message": "Update is not a fast forward"})
			return
		}
		f.gitHead = body["sha"].(string)
		answer(http.StatusOK, map[string]any{})
	default:
		answer(http.StatusUnprocessableEntity, map[string]string{"message": "Reference already exists"})
	}
}

// gitFiles are the files of the files branch's head commit, by path
// (only the head commit's own tree entries; enough for the tests).
func (f *fakeGitHub) gitFiles() map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]byte{}
	if f.gitHead == "" {
		return out
	}
	tree := f.git[f.git[f.gitHead]["tree"].(string)]
	entries, _ := tree["tree"].([]any)
	for _, e := range entries {
		m := e.(map[string]any)
		blob := f.git[m["sha"].(string)]
		data, _ := base64.StdEncoding.DecodeString(blob["content"].(string))
		out[m["path"].(string)] = data
	}
	return out
}
