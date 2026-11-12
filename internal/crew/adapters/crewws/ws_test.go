package crewws

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

// presence records the lifecycle of connections in memory.
type presence struct {
	crew.Presence
	mu     sync.Mutex
	open   map[string]bool
	ready  []crew.Agent
	refuse error
}

func (p *presence) Open(_ context.Context, _ identity.OrganizationID, c crew.Connection, _ int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuse != nil {
		return p.refuse
	}
	p.open[c.Session] = true
	return nil
}

func (p *presence) Ready(_ context.Context, _ identity.OrganizationID, _ string, a crew.Agent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = append(p.ready, a)
	return nil
}

func (p *presence) Close(_ context.Context, _ identity.OrganizationID, s string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.open, s)
	return nil
}

func (p *presence) SetDeliveries(context.Context, identity.OrganizationID, string, []crew.AgentDelivery) error {
	return nil
}

func (p *presence) Status(context.Context, identity.OrganizationID) (crew.Status, error) {
	return crew.Status{}, nil
}

func (p *presence) opened() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.open)
}

// idleShips never has work.
type idleShips struct{ ship.Commands }

func (idleShips) Claim(context.Context, identity.OrganizationID, ship.Claim) (ship.Delivery, bool, error) {
	return ship.Delivery{}, false, nil
}

func (idleShips) ReleaseSession(context.Context, identity.OrganizationID, string) (int, error) {
	return 0, nil
}

type noAgents struct{ agent.Queries }

func (noAgents) FindByName(_ context.Context, _ identity.OrganizationID, _ string) (agent.Agent, error) {
	return agent.Agent{}, errx.NotFound("agent not found")
}

type rig struct {
	hub  *Hub
	pres *presence
	addr string
	app  *fiber.App
}

func start(t *testing.T, p *httpx.Principal) *rig {
	t.Helper()
	pres := &presence{open: map[string]bool{}}
	hub := New("test", time.Minute, "http://mcp", nil, 0, pres)
	hub.Bind(idleShips{}, noAgents{})
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(func(c fiber.Ctx) error {
		if p != nil {
			httpx.SetPrincipal(c, *p)
		}
		return c.Next()
	})
	app.Get("/crew/ws", hub.Handle)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	t.Cleanup(func() { hub.Close(); _ = app.ShutdownWithTimeout(time.Second) })
	return &rig{hub: hub, pres: pres, addr: ln.Addr().String(), app: app}
}

func agentPrincipal(name string) *httpx.Principal {
	return &httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: identity.NewAgentID(), AgentName: name, Credential: "tok"}
}

func (r *rig) dial(t *testing.T) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://"+r.addr+"/crew/ws", nil)
}

func read(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func write(t *testing.T, ws *websocket.Conn, s string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out: " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHelloWelcomeAndCleanup(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	ws, _, err := r.dial(t)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws, `{"type":"hello","agent":"laptop","concurrency":2}`)
	if got := read(t, ws); !strings.Contains(got, `"welcome"`) || !strings.Contains(got, "http://mcp") {
		t.Fatalf("welcome: %s", got)
	}
	if r.pres.opened() != 1 || len(r.pres.ready) != 1 || r.pres.ready[0].Name != "laptop" || r.pres.ready[0].Concurrency != 2 {
		t.Fatalf("presence: %d %+v", r.pres.opened(), r.pres.ready)
	}
	ws.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the connection to be forgotten", func() bool { return r.pres.opened() == 0 })
}

func TestRefusedHelloClosesWithPolicyViolationAndReason(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	ws, _, err := r.dial(t)
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws, `{"type":"hello","agent":"x","as":"reviewer"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = ws.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || !strings.Contains(ce.Reason, "belongs to agent pm, not reviewer") {
		t.Fatalf("got %v", err)
	}
	waitFor(t, "the row to go", func() bool { return r.pres.opened() == 0 })
}

func TestFirstFrameMustBeHello(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	ws, _, _ := r.dial(t)
	write(t, ws, `{"type":"drain"}`)
	_, _, err := ws.Read(context.Background())
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || !strings.Contains(ce.Reason, "hello") {
		t.Fatalf("got %v", err)
	}
}

func TestReadLimit(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	ws, _, _ := r.dial(t)
	ws.SetReadLimit(crew.MaxFrame * 2)
	write(t, ws, `{"type":"hello","agent":"laptop"}`)
	read(t, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", crew.MaxFrame+1)))
	_, _, err := ws.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusMessageTooBig {
		t.Fatalf("got %v", err)
	}
	waitFor(t, "the connection to be forgotten", func() bool { return r.pres.opened() == 0 })
}

func TestRefusedBeforeTheUpgrade(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	r.pres.refuse = errx.QuotaExceeded("connections", 1, "too many connections")
	_, resp, err := r.dial(t)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("quota: %v %v", err, resp)
	}
	if r.pres.opened() != 0 {
		t.Fatal("a refused connection left a row")
	}
	r.pres.refuse = nil
	resp, err = http.Get("http://" + r.addr + "/crew/ws")
	if err != nil || resp.StatusCode != http.StatusBadRequest { // was a plain 426
		t.Fatalf("plain GET: %v %v", err, resp)
	}
	if r.pres.opened() != 0 {
		t.Fatal("a plain GET left a row")
	}
	un := start(t, nil)
	if _, resp, err := un.dial(t); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no principal: %v %v", err, resp)
	}
}

func TestCloseEndsConnectionsAndRefusesNew(t *testing.T) {
	r := start(t, agentPrincipal("pm"))
	ws, _, _ := r.dial(t)
	write(t, ws, `{"type":"hello","agent":"laptop"}`)
	read(t, ws)
	r.hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := ws.Read(ctx); err == nil {
		t.Fatal("the connection outlived Close")
	}
	waitFor(t, "the row to go", func() bool { return r.pres.opened() == 0 })
	if _, resp, err := r.dial(t); err == nil || resp == nil || resp.StatusCode < 500 {
		t.Fatalf("after Close: %v %v", err, resp)
	}
}

func TestExpiringTokenEndsTheConnection(t *testing.T) {
	p := agentPrincipal("pm")
	p.Expires = time.Now().Add(400 * time.Millisecond)
	r := start(t, p)
	ws, _, _ := r.dial(t)
	write(t, ws, `{"type":"hello","agent":"laptop"}`)
	read(t, ws)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := ws.Read(ctx); err == nil {
		t.Fatal("still open")
	}
}
