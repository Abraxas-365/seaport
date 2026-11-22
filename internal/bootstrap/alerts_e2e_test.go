package bootstrap_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Alerts: the owner names a dock destination and the supervisor tells it
// when a slot fails, the agent's image fails or builds too long, or a
// conversation waits past the agent's alert wait; once each, and again
// ("Resuelto") in the same conversation when it clears. A condition that
// clears before its notice goes sends nothing; turning alerts off, or
// pointing them elsewhere, forgets the ones still to tell.
func TestHostingAlerts(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 8)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-alerts-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: 100 * time.Millisecond, AlertEvery: 100 * time.Millisecond})
	pm := p.agent("pm")
	p.setMachine(pm.ID, "rness", map[string]any{"concurrency": 1})
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	db := testDB(t)

	ops := p.openDock("ops")
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+ops.ID.String()+"/destinations/oncall",
		map[string]string{"target": "oncall-feed"}, nil); code != http.StatusOK {
		t.Fatalf("put destination: %d", code)
	}
	set := func(alerts any) (hosting.Hosted, int) {
		t.Helper()
		var h hosting.Hosted
		code := p.admin(http.MethodPatch, hostPath, map[string]any{"alerts": alerts}, &h)
		return h, code
	}
	to := func(dockID identity.DockID, dest, wait string) map[string]string {
		return map[string]string{"dock_id": dockID.String(), "destination": dest, "wait": wait}
	}

	// Checks: the destination must exist on a dock that can send, opens
	// conversations and is the organization's; the wait is whole minutes.
	if _, code := set(to(ops.ID, "nowhere", "")); code != http.StatusNotFound {
		t.Fatalf("unknown destination: %d", code)
	}
	if _, code := set(to(identity.NewDockID(), "oncall", "")); code != http.StatusNotFound {
		t.Fatalf("unknown dock: %d", code)
	}
	if _, code := set(to(ops.ID, "oncall", "30s")); code != http.StatusBadRequest {
		t.Fatalf("wait under a minute: %d", code)
	}
	if code := p.admin(http.MethodPatch, hostPath, map[string]any{"alerts": map[string]any{"dock_id": ops.ID, "destination": "oncall", "x": 1}}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown alerts field: %d", code)
	}
	var hook dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "webhook", "name": "hook"}, &hook); code != http.StatusCreated {
		t.Fatalf("create webhook dock: %d", code)
	}
	if _, code := set(to(hook.ID, "oncall", "")); code != http.StatusUnprocessableEntity {
		t.Fatalf("a dock that can't open conversations: %d", code)
	}
	h, code := set(to(ops.ID, "oncall", "5m"))
	if code != http.StatusOK || h.Alerts == nil || h.Alerts.DockID != ops.ID || h.Alerts.Destination != "oncall" || h.Alerts.Wait != "5m" || h.Alerts.Error != "" {
		t.Fatalf("alerts on: %d %+v", code, h.Alerts)
	}

	// notices: the outbound ships the port sent to the destination.
	notices := func() []ship.Ship {
		t.Helper()
		var conv identity.ConversationID
		if err := db.Get(&conv, `SELECT id FROM conversations WHERE dock_id = $1 AND external_id = 'oncall-feed'`, ops.ID); err != nil {
			return nil
		}
		var out []ship.Ship
		for _, s := range p.history(conv) {
			if s.Direction == ship.DirectionOut {
				out = append(out, s)
			}
		}
		slices.SortFunc(out, func(a, b ship.Ship) int { return a.CreatedAt.Compare(b.CreatedAt) })
		return out
	}
	noticeN := func(n int) []ship.Ship {
		t.Helper()
		var got []ship.Ship
		eventually(t, fmt.Sprintf("notice %d", n), func() bool { got = notices(); return len(got) >= n })
		if len(got) != n {
			t.Fatalf("notices: want %d, got %d: %+v", n, len(got), texts(got))
		}
		return got
	}
	quiet := func(n int) {
		t.Helper()
		time.Sleep(600 * time.Millisecond)
		if got := notices(); len(got) != n {
			t.Fatalf("notices: want %d, got %d: %+v", n, len(got), texts(got))
		}
	}

	// The image fails to build: one notice with the reason. The warm build
	// the supervisor claimed waits warmRetry before it is tried again.
	rt.results <- "fail:apt-get exited 100"
	got := noticeN(1)
	if !strings.Contains(got[0].Text, "la imagen de @pm no se pudo construir") || !strings.Contains(got[0].Text, "apt-get exited 100") ||
		!strings.Contains(got[0].Text, "/agents/"+pm.ID.String()+"#donde") {
		t.Fatalf("build failed notice: %q", got[0].Text)
	}
	quiet(1)

	// A forced build is ready: resolved, in the same conversation.
	rt.results <- "sha256:img1"
	if code := p.admin(http.MethodPost, "/api/v1/agents/"+pm.ID.String()+"/build", map[string]any{"force": true}, nil); code != http.StatusAccepted {
		t.Fatalf("build: %d", code)
	}
	got = noticeN(2)
	if !strings.HasPrefix(got[1].Text, "Resuelto: la imagen de @pm ya no está fallando.") || got[1].Conversation != got[0].Conversation {
		t.Fatalf("build resolved notice: %+v", got[1])
	}

	// A build running past BuildSlow: told how long; resolved when it ends.
	if code := p.admin(http.MethodPost, "/api/v1/agents/"+pm.ID.String()+"/build", map[string]any{"force": true}, nil); code != http.StatusAccepted {
		t.Fatalf("build again: %d", code)
	}
	if _, err := db.Exec(`UPDATE hosting_builds SET started_at = now() - interval '50 minutes' WHERE state = 'building'`); err != nil {
		t.Fatal(err)
	}
	got = noticeN(3)
	if !strings.Contains(got[2].Text, "la imagen de @pm lleva 50 min construyéndose") {
		t.Fatalf("build slow notice: %q", got[2].Text)
	}
	rt.results <- "sha256:img2"
	got = noticeN(4)
	if !strings.HasPrefix(got[3].Text, "Resuelto: terminó la construcción de la imagen de @pm.") {
		t.Fatalf("build slow resolved notice: %q", got[3].Text)
	}

	// A slot fails: one notice with the runtime's reason, nothing more
	// while it stays failed; stopping it resolves the alert.
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	eventually(t, "slot 1 running", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotRunning })
	rt.exit(hosting.SlotRef{Organization: config.DefaultOrganization, Agent: pm.ID, Index: 1}, "the agent's container exited: Exited (137)")
	got = noticeN(5)
	if !strings.Contains(got[4].Text, "la instancia pm-1 falló") || !strings.Contains(got[4].Text, "Exited (137)") {
		t.Fatalf("slot failed notice: %q", got[4].Text)
	}
	quiet(5)
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	got = noticeN(6)
	if !strings.HasPrefix(got[5].Text, "Resuelto: la instancia pm-1 ya no está fallando.") {
		t.Fatalf("slot resolved notice: %q", got[5].Text)
	}

	// A conversation waits past the agent's wait (5m): told once, with how
	// many wait; resolved once none waits.
	support := p.openDock("support")
	p.assignAs(adminToken, support.ID, pm.ID, true)
	p.inject(support, "51901", "a1", "hola")
	p.inject(support, "51902", "b1", "buenas")
	quiet(6) // a fresh queue isn't an alert
	if _, err := db.Exec(`UPDATE ships SET created_at = now() - interval '7 minutes' WHERE agent_id = $1 AND status = 'queued'`, pm.ID); err != nil {
		t.Fatal(err)
	}
	got = noticeN(7)
	if !strings.Contains(got[6].Text, "2 conversaciones esperan a @pm") || !strings.Contains(got[6].Text, "desde hace 7 min") {
		t.Fatalf("waiting notice: %q", got[6].Text)
	}
	if _, err := db.Exec(`UPDATE ships SET status = 'handled' WHERE agent_id = $1 AND status = 'queued'`, pm.ID); err != nil {
		t.Fatal(err)
	}
	got = noticeN(8)
	if !strings.HasPrefix(got[7].Text, "Resuelto: ya no hay conversaciones esperando a @pm.") {
		t.Fatalf("waiting resolved notice: %q", got[7].Text)
	}
	// (the supervisor woke a slot for them: stop it again)
	for _, i := range []string{"1", "2"} {
		p.admin(http.MethodPost, hostPath+"/slots/"+i+"/stop", nil, nil)
	}
	eventually(t, "slots stopped", func() bool {
		return p.slot(pm.ID, 1).State == hosting.SlotStopped && p.slot(pm.ID, 2).State == hosting.SlotStopped
	})
	quiet(8)

	// A notice that can't be sent: the hosting shows why, and it is tried
	// again after AlertRetry (here, at once: made due by hand).
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+ops.ID.String(), map[string]bool{"replies": false}, nil); code != http.StatusOK {
		t.Fatalf("replies off: %d", code)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET state = 'failed', detail = 'boom', updated_at = now() WHERE agent_id = $1 AND idx = 2`, pm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alert error shown", func() bool {
		p.admin(http.MethodGet, hostPath, nil, &h)
		return h.Alerts != nil && h.Alerts.Error != ""
	})
	if !strings.Contains(h.Alerts.Error, "replies") {
		t.Fatalf("alert error: %q", h.Alerts.Error)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+ops.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusOK {
		t.Fatalf("replies on: %d", code)
	}
	if _, err := db.Exec(`UPDATE hosting_alerts SET send_after = now() WHERE agent_id = $1 AND done_at IS NULL`, pm.ID); err != nil {
		t.Fatal(err)
	}
	got = noticeN(9)
	if !strings.Contains(got[8].Text, "la instancia pm-2 falló") {
		t.Fatalf("retried notice: %q", got[8].Text)
	}
	eventually(t, "alert error cleared", func() bool {
		p.admin(http.MethodGet, hostPath, nil, &h)
		return h.Alerts != nil && h.Alerts.Error == ""
	})

	// Off: the open alert is forgotten (no "Resuelto" after it clears).
	if h, code = set(nil); code != http.StatusOK || h.Alerts != nil {
		t.Fatalf("alerts off: %d %+v", code, h.Alerts)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET state = 'stopped', detail = '' WHERE agent_id = $1 AND idx = 2`, pm.ID); err != nil {
		t.Fatal(err)
	}
	quiet(9)
	var open int
	if err := db.Get(&open, `SELECT count(*) FROM hosting_alerts WHERE agent_id = $1 AND done_at IS NULL`, pm.ID); err != nil || open != 0 {
		t.Fatalf("alerts left to tell after off: %d %v", open, err)
	}

	// A failure that clears before its notice could go tells nobody.
	if _, code := set(to(ops.ID, "oncall", "")); code != http.StatusOK {
		t.Fatalf("alerts on again: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+ops.ID.String(), map[string]bool{"replies": false}, nil); code != http.StatusOK {
		t.Fatalf("replies off: %d", code)
	}
	if _, err := db.Exec(`UPDATE agent_slots SET state = 'failed', detail = 'blip', updated_at = now() WHERE agent_id = $1 AND idx = 2`, pm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "blip alert raised", func() bool {
		var n int
		_ = db.Get(&n, `SELECT count(*) FROM hosting_alerts WHERE agent_id = $1 AND kind = 'slot_failed' AND key = '2' AND resolved_at IS NULL`, pm.ID)
		return n == 1
	})
	if _, err := db.Exec(`UPDATE agent_slots SET state = 'stopped', detail = '' WHERE agent_id = $1 AND idx = 2`, pm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "blip alert done", func() bool {
		var n int
		_ = db.Get(&n, `SELECT count(*) FROM hosting_alerts WHERE agent_id = $1 AND done_at IS NULL`, pm.ID)
		return n == 0
	})
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+ops.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusOK {
		t.Fatalf("replies on: %d", code)
	}
	quiet(9)

	// Deleting the dock turns the agent's alerts off.
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+ops.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete dock: %d", code)
	}
	if code := p.admin(http.MethodGet, hostPath, nil, &h); code != http.StatusOK || h.Alerts != nil {
		t.Fatalf("alerts after the dock is gone: %d %+v", code, h.Alerts)
	}
}

func texts(ships []ship.Ship) []string {
	out := make([]string, len(ships))
	for i, s := range ships {
		out[i] = s.Text
	}
	return out
}
