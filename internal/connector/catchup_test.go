package connector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// catchUp shows a new session the whole Earlier, a resumed one only what
// came after its mark and isn't a reply of its own deliveries, and a
// session of unknown progress (an older sessions file) nothing.
func TestCatchUpPicksWhatTheSessionMissed(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	mine, theirs := identity.NewDeliveryID(), identity.NewDeliveryID()
	self, other := identity.NewAgentID(), identity.NewAgentID()
	earlier := []ship.Ship{
		{Text: "old", Direction: ship.DirectionIn, CreatedAt: t0},
		{Text: "my reply", Direction: ship.DirectionOut, Agent: &self, Delivery: &mine, CreatedAt: t0.Add(2 * time.Minute)},
		{Text: "other slot", Direction: ship.DirectionOut, Agent: &self, Delivery: &theirs, CreatedAt: t0.Add(3 * time.Minute)},
		{Text: "other agent", Direction: ship.DirectionOut, Agent: &other, CreatedAt: t0.Add(4 * time.Minute)},
		{Text: "chatter", Direction: ship.DirectionIn, Status: ship.StatusHeld, HoldReason: conversation.HoldNotMentioned, CreatedAt: t0.Add(5 * time.Minute)},
	}
	d := ship.Delivery{Earlier: earlier}

	if c := catchUp(d, nil); len(c.ships) != 0 {
		t.Fatalf("unknown progress: %+v", c)
	}
	if c := catchUp(d, &seenMark{}); c.heading != earlierHeading || len(c.ships) != 5 {
		t.Fatalf("new session: %+v", c)
	}
	c := catchUp(d, &seenMark{Created: t0.Add(time.Minute), Mine: []identity.DeliveryID{mine}})
	if c.heading != sinceHeading || strings.Join(texts(c.ships), ",") != "other slot,other agent,chatter" {
		t.Fatalf("resumed: %q", texts(c.ships))
	}
	// A ship steered into the last turn was seen although newer than the mark.
	d.Earlier[4].ID = identity.NewShipID()
	c = catchUp(d, &seenMark{Created: t0.Add(time.Minute), Mine: []identity.DeliveryID{mine}, Steered: []identity.ShipID{d.Earlier[4].ID}})
	if strings.Join(texts(c.ships), ",") != "other slot,other agent" {
		t.Fatalf("resumed after a steer: %q", texts(c.ships))
	}
	if c := catchUp(d, &seenMark{Created: t0.Add(5 * time.Minute)}); len(c.ships) != 0 {
		t.Fatalf("seen everything: %+v", c)
	}

	var b strings.Builder
	writeEarlier(&b, catchUpOf{heading: earlierHeading, ships: earlier}, nil, self)
	got := b.String()
	for _, want := range []string{"you: my reply", "you: other slot", "another agent: other agent",
		"chatter [not handed to you: " + conversation.HoldNotMentioned + "]", "\nNew message(s):\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("catch-up lacks %q:\n%s", want, got)
		}
	}
}

func texts(ships []ship.Ship) []string {
	out := make([]string, len(ships))
	for i, s := range ships {
		out[i] = s.Text
	}
	return out
}

// The seen mark moves to the newest message handed with the delivery,
// keeps the steered ones apart, and remembers the session's newest
// deliveries (at most maxMine).
func TestSeenMarkMovesThroughWhatWasShown(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	m := seenMark{Created: t0}
	steered := ship.Ship{ID: identity.NewShipID(), CreatedAt: t0.Add(time.Hour)}
	m = m.through(identity.NewDeliveryID(), []ship.Ship{{CreatedAt: t0.Add(-time.Minute)}}, []ship.Ship{{CreatedAt: t0.Add(time.Minute)}}).
		steered([]ship.Ship{steered})
	if !m.Created.Equal(t0.Add(time.Minute)) || len(m.Mine) != 1 || len(m.Steered) != 1 || m.Steered[0] != steered.ID {
		t.Fatalf("mark: %+v", m)
	}
	for range maxMine + 3 {
		m = m.mine(identity.NewDeliveryID())
	}
	if len(m.Mine) != maxMine {
		t.Fatalf("mine kept %d", len(m.Mine))
	}
}

// A sessions file of an older connector (no seen mark) reads as unknown,
// and a mark round-trips.
func TestSavedSessionSeenReads(t *testing.T) {
	var old savedSession
	if err := json.Unmarshal([]byte(`{"id":"s1"}`), &old); err != nil || old.Seen != nil {
		t.Fatalf("old: %+v %v", old, err)
	}
	var bare savedSession
	if err := json.Unmarshal([]byte(`"s2"`), &bare); err != nil || bare.Seen != nil || bare.ID != "s2" {
		t.Fatalf("bare: %+v %v", bare, err)
	}
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(savedSession{ID: "s3", Seen: &seenMark{Created: at}})
	var back savedSession
	if err := json.Unmarshal(raw, &back); err != nil || back.Seen == nil || !back.Seen.Created.Equal(at) {
		t.Fatalf("round trip: %s → %+v %v", raw, back, err)
	}
}

// A delivery with a ship handed over before opens with the note to check
// what was already done.
func TestPromptWarnsOfARetriedDelivery(t *testing.T) {
	c := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelAPI, Kind: conversation.KindDirect, Title: "Chat"}
	d := ship.Delivery{Conversation: c, Ships: []ship.Ship{{Conversation: c.ID, SenderName: "Ana", Text: "hola", Attempts: 1}}}
	if strings.Contains(Prompt(d, true, nil), "didn't finish") {
		t.Fatal("a first hand-over is not retried")
	}
	d.Ships[0].Attempts = 2
	if got := Prompt(d, true, nil); !strings.HasPrefix(got, retriedNote) {
		t.Fatalf("retried prompt:\n%s", got)
	}
}
