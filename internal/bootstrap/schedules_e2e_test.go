package bootstrap_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// dueNow moves a schedule's next slot (the runner reads the table, so a
// test needn't wait for a real cron time).
func dueNow(t *testing.T, id identity.ScheduleID, ago time.Duration) {
	t.Helper()
	db, err := bootstrap.OpenDB(context.Background(), testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `UPDATE schedules SET next_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, ago.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func scheduleOf(t *testing.T, p *port, id identity.ScheduleID) schedule.Schedule {
	t.Helper()
	var s schedule.Schedule
	if code := p.admin(http.MethodGet, "/api/v1/schedules/"+id.String(), nil, &s); code != http.StatusOK {
		t.Fatalf("get schedule: %d", code)
	}
	return s
}

func fastTick(p *config.Server) {}

// A schedule posts its text into its conversation as the system sender when
// due: through customs and routing like any message, to the agent it names;
// a missed slot is skipped; running now works; the API validates.
func TestSchedulesFire(t *testing.T) {
	p := startPortWith(t, fastTick, bootstrap.Options{ScheduleTick: 200 * time.Millisecond})
	d := p.openDock("sched")
	pm, reviewer := p.agent("pm"), p.agent("reviewer")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	p.assignAs(adminToken, d.ID, reviewer.ID, false)
	pmConn := p.dialCrewAs(p.issueAs(adminToken, pm.ID, "pm").Value, crew.Frame{Type: crew.FrameHello, Agent: "pm", Concurrency: 2})
	revConn := p.dialCrewAs(p.issueAs(adminToken, reviewer.ID, "rev").Value, crew.Frame{Type: crew.FrameHello, Agent: "rev", Concurrency: 2})
	first := p.inject(d, "700", "s0", "hola")
	ack(pmConn, delivered(t, pmConn, first))
	conv := first.Conversation.String()

	// Validation.
	for name, body := range map[string]map[string]any{
		"bad cron":     {"name": "x", "conversation_id": conv, "cron": "* * * *", "text": "x"},
		"every minute": {"name": "x", "conversation_id": conv, "cron": "* * * * *", "text": "x"},
		"bad zone":     {"name": "x", "conversation_id": conv, "cron": "0 9 * * *", "timezone": "Mars/Base", "text": "x"},
		"no text":      {"name": "x", "conversation_id": conv, "cron": "0 9 * * *", "text": " "},
		"no conv":      {"name": "x", "cron": "0 9 * * *", "text": "x"},
		"other conv":   {"name": "x", "conversation_id": identity.NewConversationID().String(), "cron": "0 9 * * *", "text": "x"},
		"other agent":  {"name": "x", "conversation_id": conv, "agent_id": identity.NewAgentID().String(), "cron": "0 9 * * *", "text": "x"},
	} {
		if code := p.admin(http.MethodPost, "/api/v1/schedules", body, nil); code < 400 || code >= 500 {
			t.Errorf("%s: %d, want 4xx", name, code)
		}
	}

	var daily schedule.Schedule
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "standup", "conversation_id": conv,
		"cron": "0 9 * * 1-5", "timezone": "America/Lima", "text": "Resumen del día"}, &daily); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if !daily.Enabled || len(daily.Preview) != schedule.Preview || !daily.NextAt.After(time.Now()) || daily.Timezone != "America/Lima" {
		t.Fatalf("created: %+v", daily)
	}
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "standup", "conversation_id": conv,
		"cron": "0 9 * * *", "text": "x"}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate name: %d", code)
	}

	// Due: the default agent gets it from the system sender.
	dueNow(t, daily.ID, 5*time.Second)
	f := pmConn.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 {
		t.Fatalf("scheduled delivery: %+v", f)
	}
	got := f.Delivery.Ships[0]
	if got.Text != "Resumen del día" || got.SenderID != schedule.SenderID || got.Conversation != first.Conversation ||
		!strings.Contains(got.SenderName, "standup") {
		t.Fatalf("scheduled ship: %+v", got)
	}
	ack(pmConn, f)
	after := scheduleOf(t, p, daily.ID)
	if !after.NextAt.After(time.Now()) || after.LastAt == nil || after.LastShip == nil || *after.LastShip != got.ID {
		t.Fatalf("after firing: %+v", after)
	}
	pmConn.quiet(600 * time.Millisecond) // once: the slot moved on

	// Naming an agent routes to it, whoever is the default.
	var named schedule.Schedule
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "review", "conversation_id": conv,
		"agent_id": reviewer.ID.String(), "cron": "*/5 * * * *", "text": "Revisa los PRs"}, &named); code != http.StatusCreated {
		t.Fatalf("create named: %d", code)
	}
	dueNow(t, named.ID, 1*time.Second)
	f = revConn.read()
	if f.Type != crew.FrameDelivery || !strings.HasPrefix(f.Delivery.Ships[0].Text, "@reviewer ") {
		t.Fatalf("named delivery: %+v", f)
	}
	ack(revConn, f)

	// A slot missed by too long is skipped, not fired late.
	dueNow(t, named.ID, 30*time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	for scheduleOf(t, p, named.ID).NextAt.Before(time.Now()) {
		if time.Now().After(deadline) {
			t.Fatal("missed slot was not advanced")
		}
		time.Sleep(100 * time.Millisecond)
	}
	revConn.quiet(600 * time.Millisecond)

	// Disabled schedules don't fire; run-now does, and leaves the slot alone.
	var off schedule.Schedule
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+daily.ID.String(), map[string]any{"enabled": false}, &off); code != http.StatusOK || off.Enabled || len(off.Preview) != 0 {
		t.Fatalf("disable: %d %+v", code, off)
	}
	dueNow(t, daily.ID, 5*time.Second)
	pmConn.quiet(800 * time.Millisecond)
	var ran ship.Ship
	if code := p.admin(http.MethodPost, "/api/v1/schedules/"+daily.ID.String()+"/run", nil, &ran); code != http.StatusOK || ran.Text != "Resumen del día" {
		t.Fatalf("run: %d %+v", code, ran)
	}
	// No agent named: routing goes on with the one the chat last talked to.
	ack(revConn, delivered(t, revConn, ran))

	// Update recomputes; the preview endpoint saves nothing.
	var upd schedule.Schedule
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+daily.ID.String(), map[string]any{"enabled": true, "cron": "30 18 * * *", "agent_id": reviewer.ID.String()}, &upd); code != http.StatusOK ||
		upd.Cron != "30 18 * * *" || upd.Agent == nil || *upd.Agent != reviewer.ID || !upd.NextAt.After(time.Now()) {
		t.Fatalf("update: %d %+v", code, upd)
	}
	var cleared schedule.Schedule
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+daily.ID.String(), map[string]any{"agent_id": ""}, &cleared); code != http.StatusOK || cleared.Agent != nil {
		t.Fatalf("clear agent: %d %+v", code, cleared)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+daily.ID.String(), map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty update: %d", code)
	}
	var pv struct {
		Preview []time.Time `json:"preview"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/schedules/preview", map[string]any{"cron": "0 9 * * 1-5", "timezone": "Europe/Madrid"}, &pv); code != http.StatusOK || len(pv.Preview) != schedule.Preview {
		t.Fatalf("preview: %d %+v", code, pv)
	}

	// Lists: all, and by conversation.
	var all, ofConv query.Paginated[schedule.Schedule]
	if code := p.admin(http.MethodGet, "/api/v1/schedules", nil, &all); code != http.StatusOK || len(all.Items) != 2 {
		t.Fatalf("list: %d %+v", code, all.Items)
	}
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+conv+"/schedules", nil, &ofConv); code != http.StatusOK || len(ofConv.Items) != 2 {
		t.Fatalf("conversation list: %d %+v", code, ofConv.Items)
	}
	if code := p.admin(http.MethodGet, "/api/v1/schedules?conversation_id="+identity.NewConversationID().String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("list of unknown conversation: %d", code)
	}

	// Delete, and deleting the dock deletes the rest.
	if code := p.admin(http.MethodDelete, "/api/v1/schedules/"+daily.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/schedules/"+daily.ID.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleted schedule: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+d.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete dock: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/schedules", nil, &all); code != http.StatusOK || len(all.Items) != 0 {
		t.Fatalf("schedules after dock delete: %d %+v", code, all.Items)
	}
}

// Several processes run a runner: a due slot is claimed by exactly one, so
// it makes one ship.
func TestSchedulesFireOnceAcrossReplicas(t *testing.T) {
	opts := bootstrap.Options{ScheduleTick: 100 * time.Millisecond}
	a := startPortWith(t, fastTick, opts)
	joinPort(t, fastTick, opts)
	joinPort(t, fastTick, opts)
	d := a.openDock("replicas")
	first := a.inject(d, "800", "r0", "hola")
	var sc schedule.Schedule
	if code := a.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "tick", "conversation_id": first.Conversation.String(),
		"cron": "0 9 * * *", "text": "una vez"}, &sc); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	dueNow(t, sc.ID, 2*time.Second)
	time.Sleep(2 * time.Second)
	var ships query.Paginated[ship.Ship]
	if code := a.admin(http.MethodGet, "/api/v1/conversations/"+first.Conversation.String()+"/ships?limit=100", nil, &ships); code != http.StatusOK {
		t.Fatalf("ships: %d", code)
	}
	n := 0
	for _, s := range ships.Items {
		if s.SenderID == schedule.SenderID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("scheduled ships: %d, want 1 (%+v)", n, ships.Items)
	}
}

// SEAPORT_MAX_SCHEDULES bounds an organization's schedules.
func TestSchedulesQuota(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.MaxSchedules = 2 }, bootstrap.Options{})
	d := p.openDock("quota")
	conv := p.inject(d, "900", "q0", "hola").Conversation.String()
	for i, name := range []string{"a", "b", "c"} {
		code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": name, "conversation_id": conv,
			"cron": "0 9 * * *", "text": "x"}, nil)
		want := http.StatusCreated
		if i == 2 {
			want = http.StatusUnprocessableEntity
		}
		if code != want {
			t.Fatalf("schedule %s: %d, want %d", name, code, want)
		}
	}
}
