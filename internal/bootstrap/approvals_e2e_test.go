package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ask calls the ask_approval MCP tool.
func ask(t *testing.T, s *mcp.ClientSession, conv identity.ConversationID, text string, wait int, options ...string) (ship.Approval, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "ask_approval", Arguments: map[string]any{
		"conversation_id": conv.String(), "text": text, "options": options, "wait_seconds": wait}})
	if err != nil {
		t.Fatal(err)
	}
	return toolApproval(t, res), res.IsError
}

func toolApproval(t *testing.T, res *mcp.CallToolResult) ship.Approval {
	t.Helper()
	var out struct {
		ID     string `json:"approval_id"`
		Status string `json:"status"`
		Answer string `json:"answer"`
		By     string `json:"answered_by"`
	}
	if raw, err := json.Marshal(res.StructuredContent); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	a := ship.Approval{Status: ship.ApprovalStatus(out.Status), AnsweredBy: out.By}
	if out.ID != "" {
		a.ID, _ = identity.ParseApprovalID(out.ID)
	}
	if out.Answer != "" {
		a.Answer = &out.Answer
	}
	return a
}

func (p *port) approval(id identity.ApprovalID) ship.Approval {
	p.t.Helper()
	var a ship.Approval
	if code := p.admin(http.MethodGet, "/api/v1/approvals/"+id.String(), nil, &a); code != http.StatusOK {
		p.t.Fatalf("approval: %d", code)
	}
	return a
}

func (c *crewConn) try(d time.Duration) (crew.Frame, bool) {
	select {
	case f := <-c.frames:
		return f, true
	case <-time.After(d):
		return crew.Frame{}, false
	}
}

// An agent asks people before acting: the card goes out as its message,
// a channel reply that is an option (or its number) answers it, the web
// answers it too, and the agent's next delivery carries the answer.
func TestApprovals(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("approvals")
	p.letIn(d)
	conn := p.dialCrewAs(p.crewToken(), crew.Frame{Type: crew.FrameHello, Agent: "test-laptop", Concurrency: 1})
	session := mcpSession(t, p, p.crewToken())

	first := p.inject(d, "100", "m1", "despliega a producción")
	f := delivered(t, conn, first)
	conv := first.Conversation
	ack(conn, f)

	// Bad questions are refused (one option, option starting with a digit).
	if _, isErr := ask(t, session, conv, "¿Despliego?", 0, "Sí"); !isErr {
		t.Fatal("one option must be refused")
	}
	if _, isErr := ask(t, session, conv, "¿Despliego?", 0, "1 vez", "No"); !isErr {
		t.Fatal("an option starting with a digit must be refused")
	}

	// The question goes out as a card with numbered options.
	a, isErr := ask(t, session, conv, "¿Despliego a producción?", 0, "Aprobar", "Rechazar")
	if isErr || a.Status != ship.ApprovalPending || a.ID.IsZero() {
		t.Fatalf("ask: %v %+v", isErr, a)
	}
	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/ships", nil, &hist)
	card := hist.Items[0]
	if card.Direction != ship.DirectionOut || !strings.Contains(card.Text, "1. Aprobar\n2. Rechazar\n"+ship.ApprovalTrailer) {
		t.Fatalf("card: %+v", card)
	}
	// One pending question per conversation and agent.
	if _, isErr := ask(t, session, conv, "¿Otra?", 0, "Sí", "No"); !isErr {
		t.Fatal("a second pending approval must be refused")
	}

	// Something that isn't an option leaves it pending (and reaches the agent).
	chatter := p.inject(d, "100", "m2", "2 pero espera")
	ack(conn, delivered(t, conn, chatter))
	if got := p.approval(a.ID); got.Status != ship.ApprovalPending {
		t.Fatalf("not an answer: %+v", got)
	}

	// A web answer needs an option of the approval, and wakes the agent
	// with a system message that names the answer.
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+a.ID.String()+"/answer", map[string]string{"option": "Quizás"}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown option: %d", code)
	}
	var answered ship.Approval
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+a.ID.String()+"/answer", map[string]string{"option": "aprobar"}, &answered); code != http.StatusOK ||
		answered.Status != ship.ApprovalAnswered || answered.Answer == nil || *answered.Answer != "Aprobar" || answered.AnsweredBy == "" {
		t.Fatalf("answer: %+v", answered)
	}
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+a.ID.String()+"/answer", map[string]string{"option": "Rechazar"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("answering twice: %d", code)
	}
	wake := conn.read()
	if wake.Type != crew.FrameDelivery || len(wake.Delivery.Ships) != 1 {
		t.Fatalf("resume: %+v", wake)
	}
	if txt := wake.Delivery.Ships[0].Text; !strings.Contains(txt, "answered \"Aprobar\"") || !strings.HasPrefix(txt, "@test ") {
		t.Fatalf("resume text: %q", txt)
	}
	if len(wake.Delivery.Approvals) != 1 || wake.Delivery.Approvals[0].ID != a.ID {
		t.Fatalf("delivery approvals: %+v", wake.Delivery.Approvals)
	}
	ack(conn, wake)

	// From the channel: a reply of "2" answers, and the delivery of that
	// very message carries the approval (no system message besides).
	b, _ := ask(t, session, conv, "¿Y el rollback?", 0, "Aprobar", "Rechazar")
	reply := p.inject(d, "100", "m3", "2")
	rf := delivered(t, conn, reply)
	got := p.approval(b.ID)
	if got.Status != ship.ApprovalAnswered || got.Answer == nil || *got.Answer != "Rechazar" || got.AnsweredShip == nil || *got.AnsweredShip != reply.ID {
		t.Fatalf("channel answer: %+v", got)
	}
	if len(rf.Delivery.Approvals) != 1 || rf.Delivery.Approvals[0].ID != b.ID {
		t.Fatalf("delivery approvals: %+v", rf.Delivery.Approvals)
	}
	ack(conn, rf)
	conn.quiet(300 * time.Millisecond)

	// Blocking: the ask returns the answer given meanwhile and no system
	// message follows it.
	done := make(chan ship.Approval, 1)
	go func() {
		c, _ := ask(t, session, conv, "¿Ahora?", 20, "Aprobar", "Rechazar")
		done <- c
	}()
	var pending ship.Approval
	for i := 0; i < 50 && pending.ID.IsZero(); i++ {
		var list query.Paginated[ship.Approval]
		p.admin(http.MethodGet, "/api/v1/approvals?status=pending", nil, &list)
		if len(list.Items) == 1 {
			pending = list.Items[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pending.ID.IsZero() {
		t.Fatal("the blocked ask never stored its approval")
	}
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+pending.ID.String()+"/answer", map[string]string{"option": "Aprobar"}, nil); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	select {
	case c := <-done:
		if c.Status != ship.ApprovalAnswered || c.Answer == nil || *c.Answer != "Aprobar" {
			t.Fatalf("blocked ask: %+v", c)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the blocked ask did not return the answer")
	}
	if f, ok := conn.try(300 * time.Millisecond); ok {
		t.Fatalf("unexpected: %+v", f.Delivery.Ships)
	}

	// Cancelling tells the agent; a system sender's message never answers.
	c, _ := ask(t, session, conv, "¿Borro?", 0, "Sí", "No")
	var cancelled ship.Approval
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+c.ID.String()+"/cancel", nil, &cancelled); code != http.StatusOK || cancelled.Status != ship.ApprovalCancelled {
		t.Fatalf("cancel: %d %+v", code, cancelled)
	}
	cf := conn.read()
	if cf.Type != crew.FrameDelivery || !strings.Contains(cf.Delivery.Ships[0].Text, "cancelled") {
		t.Fatalf("cancel resume: %+v", cf)
	}
	ack(conn, cf)
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+c.ID.String()+"/cancel", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("cancel twice: %d", code)
	}

	// The agent reads its approval; the list filters by status.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "approval", Arguments: map[string]any{"approval_id": a.ID.String()}})
	if err != nil || res.IsError || toolApproval(t, res).Status != ship.ApprovalAnswered {
		t.Fatalf("approval tool: %v %+v", err, res)
	}
	var list query.Paginated[ship.Approval]
	p.admin(http.MethodGet, "/api/v1/approvals?status=answered", nil, &list)
	if list.Page.Total != 3 {
		t.Fatalf("answered: %d", list.Page.Total)
	}
	if code := p.admin(http.MethodGet, "/api/v1/approvals?status=bogus", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", code)
	}
	var byConv query.Paginated[ship.Approval]
	p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/approvals", nil, &byConv)
	if byConv.Page.Total != 4 {
		t.Fatalf("conversation approvals: %d", byConv.Page.Total)
	}

	// The same over plain HTTP (seaport ask): admin tokens have no agent.
	var viaHTTP ship.Approval
	body := map[string]any{"conversation_id": conv.String(), "text": "¿Por HTTP?", "options": []string{"Sí", "No"}}
	if code := p.call(http.MethodPost, "/crew/approvals", adminToken, body, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("admin on /crew/approvals: %d", code)
	}
	if code := p.call(http.MethodPost, "/crew/approvals", p.crewToken(), body, &viaHTTP); code != http.StatusCreated || viaHTTP.Status != ship.ApprovalPending {
		t.Fatalf("POST /crew/approvals: %d %+v", code, viaHTTP)
	}
	var read ship.Approval
	if code := p.call(http.MethodGet, "/crew/approvals/"+viaHTTP.ID.String(), p.crewToken(), nil, &read); code != http.StatusOK || read.ID != viaHTTP.ID {
		t.Fatalf("GET /crew/approvals: %d %+v", code, read)
	}
}

// Another agent's approval and another dock's conversation are NotFound;
// a dock that takes no replies can't ask.
func TestApprovalsAreTheAgentsOwn(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("mine")
	p.letIn(d)
	other := p.openDock("other")
	pm := p.agent("pm")
	p.assignAs(adminToken, other.ID, pm.ID, true)
	pmTok := p.issueAs(adminToken, pm.ID, "pm")
	mine := mcpSession(t, p, p.crewToken())
	theirs := mcpSession(t, p, pmTok.Value)

	s := p.inject(d, "100", "m1", "hola")
	a, isErr := ask(t, mine, s.Conversation, "¿Sí?", 0, "Sí", "No")
	if isErr {
		t.Fatal("ask refused")
	}
	if _, isErr := ask(t, theirs, s.Conversation, "¿Sí?", 0, "Sí", "No"); !isErr {
		t.Fatal("an agent of another dock asked in this conversation")
	}
	res, err := theirs.CallTool(context.Background(), &mcp.CallToolParams{Name: "approval", Arguments: map[string]any{"approval_id": a.ID.String()}})
	if err != nil || !res.IsError {
		t.Fatalf("another agent reads the approval: %v %+v", err, res)
	}
	if code := p.call(http.MethodGet, "/crew/approvals/"+a.ID.String(), pmTok.Value, nil, nil); code != http.StatusNotFound {
		t.Fatalf("GET another agent's approval: %d", code)
	}

	// Replies off: nothing can be asked (the card couldn't be sent).
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), map[string]bool{"replies": false}, nil); code != http.StatusOK {
		t.Fatalf("replies off: %d", code)
	}
	if _, isErr := ask(t, mine, s.Conversation, "¿Otra?", 0, "Sí", "No"); !isErr {
		t.Fatal("a dock with replies off can't ask")
	}
	var dk dock.Dock
	_ = dk
}

// An approval nobody answers expires; the agent is told.
func TestApprovalsExpire(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.ApprovalTTL = 400 * time.Millisecond },
		bootstrap.Options{SweepInterval: 200 * time.Millisecond})
	d := p.apiDock("expire")
	p.letIn(d)
	conn := p.dialCrewAs(p.crewToken(), crew.Frame{Type: crew.FrameHello, Agent: "test-laptop", Concurrency: 1})
	session := mcpSession(t, p, p.crewToken())
	first := p.inject(d, "100", "m1", "hola")
	ack(conn, delivered(t, conn, first))

	a, isErr := ask(t, session, first.Conversation, "¿Sí?", 0, "Sí", "No")
	if isErr {
		t.Fatal("ask refused")
	}
	f := conn.read()
	if f.Type != crew.FrameDelivery || !strings.Contains(f.Delivery.Ships[0].Text, "expired") {
		t.Fatalf("expiry resume: %+v", f)
	}
	if got := p.approval(a.ID); got.Status != ship.ApprovalExpired {
		t.Fatalf("expired: %+v", got)
	}
	ack(conn, f)
	// An expired approval can't be answered; a new one can be asked.
	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+a.ID.String()+"/answer", map[string]string{"option": "Sí"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("answer expired: %d", code)
	}
	if _, isErr := ask(t, session, first.Conversation, "¿Otra vez?", 0, "Sí", "No"); isErr {
		t.Fatal("a new approval after expiry")
	}
}
