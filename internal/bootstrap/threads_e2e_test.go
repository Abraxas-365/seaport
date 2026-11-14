package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Linked conversations make one thread: the agent works its doors in
// arrival order as one (one delivery in flight for the thread, the
// previous agent is the thread's), deliveries name every door, and the
// thread's history reads across doors (API, MCP). Unlinking puts a door
// back on its own.
func TestLinkedConversationsAreOneThread(t *testing.T) {
	p := startPort(t)
	d := p.openDock("gh")
	e := p.openDock("slack")
	pm, reviewer := p.agent("pm"), p.agent("reviewer")
	for _, dk := range []identity.DockID{d.ID, e.ID} {
		p.assignAs(adminToken, dk, pm.ID, true)
		p.assignAs(adminToken, dk, reviewer.ID, false)
	}
	pmConn := p.dialCrewAs(p.issueAs(adminToken, pm.ID, "pm").Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 2})
	revTok := p.issueAs(adminToken, reviewer.ID, "reviewer")
	revConn := p.dialCrewAs(revTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "rev-laptop", Concurrency: 2})

	// Two conversations on two docks; the reviewer was called last, on
	// the GitHub one.
	b := p.inject(e, "C1/1.0", "s1", "hola")
	ack(pmConn, delivered(t, pmConn, b))
	a := p.inject(d, "repo#1", "g1", "@reviewer revisa el PR")
	ack(revConn, delivered(t, revConn, a))

	// Each is a thread of its own.
	if ca := p.conversation(a.Conversation); ca.Thread.String() != ca.ID.String() {
		t.Fatalf("thread of a lone conversation is itself: %+v", ca)
	}

	// Link them: both answer the same thread, oldest door first.
	var doors []conversation.Conversation
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+b.Conversation.String()+"/links",
		map[string]string{"conversation_id": a.Conversation.String()}, &doors); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	if len(doors) != 2 || doors[0].ID != b.Conversation || doors[1].ID != a.Conversation || doors[0].Thread != doors[1].Thread {
		t.Fatalf("doors: %+v", doors)
	}
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+a.Conversation.String()+"/thread", nil, &doors); code != http.StatusOK || len(doors) != 2 {
		t.Fatalf("thread: %d %+v", code, doors)
	}
	// Linking a door with one already in the thread changes nothing.
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+a.Conversation.String()+"/links",
		map[string]string{"conversation_id": b.Conversation.String()}, &doors); code != http.StatusOK || len(doors) != 2 {
		t.Fatalf("relink: %d %+v", code, doors)
	}
	// A conversation can't be linked with itself.
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+a.Conversation.String()+"/links",
		map[string]string{"conversation_id": a.Conversation.String()}, nil); code != http.StatusBadRequest {
		t.Fatalf("self link: %d", code)
	}

	// The thread's previous agent (the reviewer, called last on the
	// GitHub door) takes a Slack message calling nobody.
	s := p.inject(e, "C1/1.0", "s2", "¿cómo va?")
	f := delivered(t, revConn, s)
	if len(f.Delivery.Thread) != 2 || f.Delivery.Conversation.ID != b.Conversation {
		t.Fatalf("delivery names the thread's doors: %+v", f.Delivery)
	}
	pmConn.quiet(200 * time.Millisecond)

	// One delivery in flight per thread: a message on the other door
	// for the same agent waits until this one is acked, then arrives.
	s2 := p.inject(d, "repo#1", "g2", "¿y aquí?")
	revConn.quiet(300 * time.Millisecond)
	ack(revConn, f)
	f = delivered(t, revConn, s2)
	if f.Delivery.Conversation.ID != a.Conversation || len(f.Delivery.Thread) != 2 {
		t.Fatalf("second door's delivery: %+v", f.Delivery)
	}
	// Arrival order across doors: pm's message (after the reviewer's
	// in flight) waits while the reviewer works, then the thread moves.
	forPM := p.inject(e, "C1/1.0", "s3", "@pm cierra el ticket")
	pmConn.quiet(300 * time.Millisecond)
	ack(revConn, f)
	ack(pmConn, delivered(t, pmConn, forPM))

	// A steering connector gets a message on the other door as a steer
	// of the delivery in flight.
	steer := p.dialCrewAs(revTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "rev-steer", Concurrency: 1, Steer: true})
	_ = revConn.ws.CloseNow()
	first := p.inject(d, "repo#1", "g3", "@reviewer mira esto")
	f = delivered(t, steer, first)
	second := p.inject(e, "C1/1.0", "s4", "@reviewer y esto")
	sf := steer.read()
	if sf.Type != crew.FrameSteer || sf.Delivery.ID != f.Delivery.ID || len(sf.Delivery.Ships) != 1 || sf.Delivery.Ships[0].ID != second.ID {
		t.Fatalf("steer across doors: %+v", sf)
	}
	steer.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{first.ID, second.ID}})

	// Thread history reads both doors; a door's history only its own.
	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+a.Conversation.String()+"/ships?thread=1", nil, &hist)
	if hist.Page.Total != 7 {
		t.Fatalf("thread history: %d ships", hist.Page.Total)
	}
	p.admin(http.MethodGet, "/api/v1/conversations/"+a.Conversation.String()+"/ships", nil, &hist)
	if hist.Page.Total != 3 {
		t.Fatalf("door history: %d ships", hist.Page.Total)
	}

	// The MCP history tool reads the thread too, naming each door.
	revMCP := mcpSession(t, p, revTok.Value)
	res, err := revMCP.CallTool(context.Background(), &mcp.CallToolParams{Name: "history",
		Arguments: map[string]any{"conversation_id": b.Conversation.String(), "thread": true, "limit": 50}})
	if err != nil || res.IsError {
		t.Fatalf("history: %v %+v", err, res)
	}
	var out struct {
		Messages []struct {
			ConversationID string `json:"conversation_id"`
		} `json:"messages"`
		Total int `json:"total"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	named := map[string]bool{}
	for _, m := range out.Messages {
		named[m.ConversationID] = true
	}
	if out.Total != 7 || !named[a.Conversation.String()] || !named[b.Conversation.String()] {
		t.Fatalf("mcp thread history: %+v", out)
	}

	// Unlink: the Slack door is on its own again; the GitHub one keeps
	// the thread id.
	var c conversation.Conversation
	if code := p.admin(http.MethodDelete, "/api/v1/conversations/"+b.Conversation.String()+"/links", nil, &c); code != http.StatusOK || c.Thread.String() != c.ID.String() {
		t.Fatalf("unlink: %d %+v", code, c)
	}
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+a.Conversation.String()+"/thread", nil, &doors); code != http.StatusOK || len(doors) != 1 {
		t.Fatalf("thread after unlink: %d %+v", code, doors)
	}
	// A message on the unlinked door is a conversation of its own again:
	// its previous agent is the reviewer (the last called there, s4).
	s = p.inject(e, "C1/1.0", "s5", "¿sigues?")
	ack(steer, delivered(t, steer, s))
	pmConn.quiet(200 * time.Millisecond)

	// A full thread refuses more doors.
	full := make([]ship.Ship, 0, conversation.MaxDoors)
	for i := 0; i < conversation.MaxDoors; i++ {
		full = append(full, p.inject(d, "repo#"+strings.Repeat("9", i+2), "m", "hola"))
		ack(pmConn, delivered(t, pmConn, full[i]))
	}
	for i := 1; i < conversation.MaxDoors; i++ {
		if code := p.admin(http.MethodPost, "/api/v1/conversations/"+full[0].Conversation.String()+"/links",
			map[string]string{"conversation_id": full[i].Conversation.String()}, nil); code != http.StatusOK {
			t.Fatalf("link %d: %d", i, code)
		}
	}
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+full[0].Conversation.String()+"/links",
		map[string]string{"conversation_id": s.Conversation.String()}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("past MaxDoors: %d", code)
	}
}
