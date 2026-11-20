package dockwa

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/ship"
)

func TestValidEmoji(t *testing.T) {
	for _, e := range []string{"👀", "✅", "❤️", "👍🏽", "👩‍💻", "👩🏽‍💻", "🏳️‍🌈", "🇵🇪", "1️⃣", "#⃣", "🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f", "™️", "⭐", "🫡", "⏳", "↩️", "▶️"} {
		if !ValidEmoji(e) {
			t.Errorf("%q is one emoji", e)
		}
	}
	for _, e := range []string{"", "ok", "👀👀", "👀 ", " 👀", "a👀", "\u200d👀", "👀\u200d", "🏽", "\ufe0f", "👩\u200d🏽", "🇵", "🇵🇪🇨🇱", "1", "12⃣", "\xff", "→", "■", "⌀", "⤀", string(make([]byte, 65))} {
		if ValidEmoji(e) {
			t.Errorf("%q is not one emoji", e)
		}
	}
}

func TestSettingsPatch(t *testing.T) {
	if err := (SettingsPatch{}).Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("an empty patch: %v", err)
	}
	bad := "ok"
	if err := (SettingsPatch{Emoji: &bad}).Validate(); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("a word as emoji: %v", err)
	}
	on, check := true, "✅"
	p := SettingsPatch{React: &on, Emoji: &check}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.Apply(DefaultSettings()); got != (Settings{React: true, Emoji: "✅"}) {
		t.Fatalf("apply: %+v", got)
	}
	if DefaultSettings() != (Settings{Emoji: DefaultEmoji}) {
		t.Fatalf("defaults are off with 👀: %+v", DefaultSettings())
	}
}

// A reaction takes a turn among the dock's sends only when it comes soon,
// and a turn it doesn't take delays nobody.
func TestPaceWithin(t *testing.T) {
	s := &session{}
	if !s.paceWithin(context.Background(), time.Second) {
		t.Fatal("no send before: the turn is now")
	}
	s.mu.Lock()
	s.lastSend = time.Now().Add(time.Minute) // a long queue of replies
	queued := s.lastSend
	s.mu.Unlock()
	if s.paceWithin(context.Background(), time.Second) {
		t.Fatal("took a turn a minute away")
	}
	s.mu.Lock()
	kept := s.lastSend
	s.mu.Unlock()
	if !kept.Equal(queued) {
		t.Fatalf("a skipped turn moved the queue: %v → %v", queued, kept)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.mu.Lock()
	s.lastSend = time.Now() // the next turn is minSendGap away
	s.mu.Unlock()
	if s.paceWithin(ctx, time.Minute) {
		t.Fatal("an ended context still got its turn")
	}
}

// Only a message that just went to an agent is marked: not a held one, a
// redelivery or the account's own chat.
func TestMarks(t *testing.T) {
	queued := ship.Ship{Status: ship.StatusQueued}
	cases := []struct {
		st      ship.Ship
		created bool
		kind    conversation.Kind
		want    bool
	}{
		{queued, true, conversation.KindDirect, true},
		{queued, true, conversation.KindGroup, true},
		{queued, false, conversation.KindDirect, false},
		{queued, true, conversation.KindSelf, false},
		{ship.Ship{Status: ship.StatusHeld, HoldReason: conversation.HoldNotMentioned}, true, conversation.KindGroup, false},
		{ship.Ship{Status: ship.StatusHeld, HoldReason: conversation.HoldPending}, true, conversation.KindDirect, false},
	}
	for i, c := range cases {
		if got := marks(c.st, c.created, c.kind); got != c.want {
			t.Errorf("case %d (%+v created=%v %s): %v", i, c.st.Status, c.created, c.kind, got)
		}
	}
}
