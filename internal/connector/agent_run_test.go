package connector

// These tests run the real StartAgent / Run against the test-only ACP echo
// agent (internal/connector/acptest): the test binary re-executes itself as
// the agent (TestACPAgentMain), and a small fake port plays the other end.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/connector/acptest"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"
)

// TestACPAgentMain is the echo agent when the connector starts this test
// binary as its harness; it skips in a normal run.
func TestACPAgentMain(t *testing.T) {
	if !acptest.Active() {
		t.Skip("serves ACP only when started as the agent")
	}
	acptest.RunFromEnv()
}

// echoConfig is a connector config whose harness is the echo agent.
func echoConfig(t *testing.T, caps string, vars ...string) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "acp.jsonl")
	vars = append([]string{acptest.EnvLog + "=" + log, acptest.EnvCaps + "=" + caps}, vars...)
	return Config{Server: "http://127.0.0.1:1", Token: "sp_crew_test", AutoReply: true, Cwd: dir,
		Command: acptest.Command(vars...), SessionsFile: filepath.Join(dir, "state", "sessions.json")}, log
}

func startEcho(t *testing.T, cfg Config, kit *agent.Kit) *Agent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := StartAgent(ctx, cfg, kit, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	return a
}

func deliveryOf(texts ...string) ship.Delivery {
	conv := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelAPI, Kind: conversation.KindDirect, Title: "Chat"}
	conv.Thread = identity.ThreadID(conv.ID)
	d := ship.Delivery{ID: identity.NewDeliveryID(), Conversation: conv}
	for _, s := range texts {
		d.Ships = append(d.Ships, ship.Ship{ID: identity.NewShipID(), Conversation: conv.ID, SenderName: "Ana", Text: s, SentAt: time.Now()})
	}
	return d
}

// The harness gets the kit's env except what the slot protects, the
// connector's own address and token, the profile's model (rness's base
// image starts sessions on it), and the kit's instructions as a system
// prompt when it takes one; a turn's text is the answer.
func TestStartAgentEnvironmentAndPrompt(t *testing.T) {
	cfg, log := echoConfig(t, "systemprompt,image,load", acptest.EnvEnvNames+"=REGION,KEPT,SEAPORT_URL,SEAPORT_CREW_TOKEN,SEAPORT_AGENT_MODEL")
	cfg.Protect = []string{"KEPT"}
	cfg.KitEnv = true // a hosted slot: the kit's env secrets reach the harness
	t.Setenv("KEPT", "from-the-slot")
	kit := &agent.Kit{Name: "pm", Version: 2, Instructions: " Be brief. ", Model: "openrouter/acme/m1",
		Env: map[string]string{"REGION": "eu-1", "KEPT": "from-the-kit", "SEAPORT_AGENT_MODEL": "from-the-kit"}}
	a := startEcho(t, cfg, kit)

	start := acptest.Of(acptest.ReadLog(log), "start")
	if len(start) != 1 || start[0].Env["REGION"] != "eu-1" || start[0].Env["KEPT"] != "from-the-slot" ||
		start[0].Env["SEAPORT_URL"] != cfg.Server || start[0].Env["SEAPORT_CREW_TOKEN"] != "sp_crew_test" ||
		start[0].Env["SEAPORT_AGENT_MODEL"] != "openrouter/acme/m1" {
		t.Fatalf("environment: %+v", start)
	}
	if a.Version() != 2 || a.Revision() != "" || !a.Profiled() || !a.TakesImages() || a.CanSteer() {
		t.Fatalf("agent: version %d profiled %v images %v steer %v", a.Version(), a.Profiled(), a.TakesImages(), a.CanSteer())
	}
	d := deliveryOf("hello there")
	answer, err := a.Prompt(context.Background(), d.Conversation.Thread, Prompt(d, true, nil), nil, nil)
	if err != nil || answer != "echo: hello there" {
		t.Fatalf("answer %q, %v", answer, err)
	}
	setup := acptest.Of(acptest.ReadLog(log), "setup")
	if len(setup) != 1 || setup[0].Method != "session/new" || setup[0].System != "Be brief." {
		t.Fatalf("setup: %+v", setup)
	}
	a.Close()
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the agent did not exit")
	}
}

// Sessions survive a restart through sessions.json (session/load), keyed
// per port, workspace, command and agent; another key's entries stay.
func TestSessionsFilePersistsAndResumes(t *testing.T) {
	cfg, log := echoConfig(t, "load")
	d := deliveryOf("one")
	thread := d.Conversation.Thread
	first := startEcho(t, cfg, nil)
	if _, err := first.Prompt(context.Background(), thread, "[2026-01-02 15:04] Ana: one", nil, nil); err != nil {
		t.Fatal(err)
	}
	first.Stop()

	var f sessionsFile
	raw, err := os.ReadFile(cfg.SessionsFile)
	if err != nil || json.Unmarshal(raw, &f) != nil || len(f) != 1 {
		t.Fatalf("sessions file: %v %s", err, raw)
	}
	key := first.sessionsKey()
	if !strings.HasPrefix(key, cfg.Server+" "+cfg.Cwd+" ") || strings.Count(key, " ") != 2 {
		t.Fatalf("key without a kit: %q", key)
	}
	saved := f[key][thread.String()]
	if saved.ID == "" || saved.Instructions != nil {
		t.Fatalf("saved: %+v", saved)
	}

	// Another agent's entry in the same file is kept when this one saves.
	f["other-key"] = map[string]savedSession{thread.String(): {ID: "x"}}
	raw, _ = json.Marshal(f)
	if err := os.WriteFile(cfg.SessionsFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	second := startEcho(t, cfg, nil)
	if _, err := second.Prompt(context.Background(), thread, "[2026-01-02 15:04] Ana: two", nil, nil); err != nil {
		t.Fatal(err)
	}
	second.saveSessions()
	loads := acptest.Of(acptest.ReadLog(log), "load")
	if len(loads) != 1 || loads[0].Session != string(saved.ID) {
		t.Fatalf("session/load of %s expected: %+v", saved.ID, loads)
	}
	raw, _ = os.ReadFile(cfg.SessionsFile)
	if !strings.Contains(string(raw), "other-key") {
		t.Fatalf("another key's sessions were dropped: %s", raw)
	}
}

// A session file that is garbage, missing or has bad thread ids loads as
// no sessions; resuming an agent that can't resume starts a new session.
func TestLoadSessionsToleratesBadFiles(t *testing.T) {
	cfg, log := echoConfig(t, "") // no load, no resume
	thread := identity.NewThreadID()
	for _, content := range []string{"{not json", `{"k":{"not-a-thread":"abc"}}`} {
		if err := os.MkdirAll(filepath.Dir(cfg.SessionsFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.SessionsFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &Agent{cfg: cfg, sessions: map[string]savedSession{}}
		a.loadSessions()
		if len(a.sessions) != 0 {
			t.Fatalf("%q loaded %v", content, a.sessions)
		}
	}
	// A saved session the agent can't resume: a new one is created.
	raw, _ := json.Marshal(sessionsFile{(&Agent{cfg: cfg}).sessionsKey(): {thread.String(): {ID: "old"}}})
	if err := os.WriteFile(cfg.SessionsFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a := startEcho(t, cfg, nil)
	if _, err := a.Prompt(context.Background(), thread, "[2026-01-02 15:04] Ana: hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	recs := acptest.ReadLog(log)
	if len(acptest.Of(recs, "new")) != 1 || len(acptest.Of(recs, "load")) != 0 {
		t.Fatalf("records: %+v", recs)
	}
	// No sessions file configured: nothing is read or written.
	b := &Agent{cfg: Config{}, sessions: map[string]savedSession{}}
	b.loadSessions()
	b.saveSessions()
}

// The profile's model and the connector's mode are set on a new session,
// and an unknown mode or option is left alone.
func TestSessionModeAndModelAreConfigured(t *testing.T) {
	cfg, log := echoConfig(t, "modes,options")
	cfg.Mode = "bypass"
	a := startEcho(t, cfg, &agent.Kit{Name: "pm", Model: "echo-large", Effort: "high"})
	d := deliveryOf("x")
	if _, err := a.Prompt(context.Background(), d.Conversation.Thread, "[2026-01-02 15:04] Ana: x", nil, nil); err != nil {
		t.Fatal(err)
	}
	recs := acptest.ReadLog(log)
	if m := acptest.Of(recs, "mode"); len(m) != 1 || m[0].Text != "bypass" {
		t.Fatalf("mode: %+v", m)
	}
	if c := acptest.Of(recs, "config"); len(c) != 1 || c[0].Text != "model=echo-large" {
		t.Fatalf("config: %+v", c)
	}
	cfg.Mode = "nonexistent"
	b := startEcho(t, cfg, nil)
	if _, err := b.Prompt(context.Background(), identity.NewThreadID(), "[2026-01-02 15:04] Ana: y", nil, nil); err != nil {
		t.Fatal(err)
	}
	if m := acptest.Of(acptest.ReadLog(log), "mode"); len(m) != 1 {
		t.Fatalf("an unknown mode was set: %+v", m)
	}
}

// Steering reaches an agent that advertises it, only while a turn runs on
// the thread's session; the turn's answer follows the steer.
func TestSteerReachesTheRunningTurn(t *testing.T) {
	cfg, log := echoConfig(t, "steer")
	a := startEcho(t, cfg, nil)
	if !a.CanSteer() {
		t.Fatal("the echo agent advertises steer")
	}
	d := deliveryOf("hold: wait")
	thread := d.Conversation.Thread
	if ok, err := a.Steer(context.Background(), thread, "early", nil); ok || err != nil {
		t.Fatalf("steer with no session: %v %v", ok, err)
	}
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := a.Prompt(context.Background(), thread, Prompt(d, true, nil), nil, nil)
		done <- result{answer, err}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for len(acptest.Of(acptest.ReadLog(log), "prompt")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the prompt never reached the agent")
		}
		time.Sleep(10 * time.Millisecond)
	}
	more := deliveryOf("second")
	steered, err := a.Steer(context.Background(), thread, SteerPrompt(more, nil), nil)
	if err != nil || !steered {
		t.Fatalf("steer: %v %v", steered, err)
	}
	if err := os.WriteFile(acptest.GatePath(log), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.answer != "echo: second" {
		t.Fatalf("result: %+v", r)
	}
	if ok, err := a.Steer(context.Background(), thread, "late", nil); ok || err != nil {
		t.Fatalf("steer after the turn: %v %v", ok, err)
	}
}

// A turn the agent fails returns an external error, and failures back off
// longer each time until a success resets them.
func TestFailedTurnsBackOff(t *testing.T) {
	cfg, _ := echoConfig(t, "")
	a := startEcho(t, cfg, nil)
	d := deliveryOf("fail: now")
	if _, err := a.Prompt(context.Background(), d.Conversation.Thread, "[2026-01-02 15:04] Ana: fail: now", nil, nil); err == nil {
		t.Fatal("a failing turn must be an error")
	}
	var got []time.Duration
	for range 7 {
		got = append(got, a.failed())
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff = %v, want %v", got, want)
		}
	}
	a.succeeded()
	if a.failed() != time.Second {
		t.Fatal("a success resets the backoff")
	}
}

// StartAgent refuses a command that isn't there and one that isn't ACP.
func TestStartAgentRefusesBadCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := StartAgent(ctx, Config{Command: []string{"/definitely/not/here"}}, nil, nil); err == nil {
		t.Fatal("a missing command must fail")
	}
	if _, err := StartAgent(ctx, Config{Command: []string{"true"}}, nil, nil); err == nil {
		t.Fatal("a command that is not an ACP agent must fail")
	}
	if err := Run(ctx, Config{}); err == nil {
		t.Fatal("Run needs a command")
	}
}

// A fake port: serves the kit and one crew WebSocket that sends the frames
// of a script and records what the connector sends back.
type fakePort struct {
	srv    *httptest.Server
	mu     sync.Mutex
	frames []crew.Frame
	hellos int
	kit    agent.Kit
	script func(write func(crew.Frame), read func() (crew.Frame, bool))
}

func newFakePort(t *testing.T, script func(write func(crew.Frame), read func() (crew.Frame, bool))) *fakePort {
	t.Helper()
	p := &fakePort{script: script, kit: agent.Kit{Name: "pm", Version: 1, Revision: "r1"}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crew/profile" {
			w.Header().Set("Content-Type", "application/json")
			p.mu.Lock()
			kit := p.kit
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(kit)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ctx := r.Context()
		write := func(f crew.Frame) {
			raw, _ := json.Marshal(f)
			_ = ws.Write(ctx, websocket.MessageText, raw)
		}
		read := func() (crew.Frame, bool) {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return crew.Frame{}, false
			}
			f, err := crew.Decode(raw)
			if err != nil {
				return crew.Frame{}, false
			}
			p.mu.Lock()
			p.frames = append(p.frames, f)
			if f.Type == crew.FrameHello {
				p.hellos++
			}
			p.mu.Unlock()
			return f, true
		}
		if _, ok := read(); !ok { // hello
			return
		}
		write(crew.Frame{Type: crew.FrameWelcome, MCPURL: "http://x/mcp"})
		p.script(write, read)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePort) seen(typ crew.FrameType) []crew.Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []crew.Frame
	for _, f := range p.frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

// runUntil runs the connector in a goroutine, waits for cond and stops it:
// Run must return nil once its context ends.
func runUntil(t *testing.T, cfg Config, what string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		select {
		case err := <-done:
			t.Fatalf("Run ended early (%v) while waiting for %s", err, what)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
}

// A delivery goes to the agent; the answer is replied to the door that
// takes replies and the ack names the ships handled.
func TestRunAnswersADelivery(t *testing.T) {
	d := deliveryOf("what is up")
	p := newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		write(crew.Frame{Type: crew.FrameDelivery, Delivery: &d})
		for {
			if f, ok := read(); !ok || f.Type == crew.FrameAck {
				return
			}
		}
	})
	cfg, _ := echoConfig(t, "")
	cfg.Server = p.srv.URL
	runUntil(t, cfg, "the ack", func() bool { return len(p.seen(crew.FrameAck)) == 1 })
	replies := p.seen(crew.FrameReply)
	if len(replies) != 1 || replies[0].Text != "echo: what is up" || *replies[0].ConversationID != d.Conversation.ID {
		t.Fatalf("replies: %+v", replies)
	}
	if ack := p.seen(crew.FrameAck)[0]; *ack.DeliveryID != d.ID || len(ack.ShipIDs) != 1 || ack.ShipIDs[0] != d.Ships[0].ID {
		t.Fatalf("ack: %+v", ack)
	}
	if h := p.seen(crew.FrameHello); len(h) != 1 || h[0].Steer || h[0].ProfileVersion != 1 {
		t.Fatalf("hello: %+v", h)
	}
}

// A delivery whose thread has no door that takes replies is acked with no
// reply; one the agent fails is released.
func TestRunNeverRepliesToReadOnlyThreadsAndReleasesFailures(t *testing.T) {
	quiet := deliveryOf("alert")
	quiet.ReadOnly = []identity.ConversationID{quiet.Conversation.ID}
	bad := deliveryOf("fail: boom")
	p := newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		write(crew.Frame{Type: crew.FrameDelivery, Delivery: &quiet})
		for {
			f, ok := read()
			if !ok {
				return
			}
			if f.Type == crew.FrameAck {
				write(crew.Frame{Type: crew.FrameDelivery, Delivery: &bad})
			}
			if f.Type == crew.FrameRelease {
				return
			}
		}
	})
	cfg, _ := echoConfig(t, "")
	cfg.Server = p.srv.URL
	runUntil(t, cfg, "the release", func() bool { return len(p.seen(crew.FrameRelease)) == 1 })
	if r := p.seen(crew.FrameReply); len(r) != 0 {
		t.Fatalf("a reply was sent: %+v", r)
	}
	if rel := p.seen(crew.FrameRelease)[0]; *rel.DeliveryID != bad.ID {
		t.Fatalf("release: %+v", rel)
	}
}

// A steer for a delivery in flight joins it and its ships are in the ack;
// a steer for an unknown delivery is ignored.
func TestRunSteersAndIgnoresStrays(t *testing.T) {
	d := deliveryOf("hold: first")
	more := deliveryOf("second")
	more.ID = d.ID
	more.Conversation = d.Conversation
	stray := deliveryOf("stray")
	cfg, log := echoConfig(t, "steer")
	p := newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		write(crew.Frame{Type: crew.FrameDelivery, Delivery: &d})
		for len(acptest.Of(acptest.ReadLog(log), "prompt")) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		write(crew.Frame{Type: crew.FrameSteer, Delivery: &stray})
		write(crew.Frame{Type: crew.FrameSteer, Delivery: &more})
		for len(acptest.Of(acptest.ReadLog(log), "steer")) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(acptest.GatePath(log), nil, 0o600)
		for {
			if f, ok := read(); !ok || f.Type == crew.FrameAck {
				return
			}
		}
	})
	cfg.Server = p.srv.URL
	runUntil(t, cfg, "the ack", func() bool { return len(p.seen(crew.FrameAck)) == 1 })
	ack := p.seen(crew.FrameAck)[0]
	if len(ack.ShipIDs) != 2 || ack.ShipIDs[0] != d.Ships[0].ID || ack.ShipIDs[1] != more.Ships[0].ID {
		t.Fatalf("ack: %+v", ack)
	}
	if h := p.seen(crew.FrameHello); !h[0].Steer {
		t.Fatalf("hello: %+v", h)
	}
}

// A browser frame while a turn runs steers a note into it (the viewer
// opened, then closed); a delivery that arrives while the viewer is open
// gets the note before its prompt; a repeated state is told once.
func TestRunTellsTheAgentAboutTheViewer(t *testing.T) {
	first := deliveryOf("hold: first")
	second := deliveryOf("second")
	cfg, log := echoConfig(t, "steer")
	p := newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		write(crew.Frame{Type: crew.FrameDelivery, Delivery: &first})
		for len(acptest.Of(acptest.ReadLog(log), "prompt")) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		write(crew.Frame{Type: crew.FrameBrowser, Viewer: crew.ViewerOn})
		write(crew.Frame{Type: crew.FrameBrowser, Viewer: crew.ViewerOn}) // the same state again: nothing
		write(crew.Frame{Type: crew.FrameBrowser, Viewer: crew.ViewerOff})
		for len(acptest.Of(acptest.ReadLog(log), "steer")) < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		write(crew.Frame{Type: crew.FrameBrowser, Viewer: crew.ViewerOn})
		write(crew.Frame{Type: crew.FrameDelivery, Delivery: &second}) // read in order: the viewer is on when it starts
		for len(acptest.Of(acptest.ReadLog(log), "prompt")) < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(acptest.GatePath(log), nil, 0o600)
		for {
			if f, ok := read(); !ok || (f.Type == crew.FrameAck && *f.DeliveryID == first.ID) {
				return
			}
		}
	})
	cfg.Server = p.srv.URL
	runUntil(t, cfg, "both acks", func() bool { return len(p.seen(crew.FrameAck)) == 2 })
	// on, off, on again (the second "on" in a row said nothing); the first
	// turn was running through all three.
	steers := acptest.Of(acptest.ReadLog(log), "steer")
	if len(steers) != 3 || steers[0].Text != viewerOnNote || steers[1].Text != viewerOffNote || steers[2].Text != viewerOnNote {
		t.Fatalf("steers: %+v", steers)
	}
	prompts := acptest.Of(acptest.ReadLog(log), "prompt")
	if len(prompts) != 2 || strings.Contains(prompts[0].Text, "[seaport] A person") || !strings.HasPrefix(prompts[1].Text, viewerOnNote) || !strings.Contains(prompts[1].Text, "Ana: second") {
		t.Fatalf("prompts: %+v", prompts)
	}
	// The notes are hints: the acks name only the ships.
	for _, a := range p.seen(crew.FrameAck) {
		if len(a.ShipIDs) != 1 {
			t.Fatalf("ack: %+v", a)
		}
	}
}

// A kit with a new revision makes an idle connector drain and restart its
// harness: two hellos, two harness starts, and the second with the new kit.
func TestRunRestartsTheHarnessOnANewKit(t *testing.T) {
	cfg, log := echoConfig(t, "", acptest.EnvEnvNames+"=REGION")
	var p *fakePort
	p = newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		time.Sleep(100 * time.Millisecond)
		p.mu.Lock()
		if p.hellos == 1 {
			p.kit = agent.Kit{Name: "pm", Version: 2, Revision: "r2", Env: map[string]string{"REGION": "eu-2"}}
		}
		p.mu.Unlock()
		write(crew.Frame{Type: crew.FrameProfile})
		for {
			if _, ok := read(); !ok {
				return
			}
		}
	})
	cfg.Server = p.srv.URL
	cfg.KitEnv = true
	runUntil(t, cfg, "the second connection", func() bool { return len(p.seen(crew.FrameHello)) == 2 })
	if len(p.seen(crew.FrameDrain)) != 1 {
		t.Fatalf("drain frames: %+v", p.frames)
	}
	if start := acptest.Of(acptest.ReadLog(log), "start"); start[1].Env["REGION"] != "eu-2" {
		t.Fatalf("the second harness has the new kit: %+v", start)
	}
}

// Reload (an agent that signed in) drains and restarts like a new kit.
func TestRunReloadRestartsTheHarness(t *testing.T) {
	cfg, log := echoConfig(t, "")
	reload := make(chan struct{}, 1)
	cfg.Reload = reload
	var once sync.Once
	p := newFakePort(t, func(write func(crew.Frame), read func() (crew.Frame, bool)) {
		once.Do(func() { reload <- struct{}{} }) // the first connection only
		for {
			if _, ok := read(); !ok {
				return
			}
		}
	})
	cfg.Server = p.srv.URL
	runUntil(t, cfg, "the second connection", func() bool { return len(p.seen(crew.FrameHello)) == 2 })
	if len(acptest.Of(acptest.ReadLog(log), "start")) != 2 {
		t.Fatal("the harness was not restarted")
	}
}

// The profile's MCP servers and the port's reach the session only for an
// agent that takes HTTP MCP, with the crew token and agent as headers.
func TestMCPServersOfASession(t *testing.T) {
	cfg, log := echoConfig(t, "http")
	cfg.As = "pm"
	kit := &agent.Kit{Name: "pm", MCPServers: []agent.MCPServer{{Name: "docs", URL: "https://example.com/mcp",
		Headers: []agent.Header{{Name: "X-Key", Value: "k"}}}}}
	a := startEcho(t, cfg, kit)
	a.SetMCPURL("http://127.0.0.1:1/mcp")
	servers := a.mcpServers()
	if len(servers) != 2 || servers[0].Http.Name != "seaport" || servers[1].Http.Name != "docs" {
		t.Fatalf("servers: %+v", servers)
	}
	var names []string
	for _, h := range servers[0].Http.Headers {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "Authorization,"+crew.AgentHeader {
		t.Fatalf("headers: %v", names)
	}
	d := deliveryOf("x")
	if _, err := a.Prompt(context.Background(), d.Conversation.Thread, "[2026-01-02 15:04] Ana: x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := acptest.Of(acptest.ReadLog(log), "setup"); len(s) != 1 || strings.Join(s[0].MCP, ",") != "seaport,docs" {
		t.Fatalf("setup: %+v", s)
	}
	// Without HTTP support only stdio servers (none here) are offered.
	cfg2, _ := echoConfig(t, "")
	b := startEcho(t, cfg2, kit)
	b.SetMCPURL("http://127.0.0.1:1/mcp")
	if s := b.mcpServers(); len(s) != 0 {
		t.Fatalf("servers without HTTP MCP: %+v", s)
	}
}

// The instructions the agent is given: in full, as a change, or as gone.
func TestBriefings(t *testing.T) {
	if got := briefing("Be brief."); !strings.Contains(got, "Your instructions (agent profile)") || !strings.Contains(got, "Be brief.") {
		t.Fatalf("briefing: %q", got)
	}
	if got := changedBriefing("Be loud."); !strings.Contains(got, "changed") || !strings.Contains(got, "Be loud.") {
		t.Fatalf("changed: %q", got)
	}
	if got := changedBriefing(""); !strings.Contains(got, "no longer has instructions") {
		t.Fatalf("gone: %q", got)
	}
	if instructionsHash("") != "" || len(instructionsHash("x")) != 16 || instructionsHash("x") == instructionsHash("y") {
		t.Fatal("instructionsHash")
	}
}

// The agent's own requests the connector doesn't serve are refused, and a
// text update is gathered into the answer.
func TestClientRefusesWhatItDoesNotOffer(t *testing.T) {
	c := &client{a: &Agent{answers: map[acp.SessionId]*strings.Builder{}, feeds: map[acp.SessionId]*feed{}}}
	ctx := context.Background()
	if _, err := c.ReadTextFile(ctx, acp.ReadTextFileRequest{}); err == nil {
		t.Fatal("read_text_file")
	}
	if _, err := c.WriteTextFile(ctx, acp.WriteTextFileRequest{}); err == nil {
		t.Fatal("write_text_file")
	}
	if _, err := c.CreateTerminal(ctx, acp.CreateTerminalRequest{}); err == nil {
		t.Fatal("create terminal")
	}
	if _, err := c.KillTerminal(ctx, acp.KillTerminalRequest{}); err == nil {
		t.Fatal("kill terminal")
	}
	if _, err := c.TerminalOutput(ctx, acp.TerminalOutputRequest{}); err == nil {
		t.Fatal("terminal output")
	}
	if _, err := c.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{}); err == nil {
		t.Fatal("release terminal")
	}
	if _, err := c.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{}); err == nil {
		t.Fatal("wait for terminal exit")
	}
	buf := &strings.Builder{}
	c.a.answers["s1"] = buf
	_ = c.SessionUpdate(ctx, acp.SessionNotification{SessionId: "s1", Update: acp.UpdateAgentMessageText("hi")})
	_ = c.SessionUpdate(ctx, acp.SessionNotification{SessionId: "s1", Update: acp.StartToolCall("t1", "A tool")})
	if buf.String() != "hi" {
		t.Fatalf("answer: %q", buf.String())
	}
}

// Where an auto-reply goes, step by step: the delivery's own door when it
// takes replies, else the first door of the thread that does, else nowhere.
func TestAnswerDoor(t *testing.T) {
	a := conversation.Conversation{ID: identity.NewConversationID()}
	b := conversation.Conversation{ID: identity.NewConversationID()}
	c := conversation.Conversation{ID: identity.NewConversationID()}
	cases := []struct {
		name string
		d    ship.Delivery
		want identity.ConversationID
		ok   bool
	}{
		{"own door", ship.Delivery{Conversation: a}, a.ID, true},
		{"own door in a thread", ship.Delivery{Conversation: a, Thread: []conversation.Conversation{a, b}, ReadOnly: []identity.ConversationID{b.ID}}, a.ID, true},
		{"first door that takes replies", ship.Delivery{Conversation: a, Thread: []conversation.Conversation{a, b, c}, ReadOnly: []identity.ConversationID{a.ID, b.ID}}, c.ID, true},
		{"read-only thread of one", ship.Delivery{Conversation: a, ReadOnly: []identity.ConversationID{a.ID}}, identity.ConversationID{}, false},
		{"every door read-only", ship.Delivery{Conversation: a, Thread: []conversation.Conversation{a, b}, ReadOnly: []identity.ConversationID{a.ID, b.ID}}, identity.ConversationID{}, false},
	}
	for _, tc := range cases {
		got, ok := answerDoor(tc.d)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: answerDoor = %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// fetchKitRetrying keeps trying while the port is down, and gives up on a
// refusal or when the context ends.
func TestFetchKitRetrying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(agent.Kit{Name: "pm", Revision: "r"})
	}))
	defer srv.Close()
	kit, err := fetchKitRetrying(ctx, Config{Server: srv.URL, Token: "t"})
	if err != nil || kit == nil || kit.Name != "pm" || calls != 2 {
		t.Fatalf("kit %+v err %v after %d calls", kit, err, calls)
	}
	// A cancelled context ends the retries.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer dead.Close()
	cctx, ccancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer ccancel()
	if _, err := fetchKitRetrying(cctx, Config{Server: dead.URL}); err == nil {
		t.Fatal("a port that keeps failing must end with the context")
	}
}
