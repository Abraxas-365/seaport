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
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/coder/websocket"
)

// TestBrowserViewer: the viewer WebSocket relays CDP to the slot's browser
// (the fake runtime echoes), with its guards: the environment's switch,
// the profile's switch, a running slot, one viewer at a time, same
// origin, the audit events.
func TestBrowserViewer(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-browser-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: time.Hour})
	events := p.openEvents("")
	ag := p.agent("surf")
	var env hosting.Environment
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "plain",
		"spec": map[string]any{"harness": "rness"}}, &env); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	hostPath := "/api/v1/agents/" + ag.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "plain", "slots": 1}, nil); code != http.StatusOK {
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

	// No browser in the environment: refused (422) before any upgrade.
	if _, code, err := dial(""); err == nil || code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer without a browser: %d %v", code, err)
	}
	if code := p.admin(http.MethodPut, "/api/v1/environments/"+env.ID.String(), map[string]any{"base": env.Version,
		"spec": map[string]any{"harness": "rness", "browser": map[string]any{"engine": "chromium"}}}, &env); code != http.StatusOK {
		t.Fatalf("save environment with a browser: %d", code)
	}
	// The profile's switch is off.
	if _, code, _ := dial(""); code != http.StatusUnprocessableEntity {
		t.Fatalf("viewer with the profile's browser off: %d", code)
	}
	if _, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "browser": true}); code != http.StatusOK {
		t.Fatalf("profile browser on: %d", code)
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

	// Open: CDP goes through, same origin accepted.
	conn, code, err := dial(p.srv.URL)
	if err != nil {
		t.Fatalf("open viewer: %d %v", code, err)
	}
	next(t, events, hosting.EventSlotBrowser, func(raw json.RawMessage) bool {
		var e hosting.BrowserEvent
		return json.Unmarshal(raw, &e) == nil && e.State == hosting.BrowserOpened && e.Slot == 1 && e.Agent == ag.ID && e.By != ""
	})
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
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+ag.ID.String()+"/profile", nil, &prof); code != http.StatusOK || !prof.Browser {
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
	if failed, msg, out := call(map[string]any{"host": "mail.example.com", "kind": "Captcha"}); failed || out["kind"] != "captcha" {
		t.Fatalf("captcha: %v %s %v", failed, msg, out)
	}
	call(map[string]any{"host": "app.example.com", "kind": "2fa", "note": "again"}) // replaces, newest first
	var a agent.Agent
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+ag.ID.String(), nil, &a); code != http.StatusOK ||
		len(a.Takeovers) != 2 || a.Takeovers[0].Host != "app.example.com" || a.Takeovers[0].Kind != agent.Kind2FA || a.Takeovers[0].Note != "again" ||
		a.Takeovers[0].Conversation != nil || a.Takeovers[1].Host != "mail.example.com" {
		t.Fatalf("takeovers: %d %+v", code, a.Takeovers)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/app.example.com", nil, &a); code != http.StatusOK ||
		len(a.Takeovers) != 1 || a.Takeovers[0].Host != "mail.example.com" {
		t.Fatalf("end: %d %+v", code, a.Takeovers)
	}
	// Ending an absent host is fine; a bad one isn't.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/nothing.example.com", nil, nil); code != http.StatusOK {
		t.Fatalf("end absent: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/takeovers/bad%20host", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("end bad host: %d", code)
	}
}
