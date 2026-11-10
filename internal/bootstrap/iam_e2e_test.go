package bootstrap_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authpg"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// signIn runs the browser sign-in against IAMKit's hosted pages:
// /api/v1/session/login → IAMKit authorize → hosted login (password, and
// the organization chooser when choose is set) → /auth/callback. It
// returns the callback's response (where the port sends the browser).
func signIn(t *testing.T, b *browser, p *port, u iamUser, query string, choose *iamOrg) page {
	t.Helper()
	ticket, _ := hostedLogin(t, b, p, query)
	done := b.post(iam.base+"/hosted/login/password", url.Values{"ticket": {ticket}, "email": {u.Email}, "password": {iamPassword}})
	if choose != nil {
		if done.Status != http.StatusOK || len(done.fields("organization_id")) == 0 {
			t.Fatalf("organization chooser: %d %s", done.Status, done.Body)
		}
		done = b.post(iam.base+"/hosted/login/organization", url.Values{"ticket": {ticket}, "organization_id": {choose.ID}})
	}
	if done.Status != http.StatusSeeOther || !strings.HasPrefix(done.Location, p.srv.URL+"/auth/callback?") {
		t.Fatalf("hosted login finish: %d %q %s", done.Status, done.Location, done.Body)
	}
	back := b.get(done.Location)
	if back.Status != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", back.Status, back.Body)
	}
	return back
}

// hostedLogin starts a sign-in at the port and follows it to IAMKit's
// hosted login page: the page's ticket and the authorize request's state.
func hostedLogin(t *testing.T, b *browser, p *port, query string) (ticket, state string) {
	t.Helper()
	start := b.get(p.srv.URL + "/api/v1/session/login?" + query)
	if start.Status != http.StatusSeeOther || !strings.HasPrefix(start.Location, iam.base+"/oauth/authorize?") {
		t.Fatalf("sign-in start: %d %q %s", start.Status, start.Location, start.Body)
	}
	if b.cookie(p.srv.URL, "seaport_oauth") == "" {
		t.Fatal("no pending sign-in cookie")
	}
	u, _ := url.Parse(start.Location)
	authorize := b.get(start.Location)
	if authorize.Status != http.StatusSeeOther || !strings.Contains(authorize.Location, "/hosted/login?ticket=") {
		t.Fatalf("authorize: %d %q %s", authorize.Status, authorize.Location, authorize.Body)
	}
	login := b.get(authorize.Location)
	if login.Status != http.StatusOK || login.field("ticket") == "" {
		t.Fatalf("hosted login page: %d %s", login.Status, login.Body)
	}
	return login.field("ticket"), u.Query().Get("state")
}

func currentSession(t *testing.T, b *browser, p *port) httpx.SessionState {
	t.Helper()
	var s httpx.SessionState
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/session", nil, &s); code != http.StatusOK {
		t.Fatalf("session: %d", code)
	}
	return s
}

// signinError is the message of a callback that failed (?signin_error=).
func signinError(t *testing.T, p *port, back page) string {
	t.Helper()
	u, err := url.Parse(back.Location)
	if err != nil || back.Status != http.StatusSeeOther || u.Path != "/" || u.Query().Get("signin_error") == "" ||
		!strings.HasPrefix(back.Location, p.srv.URL) {
		t.Fatalf("want a sign-in error redirect, got %d %q", back.Status, back.Location)
	}
	return u.Query().Get("signin_error")
}

// withCookie is a request carrying only the session cookie.
func withCookie(t *testing.T, method, raw, sid string) int {
	t.Helper()
	r, _ := http.NewRequest(method, raw, nil)
	r.AddCookie(&http.Cookie{Name: httpx.SessionCookie, Value: sid})
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

// TestIAMBrowserSignIn: the BFF code flow end to end against IAMKit —
// session cookie, profile and permissions, CSRF on cookie writes, refresh
// (IAMKit rotates refresh tokens: concurrent requests must not end the
// session), organization chooser and switch, sign-out (revoked at IAMKit
// too) and the failure paths of the callback.
func TestIAMBrowserSignIn(t *testing.T) {
	p, cfg := startIAMPort(t)
	var pub struct {
		Auth string `json:"auth"`
	}
	if code := p.call(http.MethodGet, "/api/v1/config", "", nil, &pub); code != http.StatusOK || pub.Auth != "iam" {
		t.Fatalf("config: %d %+v", code, pub)
	}

	orgA, orgB := newIAMOrg(t), newIAMOrg(t)
	viewer := newIAMUser(t, []string{permission.DocksRead, permission.CrewRead}, orgA)

	// Signed out: the session says so, the API refuses.
	b := newBrowser(t)
	if s := currentSession(t, b, p); s.Authenticated || s.Permissions == nil {
		t.Fatalf("signed out session: %+v", s)
	}
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/docks", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("docks signed out: %d", code)
	}
	// POST /session is token mode's form; next must stay on this site.
	if code := b.api(http.MethodPost, p.srv.URL+"/api/v1/session", map[string]string{"token": "x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("token login in iam mode: %d", code)
	}
	for _, next := range []string{"https://evil.test/", "//evil.test", "/\\evil"} {
		if r := b.get(p.srv.URL + "/api/v1/session/login?next=" + url.QueryEscape(next)); r.Status != http.StatusBadRequest {
			t.Fatalf("next %q: %d", next, r.Status)
		}
	}

	// One organization: straight in, back to next.
	back := signIn(t, b, p, viewer, "next="+url.QueryEscape("/docks?x=1"), nil)
	if back.Location != p.srv.URL+"/docks?x=1" {
		t.Fatalf("callback redirect: %q", back.Location)
	}
	sid := b.cookie(p.srv.URL, httpx.SessionCookie)
	if sid == "" || b.cookie(p.srv.URL, "seaport_oauth") != "" {
		t.Fatal("session cookie not set or pending cookie kept")
	}
	s := currentSession(t, b, p)
	if !s.Authenticated || s.Subject != viewer.ID || s.Email != viewer.Email || s.Name != viewer.Name ||
		s.Organization != orgA.ID || s.OrganizationName != orgA.Name || s.ExpiresAt == nil ||
		len(s.Permissions) != 2 || !permission.Has(s.Permissions, permission.DocksRead) || !permission.Has(s.Permissions, permission.CrewRead) {
		t.Fatalf("session: %+v", s)
	}
	// The cookie works; permissions apply (403).
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/docks", nil, nil); code != http.StatusOK {
		t.Fatalf("docks: %d", code)
	}
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/conversations", nil, nil); code != http.StatusForbidden {
		t.Fatalf("conversations without permission: %d", code)
	}
	// Cookie writes from another site are refused (CSRF).
	req, _ := http.NewRequest(http.MethodDelete, p.srv.URL+"/api/v1/session", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(&http.Cookie{Name: httpx.SessionCookie, Value: sid})
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site sign-out: %v %v", err, res)
	}

	// Refresh: make the stored access token due; concurrent requests
	// refresh once (rotated refresh token) and all succeed.
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sealer, _ := auth.NewSealer(cfg.SecretKey)
	repo := authpg.New(db, sealer)
	hash := auth.HashSessionID(sid)
	before, err := repo.Update(ctx, hash, func(cur auth.Session) (auth.Session, error) {
		cur.Tokens.ExpiresAt = time.Now().Add(-time.Second)
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = withCookie(t, http.MethodGet, p.srv.URL+"/api/v1/docks", sid)
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("request %d during refresh: %d (all %v)", i, c, codes)
		}
	}
	after, err := repo.Find(ctx, hash)
	if err != nil || after.Tokens.Refresh == before.Tokens.Refresh || after.Tokens.Access == before.Tokens.Access ||
		!after.Tokens.ExpiresAt.After(time.Now().Add(auth.RefreshAhead)) {
		t.Fatalf("session after refresh: %v (refresh rotated %v)", err, after.Tokens.Refresh != before.Tokens.Refresh)
	}

	// Sign-out: session gone, refresh token revoked at IAMKit, the browser
	// is sent to IAMKit's end_session (back to the port after).
	var out struct {
		LogoutURL string `json:"logout_url"`
	}
	if code := b.api(http.MethodDelete, p.srv.URL+"/api/v1/session", nil, &out); code != http.StatusOK ||
		!strings.HasPrefix(out.LogoutURL, iam.base+"/oauth/end_session?") || !strings.Contains(out.LogoutURL, url.QueryEscape(p.srv.URL+"/")) {
		t.Fatalf("logout: %d %+v", code, out)
	}
	if b.cookie(p.srv.URL, httpx.SessionCookie) != "" {
		t.Fatal("session cookie kept after sign-out")
	}
	if _, err := repo.Find(ctx, hash); err == nil {
		t.Fatal("session row kept after sign-out")
	}
	if code := iamRefresh(t, cfg, after.Tokens.Refresh); code == http.StatusOK {
		t.Fatal("refresh token still valid at IAMKit after sign-out")
	}
	if code := withCookie(t, http.MethodGet, p.srv.URL+"/api/v1/docks", sid); code != http.StatusUnauthorized {
		t.Fatalf("old cookie after sign-out: %d", code)
	}

	// Two organizations: the chooser picks one; switch=1 and organization=
	// move between them.
	multi := newIAMUser(t, []string{permission.DocksRead}, orgA, orgB)
	b = newBrowser(t)
	signIn(t, b, p, multi, "", &orgB)
	if s := currentSession(t, b, p); s.Organization != orgB.ID || s.OrganizationName != orgB.Name {
		t.Fatalf("chose org B: %+v", s)
	}
	signIn(t, b, p, multi, "switch=1", &orgA)
	if s := currentSession(t, b, p); s.Organization != orgA.ID {
		t.Fatalf("switched to org A: %+v", s)
	}
	signIn(t, b, p, multi, "organization="+orgB.ID, nil) // hinted: no chooser
	if s := currentSession(t, b, p); s.Organization != orgB.ID {
		t.Fatalf("organization hint: %+v", s)
	}
	// A user who isn't a member of the hinted organization can't sign in there.
	b = newBrowser(t)
	ticket, _ := hostedLogin(t, b, p, "organization="+orgB.ID)
	done := b.post(iam.base+"/hosted/login/password", url.Values{"ticket": {ticket}, "email": {viewer.Email}, "password": {iamPassword}})
	if done.Status == http.StatusSeeOther && strings.HasPrefix(done.Location, p.srv.URL) {
		t.Fatalf("non-member signed in to org B: %q", done.Location)
	}

	// Callback failure paths: back to the app with a message, no session.
	b = newBrowser(t)
	if msg := signinError(t, p, b.get(p.srv.URL+"/auth/callback?code=x&state=y")); !strings.Contains(msg, "start again") {
		t.Fatalf("callback without pending cookie: %q", msg)
	}
	_, state := hostedLogin(t, b, p, "")
	if msg := signinError(t, p, b.get(p.srv.URL+"/auth/callback?code=x&state=forged")); !strings.Contains(msg, "start again") {
		t.Fatalf("forged state: %q", msg)
	}
	_, state = hostedLogin(t, b, p, "")
	signinError(t, p, b.get(p.srv.URL+"/auth/callback?error=access_denied&state="+state))
	// The error code comes from the URL: unknown text is never echoed.
	_, state = hostedLogin(t, b, p, "")
	if msg := signinError(t, p, b.get(p.srv.URL+"/auth/callback?state="+state+"&error="+url.QueryEscape("Call +1 555 to unlock"))); msg != "sign-in failed" {
		t.Fatalf("crafted callback error shown: %q", msg)
	}
	_, state = hostedLogin(t, b, p, "")
	signinError(t, p, b.get(p.srv.URL+"/auth/callback?code=forged-code&state="+state))
	if b.cookie(p.srv.URL, httpx.SessionCookie) != "" {
		t.Fatal("failed callbacks set a session")
	}
	// A code is single use: replaying a finished callback fails.
	b = newBrowser(t)
	ticket, _ = hostedLogin(t, b, p, "")
	pendingCookie := b.cookie(p.srv.URL, "seaport_oauth")
	done = b.post(iam.base+"/hosted/login/password", url.Values{"ticket": {ticket}, "email": {viewer.Email}, "password": {iamPassword}})
	if r := b.get(done.Location); r.Status != http.StatusSeeOther || b.cookie(p.srv.URL, httpx.SessionCookie) == "" {
		t.Fatalf("first callback: %d %q", r.Status, r.Location)
	}
	replay := newBrowser(t)
	replay.cookies[p.srv.URL] = map[string]*http.Cookie{"seaport_oauth": {Name: "seaport_oauth", Value: pendingCookie}}
	signinError(t, p, replay.get(done.Location))
}

// iamRefresh redeems a refresh token at IAMKit's token endpoint.
func iamRefresh(t *testing.T, cfg config.Server, refresh string) int {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {cfg.IAMKit.WebClientID}, "refresh_token": {refresh}}
	req, _ := http.NewRequest(http.MethodPost, iam.base+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", freshIP())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

// TestIAMBearers: IAMKit access tokens and organization API keys as admin
// bearers; garbage, revoked keys and crew tokens are refused.
func TestIAMBearers(t *testing.T) {
	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	u := newIAMUser(t, []string{permission.DocksRead, permission.DocksWrite}, org)
	access := accessToken(t, u, org)

	var d dock.Dock
	if code := p.call(http.MethodPost, "/api/v1/docks", access, map[string]string{"channel": "api", "name": "token"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock with access token: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/conversations", access, nil, nil); code != http.StatusForbidden {
		t.Fatalf("conversations without permission: %d", code)
	}

	// API key (what the CLI uses): its own permissions, its organization.
	key := newOrgAPIKey(t, org, permission.DocksRead)
	var docks query.Paginated[dock.Dock]
	if code := p.call(http.MethodGet, "/api/v1/docks", key.Value, nil, &docks); code != http.StatusOK || len(docks.Items) != 1 {
		t.Fatalf("docks with API key: %d %+v", code, docks)
	}
	if code := p.call(http.MethodPost, "/api/v1/docks", key.Value, map[string]string{"channel": "api", "name": "k"}, nil); code != http.StatusForbidden {
		t.Fatalf("API key without docks:write: %d", code)
	}
	full := newOrgAPIKey(t, org, permission.All...)
	// The inbox preview is message content: hidden without ships:read.
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", full.Value,
		map[string]any{"chat_id": "222", "message_id": "p1", "sender_id": "222", "text": "secreto"}, nil); code != http.StatusCreated {
		t.Fatalf("inject: %d", code)
	}
	for _, c := range []struct {
		key     string
		preview bool
	}{{newOrgAPIKey(t, org, permission.ConversationsRead).Value, false}, {full.Value, true}} {
		var inbox query.Paginated[ship.InboxItem]
		if code := p.call(http.MethodGet, "/api/v1/inbox", c.key, nil, &inbox); code != http.StatusOK || len(inbox.Items) != 1 ||
			(inbox.Items[0].Last != nil) != c.preview {
			t.Fatalf("inbox (preview %v): %d %+v", c.preview, code, inbox)
		}
	}
	ci := p.agentAs(full.Value, "ci")
	if ci.CreatedBy != "API key "+full.Name {
		t.Fatalf("agent by API key: %+v", ci)
	}
	var issued crew.Issued
	if code := p.call(http.MethodPost, "/api/v1/crew/tokens", full.Value, map[string]any{"name": "ci", "agent_id": ci.ID}, &issued); code != http.StatusCreated ||
		issued.CreatedBy != "API key "+full.Name {
		t.Fatalf("crew token by API key: %d %+v", code, issued)
	}
	// A revoked key stops working.
	mustManage(t, "DELETE", envPath("/api-keys/"+key.ID), nil, nil)
	if code := p.call(http.MethodGet, "/api/v1/docks", key.Value, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked API key: %d", code)
	}

	for _, bad := range []string{"garbage", "ik_key_" + strings.Repeat("x", 40), access + "x", issued.Value} {
		if code := p.call(http.MethodGet, "/api/v1/docks", bad, nil, nil); code != http.StatusUnauthorized {
			t.Fatalf("bearer %.16q…: %d", bad, code)
		}
	}
	// An admin credential is not a crew token.
	for _, admin := range []string{access, full.Value} {
		if code := p.call(http.MethodPost, "/mcp", admin, map[string]any{}, nil); code != http.StatusUnauthorized {
			t.Fatalf("admin credential on /mcp: %d", code)
		}
	}

	// The manifest's roles, assigned in IAMKit, give exactly their permissions.
	for _, role := range permission.Roles {
		b := newBrowser(t)
		u := newIAMRoleUser(t, org, role.Name)
		signIn(t, b, p, u, "", nil)
		got := currentSession(t, b, p).Permissions
		if len(got) != len(role.Permissions) {
			t.Fatalf("role %s: permissions %v, want %v", role.Name, got, role.Permissions)
		}
		for _, perm := range role.Permissions {
			if !permission.Has(got, perm) {
				t.Fatalf("role %s: missing %s in %v", role.Name, perm, got)
			}
		}
		// A token made in the app records the person's name.
		if permission.Has(got, permission.CrewTokensWrite) {
			var made crew.Issued
			if code := b.api(http.MethodPost, p.srv.URL+"/api/v1/crew/tokens", map[string]any{"name": "web", "agent_id": ci.ID}, &made); code != http.StatusCreated || made.CreatedBy != u.Name {
				t.Fatalf("crew token from the app: %d created_by %q, want %q", code, made.CreatedBy, u.Name)
			}
		}
	}
}

// adminRoute is one guarded admin route with a request that reaches its
// handler (ids of org A's objects).
type adminRoute struct {
	method, path, pattern string
	body                  any
	perm                  string
}

// TestIAMOrganizationsAndPermissions: with real IAMKit identities, every
// guarded admin route answers 403 without its permission and 404 for
// another organization's object; lists, the live stream and agents stay in
// their organization. The route list is checked against the code, so a
// new route must be added here.
func TestIAMOrganizationsAndPermissions(t *testing.T) {
	p, _ := startIAMPort(t)
	orgA, orgB := newIAMOrg(t), newIAMOrg(t)
	adminA := accessToken(t, newIAMUser(t, permission.All, orgA), orgA)
	adminB := accessToken(t, newIAMUser(t, permission.All, orgB), orgB)

	// Org B streams events from the start.
	eventsB := p.openEventsAs(adminB, "")

	// Org A: a dock, an allowed conversation, a rule, a crew token and agent.
	var d dock.Dock
	if code := p.call(http.MethodPost, "/api/v1/docks", adminA, map[string]string{"channel": "api", "name": "a"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	var s ship.Ship
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", adminA,
		map[string]any{"chat_id": "111", "message_id": "m1", "sender_id": "111", "text": "hola"}, &s); code != http.StatusCreated {
		t.Fatalf("inject: %d", code)
	}
	conv := s.Conversation.String()
	if code := p.call(http.MethodPatch, "/api/v1/conversations/"+conv, adminA, map[string]string{"access": "allow"}, nil); code != http.StatusOK {
		t.Fatalf("allow: %d", code)
	}
	var rule conversation.Rule
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/customs/rules", adminA,
		map[string]string{"effect": "deny", "pattern": "+51*"}, &rule); code != http.StatusCreated {
		t.Fatalf("add rule: %d", code)
	}
	agentA, agentB := p.agentAs(adminA, "a"), p.agentAs(adminB, "b")
	p.assignAs(adminA, d.ID, agentA.ID, false)
	tokA, tokB := p.issueAs(adminA, agentA.ID, "a"), p.issueAs(adminB, agentB.ID, "b")
	crewB := p.dialCrewAs(tokB.Value, crew.Frame{Type: crew.FrameHello, Agent: "b", Concurrency: 2})
	crewA := p.dialCrewAs(tokA.Value, crew.Frame{Type: crew.FrameHello, Agent: "a", Concurrency: 2})
	// m1 (released when the chat was allowed) and m2 go to org A's agent only.
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", adminA,
		map[string]any{"chat_id": "111", "message_id": "m2", "sender_id": "111", "text": "sigues?"}, nil); code != http.StatusCreated {
		t.Fatalf("inject m2: %d", code)
	}
	if f := crewA.read(); f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID != s.Conversation {
		t.Fatalf("org A delivery: %+v", f)
	}
	crewB.quiet(300 * time.Millisecond)

	var envA hosting.Environment
	if code := p.call(http.MethodPost, "/api/v1/environments", adminA, map[string]any{"name": "a", "spec": map[string]string{"harness": "rness"}}, &envA); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	dk, cv, ag, env := d.ID.String(), conv, agentA.ID.String(), envA.ID.String()
	envSpec := map[string]any{"base": 1, "spec": map[string]string{"harness": "rness"}}
	home := "sha256:" + strings.Repeat("a", 64)
	inject := map[string]any{"chat_id": "9", "message_id": "z", "sender_id": "9", "text": "hi"}
	routes := []adminRoute{
		{"GET", "/api/v1/docks", "GET /api/v1/docks", nil, permission.DocksRead},
		{"POST", "/api/v1/docks", "POST /api/v1/docks", map[string]string{"channel": "api", "name": "x"}, permission.DocksWrite},
		{"GET", "/api/v1/docks/" + dk, "GET /api/v1/docks/{id}", nil, permission.DocksRead},
		{"PATCH", "/api/v1/docks/" + dk, "PATCH /api/v1/docks/{id}", map[string]string{"name": "x"}, permission.DocksWrite},
		{"POST", "/api/v1/docks/" + dk + "/relink", "POST /api/v1/docks/{id}/relink", nil, permission.DocksWrite},
		{"POST", "/api/v1/docks/" + dk + "/unlink", "POST /api/v1/docks/{id}/unlink", nil, permission.DocksWrite},
		{"DELETE", "/api/v1/docks/" + dk, "DELETE /api/v1/docks/{id}", nil, permission.DocksWrite},
		{"POST", "/api/v1/docks/" + dk + "/ships", "POST /api/v1/docks/{id}/ships", inject, permission.ShipsWrite},
		{"GET", "/api/v1/inbox", "GET /api/v1/inbox", nil, permission.ConversationsRead},
		{"GET", "/api/v1/conversations", "GET /api/v1/conversations", nil, permission.ConversationsRead},
		{"GET", "/api/v1/conversations/" + cv, "GET /api/v1/conversations/{id}", nil, permission.ConversationsRead},
		{"PATCH", "/api/v1/conversations/" + cv, "PATCH /api/v1/conversations/{id}", map[string]string{"access": "deny"}, permission.ConversationsWrite},
		{"GET", "/api/v1/conversations/" + cv + "/ships", "GET /api/v1/conversations/{id}/ships", nil, permission.ShipsRead},
		{"POST", "/api/v1/conversations/" + cv + "/ships", "POST /api/v1/conversations/{id}/ships", map[string]string{"text": "hi"}, permission.ShipsWrite},
		{"GET", "/api/v1/attachments/" + identity.NewAttachmentID().String(), "GET /api/v1/attachments/{id}", nil, permission.ShipsRead},
		{"GET", "/api/v1/docks/" + dk + "/customs", "GET /api/v1/docks/{id}/customs", nil, permission.CustomsRead},
		{"PATCH", "/api/v1/docks/" + dk + "/customs/policy", "PATCH /api/v1/docks/{id}/customs/policy", map[string]string{"default": "allow"}, permission.CustomsWrite},
		{"POST", "/api/v1/docks/" + dk + "/customs/rules", "POST /api/v1/docks/{id}/customs/rules", map[string]string{"effect": "allow", "pattern": "+1*"}, permission.CustomsWrite},
		{"DELETE", "/api/v1/customs/rules/" + rule.ID.String(), "DELETE /api/v1/customs/rules/{id}", nil, permission.CustomsWrite},
		{"GET", "/api/v1/crew", "GET /api/v1/crew", nil, permission.CrewRead},
		{"GET", "/api/v1/crew/tokens", "GET /api/v1/crew/tokens", nil, permission.CrewTokensRead},
		{"POST", "/api/v1/crew/tokens", "POST /api/v1/crew/tokens", map[string]any{"name": "x", "agent_id": ag}, permission.CrewTokensWrite},
		{"GET", "/api/v1/agents", "GET /api/v1/agents", nil, permission.AgentsRead},
		{"POST", "/api/v1/agents", "POST /api/v1/agents", map[string]string{"name": "x"}, permission.AgentsWrite},
		{"GET", "/api/v1/agents/" + ag, "GET /api/v1/agents/{id}", nil, permission.AgentsRead},
		{"PATCH", "/api/v1/agents/" + ag, "PATCH /api/v1/agents/{id}", map[string]string{"description": "x"}, permission.AgentsWrite},
		{"DELETE", "/api/v1/agents/" + ag, "DELETE /api/v1/agents/{id}", nil, permission.AgentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/profile", "GET /api/v1/agents/{id}/profile", nil, permission.AgentsRead},
		{"PUT", "/api/v1/agents/" + ag + "/profile", "PUT /api/v1/agents/{id}/profile", map[string]int{"base": 0}, permission.AgentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/profile/versions", "GET /api/v1/agents/{id}/profile/versions", nil, permission.AgentsRead},
		{"GET", "/api/v1/agents/" + ag + "/profile/versions/1", "GET /api/v1/agents/{id}/profile/versions/{version}", nil, permission.AgentsRead},
		{"GET", "/api/v1/agents/" + ag + "/secrets", "GET /api/v1/agents/{id}/secrets", nil, permission.AgentsRead},
		{"PUT", "/api/v1/agents/" + ag + "/secrets/TOKEN", "PUT /api/v1/agents/{id}/secrets/{name}", map[string]string{"value": "x"}, permission.AgentsWrite},
		{"DELETE", "/api/v1/agents/" + ag + "/secrets/TOKEN", "DELETE /api/v1/agents/{id}/secrets/{name}", nil, permission.AgentsWrite},
		{"GET", "/api/v1/docks/" + dk + "/agents", "GET /api/v1/docks/{dock}/agents", nil, permission.AgentsRead},
		{"PUT", "/api/v1/docks/" + dk + "/agents/" + ag, "PUT /api/v1/docks/{dock}/agents/{id}", map[string]bool{"default": true}, permission.AgentsWrite},
		{"DELETE", "/api/v1/docks/" + dk + "/agents/" + ag, "DELETE /api/v1/docks/{dock}/agents/{id}", nil, permission.AgentsWrite},
		{"GET", "/api/v1/environments", "GET /api/v1/environments", nil, permission.EnvironmentsRead},
		{"POST", "/api/v1/environments", "POST /api/v1/environments", map[string]any{"name": "x", "spec": map[string]string{"harness": "rness"}}, permission.EnvironmentsWrite},
		{"GET", "/api/v1/environments/" + env, "GET /api/v1/environments/{id}", nil, permission.EnvironmentsRead},
		{"PUT", "/api/v1/environments/" + env, "PUT /api/v1/environments/{id}", envSpec, permission.EnvironmentsWrite},
		{"DELETE", "/api/v1/environments/" + env, "DELETE /api/v1/environments/{id}", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/environments/" + env + "/versions", "GET /api/v1/environments/{id}/versions", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/environments/" + env + "/versions/1", "GET /api/v1/environments/{id}/versions/{version}", nil, permission.EnvironmentsRead},
		{"POST", "/api/v1/environments/" + env + "/build", "POST /api/v1/environments/{id}/build", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/environments/" + env + "/build", "GET /api/v1/environments/{id}/build", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/environments/" + env + "/versions/1/build", "GET /api/v1/environments/{id}/versions/{version}/build", nil, permission.EnvironmentsRead},
		{"POST", "/api/v1/homes?harness=rness", "POST /api/v1/homes", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/homes/rules", "GET /api/v1/homes/rules", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/homes/" + home, "GET /api/v1/homes/{digest}", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/homes/" + home + "/archive", "GET /api/v1/homes/{digest}/archive", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/homes/" + home + "/files/init.lua", "GET /api/v1/homes/{digest}/files/{path...}", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/hosting", "GET /api/v1/hosting", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/agents/" + ag + "/hosting", "GET /api/v1/agents/{id}/hosting", nil, permission.EnvironmentsRead},
		{"PUT", "/api/v1/agents/" + ag + "/hosting", "PUT /api/v1/agents/{id}/hosting", map[string]string{"environment": env}, permission.EnvironmentsWrite},
		{"DELETE", "/api/v1/agents/" + ag + "/hosting", "DELETE /api/v1/agents/{id}/hosting", nil, permission.EnvironmentsWrite},
		{"POST", "/api/v1/agents/" + ag + "/hosting/slots/1/start", "POST /api/v1/agents/{id}/hosting/slots/{slot}/start", nil, permission.EnvironmentsWrite},
		{"POST", "/api/v1/agents/" + ag + "/hosting/slots/1/stop", "POST /api/v1/agents/{id}/hosting/slots/{slot}/stop", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/hosting/egress", "GET /api/v1/agents/{id}/hosting/egress", nil, permission.EnvironmentsRead},
		{"GET", "/api/v1/agents/" + ag + "/hosting/slots/1/logs", "GET /api/v1/agents/{id}/hosting/slots/{slot}/logs", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/usage", "GET /api/v1/agents/{id}/usage", nil, permission.AgentsRead},
		{"POST", "/api/v1/agents/" + ag + "/hosting/slots/1/login", "POST /api/v1/agents/{id}/hosting/slots/{slot}/login", nil, permission.EnvironmentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/hosting/slots/1/login", "GET /api/v1/agents/{id}/hosting/slots/{slot}/login", nil, permission.EnvironmentsWrite},
		{"POST", "/api/v1/agents/" + ag + "/hosting/slots/1/login/code", "POST /api/v1/agents/{id}/hosting/slots/{slot}/login/code", map[string]string{"code": "x"}, permission.EnvironmentsWrite},
		{"GET", "/api/v1/agents/" + ag + "/hosting/slots/1/account", "GET /api/v1/agents/{id}/hosting/slots/{slot}/account", nil, permission.EnvironmentsRead},
		{"DELETE", "/api/v1/crew/tokens/" + tokA.ID.String(), "DELETE /api/v1/crew/tokens/{id}", nil, permission.CrewTokensWrite},
	}
	// The list covers every guarded route, with the permission the code checks.
	idents := catalogIdents(t)
	inCode := guardedRoutes(sources(t, false))
	listed := map[string]bool{}
	for _, r := range routes {
		listed[r.pattern] = true
		if ident, ok := inCode[r.pattern]; !ok {
			t.Errorf("%s is not a guarded route in the code", r.pattern)
		} else if idents[ident] != r.perm {
			t.Errorf("%s: the code checks %s, the test expects %s", r.pattern, idents[ident], r.perm)
		}
	}
	for pattern := range inCode {
		if !listed[pattern] {
			t.Errorf("guarded route %s is missing from this test", pattern)
		}
	}

	// 403 with every permission but the route's: one user per permission.
	without := map[string]string{}
	for _, perm := range permission.All {
		var others []string
		for _, o := range permission.All {
			if o != perm {
				others = append(others, o)
			}
		}
		without[perm] = accessToken(t, newIAMUser(t, others, orgA), orgA)
	}
	for _, r := range routes {
		if code := p.call(r.method, r.path, without[r.perm], r.body, nil); code != http.StatusForbidden {
			t.Errorf("%s without %s: %d, want 403", r.pattern, r.perm, code)
		}
	}

	// 404 for org B's admin on every route naming one of org A's objects.
	for _, r := range routes {
		if !strings.Contains(r.pattern, "{id}") && !strings.Contains(r.pattern, "{dock}") {
			continue
		}
		if code := p.call(r.method, r.path, adminB, r.body, nil); code != http.StatusNotFound {
			t.Errorf("org B %s %s: %d, want 404", r.method, r.path, code)
		}
	}
	var docks query.Paginated[dock.Dock]
	if code := p.call(http.MethodGet, "/api/v1/docks", adminB, nil, &docks); code != http.StatusOK || len(docks.Items) != 0 {
		t.Fatalf("org B docks: %d %+v", code, docks.Items)
	}
	var inbox query.Paginated[ship.InboxItem]
	if code := p.call(http.MethodGet, "/api/v1/inbox", adminB, nil, &inbox); code != http.StatusOK || len(inbox.Items) != 0 {
		t.Fatalf("org B inbox: %d", code)
	}
	var toks query.Paginated[crew.Token]
	if code := p.call(http.MethodGet, "/api/v1/crew/tokens", adminB, nil, &toks); code != http.StatusOK || len(toks.Items) != 1 || toks.Items[0].ID != tokB.ID {
		t.Fatalf("org B crew tokens: %d %+v", code, toks.Items)
	}
	var statusB crew.Status
	if code := p.call(http.MethodGet, "/api/v1/crew", adminB, nil, &statusB); code != http.StatusOK || len(statusB.Agents) != 1 || statusB.Agents[0].Name != "b" {
		t.Fatalf("org B crew: %d %+v", code, statusB)
	}
	var still dock.Dock
	if code := p.call(http.MethodGet, "/api/v1/docks/"+dk, adminA, nil, &still); code != http.StatusOK || still.Name != "a" {
		t.Fatalf("org A dock after org B attempts: %d %+v", code, still)
	}

	// Org B's agent can't use org A's conversation through MCP.
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "b", Version: "1"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: p.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{tokB.Value}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, tool := range []string{"reply", "history"} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"conversation_id": conv, "text": "hi"}})
		if err != nil || res == nil || !res.IsError {
			t.Errorf("org B %s on org A conversation: %v %+v", tool, err, res)
		}
	}

	// Org B's stream saw nothing of org A.
	deadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case e, ok := <-eventsB:
			if !ok {
				break drain
			}
			if raw := string(e.Data); strings.Contains(raw, dk) || strings.Contains(raw, conv) {
				t.Errorf("org B received org A event %s %s", e.Type, raw)
			}
		case <-deadline:
			break drain
		}
	}

	// Revoking org A's crew token closes its agent.
	if code := p.call(http.MethodDelete, "/api/v1/crew/tokens/"+tokA.ID.String(), adminA, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	select {
	case _, ok := <-crewA.frames:
		for ok {
			_, ok = <-crewA.frames
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked crew token's connection still open")
	}
}

// TestIAMEventsFollowPermissions: the live stream sends an admin only the
// event types its permissions can read.
func TestIAMEventsFollowPermissions(t *testing.T) {
	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	admin := accessToken(t, newIAMUser(t, permission.All, org), org)
	docksOnly := accessToken(t, newIAMUser(t, []string{permission.DocksRead}, org), org)
	events := p.openEventsAs(docksOnly, "")

	var d dock.Dock
	if code := p.call(http.MethodPost, "/api/v1/docks", admin, map[string]string{"channel": "api", "name": "a"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", admin,
		map[string]any{"chat_id": "111", "message_id": "m1", "sender_id": "111", "text": "hola"}, nil); code != http.StatusCreated {
		t.Fatalf("inject: %d", code)
	}
	if code := p.call(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), admin, map[string]string{"name": "b"}, nil); code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	deadline := time.After(3 * time.Second)
	for sawRename := false; !sawRename; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			if perm, known := bootstrap.EventPermissions[e.Type]; known && perm != permission.DocksRead {
				t.Errorf("docks:read-only admin received %s", e.Type)
			}
			sawRename = e.Type == dock.EventUpdated && strings.Contains(string(e.Data), `"name":"b"`)
		case <-deadline:
			t.Fatal("no dock.updated event")
		}
	}
}
