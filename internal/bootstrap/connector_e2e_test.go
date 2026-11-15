package bootstrap_test

// Connector e2e: the real connector (connector.Run, the code `seaport
// connect` runs) against the real port, with a test-only ACP echo agent
// (internal/connector/acptest) as its harness. The test binary starts
// itself as that agent (acptest.Command → TestACPAgentMain), so nothing
// but `go test` is needed.
//
// Architecture exception, with its reason: the echo agent speaks ACP, and
// the ACP SDK is confined to `internal/connector` (vendorSDKs). It lives in
// `internal/connector/acptest`, inside that prefix, and only _test.go
// files import it, so the rule stays as it was (see AGENTS.md, Testing).

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/connector"
	"github.com/Abraxas-365/seaport/internal/connector/acptest"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// TestACPAgentMain is the echo agent: the connector's harness command
// re-executes this test binary running only this test. In a normal run it
// skips.
func TestACPAgentMain(t *testing.T) {
	if !acptest.Active() {
		t.Skip("serves ACP only when started by the connector e2e tests")
	}
	acptest.RunFromEnv()
}

// until polls ok (bounded, no fixed sleeps) and fails the test when it
// never holds.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// connectorOpts configures a connector run of the tests.
type connectorOpts struct {
	caps        string // acptest.EnvCaps
	deliver     string // connector.Config.Deliver
	noAutoReply bool
	vars        []string // more NAME=value for the echo agent
	protect     []string
	mode        string
}

// connectorRun is a connector running in this process.
type connectorRun struct {
	t        *testing.T
	log      string
	sessions string
	cancel   context.CancelFunc
	done     chan error
	stopped  bool
}

// startConnector runs connector.Run as testAgent in a goroutine, with a
// temp working directory and sessions file; it stops with the test.
func (p *port) startConnector(opts connectorOpts) *connectorRun {
	p.t.Helper()
	dir := p.t.TempDir()
	h := &connectorRun{t: p.t, log: filepath.Join(dir, "acp.jsonl"), sessions: filepath.Join(dir, "state", "sessions.json"),
		done: make(chan error, 1)}
	vars := append([]string{acptest.EnvLog + "=" + h.log, acptest.EnvCaps + "=" + opts.caps}, opts.vars...)
	cfg := connector.Config{
		Server: p.srv.URL, Token: p.crewToken(), AgentName: "echo-e2e", Concurrency: 1,
		AutoReply: !opts.noAutoReply, Deliver: opts.deliver, Mode: opts.mode, Protect: opts.protect,
		Cwd: dir, Command: acptest.Command(vars...), SessionsFile: h.sessions,
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- connector.Run(ctx, cfg) }()
	p.t.Cleanup(h.stop)
	return h
}

// stop ends the connector (cancel, wait) and checks it ended cleanly.
func (h *connectorRun) stop() {
	h.t.Helper()
	if h.stopped {
		return
	}
	h.stopped = true
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("connector.Run ended with %v", err)
		}
	case <-time.After(30 * time.Second):
		h.t.Error("the connector did not stop after its context was cancelled")
	}
}

func (h *connectorRun) records() []acptest.Record { return acptest.ReadLog(h.log) }

// prompts waits until the agent has been prompted n times and returns them.
func (h *connectorRun) prompts(n int) []acptest.Record {
	h.t.Helper()
	var got []acptest.Record
	until(h.t, "prompts in the agent log", func() bool {
		got = acptest.Of(h.records(), "prompt")
		return len(got) >= n
	})
	if len(got) != n {
		h.t.Fatalf("the agent was prompted %d times, want %d: %+v", len(got), n, got)
	}
	return got
}

// release lets a "hold:" turn finish.
func (h *connectorRun) release() {
	h.t.Helper()
	if err := os.WriteFile(acptest.GatePath(h.log), nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// ready waits for the connector's connection to show in the crew status.
func (p *port) ready(ok func(crew.Agent) bool) crew.Agent {
	p.t.Helper()
	var found crew.Agent
	until(p.t, "the connector's connection in the crew status", func() bool {
		var st crew.Status
		p.admin(http.MethodGet, "/api/v1/crew", nil, &st)
		for _, a := range st.Agents {
			if a.AgentName == testAgent && ok(a) {
				found = a
				return true
			}
		}
		return false
	})
	return found
}

func anyConn(crew.Agent) bool { return true }

// shipsOf waits until the conversation's history satisfies ok and returns it.
func (p *port) shipsOf(conv identity.ConversationID, what string, ok func([]ship.Ship) bool) []ship.Ship {
	p.t.Helper()
	var got []ship.Ship
	until(p.t, what, func() bool {
		got = p.history(conv)
		return ok(got)
	})
	return got
}

// outbound ships of a history, oldest first (the API lists newest first).
func outbound(ships []ship.Ship) []ship.Ship {
	var out []ship.Ship
	for i := len(ships) - 1; i >= 0; i-- {
		if ships[i].Direction == ship.DirectionOut {
			out = append(out, ships[i])
		}
	}
	return out
}

func allHandled(ships []ship.Ship) bool {
	n := 0
	for _, s := range ships {
		if s.Direction == ship.DirectionIn {
			n++
			if s.Status != ship.StatusHandled {
				return false
			}
		}
	}
	return n > 0
}

// answered: every inbound ship handled and n replies stored.
func answered(n int) func([]ship.Ship) bool {
	return func(ships []ship.Ship) bool { return allHandled(ships) && len(outbound(ships)) >= n }
}

func (p *port) turns(conv identity.ConversationID) []ship.Turn {
	p.t.Helper()
	var out query.Paginated[ship.Turn]
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/turns", nil, &out); code != http.StatusOK {
		p.t.Fatalf("turns: %d", code)
	}
	return out.Items
}

// An inbound ship on an api dock reaches the agent as a prompt; the echo
// comes back as the stored reply (auto-reply), the delivery is handled and
// a tool call of the turn is recorded as the turn summary.
func TestConnectorEchoesAnInboundShip(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("echo")
	p.letIn(d)
	h := p.startConnector(connectorOpts{caps: "load"})
	p.ready(anyConn)

	in := p.inject(d, "900", "m1", "hello tool: now")
	ships := p.shipsOf(in.Conversation, "the echo reply stored and the delivery handled", answered(1))

	reply := outbound(ships)[0]
	if reply.Text != "echo: hello tool: now" || reply.Agent == nil || reply.Status != ship.StatusSent {
		t.Fatalf("reply: %+v", reply)
	}
	var inbound ship.Ship
	for _, s := range ships {
		if s.Direction == ship.DirectionIn {
			inbound = s
		}
	}
	if inbound.ID != in.ID || inbound.Status != ship.StatusHandled || inbound.Delivery == nil {
		t.Fatalf("inbound ship: %+v", inbound)
	}

	prompt := h.prompts(1)[0]
	for _, want := range []string{"conversation_id: " + in.Conversation.String(), "Ana: hello tool: now",
		"Your final answer will be sent to this conversation as a message"} {
		if !strings.Contains(prompt.Text, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt.Text)
		}
	}
	if prompt.Session == "" || prompt.Images != 0 {
		t.Fatalf("prompt record: %+v", prompt)
	}

	// The tool call of the turn is its summary (ship.turn).
	var turns []ship.Turn
	until(t, "the turn summary", func() bool { turns = p.turns(in.Conversation); return len(turns) == 1 })
	tr := turns[0]
	if tr.Outcome != ship.TurnAcked || tr.Delivery != *inbound.Delivery || tr.AgentName != testAgent ||
		len(tr.Tools) != 1 || tr.Tools[0].Title != "Run echo tool" {
		t.Fatalf("turn: %+v", tr)
	}

	// The session is kept for the thread.
	raw, err := os.ReadFile(h.sessions)
	if err != nil || !strings.Contains(string(raw), prompt.Session) {
		t.Fatalf("sessions file: %v %s", err, raw)
	}
	h.stop()
}

// A message that arrives while the turn runs joins its delivery as a steer
// (the agent advertises _rness/steer): one delivery, one prompt, both ships
// handled by the one ack, and the connection says steer in its hello.
func TestConnectorSteersAMessageMidTurn(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("steer")
	p.letIn(d)
	h := p.startConnector(connectorOpts{caps: "steer"})
	p.ready(func(a crew.Agent) bool { return a.Steer })

	first := p.inject(d, "900", "m1", "hold: first")
	h.prompts(1) // the turn is running (held)
	second := p.inject(d, "900", "m2", "second")
	var steer acptest.Record
	until(t, "the steer reaching the agent", func() bool {
		s := acptest.Of(h.records(), "steer")
		if len(s) == 0 {
			return false
		}
		steer = s[0]
		return true
	})
	if !strings.Contains(steer.Text, "Ana: second") || !strings.Contains(steer.Text, "sent while you were working") {
		t.Fatalf("steer: %+v", steer)
	}
	// The hub shows the one delivery with both ships.
	p.ready(func(a crew.Agent) bool { return len(a.Deliveries) == 1 && a.Deliveries[0].Ships == 2 })
	h.release()

	ships := p.shipsOf(first.Conversation, "both ships handled and the reply stored", answered(1))
	var del []identity.DeliveryID
	for _, s := range ships {
		if s.Direction == ship.DirectionIn {
			if s.Delivery == nil || s.Status != ship.StatusHandled {
				t.Fatalf("ship not handled by the delivery: %+v", s)
			}
			del = append(del, *s.Delivery)
		}
	}
	if len(del) != 2 || del[0] != del[1] {
		t.Fatalf("the two ships should share one delivery: %v (%s, %s)", del, first.ID, second.ID)
	}
	if got := outbound(ships); len(got) != 1 || got[0].Text != "echo: second" {
		t.Fatalf("replies: %+v", got)
	}
	h.prompts(1)
	h.stop()
}

// An agent that can't take input mid-turn (no _rness/steer) gets the second
// message as the next turn's prompt: two deliveries, no steer.
func TestConnectorQueuesWhenTheAgentCannotSteer(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("queue")
	p.letIn(d)
	h := p.startConnector(connectorOpts{})
	if a := p.ready(anyConn); a.Steer {
		t.Fatalf("hello.steer without the capability: %+v", a)
	}

	first := p.inject(d, "900", "m1", "hold: first")
	h.prompts(1)
	p.inject(d, "900", "m2", "second")
	h.release()
	ships := p.shipsOf(first.Conversation, "both messages answered", answered(2))
	prompts := h.prompts(2)
	if !strings.Contains(prompts[0].Text, "Ana: hold: first") || !strings.Contains(prompts[1].Text, "Ana: second") ||
		prompts[0].Session != prompts[1].Session {
		t.Fatalf("prompts: %+v", prompts)
	}
	if len(acptest.Of(h.records(), "steer")) != 0 {
		t.Fatalf("a steer reached an agent that can't take it: %+v", h.records())
	}
	var del []identity.DeliveryID
	for _, s := range ships {
		if s.Direction == ship.DirectionIn {
			del = append(del, *s.Delivery)
		}
	}
	if len(del) != 2 || del[0] == del[1] {
		t.Fatalf("two deliveries expected: %v", del)
	}
}

// The queue mode works with an agent that could steer: -deliver queue.
func TestConnectorQueueModeOverridesSteering(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("queue-mode")
	p.letIn(d)
	h := p.startConnector(connectorOpts{caps: "steer", deliver: connector.DeliverQueue})
	if a := p.ready(anyConn); a.Steer {
		t.Fatalf("queue mode says steer: %+v", a)
	}
	first := p.inject(d, "900", "m1", "hold: first")
	h.prompts(1)
	p.inject(d, "900", "m2", "second")
	h.release()
	p.shipsOf(first.Conversation, "both messages answered", answered(2))
	h.prompts(2)
}

// Linked doors are one thread: after the link both doors' messages go to
// the same agent session, each prompt names the other door, and each reply
// goes back to the door the message came from.
func TestConnectorWorksALinkedThreadAsOne(t *testing.T) {
	p := startPort(t)
	a, b := p.apiDock("door-a"), p.apiDock("door-b")
	p.letIn(a)
	p.letIn(b)
	h := p.startConnector(connectorOpts{caps: "load"})
	p.ready(anyConn)

	a1 := p.inject(a, "1", "a1", "first on A")
	p.shipsOf(a1.Conversation, "A answered", answered(1))
	b1 := p.inject(b, "2", "b1", "first on B")
	p.shipsOf(b1.Conversation, "B answered", answered(1))
	before := h.prompts(2)
	if before[0].Session == before[1].Session {
		t.Fatalf("separate conversations share a session: %+v", before)
	}

	var doors []struct{}
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+a1.Conversation.String()+"/links",
		map[string]string{"conversation_id": b1.Conversation.String()}, &doors); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}

	a2 := p.inject(a, "1", "a2", "second on A")
	p.shipsOf(a2.Conversation, "A answered again", answered(2))
	b2 := p.inject(b, "2", "b2", "second on B")
	p.shipsOf(b2.Conversation, "B answered again", answered(2))
	after := h.prompts(4)[2:]
	if after[0].Session != after[1].Session {
		t.Fatalf("the linked doors should share one session: %+v", after)
	}
	for _, old := range before {
		if after[0].Session == old.Session {
			t.Fatalf("linking should start the thread's own session, not reuse %s", old.Session)
		}
	}
	for _, want := range []string{
		"This conversation is one door of a thread",
		"- api Chat 2 (direct): conversation_id " + b1.Conversation.String(),
		"(api Chat 1) Ana: second on A",
	} {
		if !strings.Contains(after[0].Text, want) {
			t.Fatalf("prompt of door A lacks %q:\n%s", want, after[0].Text)
		}
	}
	if !strings.Contains(after[1].Text, "- api Chat 1 (direct): conversation_id "+a1.Conversation.String()) {
		t.Fatalf("prompt of door B:\n%s", after[1].Text)
	}
	// Each answer went to its own door.
	for conv, want := range map[identity.ConversationID]string{a1.Conversation: "echo: second on A", b1.Conversation: "echo: second on B"} {
		var texts []string
		for _, s := range outbound(p.history(conv)) {
			texts = append(texts, s.Text)
		}
		if len(texts) != 2 || texts[1] != want {
			t.Fatalf("replies of %s: %v, want the last to be %q", conv, texts, want)
		}
	}
}

// A door whose dock takes no replies (a webhook) linked to one that does:
// the delivery starts at the webhook, the prompt says it is read-only and
// the auto-reply goes to the api door.
func TestConnectorAnswersOnTheDoorThatTakesReplies(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "connector-e2e-secret-key-0123456789abcdef" }, bootstrap.Options{})
	api := p.apiDock("people")
	p.letIn(api)
	var w dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "webhook", "name": "Alerts"}, &w); code != http.StatusCreated {
		t.Fatalf("create webhook dock: %d", code)
	}
	p.assignAs(adminToken, w.ID, p.agent(testAgent).ID, true)
	p.letIn(w)
	path := "/api/v1/docks/" + w.ID.String() + "/webhook"
	if code := p.admin(http.MethodPut, path, map[string]any{"template": map[string]string{
		"chat_id": "alerts", "external_id": "{{.id}}", "text": "{{.msg}}"}}, nil); code != http.StatusOK {
		t.Fatalf("template: %d", code)
	}
	var secret webhookInfo
	if code := p.admin(http.MethodPost, path+"/secret", nil, &secret); code != http.StatusOK || secret.Secret == "" {
		t.Fatalf("secret: %d", code)
	}
	auth := map[string]string{"Authorization": "Bearer " + secret.Secret}
	h := p.startConnector(connectorOpts{})
	p.ready(anyConn)

	// Both conversations exist: one message on each, the webhook's goes to nobody's chat.
	hello := p.inject(api, "1", "a1", "hello from a person")
	p.shipsOf(hello.Conversation, "the person answered", answered(1))
	if code, out := p.hook(w, "", auth, `{"id":"1","msg":"disk is full"}`); code != http.StatusAccepted || out["accepted"] != float64(1) {
		t.Fatalf("webhook: %d %v", code, out)
	}
	var convs query.Paginated[struct {
		ID identity.ConversationID `json:"id"`
	}]
	if code := p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+w.ID.String(), nil, &convs); code != http.StatusOK || len(convs.Items) != 1 {
		t.Fatalf("webhook conversations: %d %+v", code, convs)
	}
	hookConv := convs.Items[0].ID
	until(t, "the webhook ship handled", func() bool { return allHandled(p.history(hookConv)) })
	// A webhook door never gets a reply, even before it is linked.
	if got := outbound(p.history(hookConv)); len(got) != 0 {
		t.Fatalf("replies on a door that takes none: %+v", got)
	}
	before := len(outbound(p.history(hello.Conversation)))

	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+hello.Conversation.String()+"/links",
		map[string]string{"conversation_id": hookConv.String()}, nil); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	if code, out := p.hook(w, "", auth, `{"id":"2","msg":"disk is still full"}`); code != http.StatusAccepted || out["accepted"] != float64(1) {
		t.Fatalf("second webhook: %d %v", code, out)
	}
	p.shipsOf(hello.Conversation, "the answer on the api door", func(s []ship.Ship) bool { return len(outbound(s)) > before })
	until(t, "the second webhook ship handled", func() bool { return allHandled(p.history(hookConv)) })

	last := h.prompts(3)[2]
	for _, want := range []string{
		"This conversation is read-only",
		"conversation_id: " + hookConv.String(),
		"conversation_id " + hello.Conversation.String(),
		"Your final answer will be sent as a message to conversation " + hello.Conversation.String(),
	} {
		if !strings.Contains(last.Text, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, last.Text)
		}
	}
	got := outbound(p.history(hello.Conversation))
	if got[len(got)-1].Text != "echo: disk is still full" {
		t.Fatalf("answers on the api door: %+v", got)
	}
	if got := outbound(p.history(hookConv)); len(got) != 0 {
		t.Fatalf("a reply was stored on the read-only door: %+v", got)
	}
}

// A changed profile: the port tells the connector (profile frame), it
// drains, restarts the harness and resumes the thread's session with
// session/load; the next prompt carries the new instructions. The kit's
// env reaches the harness (except what Protect keeps), and the profile's
// model and the connector's mode are set on the session.
func TestConnectorRestartsOnAProfileChange(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "connector-e2e-secret-key-0123456789abcdef" }, bootstrap.Options{})
	d := p.apiDock("profile")
	p.letIn(d)
	tester := p.agent(testAgent)
	base := "/api/v1/agents/" + tester.ID.String()
	for name, value := range map[string]string{"REGION": "sa-east-1", "PROTECTED_VAR": "from-the-kit"} {
		if code := p.admin(http.MethodPut, base+"/secrets/"+name, map[string]any{"value": value, "env": true}, nil); code != http.StatusOK {
			t.Fatalf("secret %s: %d", name, code)
		}
	}
	v1, code := p.saveProfile(tester.ID, map[string]any{"base": 0, "instructions": "You are the first persona.", "model": "echo-large"})
	if code != http.StatusOK || v1.Version != 1 {
		t.Fatalf("save v1: %d %+v", code, v1)
	}

	h := p.startConnector(connectorOpts{
		caps: "load,systemprompt,modes,options", mode: "bypass", protect: []string{"PROTECTED_VAR"},
		vars: []string{acptest.EnvEnvNames + "=REGION,PROTECTED_VAR"},
	})
	p.ready(func(a crew.Agent) bool { return a.ProfileVersion == 1 && !a.Outdated })

	start := acptest.Of(h.records(), "start")
	if len(start) != 1 || start[0].Env["REGION"] != "sa-east-1" || start[0].Env["PROTECTED_VAR"] != "" {
		t.Fatalf("the harness's environment: %+v", start)
	}

	m1 := p.inject(d, "900", "m1", "first message")
	p.shipsOf(m1.Conversation, "first answer", answered(1))
	recs := h.records()
	setup := acptest.Of(recs, "setup")
	if len(setup) != 1 || setup[0].Method != "session/new" || setup[0].System != "You are the first persona." {
		t.Fatalf("session setup: %+v", setup)
	}
	if m := acptest.Of(recs, "mode"); len(m) != 1 || m[0].Text != "bypass" {
		t.Fatalf("session mode: %+v", m)
	}
	if c := acptest.Of(recs, "config"); len(c) != 1 || c[0].Text != "model=echo-large" {
		t.Fatalf("session option: %+v", c)
	}
	first := h.prompts(1)[0]
	if strings.Contains(first.Text, "You are the first persona.") {
		t.Fatalf("instructions sent in the prompt although the agent takes a system prompt:\n%s", first.Text)
	}

	// The profile changes: drain, restart, resume.
	v2, code := p.saveProfile(tester.ID, map[string]any{"base": 1, "instructions": "You are the second persona.", "model": "echo-large"})
	if code != http.StatusOK || v2.Version != 2 {
		t.Fatalf("save v2: %d %+v", code, v2)
	}
	p.ready(func(a crew.Agent) bool { return a.ProfileVersion == 2 && !a.Outdated })
	until(t, "the harness restarted", func() bool { return len(acptest.Of(h.records(), "start")) == 2 })

	m2 := p.inject(d, "900", "m2", "second message")
	p.shipsOf(m2.Conversation, "second answer", answered(2))
	recs = h.records()
	loads := acptest.Of(recs, "load")
	if len(loads) != 1 || loads[0].Session != first.Session {
		t.Fatalf("session/load of %s expected: %+v", first.Session, loads)
	}
	if setup := acptest.Of(recs, "setup"); len(setup) != 2 || setup[1].Method != "session/load" || setup[1].Session != first.Session ||
		setup[1].System != "You are the second persona." {
		t.Fatalf("session setup: %+v", setup)
	}
	second := h.prompts(2)[1]
	if second.Session != first.Session {
		t.Fatalf("the thread's session changed: %s vs %s", second.Session, first.Session)
	}
	if !strings.Contains(second.Text, "changed since earlier in this conversation") || !strings.Contains(second.Text, "You are the second persona.") ||
		!strings.Contains(second.Text, "Ana: second message") {
		t.Fatalf("the resumed session's prompt lacks the new instructions:\n%s", second.Text)
	}
}

// An agent without a system prompt takes the instructions with the first
// prompt of each session instead.
func TestConnectorBriefsAnAgentWithoutSystemPrompt(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "connector-e2e-secret-key-0123456789abcdef" }, bootstrap.Options{})
	d := p.apiDock("brief")
	p.letIn(d)
	tester := p.agent(testAgent)
	if _, code := p.saveProfile(tester.ID, map[string]any{"base": 0, "instructions": "Answer in rhymes."}); code != http.StatusOK {
		t.Fatalf("save profile: %d", code)
	}
	h := p.startConnector(connectorOpts{})
	p.ready(func(a crew.Agent) bool { return a.ProfileVersion == 1 })
	m1 := p.inject(d, "900", "m1", "one")
	p.shipsOf(m1.Conversation, "answer", answered(1))
	m2 := p.inject(d, "900", "m2", "two")
	p.shipsOf(m2.Conversation, "second answer", answered(2))
	prompts := h.prompts(2)
	if !strings.Contains(prompts[0].Text, "Your instructions (agent profile)") || !strings.Contains(prompts[0].Text, "Answer in rhymes.") {
		t.Fatalf("first prompt:\n%s", prompts[0].Text)
	}
	if strings.Contains(prompts[1].Text, "Answer in rhymes.") {
		t.Fatalf("instructions repeated in the second prompt:\n%s", prompts[1].Text)
	}
	if setup := acptest.Of(h.records(), "setup"); len(setup) != 1 || setup[0].System != "" {
		t.Fatalf("session setup: %+v", setup)
	}
}

// Images reach the agent as ACP image blocks only when it advertises
// promptCapabilities.image; otherwise the prompt names them.
func TestConnectorImages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caps   string
		images int
		text   string
	}{
		{"agent takes images", "image", 1, "[image foto.png attached below]"},
		{"agent does not", "", 0, "[image foto.png attached; not shown to you]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := mediaPort(t)
			d := p.apiDock("fotos")
			p.letIn(d)
			h := p.startConnector(connectorOpts{caps: tc.caps})
			p.ready(anyConn)
			s := p.injectWith(d, map[string]any{"chat_id": "200", "message_id": "img1", "sender_id": "200", "sender_name": "Ana",
				"text": "look at this", "images": []map[string]any{{"name": "foto.png", "data": png}}})
			p.shipsOf(s.Conversation, "the image answered", answered(1))
			got := h.prompts(1)[0]
			if got.Images != tc.images || !strings.Contains(got.Text, tc.text) || !strings.Contains(got.Text, "Ana: look at this") {
				t.Fatalf("prompt record: %+v\n%s", got, got.Text)
			}
			if tc.images == 1 && (len(got.ImageTypes) != 1 || got.ImageTypes[0] != "image/png") {
				t.Fatalf("image blocks: %+v", got.ImageTypes)
			}
		})
	}
}

// An approval asked over the port's HTTP API by the agent (what the
// ask_approval tool and `seaport ask` do; the echo agent makes the call
// with the SEAPORT_URL and SEAPORT_CREW_TOKEN the connector gives it): the
// card is stored, a web answer wakes the agent and its prompt carries the
// answer. The MCP tool itself isn't driven: the echo agent has no MCP
// client, and the tool is covered by the MCP e2e tests.
func TestConnectorApprovalRoundTrip(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("approvals")
	p.letIn(d)
	h := p.startConnector(connectorOpts{})
	p.ready(anyConn)

	m1 := p.inject(d, "900", "m1", "ask: Deploy now?|Yes|No")
	var approvals query.Paginated[ship.Approval]
	until(t, "the approval stored", func() bool {
		p.admin(http.MethodGet, "/api/v1/conversations/"+m1.Conversation.String()+"/approvals", nil, &approvals)
		return len(approvals.Items) == 1
	})
	a := approvals.Items[0]
	if a.Status != ship.ApprovalPending || a.Text != "Deploy now?" || len(a.Options) != 2 {
		t.Fatalf("approval: %+v", a)
	}
	until(t, "the ask's outcome in the agent log", func() bool { return len(acptest.Of(h.records(), "ask")) == 1 })
	if r := acptest.Of(h.records(), "ask")[0]; !strings.HasPrefix(r.Text, "201 ") {
		t.Fatalf("ask: %+v", r)
	}
	p.shipsOf(m1.Conversation, "the first turn answered", answered(2)) // the card and the echo

	if code := p.admin(http.MethodPost, "/api/v1/approvals/"+a.ID.String()+"/answer", map[string]string{"option": "Yes"}, nil); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	woken := h.prompts(2)[1]
	for _, want := range []string{"Approvals you asked", a.ID.String() + ` answered "Yes"`, "[approval " + a.ID.String() + "]"} {
		if !strings.Contains(woken.Text, want) {
			t.Fatalf("the wake prompt lacks %q:\n%s", want, woken.Text)
		}
	}
	if got := p.approval(a.ID); got.Status != ship.ApprovalAnswered {
		t.Fatalf("approval after the answer: %+v", got)
	}
}

// A schedule firing is a message from system:schedule that goes through
// the port like any other; the agent answers it.
func TestConnectorAnswersAScheduledMessage(t *testing.T) {
	p := startPortWith(t, fastTick, bootstrap.Options{ScheduleTick: 200 * time.Millisecond})
	d := p.apiDock("sched")
	p.letIn(d)
	h := p.startConnector(connectorOpts{})
	p.ready(anyConn)

	first := p.inject(d, "900", "m1", "hello")
	p.shipsOf(first.Conversation, "the first answer", answered(1))
	var sc struct {
		ID identity.ScheduleID `json:"id"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "standup", "conversation_id": first.Conversation.String(),
		"cron": "0 9 * * *", "text": "Daily summary please"}, &sc); code != http.StatusCreated {
		t.Fatalf("create schedule: %d", code)
	}
	dueNow(t, sc.ID, 5*time.Second)

	ships := p.shipsOf(first.Conversation, "the scheduled message answered", answered(2))
	var fired ship.Ship
	for _, s := range ships {
		if s.SenderID == "system:schedule" {
			fired = s
		}
	}
	if fired.ID == (identity.ShipID{}) || fired.Text != "Daily summary please" || fired.Status != ship.StatusHandled || fired.SenderName != "Seaport · standup" {
		t.Fatalf("scheduled ship: %+v", fired)
	}
	if got := outbound(ships); got[1].Text != "echo: Daily summary please" {
		t.Fatalf("replies: %+v", got)
	}
	second := h.prompts(2)[1]
	if !strings.Contains(second.Text, "Seaport · standup: Daily summary please") {
		t.Fatalf("prompt:\n%s", second.Text)
	}
}

// A turn that fails (the agent errors) releases the delivery, so the ship
// goes back to the queue and is not marked handled; the connector goes on
// working and answers the next message.
func TestConnectorReleasesAFailedTurn(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("fail")
	p.letIn(d)
	h := p.startConnector(connectorOpts{})
	p.ready(anyConn)

	bad := p.inject(d, "900", "m1", "fail: please")
	until(t, "the failing prompt", func() bool { return len(acptest.Of(h.records(), "prompt")) >= 1 })
	for _, s := range p.history(bad.Conversation) {
		if s.Direction == ship.DirectionIn && s.Status == ship.StatusHandled {
			t.Fatalf("a failed turn was acked: %+v", s)
		}
	}
	if got := outbound(p.history(bad.Conversation)); len(got) != 0 {
		t.Fatalf("a failed turn replied: %+v", got)
	}
}
