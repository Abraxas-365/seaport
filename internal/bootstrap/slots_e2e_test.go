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
	spec := map[string]any{"harness": "rness", "workspace": "persistent",
		"repos": []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${GITHUB_TOKEN}", "on_start": "make deps"}}}
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, nil); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "web", "slots": 2}, nil); code != http.StatusOK {
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
	if s1.Image != "sha256:img1" || s1.EnvironmentVersion != 1 || s1.StartedAt == nil {
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

	// The slot token isn't listed with people's tokens; it reads the
	// slot's workspace (credentials resolved) and works as the agent.
	var tokens query.Paginated[crew.Token]
	if code := p.admin(http.MethodGet, "/api/v1/crew/tokens", nil, &tokens); code != http.StatusOK || len(tokens.Items) != 0 {
		t.Fatalf("tokens: %d %+v", code, tokens.Items)
	}
	var ws hosting.Workspace
	if code := p.call(http.MethodGet, "/crew/workspace", slotToken, nil, &ws); code != http.StatusOK || ws.Slot != "pm-1" ||
		ws.Harness != "rness" || !ws.Persistent || len(ws.Repos) != 1 || ws.Repos[0].Token != "ghp_slot_secret" ||
		ws.Repos[0].Path != "app" || ws.Repos[0].OnStart != "make deps" {
		t.Fatalf("workspace: %d %+v", code, ws)
	}
	people := p.issueAs(adminToken, pm.ID, "laptop")
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

	// New work wakes the stopped agent (the image is already built).
	d := p.openDock("support")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	p.inject(d, "51900", "w1", "hola")
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	if !rt.running(ref1) {
		t.Fatal("woken slot not running")
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

// An idle running slot on an outdated environment version restarts on the
// new one; one idle past idle_stop is stopped.
func TestIdleAndOutdatedSlots(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.ImageRness = "registry.test/seaport-rness:1"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: 200 * time.Millisecond})
	events := p.openEvents("")
	pm := p.agent("pm")
	spec := map[string]any{"harness": "rness", "idle_stop": "1m"}
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, nil); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "web"}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	rt.results <- "sha256:v1"
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))

	// A new version: the idle slot restarts on it (built first).
	rt.results <- "sha256:v2"
	spec["packages"] = []string{"jq"}
	if code := p.admin(http.MethodPut, "/api/v1/environments/web", map[string]any{"base": 1, "spec": spec}, nil); code != http.StatusOK {
		t.Fatalf("v2: %d", code)
	}
	eventually(t, "restarted on v2", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.EnvironmentVersion == 2 && s.Image == "sha256:v2"
	})

	// Idle past idle_stop: stopped.
	db := testDB(t)
	if _, err := db.Exec(`UPDATE agent_slots SET active_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped when idle", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotStopped && strings.Contains(s.Detail, "idle")
	})
}
