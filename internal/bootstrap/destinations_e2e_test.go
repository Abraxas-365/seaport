package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Destinations are the places the owner lets agents open conversations
// at: named per dock, listed to the agent through MCP (the open tool shows
// only when it has one), and opened with a first message. The new
// conversation is allowed (the owner chose the place), stored with its
// first ship sent, and joins the thread of thread_of, so answers there
// reach the same session.
func TestDestinationsOpenConversations(t *testing.T) {
	p := startPort(t)
	gh := p.openDock("gh")
	chat := p.openDock("chat")
	other := p.openDock("other")
	pm := p.agent("pm")
	p.assignAs(adminToken, gh.ID, pm.ID, true)
	p.assignAs(adminToken, chat.ID, pm.ID, true)
	tok := p.issueAs(adminToken, pm.ID, "pm")
	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 1})

	// No destinations: no open tool.
	session := mcpSession(t, p, tok.Value)
	if names := toolNames(t, session); names["open"] || names["destinations"] {
		t.Fatalf("open tools without destinations: %v", names)
	}

	// The owner names places; names are checked, the list is per dock.
	var dest dock.Destination
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+chat.ID.String()+"/destinations/ops",
		map[string]string{"target": "ops-feed", "title": "the on-call feed"}, &dest); code != http.StatusOK || dest.Target != "ops-feed" || dest.Channel != dock.ChannelAPI {
		t.Fatalf("put destination: %d %+v", code, dest)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+chat.ID.String()+"/destinations/Bad Name",
		map[string]string{"target": "x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+other.ID.String()+"/destinations/elsewhere",
		map[string]string{"target": "else"}, nil); code != http.StatusOK {
		t.Fatalf("put destination on another dock: %d", code)
	}
	var list []dock.Destination
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+chat.ID.String()+"/destinations", nil, &list); code != http.StatusOK || len(list) != 1 || list[0].Name != "ops" {
		t.Fatalf("list destinations: %d %+v", code, list)
	}
	// A trigger-only dock has no destinations: nothing can be posted there.
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+gh.ID.String(), map[string]bool{"replies": false}, nil); code != http.StatusOK {
		t.Fatalf("replies off: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+gh.ID.String()+"/destinations/pr",
		map[string]string{"target": "x"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("destination on a trigger dock: %d", code)
	}

	// The agent sees only its docks' destinations.
	session = mcpSession(t, p, tok.Value)
	if names := toolNames(t, session); !names["open"] || !names["destinations"] {
		t.Fatalf("open tools with a destination: %v", names)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "destinations", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("destinations: %v %+v", err, res)
	}
	var dests struct {
		Destinations []struct{ Name, Channel, Title string } `json:"destinations"`
	}
	decodeResult(t, res, &dests)
	if len(dests.Destinations) != 1 || dests.Destinations[0].Name != "ops" || dests.Destinations[0].Title != "the on-call feed" {
		t.Fatalf("agent's destinations: %+v", dests)
	}

	// A review arrives on GitHub; the agent opens the matter at ops, linked.
	a := p.inject(gh, "repo#1", "g1", "@pm revisa")
	f := delivered(t, conn, a)
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "open",
		Arguments: map[string]any{"destination": "ops", "text": "PR #1 needs eyes", "thread_of": a.Conversation.String()}})
	if err != nil || res.IsError {
		t.Fatalf("open: %v %+v", err, res)
	}
	var opened struct {
		ConversationID string `json:"conversation_id"`
		ShipID         string `json:"ship_id"`
		Status         string `json:"status"`
	}
	decodeResult(t, res, &opened)
	if opened.Status != string(ship.StatusSent) {
		t.Fatalf("opened ship: %+v", opened)
	}
	convID, err := identity.ParseConversationID(opened.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	c := p.conversation(convID)
	if c.Dock != chat.ID || c.ExternalID != "ops-feed" || !c.Allowed() || c.Access != conversation.AccessAllow || c.Thread != p.conversation(a.Conversation).Thread {
		t.Fatalf("opened conversation: %+v", c)
	}
	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+convID.String()+"/ships", nil, &hist)
	if hist.Page.Total != 1 || hist.Items[0].Direction != ship.DirectionOut || hist.Items[0].Text != "PR #1 needs eyes" || hist.Items[0].Agent == nil || *hist.Items[0].Agent != pm.ID {
		t.Fatalf("opened history: %+v", hist.Items)
	}
	ack(conn, f)

	// Someone answers at ops: it reaches the same thread (one delivery,
	// two doors); opening again at the same place is the same conversation.
	b := p.inject(chat, "ops-feed", "o1", "on it")
	if b.Conversation != convID || b.Status != ship.StatusQueued {
		t.Fatalf("answer at ops lands in the opened conversation, queued: %+v", b)
	}
	f = delivered(t, conn, b)
	if len(f.Delivery.Thread) != 2 {
		t.Fatalf("thread delivery: %+v", f.Delivery)
	}
	ack(conn, f)

	// Unknown names, other docks' destinations and a thread the agent
	// can't see are refused.
	for _, args := range []map[string]any{
		{"destination": "nope", "text": "x"},
		{"destination": "elsewhere", "text": "x"},
		{"destination": "ops", "text": "x", "thread_of": p.inject(other, "z", "z1", "hi").Conversation.String()},
	} {
		if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "open", Arguments: args}); err != nil || !res.IsError {
			t.Fatalf("open %v must be refused: %v %+v", args, err, res)
		}
	}

	// The owner opens too (admin), and removes a destination.
	var s ship.Ship
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+chat.ID.String()+"/destinations/ops/ships", map[string]string{"text": "hola"}, &s); code != http.StatusCreated || s.Conversation != convID {
		t.Fatalf("admin open: %d %+v", code, s)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+chat.ID.String()+"/destinations/ops", nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove destination: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+chat.ID.String()+"/destinations/ops", nil, nil); code != http.StatusNotFound {
		t.Fatalf("remove twice: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+chat.ID.String()+"/destinations/ops/ships", map[string]string{"text": "hola"}, nil); code != http.StatusNotFound {
		t.Fatalf("open at a removed destination: %d", code)
	}
	conn.quiet(100 * time.Millisecond)
}

// decodeResult reads a tool's structured result into out.
func decodeResult(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func toolNames(t *testing.T, s *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}
