package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A dock with replies off is a trigger: its messages wake the agent as
// before, deliveries name its doors as read-only, and a reply there (MCP
// tool, crew frame, admin send) is refused without storing anything. The
// agent answers on another door of the thread.
func TestRepliesOffMakesADockATrigger(t *testing.T) {
	p := startPort(t)
	gh := p.openDock("gh")
	slack := p.openDock("slack")
	pm := p.agent("pm")
	p.assignAs(adminToken, gh.ID, pm.ID, true)
	p.assignAs(adminToken, slack.ID, pm.ID, true)
	tok := p.issueAs(adminToken, pm.ID, "pm")
	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 1})
	session := mcpSession(t, p, tok.Value)

	// Docks reply by default; the switch is a PATCH.
	if !gh.Replies {
		t.Fatalf("a new dock replies: %+v", gh)
	}
	var d dock.Dock
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+gh.ID.String(), map[string]bool{"replies": false}, &d); code != http.StatusOK || d.Replies {
		t.Fatalf("replies off: %d %+v", code, d)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+gh.ID.String(), map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", code)
	}

	// Messages still wake the agent; the delivery marks the door read-only.
	a := p.inject(gh, "repo#1", "g1", "@pm revisa")
	f := delivered(t, conn, a)
	if len(f.Delivery.ReadOnly) != 1 || f.Delivery.ReadOnly[0] != a.Conversation {
		t.Fatalf("read-only doors: %+v", f.Delivery)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+a.Conversation.String(), map[string]string{"access": "allow"}, nil); code != http.StatusOK {
		t.Fatalf("allow: %d", code)
	}

	// A reply there is refused (422), by every way in, and nothing is stored.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": a.Conversation.String(), "text": "hecho"}})
	if err != nil || !res.IsError {
		t.Fatalf("reply tool on a trigger dock must be refused: %v %+v", err, res)
	}
	conv := a.Conversation
	conn.send(crew.Frame{Type: crew.FrameReply, ConversationID: &conv, Text: "hecho"})
	if ef := conn.read(); ef.Type != crew.FrameError {
		t.Fatalf("reply frame: %+v", ef)
	}
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+conv.String()+"/ships", map[string]string{"text": "hola"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("admin send: %d", code)
	}
	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/ships", nil, &hist)
	if hist.Page.Total != 1 {
		t.Fatalf("a refused reply stores nothing: %d ships", hist.Page.Total)
	}
	ack(conn, f)

	// Linked to a Slack door, the thread's delivery names only the GitHub
	// door as read-only and the agent answers on Slack.
	b := p.inject(slack, "C1/1.0", "s1", "hola")
	f = delivered(t, conn, b)
	ack(conn, f)
	var doors []conversation.Conversation
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+conv.String()+"/links",
		map[string]string{"conversation_id": b.Conversation.String()}, &doors); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+b.Conversation.String(), map[string]string{"access": "allow"}, nil); code != http.StatusOK {
		t.Fatalf("allow slack: %d", code)
	}
	a2 := p.inject(gh, "repo#1", "g2", "@pm ¿y ahora?")
	f = delivered(t, conn, a2)
	if len(f.Delivery.Thread) != 2 || len(f.Delivery.ReadOnly) != 1 || f.Delivery.ReadOnly[0] != conv || !f.Delivery.CanReply(b.Conversation) {
		t.Fatalf("thread delivery: %+v", f.Delivery)
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": b.Conversation.String(), "text": "lo miro"}}); err != nil || res.IsError {
		t.Fatalf("reply on the Slack door: %v %+v", err, res)
	}
	ack(conn, f)

	// Replies back on: the GitHub door answers again.
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+gh.ID.String(), map[string]bool{"replies": true}, &d); code != http.StatusOK || !d.Replies {
		t.Fatalf("replies on: %d %+v", code, d)
	}
	a3 := p.inject(gh, "repo#1", "g3", "@pm listo?")
	f = delivered(t, conn, a3)
	if len(f.Delivery.ReadOnly) != 0 {
		t.Fatalf("no read-only doors: %+v", f.Delivery)
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": conv.String(), "text": "listo"}}); err != nil || res.IsError {
		t.Fatalf("reply after replies on: %v %+v", err, res)
	}
	ack(conn, f)
	conn.quiet(100 * time.Millisecond)
}
