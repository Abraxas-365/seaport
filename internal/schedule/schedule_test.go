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
