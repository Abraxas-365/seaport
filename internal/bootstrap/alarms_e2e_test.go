package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
)

// TestAgentAlarms: the alarm tools exist only while the profile's switch is
// on; an agent sets, lists and cancels its own alarms in conversations of
// its docks; the limits and the time window are enforced; a due alarm posts
// the note to the agent as the system sender; with the switch off at fire
// time it doesn't fire; a missed one-shot is closed without firing; people
// see the alarms with the other schedules.
func TestAgentAlarms(t *testing.T) {
	p := startPortWith(t, fastTick, bootstrap.Options{ScheduleTick: 200 * time.Millisecond})
	ag, other := p.agent("clock"), p.agent("other")
	d, foreign := p.openDock("alarm-d"), p.openDock("alarm-foreign")
	p.assignAs(adminToken, d.ID, ag.ID, true)
	p.assignAs(adminToken, foreign.ID, other.ID, true)
	first := p.inject(d, "1", "m1", "hi")
	conv := first.Conversation
	foreignConv := p.inject(foreign, "2", "m2", "hi").Conversation
	tok := p.issueAs(adminToken, ag.ID, "laptop")
	conn := p.dialCrewAs(tok.Value, crew.Frame{Type: crew.FrameHello, Agent: "clock", Concurrency: 2})
	ack(conn, delivered(t, conn, first))

	alarmTools := func() map[string]bool {
		names := toolNames(t, mcpSession(t, p, tok.Value))
		return map[string]bool{"set_alarm": names["set_alarm"], "alarms": names["alarms"], "cancel_alarm": names["cancel_alarm"]}
	}
	for n, on := range alarmTools() {
		if on {
			t.Fatalf("%s with alarms off", n)
		}
	}
	prof, code := p.saveProfile(ag.ID, map[string]any{"base": 0, "alarms": true})
	if code != http.StatusOK || !prof.Alarms {
		t.Fatalf("profile alarms on: %d %+v", code, prof)
	}
	for n, on := range alarmTools() {
		if !on {
			t.Fatalf("no %s with alarms on", n)
		}
	}
	otherTok := p.issueAs(adminToken, other.ID, "other-laptop")
	if toolNames(t, mcpSession(t, p, otherTok.Value))["set_alarm"] {
		t.Fatal("set_alarm for an agent with alarms off")
	}

	s := mcpSession(t, p, tok.Value)
	call := func(name string, args map[string]any) (bool, string, map[string]any) { return tool(t, s, name, args) }
	events := p.openEvents("")

	// Validation and scope.
	for name, args := range map[string]map[string]any{
		"no when":              {"conversation_id": conv.String(), "note": "x"},
		"two whens":            {"conversation_id": conv.String(), "note": "x", "in_minutes": 5, "cron": "0 9 * * *"},
		"too soon":             {"conversation_id": conv.String(), "note": "x", "at": time.Now().Add(20 * time.Second).Format(time.RFC3339)},
		"too far":              {"conversation_id": conv.String(), "note": "x", "in_minutes": 43201},
		"bad at":               {"conversation_id": conv.String(), "note": "x", "at": "tomorrow"},
		"too frequent":         {"conversation_id": conv.String(), "note": "x", "cron": "*/2 * * * *"},
		"no note":              {"conversation_id": conv.String(), "in_minutes": 5},
		"bad conversation":     {"conversation_id": "nope", "note": "x", "in_minutes": 5},
		"foreign conversation": {"conversation_id": foreignConv.String(), "note": "x", "in_minutes": 5},
		"unknown conversation": {"conversation_id": identity.NewConversationID().String(), "note": "x", "in_minutes": 5},
	} {
		if failed, msg, _ := call("set_alarm", args); !failed {
			t.Errorf("%s: accepted (%s)", name, msg)
		}
	}
	if failed, msg, _ := call("set_alarm", map[string]any{"conversation_id": foreignConv.String(), "note": "x", "in_minutes": 5}); !strings.Contains(msg, "conversation not found") {
		t.Errorf("foreign conversation: %v %q", failed, msg)
	}

	// Set, list, cancel.
	failed, msg, out := call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "check the deploy", "in_minutes": 20, "name": "deploy"})
	if failed || out["name"] != "deploy" || out["recurring"] != false {
		t.Fatalf("set_alarm: %v %s %v", failed, msg, out)
	}
	id := out["id"].(string)
	next(t, events, "schedule.created", func(d json.RawMessage) bool {
		var sc schedule.Schedule
		return json.Unmarshal(d, &sc) == nil && sc.ID.String() == id && sc.CreatedBy != nil && *sc.CreatedBy == ag.ID && sc.RunAt != nil
	})
	if failed, msg, _ = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "again", "in_minutes": 5, "name": "deploy"}); !failed {
		t.Fatalf("duplicate name accepted: %s", msg)
	}
	failed, msg, out = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "daily", "cron": "0 9 * * *", "timezone": "Europe/Madrid"})
	if failed || out["recurring"] != true || !strings.HasPrefix(out["name"].(string), "clock-") {
		t.Fatalf("cron alarm: %v %s %v", failed, msg, out)
	}
	cronID := out["id"].(string)
	if failed, msg, _ = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "third", "in_minutes": 30}); failed {
		t.Fatalf("third alarm: %s", msg)
	}
	if failed, msg, _ = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "fourth", "in_minutes": 40}); !failed ||
		!strings.Contains(msg, "cancel") {
		t.Fatalf("a fourth alarm in one conversation: %v %q", failed, msg)
	}
	if _, _, out = call("alarms", map[string]any{}); len(out["alarms"].([]any)) != 3 {
		t.Fatalf("alarms: %v", out)
	}
	if _, _, out = call("alarms", map[string]any{"conversation_id": foreignConv.String()}); len(out["alarms"].([]any)) != 0 {
		t.Fatalf("alarms of another conversation: %v", out)
	}

	// People see them with the other schedules, marked with the agent.
	var list query.Paginated[schedule.Schedule]
	if code := p.admin(http.MethodGet, "/api/v1/schedules?agent_id="+ag.ID.String(), nil, &list); code != http.StatusOK || len(list.Items) != 3 {
		t.Fatalf("list by agent: %d %+v", code, list.Items)
	}
	for _, sc := range list.Items {
		if sc.CreatedBy == nil || *sc.CreatedBy != ag.ID || sc.Agent == nil || *sc.Agent != ag.ID {
			t.Fatalf("alarm fields: %+v", sc)
		}
	}
	oneShot := scheduleOf(t, p, identity.MustParseScheduleID(id))
	if oneShot.RunAt == nil || oneShot.Cron != "" || len(oneShot.Preview) != 1 {
		t.Fatalf("one-shot: %+v", oneShot)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+id, map[string]any{"cron": "0 9 * * *"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("cron on a one-shot: %d", code)
	}

	// Only its creator cancels it through the tool; another agent's tool is
	// not even there, and an unknown id is not found.
	if failed, _, _ = call("cancel_alarm", map[string]any{"id": identity.NewScheduleID().String()}); !failed {
		t.Fatal("cancelling an unknown alarm worked")
	}
	var person schedule.Schedule
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "people", "conversation_id": conv.String(),
		"cron": "0 9 * * *", "text": "x"}, &person); code != http.StatusCreated {
		t.Fatalf("person's schedule: %d", code)
	}
	if failed, _, _ = call("cancel_alarm", map[string]any{"id": person.ID.String()}); !failed {
		t.Fatal("an agent cancelled a person's schedule")
	}
	if failed, msg, _ = call("cancel_alarm", map[string]any{"id": cronID}); failed {
		t.Fatalf("cancel_alarm: %s", msg)
	}
	if _, _, out = call("alarms", map[string]any{}); len(out["alarms"].([]any)) != 2 {
		t.Fatalf("alarms after cancel: %v", out)
	}

	// A person creates a one-shot too: at least 30 seconds ahead.
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "soon", "conversation_id": conv.String(),
		"at": time.Now().Add(5 * time.Second).Format(time.RFC3339), "text": "x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("one-shot too soon: %d", code)
	}
	var once schedule.Schedule
	if code := p.admin(http.MethodPost, "/api/v1/schedules", map[string]any{"name": "once", "conversation_id": conv.String(),
		"at": time.Now().Add(time.Hour).Format(time.RFC3339), "text": "una vez"}, &once); code != http.StatusCreated || once.RunAt == nil || once.CreatedBy != nil {
		t.Fatalf("person's one-shot: %d %+v", code, once)
	}

	// Due: the agent gets its note from the system sender; the alarm is done.
	dueNow(t, identity.MustParseScheduleID(id), 5*time.Second)
	f := nextFrame(conn)
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 {
		t.Fatalf("alarm delivery: %+v", f)
	}
	got := f.Delivery.Ships[0]
	if got.SenderID != schedule.SenderID || !strings.HasPrefix(got.Text, "@clock Alarm you set (deploy, set ") ||
		!strings.HasSuffix(got.Text, "): check the deploy") || !strings.Contains(got.SenderName, "alarma deploy") {
		t.Fatalf("alarm ship: %+v", got)
	}
	ack(conn, f)
	done := scheduleOf(t, p, identity.MustParseScheduleID(id))
	if done.DoneAt == nil || done.Enabled || done.LastShip == nil || *done.LastShip != got.ID {
		t.Fatalf("after ringing: %+v", done)
	}
	noDelivery(t, conn, 600*time.Millisecond) // once
	if _, _, out = call("alarms", map[string]any{}); len(out["alarms"].([]any)) != 1 {
		t.Fatalf("a done alarm is still pending: %v", out)
	}
	var visible query.Paginated[schedule.Schedule]
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/schedules", nil, &visible); code != http.StatusOK {
		t.Fatalf("conversation list: %d", code)
	}
	for _, sc := range visible.Items {
		if sc.ID == done.ID {
			t.Fatal("a done one-shot is listed by default")
		}
	}
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+conv.String()+"/schedules?done=true", nil, &visible); code != http.StatusOK {
		t.Fatalf("conversation list with done: %d", code)
	}
	found := false
	for _, sc := range visible.Items {
		found = found || sc.ID == done.ID
	}
	if !found {
		t.Fatal("done=true doesn't list the done one-shot")
	}
	// A rung alarm frees its name; people can't hand an alarm to someone else.
	failed, msg, out = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "again", "in_minutes": 30, "name": "deploy"})
	if failed {
		t.Fatalf("a rung alarm's name is still taken: %s", msg)
	}
	again := out["id"].(string)
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+again, map[string]any{"agent_id": ""}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("clearing an alarm's agent: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/schedules/"+again, map[string]any{"text": "reworded"}, nil); code != http.StatusOK {
		t.Fatalf("rewording an alarm: %d", code)
	}
	if failed, msg, _ = call("cancel_alarm", map[string]any{"id": again}); failed {
		t.Fatalf("cancel the second deploy: %s", msg)
	}

	// Missed by more than the grace: closed without firing.
	var late schedule.Schedule
	_, _, out = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "late", "in_minutes": 10, "name": "late"})
	late.ID = identity.MustParseScheduleID(out["id"].(string))
	dueNow(t, late.ID, 3*time.Hour)
	waitUntil(t, "missed alarm closed", func() bool { return scheduleOf(t, p, late.ID).DoneAt != nil })
	if sc := scheduleOf(t, p, late.ID); sc.LastShip != nil {
		t.Fatalf("a missed alarm fired: %+v", sc)
	}
	noDelivery(t, conn, 600*time.Millisecond)

	// Switch off: an alarm already set doesn't fire.
	_, _, out = call("set_alarm", map[string]any{"conversation_id": conv.String(), "note": "muted", "in_minutes": 10, "name": "muted"})
	muted := identity.MustParseScheduleID(out["id"].(string))
	prof, code = p.saveProfile(ag.ID, map[string]any{"base": prof.Version, "alarms": false})
	if code != http.StatusOK || prof.Alarms {
		t.Fatalf("profile alarms off: %d %+v", code, prof)
	}
	if toolNames(t, mcpSession(t, p, tok.Value))["set_alarm"] {
		t.Fatal("set_alarm after turning alarms off")
	}
	dueNow(t, muted, 5*time.Second)
	waitUntil(t, "muted alarm closed", func() bool { return scheduleOf(t, p, muted).DoneAt != nil })
	if sc := scheduleOf(t, p, muted); sc.LastShip != nil {
		t.Fatalf("an alarm fired with alarms off: %+v", sc)
	}
	noDelivery(t, conn, 600*time.Millisecond)

	// Run now of a one-shot posts but doesn't close it.
	pending := scheduleOf(t, p, once.ID)
	if code := p.admin(http.MethodPost, "/api/v1/schedules/"+pending.ID.String()+"/run", nil, nil); code != http.StatusOK {
		t.Fatalf("run a one-shot: %d", code)
	}
	if sc := scheduleOf(t, p, once.ID); sc.DoneAt != nil || !sc.Enabled {
		t.Fatalf("run now closed the one-shot: %+v", sc)
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // the runner ticks; -race and a loaded CI are slow
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// nextFrame reads the next frame that isn't a profile notice (saving the
// profile tells the connection).
func nextFrame(c *crewConn) crew.Frame {
	c.t.Helper()
	for {
		if f := c.read(); f.Type != crew.FrameProfile {
			return f
		}
	}
}

// noDelivery asserts no delivery arrives within d.
func noDelivery(t *testing.T, c *crewConn, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-c.frames:
			if ok && f.Type != crew.FrameProfile {
				t.Fatalf("unexpected frame: %+v", f)
			}
		case <-deadline:
			return
		}
	}
}
