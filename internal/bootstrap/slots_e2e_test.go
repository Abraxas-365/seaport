package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

func slotState(index int, state string) func(json.RawMessage) bool {
	return func(d json.RawMessage) bool {
		var h hosting.Hosted
		if json.Unmarshal(d, &h) != nil {
			return false
		}
		for _, s := range h.Slots {
			if s.Index == index && s.State == state {
				return true
			}
		}
		return false
	}
}

func (p *port) slot(agentID identity.AgentID, index int) hosting.Slot {
	p.t.Helper()
	var h hosting.Hosted
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+agentID.String()+"/hosting", nil, &h); code != http.StatusOK {
		p.t.Fatalf("hosting: %d", code)
	}
	for _, s := range h.Slots {
		if s.Index == index {
			return s
		}
	}
	p.t.Fatalf("no slot %d: %+v", index, h)
	return hosting.Slot{}
}

// A hosted slot starts on its environment's image (built first when
// needed) with a token of its own (not listed, not counted), which reads
// the slot's workspace with the repo credentials resolved; stopping it
// revokes the token. New work wakes a stopped agent; a container that
// dies fails its slot; runtime slots with no row are removed.
func TestHostedSlots(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-slots-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: 200 * time.Millisecond})
	events := p.openEvents("")
	pm := p.agent("pm")
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/secrets/GITHUB_TOKEN", map[string]any{"value": "ghp_slot_secret"}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	machine := map[string]any{"workspace": "persistent",
		"repos": []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${GITHUB_TOKEN}", "on_start": "make deps"}}}
	p.setMachine(pm.ID, "rness", machine)
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}

	// Start: the image is built first, then the container runs.
	rt.results <- "sha256:img1"
	var h hosting.Hosted
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, &h); code != http.StatusAccepted || h.Slots[0].State != hosting.SlotStarting {
		t.Fatalf("start: %d %+v", code, h)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	s1 := p.slot(pm.ID, 1)
	if s1.Image != "sha256:img1" || s1.ConfigVersion != 1 || s1.Outdated || s1.StartedAt == nil {
		t.Fatalf("running slot: %+v", s1)
	}
	st := rt.lastStart()
	if st.Image != "sha256:img1" || st.Name != "pm-1" || st.Harness != "rness" || !st.Persistent ||
		st.Env[hosting.EnvURL] != "http://port.test:8740" || st.Env[hosting.EnvSlot] != "pm-1" || st.Run == "" {
		t.Fatalf("start request: %+v", st)
	}
	slotToken := st.Env[hosting.EnvToken]
	if !strings.HasPrefix(slotToken, "sp_crew_") {
		t.Fatalf("slot token: %q", slotToken)
	}
	// Starting again while running changes nothing.
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, &h); code != http.StatusAccepted || h.Slots[0].State != hosting.SlotRunning {
		t.Fatalf("start running: %d %+v", code, h)
	}

	// Its log: the runtime's tail (300 lines by default); a slot the agent
	// doesn't have is NotFound, a bad lines refused.
	var sl hosting.SlotLog
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/logs", nil, &sl); code != http.StatusOK || sl.Slot != 1 || sl.Log != "slot 1: 300 lines\n" {
		t.Fatalf("slot log: %d %+v", code, sl)
	}
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/logs?lines=20", nil, &sl); code != http.StatusOK || sl.Log != "slot 1: 20 lines\n" {
		t.Fatalf("slot log lines: %d %+v", code, sl)
	}
	if code := p.admin(http.MethodGet, hostPath+"/slots/3/logs", nil, nil); code != http.StatusNotFound {
		t.Fatalf("log of a slot it doesn't have: %d", code)
	}
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/logs?lines=0", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("log lines=0: %d", code)
	}

	// The slot token isn't listed with people's tokens; it reads the
	// slot's workspace (credentials resolved) and works as the agent.
	var tokens query.Paginated[crew.Token]
	if code := p.admin(http.MethodGet, "/api/v1/crew/tokens", nil, &tokens); code != http.StatusOK || len(tokens.Items) != 0 {
		t.Fatalf("tokens: %d %+v", code, tokens.Items)
	}
	var ws hosting.Workspace
	if code := p.call(http.MethodGet, "/crew/workspace", slotToken, nil, &ws); code != http.StatusOK || ws.Slot != "pm-1" ||
		ws.Harness != "rness" || !ws.Persistent || len(ws.Repos) != 1 || ws.Repos[0].Token != "ghp_slot_secret" ||
		ws.Repos[0].Path != "app" || ws.Repos[0].OnStart != "make deps" || ws.Concurrency != hosting.DefaultConcurrency {
		t.Fatalf("workspace: %d %+v", code, ws)
	}
	// (a hosted agent has no people's tokens: another agent's is refused too)
	people := p.issueAs(adminToken, p.agent("other").ID, "laptop")
	if code := p.call(http.MethodGet, "/crew/workspace", people.Value, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a person's token reads a workspace: %d", code)
	}
	if code := p.call(http.MethodGet, "/crew/workspace", adminToken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin token on /crew/workspace: %d", code)
	}
	if _, code := p.kit(slotToken); code != http.StatusOK {
		t.Fatalf("slot token kit: %d", code)
	}

	// Stop: the container goes, the token is revoked.
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/stop", nil, &h); code != http.StatusAccepted || h.Slots[0].State != hosting.SlotStopping {
		t.Fatalf("stop: %d %+v", code, h)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotStopped))
	ref1 := hosting.SlotRef{Organization: config.DefaultOrganization, Agent: pm.ID, Index: 1}
	if rt.running(ref1) {
		t.Fatal("container still running after stop")
	}
	if code := p.call(http.MethodGet, "/crew/workspace", slotToken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked slot token: %d", code)
	}
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/logs", nil, nil); code != http.StatusNotFound {
		t.Fatalf("log of a stopped slot: %d", code)
	}

	// New work wakes the stopped agent (the image is already built).
	d := p.openDock("support")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	p.inject(d, "51900", "w1", "hola")
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	if !rt.running(ref1) {
		t.Fatal("woken slot not running")
	}
	// Its conversations: one waiting (no connector claims it here), and a
	// second message of the same chat is still one conversation.
	p.inject(d, "51900", "w2", "¿sigues?")
	other := p.inject(d, "51901", "w3", "hola")
	if code := p.admin(http.MethodGet, hostPath, nil, &h); code != http.StatusOK || h.Working != 0 || h.Waiting != 2 {
		t.Fatalf("hosted counts: %d working %d waiting %d", code, h.Working, h.Waiting)
	}
	// A claimed one is in work (as a connector's claim leaves it).
	db := testDB(t)
	if _, err := db.Exec(`UPDATE ships SET status = 'claimed', claimed_until = now() + interval '1 hour', claimed_by = 'e2e-session'
		WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if code := p.admin(http.MethodGet, hostPath, nil, &h); code != http.StatusOK || h.Working != 1 || h.Waiting != 1 {
		t.Fatalf("hosted counts with one claimed: %d working %d waiting %d", code, h.Working, h.Waiting)
	}
	if _, err := db.Exec(`UPDATE ships SET status = 'queued', claimed_until = NULL, claimed_by = NULL
		WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	// The machine's concurrency reaches the slot through its workspace.
	machine["concurrency"] = 5
	p.setMachine(pm.ID, "rness", machine)
	running := rt.lastStart().Env[hosting.EnvToken]
	if code := p.call(http.MethodGet, "/crew/workspace", running, nil, &ws); code != http.StatusOK || ws.Concurrency != 5 {
		t.Fatalf("workspace concurrency: %d %+v", code, ws)
	}

	// The container dies: the slot fails with the runtime's detail, and
	// its token is revoked.
	woken := rt.lastStart().Env[hosting.EnvToken]
	rt.exit(ref1, "the agent's container exited: Exited (1)")
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotFailed))
	if s := p.slot(pm.ID, 1); !strings.Contains(s.Detail, "Exited (1)") {
		t.Fatalf("failed slot: %+v", s)
	}
	if code := p.call(http.MethodGet, "/crew/workspace", woken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("failed run's token: %d", code)
	}
	// With work still waiting, the agent's other (stopped) slot wakes.
	next(t, events, hosting.EventSlotsUpdated, slotState(2, hosting.SlotRunning))

	// A failed start records why and revokes its token.
	if code := p.admin(http.MethodPost, hostPath+"/slots/2/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop 2: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(2, hosting.SlotStopped))
	rt.mu.Lock()
	rt.startErr = errNoEngine
	rt.mu.Unlock()
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start 1 again: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotFailed))
	if s := p.slot(pm.ID, 1); s.Detail != "the Docker engine is unreachable" {
		t.Fatalf("failed start detail: %+v", s)
	}

	// Unhosting needs the slots stopped; afterwards the supervisor removes
	// what the runtime keeps of them.
	if code := p.admin(http.MethodDelete, hostPath, nil, nil); code != http.StatusConflict {
		t.Fatalf("unhost with a failed slot: %d", code)
	}
	for _, i := range []string{"1", "2"} {
		if code := p.admin(http.MethodPost, hostPath+"/slots/"+i+"/stop", nil, nil); code != http.StatusAccepted {
			t.Fatalf("stop %s: %d", i, code)
		}
	}
	eventually(t, "slots stopped", func() bool {
		return p.slot(pm.ID, 1).State == hosting.SlotStopped && p.slot(pm.ID, 2).State == hosting.SlotStopped
	})
	if code := p.admin(http.MethodDelete, hostPath, nil, nil); code != http.StatusNoContent {
		t.Fatalf("unhost: %d", code)
	}
	eventually(t, "orphan slots removed", func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return len(rt.slots) == 0
	})
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusNotFound {
		t.Fatalf("start an unhosted agent's slot: %d", code)
	}
}

var errNoEngine = errx.External("the Docker engine is unreachable")

// Autoscale: with it on, a conversation waiting ScaleAfter while every
// running slot works all it can starts another slot, only one that can
// work unattended (a subscription slot needs a sign-in, or the harness's
// fallback key); the extra slot stops once idle ScaleDownAfter, the
// lowest keeps running.
func TestAutoscaleSlots(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-autoscale-secrets-key-0123456789abcdef"
		c.ImageRness = "registry.test/seaport-rness:1"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: 200 * time.Millisecond})
	pm := p.agent("pm")
	// rness with no providers signs in to a subscription.
	p.setMachine(pm.ID, "rness", map[string]any{"concurrency": 1})
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	var h hosting.Hosted
	if code := p.admin(http.MethodPatch, hostPath, map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/agents/"+p.agent("other").ID.String()+"/hosting",
		map[string]any{"autoscale": true}, nil); code != http.StatusNotFound {
		t.Fatalf("patch an agent that isn't hosted: %d", code)
	}

	// Work wakes slot 1; its connection takes the first conversation.
	rt.results <- "sha256:v1"
	d := p.openDock("support")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	p.inject(d, "51901", "a1", "hola")
	eventually(t, "slot 1 woken", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotRunning })
	c1 := p.dialCrewAs(rt.lastStart().Env[hosting.EnvToken], crew.Frame{Type: crew.FrameHello, Agent: "pm-1", Concurrency: 1})
	f1 := c1.read()
	if f1.Type != crew.FrameDelivery {
		t.Fatalf("slot 1 delivery: %+v", f1)
	}

	// A second conversation waits (well past ScaleAfter): autoscale off,
	// nothing starts.
	p.inject(d, "51902", "b1", "buenas")
	db := testDB(t)
	if _, err := db.Exec(`UPDATE ships SET created_at = now() - interval '2 minutes' WHERE agent_id = $1 AND status = 'queued'`, pm.ID); err != nil {
		t.Fatal(err)
	}
	stays := func(what string) {
		t.Helper()
		time.Sleep(700 * time.Millisecond)
		if s := p.slot(pm.ID, 2); s.State != hosting.SlotStopped {
			t.Fatalf("%s: slot 2 %s (%s)", what, s.State, s.Detail)
		}
	}
	stays("autoscale off")

	// On, but slot 2 would wait for a person to sign it in.
	if code := p.admin(http.MethodPatch, hostPath, map[string]any{"autoscale": true}, &h); code != http.StatusOK || !h.Autoscale ||
		h.Working != 1 || h.Waiting != 1 {
		t.Fatalf("autoscale on: %d %+v", code, h)
	}
	stays("a slot that needs a sign-in")

	// With the harness's fallback key it can work unattended: it starts and
	// takes the waiting conversation.
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/secrets/ANTHROPIC_API_KEY",
		map[string]any{"value": "sk-test", "env": true}, nil); code != http.StatusOK {
		t.Fatalf("set fallback secret: %d", code)
	}
	eventually(t, "slot 2 started by autoscale", func() bool { return p.slot(pm.ID, 2).State == hosting.SlotRunning })
	c2 := p.dialCrewAs(rt.lastStart().Env[hosting.EnvToken], crew.Frame{Type: crew.FrameHello, Agent: "pm-2", Concurrency: 1})
	f2 := c2.read()
	if f2.Type != crew.FrameDelivery || f2.Delivery.Conversation.ID == f1.Delivery.Conversation.ID {
		t.Fatalf("slot 2 delivery: %+v", f2)
	}

	// Done: the extra slot stops once idle ScaleDownAfter (not before);
	// slot 1 keeps running.
	c1.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f1.Delivery.ID})
	c2.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f2.Delivery.ID})
	eventually(t, "both idle", func() bool {
		var h hosting.Hosted
		return p.admin(http.MethodGet, hostPath, nil, &h) == http.StatusOK && h.Working == 0 && h.Waiting == 0
	})
	time.Sleep(500 * time.Millisecond)
	if s := p.slot(pm.ID, 2); s.State != hosting.SlotRunning {
		t.Fatalf("extra slot stopped before ScaleDownAfter: %s (%s)", s.State, s.Detail)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET active_at = now() - make_interval(secs => $2::float8) - interval '1 second'
		WHERE agent_id = $1`, pm.ID, hosting.ScaleDownAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "extra slot stopped", func() bool {
		s := p.slot(pm.ID, 2)
		return s.State == hosting.SlotStopped && strings.Contains(s.Detail, "autoscale")
	})
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotRunning {
		t.Fatalf("the lowest slot stopped: %s (%s)", s.State, s.Detail)
	}
}

// An idle running slot whose machine is outdated restarts on the new one
// (an instructions edit doesn't restart it); one idle past idle_stop is
// stopped.
func TestIdleAndOutdatedSlots(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.ImageRness = "registry.test/seaport-rness:1"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: 200 * time.Millisecond})
	events := p.openEvents("")
	pm := p.agent("pm")
	machine := map[string]any{"idle_stop": "1m"}
	p.setMachine(pm.ID, "rness", machine)
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	rt.results <- "sha256:v1"
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))

	// An instructions edit: the slot keeps running on its machine.
	if code, v2 := p.saveConfig(pm.ID, func(b map[string]any) { b["instructions"] = "Be brief." }); code != http.StatusOK || v2.Version != 2 {
		t.Fatalf("instructions edit: %d %+v", code, v2)
	}
	if s := p.slot(pm.ID, 1); s.Outdated || s.ConfigVersion != 1 || s.Image != "sha256:v1" {
		t.Fatalf("instructions edit outdated the slot: %+v", s)
	}
	// A machine edit: the idle slot restarts on it (built first).
	rt.results <- "sha256:v3"
	machine["packages"] = []string{"jq"}
	p.setMachine(pm.ID, "rness", machine)
	eventually(t, "restarted on v3", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.ConfigVersion == 3 && s.Image == "sha256:v3" && !s.Outdated
	})

	// Idle past idle_stop while someone signs it in: kept running (the
	// sign-in keeps it active), then stopped once the sign-in ends.
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO slot_logins (organization_id, agent_id, idx, id, run, state)
		SELECT organization_id, agent_id, idx, 'login-1', run, 'waiting' FROM agent_slots WHERE agent_id = $1 AND idx = 1`, pm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET active_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "kept active while signing in", func() bool {
		var recent bool
		if err := db.QueryRow(`SELECT active_at > now() - interval '1 minute' FROM agent_slots WHERE agent_id = $1 AND idx = 1`, pm.ID).Scan(&recent); err != nil {
			t.Fatal(err)
		}
		return recent
	})
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotRunning {
		t.Fatalf("a slot being signed in was stopped: %s (%s)", s.State, s.Detail)
	}

	// Idle past idle_stop with a person in its browser viewer (a fresh
	// mark): kept running too; a stale mark (the relaying process died)
	// doesn't count.
	if _, err := db.Exec(`UPDATE slot_logins SET state = 'failed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET viewer_at = now(), active_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "kept active while viewed", func() bool {
		var recent bool
		if err := db.QueryRow(`SELECT active_at > now() - interval '1 minute' FROM agent_slots WHERE agent_id = $1 AND idx = 1`, pm.ID).Scan(&recent); err != nil {
			t.Fatal(err)
		}
		return recent
	})
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotRunning {
		t.Fatalf("a slot being viewed was stopped: %s (%s)", s.State, s.Detail)
	}

	// Idle past idle_stop: stopped.
	if _, err := db.Exec(`UPDATE agent_slots SET viewer_at = now() - make_interval(secs => $1::float8) - interval '1 second'`,
		hosting.ViewerStale.Seconds()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET active_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped when idle", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotStopped && strings.Contains(s.Detail, "idle")
	})

	// A machine edit whose build fails: the slot stays on its machine,
	// outdated, and idle_stop still stops it.
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start again: %d", code)
	}
	eventually(t, "running again", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotRunning })
	rt.results <- "fail:apt: no package nope"
	machine["packages"] = []string{"nope"}
	p.setMachine(pm.ID, "rness", machine)
	eventually(t, "the new build failed", func() bool {
		var b hosting.Build
		return p.admin(http.MethodGet, "/api/v1/agents/"+pm.ID.String()+"/build", nil, &b) == http.StatusOK && b.State == hosting.BuildFailed
	})
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotRunning || !s.Outdated || s.Image != "sha256:v3" {
		t.Fatalf("after a failed build: %+v", s)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET active_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outdated, stopped when idle", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotStopped && strings.Contains(s.Detail, "idle")
	})
}

// A hosted agent's image is built ahead of its work. After an upgrade (a
// new base image, so a new build) work wakes the agent on its previous
// image at once while the new one builds; once it is built the idle slot
// restarts on it. A previous image the engine no longer has isn't used.
func TestWarmBuilds(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 1)}
	base := func(tag string) func(*config.Server) {
		return func(c *config.Server) { c.ImageRness = "registry.test/seaport-rness:" + tag }
	}
	opts := bootstrap.Options{Runtime: rt, SupervisePeriod: 200 * time.Millisecond}
	p := startPortWith(t, base("1"), opts)
	pm := p.agent("pm")
	p.setMachine(pm.ID, "rness", map[string]any{})
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	buildPath := "/api/v1/agents/" + pm.ID.String() + "/build"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	built := func(p *port, image string) func() bool {
		return func() bool {
			var b hosting.Build
			return p.admin(http.MethodGet, buildPath, nil, &b) == http.StatusOK && b.State == hosting.BuildReady && b.Image == image
		}
	}

	// Hosted with no work: the supervisor builds its image anyway, and
	// starts nothing.
	rt.results <- "sha256:v1"
	eventually(t, "built ahead of work", built(p, "sha256:v1"))
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotStopped {
		t.Fatalf("a warm build started the slot: %+v", s)
	}
	// A start finds it built (the results channel is empty: a build would
	// hang the start).
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	eventually(t, "running on v1", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v1"
	})
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	eventually(t, "stopped", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotStopped })
	p.stop()

	// The upgrade: a new base image. The supervisor starts the new build
	// (it waits on results); work wakes the slot on its previous image
	// without waiting for it.
	p = joinPort(t, base("2"), opts)
	eventually(t, "the new build started", func() bool {
		var b hosting.Build
		return p.admin(http.MethodGet, buildPath, nil, &b) == http.StatusOK && b.State == hosting.BuildBuilding &&
			b.Base == "registry.test/seaport-rness:2"
	})
	d := p.openDock("support")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	p.inject(d, "51900", "w1", "hola")
	eventually(t, "woken on the previous image", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v1"
	})
	if s := p.slot(pm.ID, 1); s.Outdated {
		t.Fatalf("outdated before the new image is built: %+v", s)
	}
	// Built while the agent works: outdated, but kept running while it has
	// work.
	rt.results <- "sha256:v2"
	eventually(t, "the new image built", built(p, "sha256:v2"))
	eventually(t, "outdated", func() bool { return p.slot(pm.ID, 1).Outdated })
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotRunning || s.Image != "sha256:v1" {
		t.Fatalf("restarted while it had work: %+v", s)
	}
	// Idle: restarted on the new image.
	db := testDB(t)
	if _, err := db.Exec(`UPDATE ships SET status = 'handled' WHERE agent_id = $1 AND status IN ('queued', 'claimed')`, pm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restarted on v2", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v2" && !s.Outdated
	})
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	eventually(t, "stopped again", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotStopped })
	p.stop()

	// Another upgrade, and the engine pruned the previous image: a start
	// waits for the new build.
	rt.mu.Lock()
	rt.pruned = map[string]bool{"sha256:v2": true}
	rt.mu.Unlock()
	p = joinPort(t, base("3"), opts)
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start after the prune: %d", code)
	}
	time.Sleep(500 * time.Millisecond)
	if s := p.slot(pm.ID, 1); s.State != hosting.SlotStarting {
		t.Fatalf("started without its image: %+v", s)
	}
	rt.results <- "sha256:v3"
	eventually(t, "running on v3", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v3"
	})
	p.stop()

	// The build an agent wants may have no row yet (the supervisor records
	// every agent's, then builds one at a time): its running slot reads as
	// not outdated (no NULL). A port with the runtime off runs no
	// supervisor, so nothing records another build meanwhile.
	if _, err := db.Exec(`UPDATE agent_hosting h SET build_digest = 'not-built', build_machine = s.machine_digest
		FROM agent_slots s WHERE s.organization_id = h.organization_id AND s.agent_id = h.agent_id AND h.agent_id = $1 AND s.idx = 1`,
		pm.ID); err != nil {
		t.Fatal(err)
	}
	off := joinPort(t, base("3"), bootstrap.Options{})
	var h hosting.Hosted
	if code := off.admin(http.MethodGet, hostPath, nil, &h); code != http.StatusOK || len(h.Slots) != 1 ||
		h.Slots[0].State != hosting.SlotRunning || h.Slots[0].Outdated {
		t.Fatalf("a wanted build with no row: %d %+v", code, h)
	}
}
