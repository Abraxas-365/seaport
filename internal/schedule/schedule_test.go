package schedule_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/schedule"
)

func TestCreateValidate(t *testing.T) {
	conv := identity.NewConversationID()
	ok := schedule.Create{Name: "standup", Conversation: conv, Cron: "0 9 * * 1-5", Timezone: "America/Lima", Text: "hola"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid create refused: %v", err)
	}
	for name, mut := range map[string]func(*schedule.Create){
		"no name":       func(c *schedule.Create) { c.Name = " " },
		"long name":     func(c *schedule.Create) { c.Name = strings.Repeat("x", schedule.MaxName+1) },
		"no chat":       func(c *schedule.Create) { c.Conversation = identity.ConversationID{} },
		"bad cron":      func(c *schedule.Create) { c.Cron = "nope" },
		"too frequent":  func(c *schedule.Create) { c.Cron = "*/2 * * * *" },
		"never fires":   func(c *schedule.Create) { c.Cron = "0 0 31 2 *" },
		"unknown zone":  func(c *schedule.Create) { c.Timezone = "Mars/Olympus" },
		"local zone":    func(c *schedule.Create) { c.Timezone = "Local" },
		"no text":       func(c *schedule.Create) { c.Text = "" },
		"huge text":     func(c *schedule.Create) { c.Text = strings.Repeat("x", schedule.MaxText+1) },
		"control chars": func(c *schedule.Create) { c.Name = "a\x00b" },
	} {
		c := ok
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var empty schedule.Update
	if err := empty.Validate(); err == nil {
		t.Error("an empty update was accepted")
	}
}

// A saved schedule's next time is the cron's next after now, in its zone;
// Upcoming lists successive ones.
func TestUpcoming(t *testing.T) {
	c, err := schedule.ParseCron("0 9 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := schedule.Location("America/Lima")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC) // Friday 10:00 Lima
	got := schedule.Upcoming(c, loc, after, 3)
	want := []time.Time{
		time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC), // Monday 09:00 Lima = 14:00 UTC
		time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 14, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("upcoming[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSetAlarmValidateAndWhen(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Duration { return &d }
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	ok := schedule.SetAlarm{Agent: identity.NewAgentID(), Conversation: identity.NewConversationID(), Note: "check the deploy", In: in(20 * time.Minute)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid alarm refused: %v", err)
	}
	for name, mut := range map[string]func(*schedule.SetAlarm){
		"no agent":      func(a *schedule.SetAlarm) { a.Agent = identity.AgentID{} },
		"no chat":       func(a *schedule.SetAlarm) { a.Conversation = identity.ConversationID{} },
		"no note":       func(a *schedule.SetAlarm) { a.Note = " " },
		"huge note":     func(a *schedule.SetAlarm) { a.Note = strings.Repeat("x", schedule.MaxAlarmNote+1) },
		"no when":       func(a *schedule.SetAlarm) { a.In = nil },
		"two whens":     func(a *schedule.SetAlarm) { a.At = at(time.Hour) },
		"in and cron":   func(a *schedule.SetAlarm) { a.Cron = "0 9 * * *" },
		"negative in":   func(a *schedule.SetAlarm) { a.In = in(-time.Minute) },
		"bad cron":      func(a *schedule.SetAlarm) { a.In, a.Cron = nil, "nope" },
		"too frequent":  func(a *schedule.SetAlarm) { a.In, a.Cron = nil, "*/2 * * * *" },
		"unknown zone":  func(a *schedule.SetAlarm) { a.Timezone = "Mars/Olympus" },
		"control chars": func(a *schedule.SetAlarm) { a.Name = "a\x00b" },
	} {
		a := ok
		mut(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	for name, tc := range map[string]struct {
		mut  func(*schedule.SetAlarm)
		fail bool
		want time.Time
	}{
		"in":        {func(a *schedule.SetAlarm) {}, false, now.Add(20 * time.Minute)},
		"at":        {func(a *schedule.SetAlarm) { a.In, a.At = nil, at(2*time.Hour) }, false, now.Add(2 * time.Hour)},
		"too soon":  {func(a *schedule.SetAlarm) { a.In = in(30 * time.Second) }, true, time.Time{}},
		"too far":   {func(a *schedule.SetAlarm) { a.In = in(schedule.MaxAlarmAhead + time.Minute) }, true, time.Time{}},
		"edge soon": {func(a *schedule.SetAlarm) { a.In = in(schedule.MinAlarmDelay) }, false, now.Add(time.Minute)},
		"edge far":  {func(a *schedule.SetAlarm) { a.In = in(schedule.MaxAlarmAhead) }, false, now.Add(schedule.MaxAlarmAhead)},
	} {
		a := ok
		tc.mut(&a)
		cron, next, err := a.When(now)
		if tc.fail {
			if err == nil {
				t.Errorf("%s: accepted", name)
			}
			continue
		}
		if err != nil || cron != "" || !next.Equal(tc.want) {
			t.Errorf("%s: %q %v %v, want %v", name, cron, next, err, tc.want)
		}
	}

	rec := ok
	rec.In, rec.Cron, rec.Timezone = nil, "0 9 * * *", "UTC"
	cron, next, err := rec.When(now)
	if err != nil || cron == "" || !next.Equal(time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("cron alarm: %q %v %v", cron, next, err)
	}
}

func TestCreateOneShotValidate(t *testing.T) {
	conv := identity.NewConversationID()
	at := time.Now().Add(time.Hour)
	ok := schedule.Create{Name: "once", Conversation: conv, At: &at, Text: "hola"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid one-shot refused: %v", err)
	}
	both := ok
	both.Cron = "0 9 * * *"
	if err := both.Validate(); err == nil {
		t.Error("cron and at together were accepted")
	}
	none := ok
	none.At = nil
	if err := none.Validate(); err == nil {
		t.Error("neither cron nor at was accepted")
	}
	upd := schedule.Update{At: &at, Cron: new(string)}
	if err := upd.Validate(); err == nil {
		t.Error("an update with cron and at was accepted")
	}
	if err := (schedule.Update{At: &at}).Validate(); err != nil {
		t.Errorf("an update with only at was refused: %v", err)
	}
}
