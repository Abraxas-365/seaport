package event_test

import (
	"encoding/json"
	"testing"

	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
)

var (
	orgA = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000a")
	orgB = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000b")
)

func TestReplayAndReset(t *testing.T) {
	b := event.NewBus()
	s, replay, reset := b.Subscribe(orgA, "")
	if replay != nil || reset {
		t.Fatalf("fresh subscribe: %v %v", replay, reset)
	}
	b.Publish(orgA, "a", map[string]int{"n": 1})
	b.Publish(orgA, "b", map[string]int{"n": 2})
	first := <-s.C
	second := <-s.C
	if first.Type != "a" || second.Type != "b" {
		t.Fatalf("order: %s %s", first.Type, second.Type)
	}
	var data map[string]int
	if err := json.Unmarshal(second.Data, &data); err != nil || data["n"] != 2 {
		t.Fatalf("data: %s", second.Data)
	}
	s.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("closed subscription must close C")
	}

	// Reconnect after the first event: replay the second.
	s2, replay, reset := b.Subscribe(orgA, first.ID)
	defer s2.Close()
	if reset || len(replay) != 1 || replay[0].ID != second.ID {
		t.Fatalf("replay: %+v reset=%v", replay, reset)
	}
	if _, replay, reset := b.Subscribe(orgA, second.ID); reset || len(replay) != 0 {
		t.Fatalf("up to date: %+v %v", replay, reset)
	}
	for _, id := range []string{"otherboot-1", "garbage", second.ID + "0"} {
		if _, _, reset := b.Subscribe(orgA, id); !reset {
			t.Errorf("Subscribe(%q) must reset", id)
		}
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := event.NewBus()
	s, _, _ := b.Subscribe(orgA, "")
	for i := 0; i < 2000; i++ {
		b.Publish(orgA, "x", i)
	}
	n := 0
	for range s.C {
		n++
	}
	if n == 0 || n >= 2000 || b.Subscribers() != 0 {
		t.Fatalf("got %d events, %d subscribers", n, b.Subscribers())
	}
	// The history still lets it catch up only as far back as it reaches.
	if _, _, reset := b.Subscribe(orgA, b.Boot()+"-1"); !reset {
		t.Fatal("an event older than the history must reset")
	}
}

func TestEventsStayInTheirOrganization(t *testing.T) {
	b := event.NewBus()
	a, _, _ := b.Subscribe(orgA, "")
	defer a.Close()
	bs, _, _ := b.Subscribe(orgB, "")
	defer bs.Close()
	b.Publish(orgA, "a", 1)
	b.Publish(orgB, "b", 2)
	if e := <-a.C; e.Type != "a" {
		t.Fatalf("org A got %s", e.Type)
	}
	if e := <-bs.C; e.Type != "b" {
		t.Fatalf("org B got %s", e.Type)
	}
	select {
	case e := <-a.C:
		t.Fatalf("org A received another org's event %s", e.Type)
	default:
	}
	// Replay only returns the subscriber's organization.
	_, replay, reset := b.Subscribe(orgB, b.Boot()+"-0")
	if reset || len(replay) != 1 || replay[0].Type != "b" {
		t.Fatalf("org B replay: %+v reset=%v", replay, reset)
	}
}
