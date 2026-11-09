package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// openDock creates an api dock that lets every chat in (no customs step).
func (p *port) openDock(name string) dock.Dock {
	p.t.Helper()
	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": name}, &d); code != http.StatusCreated {
		p.t.Fatalf("create dock: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String()+"/customs/policy", map[string]string{"default": "allow"}, nil); code != http.StatusOK {
		p.t.Fatalf("customs policy: %d", code)
	}
	return d
}

// delivered reads one delivery and checks it carries exactly want.
func delivered(t *testing.T, c *crewConn, want ship.Ship) crew.Frame {
	t.Helper()
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != want.ID {
		t.Fatalf("expected delivery of %q, got %+v", want.Text, f)
	}
	return f
}

func ack(c *crewConn, f crew.Frame) {
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
}

func held(t *testing.T, s ship.Ship, reason string) {
	t.Helper()
	if s.Status != ship.StatusHeld || s.HoldReason != reason || s.Agent != nil {
		t.Fatalf("expected held (%q), got %s %q agent=%v", reason, s.Status, s.HoldReason, s.Agent)
	}
}

func mcpSession(t *testing.T, p *port, token string) *mcp.ClientSession {
	t.Helper()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: p.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A dock routes each message to one agent of its team: the only one, the
// one called (@name), the one the chat was talking to, the default; else
// it is held. A token works only as its agent, and an agent sees only the
// conversations of its docks.
func TestAgentsRouteMessages(t *testing.T) {
	p := startPort(t)
	d := p.openDock("team")

	// No agent on the dock: stored, held, never delivered.
	held(t, p.inject(d, "100", "n1", "hola?"), agent.HoldNoAgent)

	pm, reviewer := p.agent("pm"), p.agent("reviewer")
	pmTok, revTok := p.issueAs(adminToken, pm.ID, "pm"), p.issueAs(adminToken, reviewer.ID, "reviewer")

	// A token can't work as another agent.
	if reason := p.dialRefused(pmTok.Value, crew.Frame{Type: crew.FrameHello, As: "reviewer"}); !strings.Contains(reason, "belongs to agent pm") {
		t.Fatalf("pm token as reviewer: %q", reason)
	}
	pmConn := p.dialCrewAs(pmTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", As: "pm", Concurrency: 2})
	revConn := p.dialCrewAs(revTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "rev-laptop", Concurrency: 2})

	// One agent: it takes everything, mention or not.
	p.assignAs(adminToken, d.ID, pm.ID, false)
	first := p.inject(d, "200", "a1", "hola @reviewer")
	if first.Agent == nil || *first.Agent != pm.ID {
		t.Fatalf("single member: %+v", first)
	}
	ack(pmConn, delivered(t, pmConn, first))
	revConn.quiet(200 * time.Millisecond)

	// Two agents, pm the default.
	p.assignAs(adminToken, d.ID, reviewer.ID, false)
	p.assignAs(adminToken, d.ID, pm.ID, true)
	var team struct {
		Agents []agent.Member `json:"agents"`
	}
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String()+"/agents", nil, &team); code != http.StatusOK || len(team.Agents) != 2 {
		t.Fatalf("team: %d %+v", code, team)
	}

	// A new chat calling nobody goes to the default.
	s := p.inject(d, "300", "b1", "necesito un ticket")
	ack(pmConn, delivered(t, pmConn, s))
	// Called by name: the reviewer, and the chat stays with it.
	s = p.inject(d, "300", "b2", "@Reviewer revisa el PR")
	ack(revConn, delivered(t, revConn, s))
	s = p.inject(d, "300", "b3", "y el otro también")
	ack(revConn, delivered(t, revConn, s))
	pmConn.quiet(200 * time.Millisecond)

	// One agent at a time per conversation, in arrival order: a message
	// for the reviewer waits while pm works the one before it.
	forPM := p.inject(d, "300", "c1", "@pm cierra el ticket")
	fpm := delivered(t, pmConn, forPM)
	forRev := p.inject(d, "300", "c2", "@reviewer y aprueba")
	revConn.quiet(300 * time.Millisecond)
	ack(pmConn, fpm)
	ack(revConn, delivered(t, revConn, forRev))

	// The reviewer's token never gets pm's ships (another chat, pm's).
	s = p.inject(d, "400", "d1", "@pm hola")
	ack(pmConn, delivered(t, pmConn, s))
	revConn.quiet(200 * time.Millisecond)

	// No default and nobody called: held; mentioning someone still works.
	p.assignAs(adminToken, d.ID, pm.ID, false)
	held(t, p.inject(d, "500", "e1", "hola"), agent.HoldNotCalled)
	held(t, p.inject(d, "500", "e2", "@nadie hola"), agent.HoldNotCalled)
	s = p.inject(d, "500", "e3", "@pm hola")
	ack(pmConn, delivered(t, pmConn, s))

	// An agent on no dock sees no conversation through MCP.
	ops := p.agent("ops")
	opsMCP := mcpSession(t, p, p.issueAs(adminToken, ops.ID, "ops").Value)
	res, err := opsMCP.CallTool(context.Background(), &mcp.CallToolParams{Name: "conversations", Arguments: map[string]any{}})
	if raw, _ := json.Marshal(res.StructuredContent); err != nil || strings.Contains(string(raw), s.Conversation.String()) {
		t.Fatalf("ops lists the team's conversations: %v %s", err, raw)
	}
	for _, tool := range []string{"history", "reply"} {
		res, err := opsMCP.CallTool(context.Background(), &mcp.CallToolParams{Name: tool,
			Arguments: map[string]any{"conversation_id": s.Conversation.String(), "text": "hola"}})
		if err != nil || res == nil || !res.IsError {
			t.Fatalf("ops %s on the team's conversation: %v %+v", tool, err, res)
		}
	}
	// pm replies; the reply records it.
	pmMCP := mcpSession(t, p, pmTok.Value)
	if res, err := pmMCP.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": s.Conversation.String(), "text": "listo"}}); err != nil || res.IsError {
		t.Fatalf("pm reply: %v %+v", err, res)
	}
	var history query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+s.Conversation.String()+"/ships", nil, &history)
	var reply *ship.Ship
	for i := range history.Items {
		if history.Items[i].Text == "listo" {
			reply = &history.Items[i]
		}
	}
	if reply == nil || reply.Agent == nil || *reply.Agent != pm.ID {
		t.Fatalf("reply by pm: %+v", reply)
	}

	// Unassigning holds what waited for the agent on that dock.
	waiting := p.inject(d, "300", "f1", "@reviewer otra cosa")
	f := delivered(t, revConn, waiting)
	queued := p.inject(d, "300", "f2", "y esto")
	var after agent.Agent
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+d.ID.String()+"/agents/"+reviewer.ID.String(), nil, &after); code != http.StatusOK || len(after.Docks) != 0 {
		t.Fatalf("unassign: %d %+v", code, after)
	}
	for _, id := range []ship.Ship{waiting, queued} {
		got := shipOf(t, p, id)
		if got.Status != ship.StatusHeld || got.HoldReason != agent.HoldAgentGone {
			t.Fatalf("ship of an unassigned agent: %+v", got)
		}
	}
	revConn.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	if e := revConn.read(); e.Type != crew.FrameError {
		t.Fatalf("ack after unassign: %+v", e)
	}
	// The chat goes back to the default rule (reviewer gone from the team).
	p.assignAs(adminToken, d.ID, pm.ID, true)
	s = p.inject(d, "300", "f3", "hola de nuevo")
	ack(pmConn, delivered(t, pmConn, s))

	// Deleting an agent closes its connections and revokes its tokens.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+reviewer.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete agent: %d", code)
	}
	closed(t, revConn)
	if code := p.call(http.MethodPost, "/mcp", revTok.Value, map[string]any{}, nil); code != http.StatusUnauthorized {
		t.Fatalf("deleted agent's token: %d", code)
	}
	var list query.Paginated[crew.Token]
	p.admin(http.MethodGet, "/api/v1/crew/tokens?agent_id="+pm.ID.String(), nil, &list)
	if len(list.Items) != 1 || list.Items[0].ID != pmTok.ID {
		t.Fatalf("pm's tokens: %+v", list.Items)
	}
	// Its name is free again.
	if again := p.agent("reviewer"); again.ID == reviewer.ID {
		t.Fatal("deleted agent came back")
	}
	// Unknown agents and docks are 404.
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+d.ID.String()+"/agents/"+reviewer.ID.String(), map[string]bool{}, nil); code != http.StatusNotFound {
		t.Fatalf("assign a deleted agent: %d", code)
	}
}

func shipOf(t *testing.T, p *port, s ship.Ship) ship.Ship {
	t.Helper()
	var history query.Paginated[ship.Ship]
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+s.Conversation.String()+"/ships?limit=100", nil, &history); code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	for _, got := range history.Items {
		if got.ID == s.ID {
			return got
		}
	}
	t.Fatalf("ship %s not in its history", s.ID)
	return ship.Ship{}
}

// A message for an agent connected to another process wakes it there,
// and only it.
func TestAgentsAcrossProcesses(t *testing.T) {
	a := startPort(t)
	b := joinPort(t, func(*config.Server) {}, bootstrap.Options{})
	d := a.openDock("team")
	pm, reviewer := a.agent("pm"), a.agent("reviewer")
	a.assignAs(adminToken, d.ID, pm.ID, true)
	a.assignAs(adminToken, d.ID, reviewer.ID, false)
	onA := a.dialCrewAs(a.issueAs(adminToken, pm.ID, "pm").Value, crew.Frame{Type: crew.FrameHello, Agent: "pm", Concurrency: 1})
	onB := b.dialCrewAs(a.issueAs(adminToken, reviewer.ID, "reviewer").Value, crew.Frame{Type: crew.FrameHello, Agent: "rev", Concurrency: 1})
	waitCrew(t, a, func(st crew.Status) bool { return len(st.Agents) == 2 })

	s := a.inject(d, "700", "x1", "@reviewer hola")
	ack(onB, delivered(t, onB, s))
	onA.quiet(200 * time.Millisecond)
	s = a.inject(d, "701", "x2", "hola")
	ack(onA, delivered(t, onA, s))
	onB.quiet(200 * time.Millisecond)
}
