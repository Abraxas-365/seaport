package event_test

import (
	"encoding/json"
	"testing"

	"github.com/Abraxas-365/puerto/internal/event"
)

func TestReplayAndReset(t *testing.T) {
	b := event.NewBus()
	s, replay, reset := b.Subscribe("")
	if replay != nil || reset {
		t.Fatalf("fresh subscribe: %v %v", replay, reset)
	}
	b.Publish("a", map[string]int{"n": 1})
	b.Publish("b", map[string]int{"n": 2})
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
	s2, replay, reset := b.Subscribe(first.ID)
	defer s2.Close()
	if reset || len(replay) != 1 || replay[0].ID != second.ID {
		t.Fatalf("replay: %+v reset=%v", replay, reset)
	}
	if _, replay, reset := b.Subscribe(second.ID); reset || len(replay) != 0 {
		t.Fatalf("up to date: %+v %v", replay, reset)
	}
	for _, id := range []string{"otherboot-1", "garbage", second.ID + "0"} {
		if _, _, reset := b.Subscribe(id); !reset {
			t.Errorf("Subscribe(%q) must reset", id)
		}
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := event.NewBus()
	s, _, _ := b.Subscribe("")
	for i := 0; i < 2000; i++ {
		b.Publish("x", i)
	}
	n := 0
	for range s.C {
		n++
	}
	if n == 0 || n >= 2000 || b.Subscribers() != 0 {
		t.Fatalf("got %d events, %d subscribers", n, b.Subscribers())
	}
	// The history still lets it catch up only as far back as it reaches.
	if _, _, reset := b.Subscribe(b.Boot() + "-1"); !reset {
		t.Fatal("an event older than the history must reset")
	}
}
