package bootstrap_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two organizations on one port (what iam mode does), each with its own
// admin and crew token.
var (
	orgA = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000a")
	orgB = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000b")
)

const (
	adminA = "admin-a-0123456789"
	adminB = "admin-b-0123456789"
	crewA  = "crew-a-0123456789"
	crewB  = "crew-b-0123456789"
)

// twoOrgs authenticates bearer tokens of two organizations.
type twoOrgs struct{}

func (twoOrgs) principal(r *http.Request, admin bool) (httpx.Principal, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	byToken := map[string]httpx.Principal{
		adminA: {Organization: orgA, Role: httpx.RoleAdmin, Subject: "a", Permissions: permission.All},
		adminB: {Organization: orgB, Role: httpx.RoleAdmin, Subject: "b", Permissions: permission.All},
		crewA:  {Organization: orgA, Role: httpx.RoleCrew, Subject: "a"},
		crewB:  {Organization: orgB, Role: httpx.RoleCrew, Subject: "b"},
	}
	p, ok := byToken[tok]
	return p, ok && (p.Role == httpx.RoleAdmin) == admin
}

func (a twoOrgs) guard(admin bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.principal(r, admin)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(httpx.WithPrincipal(r.Context(), p)))
	})
}

func (a twoOrgs) Admin(next http.Handler) http.Handler { return a.guard(true, next) }
func (a twoOrgs) Crew(next http.Handler) http.Handler  { return a.guard(false, next) }
func (twoOrgs) Session() httpx.SessionRoutes {
	nf := http.HandlerFunc(httpx.NotFound)
	return httpx.SessionRoutes{Login: nf, Current: nf, Logout: nf}
}

func TestOrganizationsAreIsolated(t *testing.T) {
	p := startPortWith(t, func(*config.Server) {}, bootstrap.Options{Authenticator: twoOrgs{}})
	agentB := p.agentAs(adminB, "b") // B's own agent.updated may reach its stream

	// Org B's admin streams events: it must never see org A's.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminB)
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("events: %v %v", err, stream)
	}
	defer stream.Body.Close()
	eventsB := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			eventsB <- sc.Text()
		}
		close(eventsB)
	}()

	// Org A: a dock, an allowed conversation, a crew member with a delivery.
	var d dock.Dock
	var agents query.Paginated[agent.Agent]
	if code := p.call(http.MethodPost, "/api/v1/docks", adminA, map[string]string{"channel": "api", "name": "a"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	agentA := p.agentAs(adminA, "a")
	p.assignAs(adminA, d.ID, agentA.ID, false)
	// Agents are per organization: B's admin can't put A's agent on a dock
	// or see it, and B's crew can't work as it.
	if code := p.call(http.MethodGet, "/api/v1/agents", adminB, nil, &agents); code != http.StatusOK || len(agents.Items) != 1 || agents.Items[0].Name != "b" {
		t.Fatalf("org B agents: %d %+v", code, agents)
	}
	if reason := p.dialRefused(crewB, crew.Frame{Type: crew.FrameHello, As: "a"}); !strings.Contains(reason, "not found") {
		t.Fatalf("org B crew as org A agent: %q", reason)
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

	// Org B's crew connects first; org A's ships must not reach it.
	crewConnB := p.dialCrewAs(crewB, crew.Frame{Type: crew.FrameHello, Agent: "b", As: "b", Concurrency: 2})
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", adminA,
		map[string]any{"chat_id": "111", "message_id": "m2", "sender_id": "111", "text": "sigues?"}, nil); code != http.StatusCreated {
		t.Fatalf("inject m2: %d", code)
	}
	crewConnB.quiet(500 * time.Millisecond)
	crewConnA := p.dialCrewAs(crewA, crew.Frame{Type: crew.FrameHello, Agent: "a", As: "a", Concurrency: 2})
	if f := crewConnA.read(); f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID != s.Conversation {
		t.Fatalf("org A delivery: %+v", f)
	}

	// Org B's admin sees nothing of org A: lists are empty, ids are 404.
	var docks query.Paginated[dock.Dock]
	if code := p.call(http.MethodGet, "/api/v1/docks", adminB, nil, &docks); code != http.StatusOK || len(docks.Items) != 0 {
		t.Fatalf("org B docks: %d %+v", code, docks.Items)
	}
	var inbox query.Paginated[ship.InboxItem]
	if code := p.call(http.MethodGet, "/api/v1/inbox", adminB, nil, &inbox); code != http.StatusOK || len(inbox.Items) != 0 {
		t.Fatalf("org B inbox: %d %+v", code, inbox.Items)
	}
	var statusB crew.Status
	if code := p.call(http.MethodGet, "/api/v1/crew", adminB, nil, &statusB); code != http.StatusOK ||
		len(statusB.Agents) != 1 || statusB.Agents[0].Name != "b" {
		t.Fatalf("org B crew: %d %+v", code, statusB)
	}
	inject := map[string]any{"chat_id": "9", "message_id": "z", "sender_id": "9", "text": "hi"}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/docks/" + d.ID.String(), nil},
		{http.MethodPatch, "/api/v1/docks/" + d.ID.String(), map[string]string{"name": "x"}},
		{http.MethodPost, "/api/v1/docks/" + d.ID.String() + "/unlink", nil},
		{http.MethodPost, "/api/v1/docks/" + d.ID.String() + "/relink", nil},
		{http.MethodDelete, "/api/v1/docks/" + d.ID.String(), nil},
		{http.MethodGet, "/api/v1/docks/" + d.ID.String() + "/customs", nil},
		{http.MethodPatch, "/api/v1/docks/" + d.ID.String() + "/customs/policy", map[string]string{"default": "allow"}},
		{http.MethodPost, "/api/v1/docks/" + d.ID.String() + "/customs/rules", map[string]string{"effect": "allow", "pattern": "+1*"}},
		{http.MethodPost, "/api/v1/docks/" + d.ID.String() + "/ships", inject},
		{http.MethodGet, "/api/v1/conversations/" + conv, nil},
		{http.MethodPatch, "/api/v1/conversations/" + conv, map[string]string{"access": "deny"}},
		{http.MethodGet, "/api/v1/conversations/" + conv + "/ships", nil},
		{http.MethodPost, "/api/v1/conversations/" + conv + "/ships", map[string]string{"text": "hi"}},
		{http.MethodGet, "/api/v1/agents/" + agentA.ID.String(), nil},
		{http.MethodPatch, "/api/v1/agents/" + agentA.ID.String(), map[string]string{"description": "x"}},
		{http.MethodDelete, "/api/v1/agents/" + agentA.ID.String(), nil},
		{http.MethodGet, "/api/v1/docks/" + d.ID.String() + "/agents", nil},
		{http.MethodPut, "/api/v1/docks/" + d.ID.String() + "/agents/" + agentA.ID.String(), map[string]bool{"default": true}},
		{http.MethodDelete, "/api/v1/docks/" + d.ID.String() + "/agents/" + agentA.ID.String(), nil},
	} {
		if code := p.call(c.method, c.path, adminB, c.body, nil); code != http.StatusNotFound {
			t.Errorf("org B %s %s: %d, want 404", c.method, c.path, code)
		}
	}
	// Org A still has its dock and conversation untouched.
	var still dock.Dock
	if code := p.call(http.MethodGet, "/api/v1/docks/"+d.ID.String(), adminA, nil, &still); code != http.StatusOK || still.Name != "a" {
		t.Fatalf("org A dock after org B attempts: %d %+v", code, still)
	}
	// A rule of org A can't be removed by org B.
	var rule conversation.Rule
	if code := p.call(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/customs/rules", adminA,
		map[string]string{"effect": "deny", "pattern": "+51*"}, &rule); code != http.StatusCreated {
		t.Fatalf("add rule: %d", code)
	}
	if code := p.call(http.MethodDelete, "/api/v1/customs/rules/"+rule.ID.String(), adminB, nil, nil); code != http.StatusNotFound {
		t.Errorf("org B removes org A rule: %d", code)
	}

	// Org B's agent can't use org A's conversation through MCP.
	mctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "b", Version: "1"}, nil)
	session, err := client.Connect(mctx, &mcp.StreamableClientTransport{Endpoint: p.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{crewB}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, tool := range []string{"reply", "history"} {
		res, err := session.CallTool(mctx, &mcp.CallToolParams{Name: tool,
			Arguments: map[string]any{"conversation_id": conv, "text": "hi"}})
		if err != nil || res == nil || !res.IsError {
			t.Errorf("org B %s on org A conversation: %v %+v", tool, err, res)
		}
	}
	res, err := session.CallTool(mctx, &mcp.CallToolParams{Name: "conversations", Arguments: map[string]any{}})
	if raw, _ := json.Marshal(res.StructuredContent); err != nil || strings.Contains(string(raw), conv) {
		t.Errorf("org B conversations list org A: %v %s", err, raw)
	}

	cancel()
	for line := range eventsB {
		if strings.HasPrefix(line, "event: ") && !strings.Contains(line, "ready") && !strings.Contains(line, "crew.changed") &&
			!strings.Contains(line, "agent.updated") {
			t.Errorf("org B received %q", line)
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "agent_id") && !strings.Contains(line, agentB.ID.String()) {
			t.Errorf("org B received another agent %q", line)
		}
		if strings.HasPrefix(line, "data: ") && (strings.Contains(line, d.ID.String()) || strings.Contains(line, conv) ||
			strings.Contains(line, agentA.ID.String())) {
			t.Errorf("org B received org A data %q", line)
		}
	}
}

func TestOpenModeNeedsNoCredentials(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) {
		c.Auth, c.AdminToken = config.AuthNone, ""
	}, bootstrap.Options{})

	var cfg map[string]any
	if code := p.call(http.MethodGet, "/api/v1/config", "", nil, &cfg); code != http.StatusOK || cfg["auth"] != "none" {
		t.Fatalf("config: %d %+v", code, cfg)
	}
	var session map[string]any
	if code := p.call(http.MethodGet, "/api/v1/session", "", nil, &session); code != http.StatusOK || session["authenticated"] != true {
		t.Fatalf("session: %d %+v", code, session)
	}
	var d dock.Dock
	if code := p.call(http.MethodPost, "/api/v1/docks", "", map[string]string{"channel": "api", "name": "local"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock without credentials: %d", code)
	}
	// Agents connect without a token too, naming the agent they work as.
	local := p.agentAs("", "local")
	p.assignAs("", d.ID, local.ID, true)
	if reason := p.dialRefused("", crew.Frame{Type: crew.FrameHello, Agent: "local"}); !strings.Contains(reason, "-agent") {
		t.Fatalf("open mode hello without an agent: %q", reason)
	}
	if reason := p.dialRefused("", crew.Frame{Type: crew.FrameHello, As: "nobody"}); !strings.Contains(reason, "not found") {
		t.Fatalf("open mode hello naming a missing agent: %q", reason)
	}
	c := p.dialCrewAs("", crew.Frame{Type: crew.FrameHello, Agent: "local", As: "local", Concurrency: 1})
	c.quiet(100 * time.Millisecond)
	waitCrew(t, p, func(st crew.Status) bool { return len(st.Agents) == 1 && st.Agents[0].AgentID == local.ID })

	// A browser on another site still can't drive it.
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/api/v1/docks", strings.NewReader(`{"channel":"api","name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site write in open mode: %d", resp.StatusCode)
	}

	// Nor call the agent tools.
	req, _ = http.NewRequest(http.MethodPost, p.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site mcp in open mode: %d", resp.StatusCode)
	}

	// A proxy or tunnel on this machine (Caddy, cloudflared, ngrok...) makes
	// remote callers look local, even with Host rewritten to localhost: an
	// open port refuses anything forwarded.
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "Cf-Connecting-Ip"} {
		for _, path := range []string{"/api/v1/docks", "/api/v1/session", "/mcp", "/crew/ws"} {
			req, _ := http.NewRequest(http.MethodGet, p.srv.URL+path, nil)
			req.Header.Set(h, "203.0.113.7")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("proxied (%s) GET %s: %d", h, path, resp.StatusCode)
			}
		}
	}
	// Loopback spellings still work.
	for _, host := range []string{"LOCALHOST:8740", "localhost.:8740", "[::1]:8740", "127.0.0.2:8740"} {
		req, _ := http.NewRequest(http.MethodGet, p.srv.URL+"/api/v1/docks", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %s: %d", host, resp.StatusCode)
		}
	}

	// DNS rebinding: a foreign hostname pointed at 127.0.0.1 is refused,
	// reads included.
	for _, path := range []string{"/api/v1/docks", "/api/v1/session", "/mcp", "/crew/ws"} {
		req, _ := http.NewRequest(http.MethodGet, p.srv.URL+path, nil)
		req.Host = "evil.example:8740"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("rebinding GET %s: %d", path, resp.StatusCode)
		}
	}
}

func TestHealthzRevealsNoTenantData(t *testing.T) {
	p := startPortWith(t, func(*config.Server) {}, bootstrap.Options{Authenticator: twoOrgs{}})
	p.agentAs(adminA, "a")
	p.dialCrewAs(crewA, crew.Frame{Type: crew.FrameHello, Agent: "a", As: "a", Concurrency: 1})
	var health map[string]any
	if code := p.call(http.MethodGet, "/healthz", "", nil, &health); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}
	for k := range health {
		if k != "status" && k != "version" {
			t.Errorf("healthz exposes %q: %+v", k, health)
		}
	}
}

func TestOpenModeRefusesPublicAddresses(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8740": true, "[::1]:8740": true, "localhost:8740": true,
		"0.0.0.0:8740": false, ":8740": false, "192.168.1.5:8740": false,
	} {
		err := config.Server{DatabaseURL: "postgres://x", Addr: addr, Auth: config.AuthNone}.Validate()
		if (err == nil) != ok {
			t.Errorf("none mode on %s: err=%v", addr, err)
		}
	}
}
