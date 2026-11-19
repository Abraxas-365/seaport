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
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/coder/websocket"
)

// TestBrowserViewer: the viewer WebSocket relays CDP to the slot's browser
// (the fake runtime echoes), with its guards: the machine's engine, the
// config's switch, a running slot, one viewer at a time, same origin, the
// audit events.
func TestBrowserViewer(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-browser-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: time.Hour})
	events := p.openEvents("")
	ag := p.agent("surf")
	p.setMachine(ag.ID, "rness", map[string]any{})
	hostPath := "/api/v1/agents/" + ag.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	viewer := hostPath + "/slots/1/browser"
	dial := func(origin string) (*websocket.Conn, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h := http.Header{"Authorization": {"Bearer " + adminToken}}
		if origin != "" {
			h.Set("Origin", origin)
		}
		ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.srv.URL, "http")+viewer, &websocket.DialOptions{HTTPHeader: h})
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return ws, code, err
	}

	// No browser on the machine: refused (422) before any upgrade; a hosted
	// agent can't turn its browser on without one.
	if _, code, err := dial(""); err == nil || code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer without a browser: %d %v", code, err)
	}
	if code, _ := p.saveConfig(ag.ID, func(b map[string]any) { b["browser"] = true }); code != http.StatusUnprocessableEntity {
		t.Fatalf("browser on, machine without an engine: %d", code)
	}
	p.setMachine(ag.ID, "rness", map[string]any{"browser": map[string]any{"engine": "chromium"}})
	// The config's switch is off.
	if _, code, _ := dial(""); code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer with the config's browser off: %d", code)
	}
	if code, _ := p.saveConfig(ag.ID, func(b map[string]any) { b["browser"] = true }); code != http.StatusOK {
		t.Fatalf("config browser on: %d", code)
	}
	// The slot is stopped.
	if _, code, _ := dial(""); code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer of a stopped slot: %d", code)
	}
	rt.results <- "sha256:plain1"
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	// The workspace tells the connector which engine to run.
	st := rt.lastStart()
	var ws hosting.Workspace
	if code := p.call(http.MethodGet, "/crew/workspace", st.Env[hosting.EnvToken], nil, &ws); code != http.StatusOK || ws.Browser != hosting.BrowserChromium {
		t.Fatalf("workspace browser: %d %+v", code, ws)
	}

	// Another site's page: refused.
	if _, code, _ := dial("https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("viewer from another origin: %d", code)
	}
	// The engine isn't up: the relay's failure is the answer.
	rt.mu.Lock()
	rt.noBrowser = true
	rt.mu.Unlock()
	if _, code, _ := dial(""); code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer with the engine down: %d", code)
	}
	rt.mu.Lock()
	rt.noBrowser = false
	rt.mu.Unlock()

	// The slot's connector and another slot's, both as the agent (a hosted
	// agent takes no people's tokens): the viewer's state goes to the
	// viewed slot's connection only.
	if code := p.admin(http.MethodPost, hostPath+"/slots/2/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start slot 2: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(2, hosting.SlotRunning))
	other := rt.lastStart()
	slotConn := p.dialCrewAs(st.Env[hosting.EnvToken], crew.Frame{Type: crew.FrameHello, Agent: "slot"})
	otherConn := p.dialCrewAs(other.Env[hosting.EnvToken], crew.Frame{Type: crew.FrameHello, Agent: "slot-2"})

	// Open: CDP goes through, same origin accepted.
	conn, code, err := dial(p.srv.URL)
	if err != nil {
		t.Fatalf("open viewer: %d %v", code, err)
	}
	next(t, events, hosting.EventSlotBrowser, func(raw json.RawMessage) bool {
		var e hosting.BrowserEvent
		return json.Unmarshal(raw, &e) == nil && e.State == hosting.BrowserOpened && e.Slot == 1 && e.Agent == ag.ID && e.By != ""
	})
	if f := slotConn.read(); f.Type != crew.FrameBrowser || f.Viewer != crew.ViewerOn {
		t.Fatalf("slot connection on viewer open: %+v", f)
	}
	// The person in it keeps the slot busy (the supervisor reads the mark).
	db := testDB(t)
	viewed := func() bool {
		var on bool
		if err := db.QueryRow(`SELECT viewer_at IS NOT NULL FROM agent_slots WHERE agent_id = $1 AND idx = 1`, ag.ID).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}
	eventually(t, "slot marked viewed", viewed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":7,"method":"Target.getTargets"}`)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ID     int `json:"id"`
		Result struct {
			Echo struct {
				Method string `json:"method"`
			} `json:"echo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(msg, &res); err != nil || res.ID != 7 || res.Result.Echo.Method != "Target.getTargets" {
		t.Fatalf("CDP answer: %s %v", msg, err)
	}
	// One viewer per slot.
	if _, code, _ := dial(""); code != http.StatusConflict {
		t.Fatalf("second viewer: %d", code)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	next(t, events, hosting.EventSlotBrowser, func(raw json.RawMessage) bool {
		var e hosting.BrowserEvent
		return json.Unmarshal(raw, &e) == nil && e.State == hosting.BrowserClosed
	})
	if f := slotConn.read(); f.Type != crew.FrameBrowser || f.Viewer != crew.ViewerOff {
		t.Fatalf("slot connection on viewer close: %+v", f)
	}
	if viewed() {
		t.Fatal("the viewer mark outlived the viewer")
	}
	otherConn.quiet(300 * time.Millisecond)
	// Closed: another may open.
	conn2, code, err := dial("")
	if err != nil {
		t.Fatalf("open after close: %d %v", code, err)
	}
	conn2.Close(websocket.StatusNormalClosure, "")
}

// TestAskTakeover: the MCP tool exists only when the profile's browser is
// on; it records the site on the agent (newest first, one per host, with
// kind, note and the conversation it was working), the agent.updated event
// carries the list, and DELETE ends a host's takeover.
func TestAskTakeover(t *testing.T) {
	p := startPort(t)
	ag, other := p.agent("surf"), p.agent("other")
	d, foreign := p.openDock("surf-d"), p.openDock("foreign-d")
	p.assignAs(adminToken, d.ID, ag.ID, true)
	p.assignAs(adminToken, foreign.ID, other.ID, true)
	conv := p.inject(d, "1", "m1", "hi").Conversation
	foreignConv := p.inject(foreign, "2", "m2", "hi").Conversation
	tok := p.issueAs(adminToken, ag.ID, "laptop")
	if toolNames(t, mcpSession(t, p, tok.Value))["ask_takeover"] {
		t.Fatal("ask_takeover with the browser off")
	}
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true}); code != http.StatusOK {
		t.Fatalf("profile browser on: %d", code)
	}
	var prof agent.Profile
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+ag.ID.String()+"/config", nil, &prof); code != http.StatusOK || !prof.Browser {
		t.Fatalf("profile: %d %+v", code, prof)
	}
	s := mcpSession(t, p, tok.Value)
	if !toolNames(t, s)["ask_takeover"] {
		t.Fatal("no ask_takeover with the browser on")
	}
	events := p.openEvents("")
	call := func(args map[string]any) (bool, string, map[string]any) { return tool(t, s, "ask_takeover", args) }
	for name, args := range map[string]map[string]any{
		"bad host":             {"host": "not a host"},
		"bad kind":             {"host": "app.example.com", "kind": "payment"},
		"other without note":   {"host": "app.example.com", "kind": "other"},
		"bad conversation":     {"host": "app.example.com", "conversation_id": "nope"},
		"foreign conversation": {"host": "app.example.com", "conversation_id": foreignConv.String()},
	} {
		if failed, _, _ := call(args); !failed {
			t.Fatalf("%s was accepted", name)
		}
	}
	if failed, msg, out := call(map[string]any{"host": "App.Example.com", "note": "reading the board", "conversation_id": conv.String()}); failed ||
		out["host"] != "app.example.com" || out["kind"] != "login" || out["pending_takeovers"] != float64(1) {
		t.Fatalf("ask_takeover: %v %s %v", failed, msg, out)
	}
	next(t, events, agent.EventUpdated, func(raw json.RawMessage) bool {
		var a agent.Agent
		return json.Unmarshal(raw, &a) == nil && len(a.Takeovers) == 1 && a.Takeovers[0].Host == "app.example.com" &&
			a.Takeovers[0].Kind == agent.KindLogin && a.Takeovers[0].Note == "reading the board" &&
			a.Takeovers[0].Conversation != nil && *a.Takeovers[0].Conversation == conv
	})
	// The audit trail: asked, by the agent, about that conversation; the
	// note (what the agent was doing) stays out of it.
	trail := func(state agent.TakeoverState) agent.TakeoverEvent {
		var e agent.TakeoverEvent
		raw := next(t, events, agent.EventTakeover, func(raw json.RawMessage) bool {
			return json.Unmarshal(raw, &e) == nil && e.State == state
		})
		if strings.Contains(string(raw.Data), "reading the board") || strings.Contains(string(raw.Data), "again") {
			t.Fatalf("the trail carries the note: %s", raw.Data)
		}
		return e
	}
	if e := trail(agent.TakeoverStateAsked); e.Agent != ag.ID || e.Host != "app.example.com" || e.Kind != agent.KindLogin ||
		e.By != agent.TakeoverByAgent || e.Conversation == nil || *e.Conversation != conv || e.AskedAt.IsZero() || e.EndedBy != "" {
		t.Fatalf("asked: %+v", e)
	}
	if failed, msg, out := call(map[string]any{"host": "mail.example.com", "kind": "Captcha"}); failed || out["kind"] != "captcha" {
		t.Fatalf("captcha: %v %s %v", failed, msg, out)
	}
	if e := trail(agent.TakeoverStateAsked); e.Host != "mail.example.com" || e.Kind != agent.KindCaptcha {
		t.Fatalf("asked captcha: %+v", e)
	}
	call(map[string]any{"host": "app.example.com", "kind": "2fa", "note": "again"}) // replaces, newest first
	var a agent.Agent
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+ag.ID.String(), nil, &a); code != http.StatusOK ||
		len(a.Takeovers) != 2 || a.Takeovers[0].Host != "app.example.com" || a.Takeovers[0].Kind != agent.Kind2FA || a.Takeovers[0].Note != "again" ||
		a.Takeovers[0].Conversation != nil || a.Takeovers[1].Host != "mail.example.com" {
		t.Fatalf("takeovers: %d %+v", code, a.Takeovers)
	}
	// A new kind for an open host is asked again in the trail; the same
	// kind again (the agent retrying) is not.
	if e := trail(agent.TakeoverStateAsked); e.Host != "app.example.com" || e.Kind != agent.Kind2FA {
		t.Fatalf("asked 2fa: %+v", e)
	}
	call(map[string]any{"host": "app.example.com", "kind": "2fa"})
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/app.example.com", nil, &a); code != http.StatusOK ||
		len(a.Takeovers) != 1 || a.Takeovers[0].Host != "mail.example.com" {
		t.Fatalf("end: %d %+v", code, a.Takeovers)
	}
	// The next trail event is the end by the caller: the retry added none.
	var done agent.TakeoverEvent
	next(t, events, agent.EventTakeover, func(raw json.RawMessage) bool {
		if err := json.Unmarshal(raw, &done); err != nil {
			t.Fatal(err)
		}
		if done.State != agent.TakeoverStateDone {
			t.Fatalf("after the retry and Listo: %+v", done)
		}
		return true
	})
	if done.Host != "app.example.com" || done.Kind != agent.Kind2FA || done.EndedBy == "" || done.Conversation != nil {
		t.Fatalf("done: %+v", done)
	}
	// Ending an absent host is fine; a bad one isn't.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/nothing.example.com", nil, nil); code != http.StatusOK {
		t.Fatalf("end absent: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/bad%20host", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("end bad host: %d", code)
	}
}

// TestTakeoverAssist: the profile's browser_assist tells a person about a
// takeover (once per host) in the call's conversation or at a destination,
// with the viewer's link; Listo wakes the agent there with a system ship;
// wait_seconds blocks until then; off, nobody is told and nobody is woken
// beyond the call's conversation.
func TestTakeoverAssist(t *testing.T) {
	p := startPort(t)
	ag := p.agent("surf")
	d, other := p.openDock("surf-d"), p.openDock("surf-other")
	p.assignAs(adminToken, d.ID, ag.ID, true)
	p.assignAs(adminToken, other.ID, ag.ID, true)
	tok := p.issueAs(adminToken, ag.ID, "laptop")
	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "laptop", Concurrency: 1})
	first := p.inject(d, "1", "m1", "hi")
	ack(conn, delivered(t, conn, first))
	conv := first.Conversation
	agentPath := "/api/v1/agents/" + ag.ID.String()
	// nextDelivery reads the next delivery (profile saves send profile frames).
	nextDelivery := func() crew.Frame {
		t.Helper()
		for {
			if f := conn.read(); f.Type == crew.FrameDelivery {
				return f
			}
		}
	}

	// browser_assist needs the browser on; a bad value is refused.
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser_assist": "conversation"}); code != http.StatusBadRequest {
		t.Fatalf("assist without browser: %d", code)
	}
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true, "browser_assist": "On Call"}); code != http.StatusBadRequest {
		t.Fatalf("bad assist: %d", code)
	}
	prof, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true, "browser_assist": "conversation"})
	if code != http.StatusOK || prof.BrowserAssist != "conversation" {
		t.Fatalf("profile: %d %+v", code, prof)
	}
	s := mcpSession(t, p, tok.Value)
	call := func(args map[string]any) (bool, string, map[string]any) { return tool(t, s, "ask_takeover", args) }

	// A call naming no conversation tells nobody.
	if failed, msg, out := call(map[string]any{"host": "quiet.example.com"}); failed || out["notified"] != false {
		t.Fatalf("no conversation: %v %s %v", failed, msg, out)
	}
	if len(p.history(conv)) != 1 {
		t.Fatal("a notice went out with no conversation")
	}

	// In the conversation: an outbound ship by the agent with the link.
	if failed, msg, out := call(map[string]any{"host": "app.example.com", "kind": "2fa", "note": "paying the invoice", "conversation_id": conv.String()}); failed || out["notified"] != true || out["done"] != false {
		t.Fatalf("ask: %v %s %v", failed, msg, out)
	}
	hist := p.history(conv) // newest first
	if len(hist) != 2 || hist[0].Direction != ship.DirectionOut || hist[0].Agent == nil || *hist[0].Agent != ag.ID ||
		!strings.Contains(hist[0].Text, "El agente *surf* necesita que una persona introduzca un código de verificación en app.example.com") ||
		!strings.Contains(hist[0].Text, "Qué hacía: paying the invoice.") ||
		!strings.Contains(hist[0].Text, p.srv.URL+"/agents/"+ag.ID.String()+"#navegador\n") || !strings.Contains(hist[0].Text, "pulsa Listo") {
		t.Fatalf("notice: %+v", hist)
	}
	var a agent.Agent
	if code := p.admin(http.MethodGet, agentPath, nil, &a); code != http.StatusOK || len(a.Takeovers) != 2 ||
		a.Takeovers[0].NotifiedAt == nil || a.Takeovers[0].NotifiedIn == nil || *a.Takeovers[0].NotifiedIn != conv ||
		a.Takeovers[1].NotifiedAt != nil {
		t.Fatalf("takeovers: %d %+v", code, a.Takeovers)
	}
	// Asking again for the host sends nothing and keeps the notice.
	if failed, _, out := call(map[string]any{"host": "app.example.com", "kind": "login", "conversation_id": conv.String()}); failed || out["notified"] != true {
		t.Fatalf("again: %v", out)
	}
	if len(p.history(conv)) != 2 {
		t.Fatal("a second notice went out")
	}

	// Listo: the agent is woken in the conversation with a system ship.
	if code := p.admin(http.MethodDelete, agentPath+"/takeovers/app.example.com", nil, &a); code != http.StatusOK || len(a.Takeovers) != 1 {
		t.Fatalf("end: %d %+v", code, a.Takeovers)
	}
	f := nextDelivery()
	if len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].SenderID != ship.SystemBrowser ||
		f.Delivery.Ships[0].Conversation != conv || !strings.HasPrefix(f.Delivery.Ships[0].ExternalID, "takeover-"+ag.ID.String()+"-app.example.com-") ||
		!strings.Contains(f.Delivery.Ships[0].Text, "@surf [takeover app.example.com] a person is done (login) at app.example.com") {
		t.Fatalf("wake: %+v", f)
	}
	ack(conn, f)
	// The quiet one was told nowhere: ending it wakes nobody.
	if code := p.admin(http.MethodDelete, agentPath+"/takeovers/quiet.example.com", nil, &a); code != http.StatusOK || len(a.Takeovers) != 0 {
		t.Fatalf("end quiet: %d %+v", code, a.Takeovers)
	}

	// wait_seconds blocks until Listo and answers done.
	type result struct {
		failed bool
		out    map[string]any
	}
	got := make(chan result, 1)
	go func() {
		failed, _, out := call(map[string]any{"host": "slow.example.com", "conversation_id": conv.String(), "wait_seconds": 30})
		got <- result{failed, out}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.admin(http.MethodGet, agentPath, nil, &a)
		if _, open := a.Takeovers.Find("slow.example.com"); open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the blocked ask recorded nothing")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case r := <-got:
		t.Fatalf("the ask returned before Listo: %+v", r)
	case <-time.After(1500 * time.Millisecond):
	}
	if code := p.admin(http.MethodDelete, agentPath+"/takeovers/slow.example.com", nil, nil); code != http.StatusOK {
		t.Fatalf("end slow: %d", code)
	}
	select {
	case r := <-got:
		if r.failed || r.out["done"] != true || r.out["pending_takeovers"] != float64(0) {
			t.Fatalf("blocked ask: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked ask did not return after Listo")
	}
	ack(conn, nextDelivery()) // its wake
	if failed, _, _ := call(map[string]any{"host": "x.example.com", "wait_seconds": 291}); !failed {
		t.Fatal("wait_seconds over the cap was accepted")
	}

	// At a destination: a conversation opened on the dock that has it (a
	// thread of the call's conversation when it is the same dock), where
	// Listo wakes the agent.
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+other.ID.String()+"/destinations/oncall",
		map[string]string{"target": "oncall-chat", "title": "On call"}, nil); code != http.StatusOK {
		t.Fatalf("put destination: %d", code)
	}
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": prof.Version, "browser": true, "browser_assist": "oncall"}); code != http.StatusOK {
		t.Fatalf("profile oncall: %d", code)
	}
	s = mcpSession(t, p, tok.Value)
	if failed, msg, out := tool(t, s, "ask_takeover", map[string]any{"host": "crm.example.com", "kind": "captcha", "conversation_id": conv.String()}); failed || out["notified"] != true {
		t.Fatalf("ask oncall: %v %s %v", failed, msg, out)
	}
	var convs query.Paginated[conversation.Conversation]
	if code := p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+other.ID.String(), nil, &convs); code != http.StatusOK || len(convs.Items) != 1 ||
		convs.Items[0].ExternalID != "oncall-chat" {
		t.Fatalf("opened conversation: %d %+v", code, convs.Items)
	}
	opened := convs.Items[0]
	if h := p.history(opened.ID); len(h) != 1 || h[0].Direction != ship.DirectionOut || !strings.Contains(h[0].Text, "resuelva un captcha en crm.example.com") {
		t.Fatalf("destination notice: %+v", h)
	}
	p.admin(http.MethodGet, agentPath, nil, &a)
	if tk, ok := a.Takeovers.Find("crm.example.com"); !ok || tk.NotifiedIn == nil || *tk.NotifiedIn != opened.ID {
		t.Fatalf("notified_in: %+v", a.Takeovers)
	}
	if code := p.admin(http.MethodDelete, agentPath+"/takeovers/crm.example.com", nil, nil); code != http.StatusOK {
		t.Fatalf("end crm: %d", code)
	}
	f = nextDelivery()
	if len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Conversation != opened.ID || f.Delivery.Ships[0].SenderID != ship.SystemBrowser {
		t.Fatalf("wake at destination: %+v", f)
	}
	ack(conn, f)

	// A destination none of its docks has: nobody is told, the takeover stays.
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": prof.Version + 1, "browser": true, "browser_assist": "nowhere"}); code != http.StatusOK {
		t.Fatalf("profile nowhere: %d", code)
	}
	s = mcpSession(t, p, tok.Value)
	if failed, _, out := tool(t, s, "ask_takeover", map[string]any{"host": "lost.example.com", "conversation_id": conv.String()}); failed || out["notified"] != false {
		t.Fatalf("ask nowhere: %v %v", failed, out)
	}
}

// A takeover nobody attends within agent.TakeoverTTL is swept: it leaves
// the agent and the agent is told nobody came, once, where the notice
// went; a fresh one stays.
func TestTakeoverExpires(t *testing.T) {
	p := startPortWith(t, func(*config.Server) {}, bootstrap.Options{SweepInterval: 200 * time.Millisecond})
	ag := p.agent("patient")
	d := p.openDock("patient-d")
	p.assignAs(adminToken, d.ID, ag.ID, true)
	tok := p.issueAs(adminToken, ag.ID, "laptop")
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true, "browser_assist": "conversation"}); code != http.StatusOK {
		t.Fatalf("profile: %d", code)
	}
	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "laptop", Concurrency: 1})
	first := p.inject(d, "1", "m1", "hi")
	ack(conn, delivered(t, conn, first))
	conv := first.Conversation
	agentPath := "/api/v1/agents/" + ag.ID.String()
	events := p.openEvents("")
	s := mcpSession(t, p, tok.Value)
	for _, host := range []string{"old.example.com", "fresh.example.com"} {
		if failed, _, out := tool(t, s, "ask_takeover", map[string]any{"host": host, "kind": "captcha", "conversation_id": conv.String()}); failed || out["notified"] != true {
			t.Fatalf("ask %s: %v %v", host, failed, out)
		}
	}
	// Backdate one past the TTL (the sweeper reads the jsonb's `at`).
	db := testDB(t)
	stale := time.Now().Add(-agent.TakeoverTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(context.Background(), `UPDATE agents SET takeovers = (
		SELECT jsonb_agg(CASE WHEN t->>'host' = 'old.example.com' THEN jsonb_set(t, '{at}', to_jsonb($2::text)) ELSE t END)
		FROM jsonb_array_elements(takeovers) t) WHERE id = $1`, ag.ID, stale); err != nil {
		t.Fatal(err)
	}
	var a agent.Agent
	waitFor(t, func() bool {
		p.admin(http.MethodGet, agentPath, nil, &a)
		_, old := a.Takeovers.Find("old.example.com")
		return !old
	})
	if _, fresh := a.Takeovers.Find("fresh.example.com"); !fresh || len(a.Takeovers) != 1 {
		t.Fatalf("after the sweep: %+v", a.Takeovers)
	}
	f := conn.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].SenderID != ship.SystemBrowser ||
		!strings.Contains(f.Delivery.Ships[0].Text, "@patient [takeover old.example.com] nobody came within 1h0m0s (captcha) at old.example.com") {
		t.Fatalf("expiry wake: %+v", f)
	}
	next(t, events, agent.EventTakeover, func(raw json.RawMessage) bool {
		var e agent.TakeoverEvent
		return json.Unmarshal(raw, &e) == nil && e.State == agent.TakeoverStateExpired && e.Host == "old.example.com" &&
			e.Kind == agent.KindCaptcha && e.EndedBy == "" && e.Conversation != nil && *e.Conversation == conv
	})
	ack(conn, f)
	// Once: the next sweeps find nothing stale.
	time.Sleep(600 * time.Millisecond)
	if n := len(p.history(conv)); n != 4 { // hi, two notices, one wake
		t.Fatalf("history after sweeps: %d ships", n)
	}
}

// TestDetectorTakeover: the connector's page detector asks a takeover over
// HTTP (POST /crew/takeovers, agent tokens only) with the same rules as the
// tool, recorded as the detector's with the page's path, told as
// browser_assist says and ended by Listo like any other; the profile's
// browser_detect / browser_ignore_hosts reach the kit and are validated.
func TestDetectorTakeover(t *testing.T) {
	p := startPort(t)
	ag := p.agent("watcher")
	d := p.openDock("watcher-d")
	p.assignAs(adminToken, d.ID, ag.ID, true)
	tok := p.issueAs(adminToken, ag.ID, "laptop")
	agentPath := "/api/v1/agents/" + ag.ID.String()

	// Profile: detection needs the browser; the ignore list is validated and
	// normalized; everything reaches the kit.
	for name, body := range map[string]map[string]any{
		"detect without browser": {"base": 0, "browser_detect": true},
		"ignore without browser": {"base": 0, "browser_ignore_hosts": []string{"a.example.com"}},
		"ignore not a host":      {"base": 0, "browser": true, "browser_ignore_hosts": []string{"not a host"}},
		"ignore twice":           {"base": 0, "browser": true, "browser_ignore_hosts": []string{"a.example.com", "A.example.com"}},
	} {
		if _, code := p.saveProfile(ag.ID, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, code)
		}
	}
	prof, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true, "browser_detect": true, "browser_assist": "conversation",
		"browser_ignore_hosts": []string{" Intranet.example.com ", "wiki.example.com"}})
	if code != http.StatusOK || !prof.BrowserDetect || strings.Join(prof.BrowserIgnoreHosts, ",") != "intranet.example.com,wiki.example.com" {
		t.Fatalf("profile: %d %+v", code, prof)
	}
	kit, code := p.kit(tok.Value)
	if code != http.StatusOK || !kit.Browser || !kit.BrowserDetect || len(kit.BrowserIgnoreHosts) != 2 {
		t.Fatalf("kit: %d %+v", code, kit)
	}

	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "laptop", Concurrency: 1})
	first := p.inject(d, "1", "m1", "hi")
	ack(conn, delivered(t, conn, first))
	conv := first.Conversation

	post := func(token string, req crew.TakeoverRequest) (crew.TakeoverAnswer, int) {
		var out crew.TakeoverAnswer
		code := p.call(http.MethodPost, crew.TakeoversPath, token, req, &out)
		return out, code
	}
	// Refusals: a people's token, a bad path, a conversation of another dock.
	if _, code := post(adminToken, crew.TakeoverRequest{Host: "a.example.com"}); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("admin token on a crew route: %d", code)
	}
	if _, code := post(tok.Value, crew.TakeoverRequest{Host: "a.example.com", Detected: true, Path: "/login?token=x"}); code != http.StatusBadRequest {
		t.Fatalf("path with a query: %d", code)
	}
	if _, code := post(tok.Value, crew.TakeoverRequest{Host: "a.example.com", Kind: "payment"}); code != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", code)
	}
	elsewhere := p.openDock("watcher-elsewhere")
	foreign := p.inject(elsewhere, "9", "m9", "yo").Conversation
	if _, code := post(tok.Value, crew.TakeoverRequest{Host: "a.example.com", ConversationID: &foreign}); code != http.StatusNotFound {
		t.Fatalf("a conversation off the agent's docks: %d", code)
	}

	// The detector's ask: recorded with by/path, told in the conversation
	// with the detector's wording.
	ans, code := post(tok.Value, crew.TakeoverRequest{Host: "SSO.example.com", Kind: "login", Note: "the page detector saw a sign-in form", Detected: true, Path: "/signin", ConversationID: &conv})
	if code != http.StatusCreated || !ans.Notified || ans.Done || ans.Pending != 1 || ans.Host != "sso.example.com" {
		t.Fatalf("detected ask: %d %+v", code, ans)
	}
	var a agent.Agent
	if code := p.admin(http.MethodGet, agentPath, nil, &a); code != http.StatusOK || len(a.Takeovers) != 1 ||
		a.Takeovers[0].By != agent.TakeoverByDetector || a.Takeovers[0].Path != "/signin" || a.Takeovers[0].Kind != agent.KindLogin ||
		a.Takeovers[0].NotifiedIn == nil || *a.Takeovers[0].NotifiedIn != conv {
		t.Fatalf("takeover: %d %+v", code, a.Takeovers)
	}
	hist := p.history(conv)
	if len(hist) != 2 || !strings.Contains(hist[0].Text, "Seaport detectó que el navegador del agente *watcher* necesita que una persona inicie sesión en sso.example.com/signin") ||
		!strings.Contains(hist[0].Text, "pulsa Listo") || strings.Contains(hist[0].Text, "Qué hacía") {
		t.Fatalf("notice: %+v", hist)
	}
	// The same host again (the page still needs someone) updates and tells
	// nobody twice; the model's own ask over HTTP is the agent's.
	if ans, code := post(tok.Value, crew.TakeoverRequest{Host: "sso.example.com", Detected: true, Path: "/signin/step2", ConversationID: &conv}); code != http.StatusCreated || !ans.Notified || ans.Pending != 1 {
		t.Fatalf("again: %d %+v", code, ans)
	}
	if ans, code := post(tok.Value, crew.TakeoverRequest{Host: "shop.example.com", Kind: "captcha"}); code != http.StatusCreated || ans.Notified || ans.Pending != 2 {
		t.Fatalf("agent's ask: %d %+v", code, ans)
	}
	a = agent.Agent{} // a decode into the old value keeps omitted fields
	p.admin(http.MethodGet, agentPath, nil, &a)
	if len(a.Takeovers) != 2 || a.Takeovers[0].Asker() != agent.TakeoverByAgent || a.Takeovers[0].Path != "" || a.Takeovers[0].NotifiedAt != nil ||
		a.Takeovers[1].Path != "/signin/step2" || a.Takeovers[1].NotifiedAt == nil {
		t.Fatalf("takeovers: %+v", a.Takeovers)
	}
	if n := len(p.history(conv)); n != 2 {
		t.Fatalf("one notice per host: %d ships", n)
	}
	// Listo ends it and wakes the agent where the notice went.
	if code := p.admin(http.MethodDelete, agentPath+"/takeovers/sso.example.com", nil, nil); code != http.StatusOK {
		t.Fatalf("listo: %d", code)
	}
	f := conn.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].SenderID != ship.SystemBrowser ||
		!strings.Contains(f.Delivery.Ships[0].Text, "[takeover sso.example.com] a person is done (login)") {
		t.Fatalf("wake: %+v", f)
	}
	ack(conn, f)
}
