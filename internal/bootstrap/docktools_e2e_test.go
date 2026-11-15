package bootstrap_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewmcp"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A dock's agent tools (dock.Tools) are the owner's switches of what
// agents may read there. On by default; each off is enforced wherever an
// agent reads (MCP tools, crew routes, deliveries), never for people (the
// admin API), and the MCP server offers a switched tool only while some
// dock of the agent turns it on.
func TestDockToolsLimitWhatAgentsRead(t *testing.T) {
	p, _ := mediaPort(t)
	d := p.apiDock("fotos")
	p.letIn(d)
	other := p.apiDock("otro")
	p.letIn(other)
	tok := p.crewToken()
	c := p.dialCrewAs(tok, crew.Frame{Type: crew.FrameHello, Agent: "laptop", Concurrency: 1})

	if d.Tools != dock.AllTools {
		t.Fatalf("a new dock lets agents read everything: %+v", d.Tools)
	}
	s := p.injectWith(d, map[string]any{"chat_id": "200", "message_id": "img1", "sender_id": "200",
		"text": "mira esto", "images": []map[string]any{{"name": "foto.png", "data": png}}})
	a := s.Attachments[0]
	f := delivered(t, c, s)
	if f.Delivery.Ships[0].Attachments[0].Withheld || len(f.Delivery.NoHistory) != 0 {
		t.Fatalf("switches on: %+v", f.Delivery)
	}
	ack(c, f)

	// Off: history and attachments of this dock. The PATCH answers the dock.
	var got dock.Dock
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(),
		map[string]any{"tools": map[string]bool{"history": false, "attachments": false}}, &got); code != http.StatusOK ||
		got.Tools != (dock.Tools{Conversations: true}) || !got.Replies {
		t.Fatalf("tools off: %d %+v", code, got)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), map[string]any{"tools": map[string]bool{}}, nil); code != http.StatusBadRequest {
		t.Fatalf("an empty tools patch: %d", code)
	}

	// A new message: the delivery says so; the image is named, not opened.
	s2 := p.injectWith(d, map[string]any{"chat_id": "200", "message_id": "img2", "sender_id": "200",
		"text": "otra", "images": []map[string]any{{"name": "otra.png", "data": png}}})
	f = delivered(t, c, s2)
	if !f.Delivery.Ships[0].Attachments[0].Withheld || len(f.Delivery.NoHistory) != 1 || f.Delivery.NoHistory[0] != s.Conversation {
		t.Fatalf("switches off: %+v", f.Delivery)
	}
	ack(c, f)

	// The crew route and the MCP tools refuse (403 / tool error)...
	if code, _, _ := p.fetch("/crew/attachments/"+a.ID.String(), tok); code != http.StatusForbidden {
		t.Fatalf("crew attachment: %d", code)
	}
	if code, _, _ := p.fetch("/crew/attachments/"+s2.Attachments[0].ID.String(), tok); code != http.StatusForbidden {
		t.Fatalf("crew attachment (new): %d", code)
	}
	sess := mcpSession(t, p, tok)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}
	// (the other dock keeps history and attachments on: the tools stay)
	if names := toolNames(t, sess); !names["history"] || !names["attachment"] || !names["conversations"] {
		t.Fatalf("tools: %v", names)
	}
	res := call("history", map[string]any{"conversation_id": s.Conversation.String()})
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "history") {
		t.Fatalf("history of a dock with history off: %+v", res)
	}
	if res := call("attachment", map[string]any{"attachment_id": a.ID.String()}); !res.IsError {
		t.Fatalf("attachment of a dock with attachments off: %+v", res)
	}

	// ...while people still read everything.
	if code, _, body := p.fetch("/api/v1/attachments/"+a.ID.String(), adminToken); code != http.StatusOK || !bytes.Equal(body, png) {
		t.Fatalf("admin attachment: %d", code)
	}
	if hist := p.history(s.Conversation); len(hist) != 2 || hist[0].Attachments[0].Withheld {
		t.Fatalf("admin history: %+v", hist)
	}

	// A thread read from the other dock skips the door whose dock keeps
	// its history, and names files of a dock that withholds them (no id).
	o := p.inject(other, "300", "o1", "hola")
	ack(c, delivered(t, c, o))
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+o.Conversation.String()+"/links",
		map[string]string{"conversation_id": s.Conversation.String()}, &[]conversation.Conversation{}); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	var hist crewmcp.HistoryOut
	res = call("history", map[string]any{"conversation_id": o.Conversation.String(), "thread": true})
	decodeResult(t, res, &hist)
	if res.IsError || hist.Total != 1 || hist.Messages[0].Text != "hola" {
		t.Fatalf("thread history skips the door with history off: %+v", hist)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(),
		map[string]any{"tools": map[string]bool{"history": true}}, nil); code != http.StatusOK {
		t.Fatalf("history on: %d", code)
	}
	hist = crewmcp.HistoryOut{}
	decodeResult(t, call("history", map[string]any{"conversation_id": s.Conversation.String()}), &hist)
	if hist.Total != 2 || len(hist.Messages[0].Attachments) != 1 {
		t.Fatalf("history on: %+v", hist)
	}
	if at := hist.Messages[0].Attachments[0]; !at.Withheld || at.ID != "" || at.Name != "otra.png" {
		t.Fatalf("a withheld file is named without its id: %+v", at)
	}

	// conversations off: the dock's chats leave the list.
	var list crewmcp.ConversationsOut
	decodeResult(t, call("conversations", nil), &list)
	if len(list.Conversations) != 2 {
		t.Fatalf("conversations: %+v", list)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(),
		map[string]any{"tools": map[string]bool{"conversations": false}}, nil); code != http.StatusOK {
		t.Fatalf("conversations off: %d", code)
	}
	list = crewmcp.ConversationsOut{}
	decodeResult(t, call("conversations", nil), &list)
	if len(list.Conversations) != 1 || list.Conversations[0].ID != o.Conversation.String() {
		t.Fatalf("conversations without the dock's: %+v", list)
	}

	// Off on every dock of the agent: the tools are not offered at all.
	off := map[string]any{"tools": map[string]bool{"history": false, "conversations": false, "attachments": false}}
	for _, id := range []string{d.ID.String(), other.ID.String()} {
		if code := p.admin(http.MethodPatch, "/api/v1/docks/"+id, off, nil); code != http.StatusOK {
			t.Fatalf("all off: %d", code)
		}
	}
	names := toolNames(t, mcpSession(t, p, tok))
	if names["history"] || names["attachment"] || names["conversations"] || !names["reply"] {
		t.Fatalf("tools with every switch off: %v", names)
	}
	c.quiet(100 * time.Millisecond)
}
