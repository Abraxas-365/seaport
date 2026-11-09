package event_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
)

var (
	orgA = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000a")
	orgB = identity.MustParseOrganizationID("00000000-0000-4000-8000-00000000000b")
)

func startBus(t *testing.T, log event.Log) *event.Bus {
	t.Helper()
	b := event.NewBus(log)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func subscribe(t *testing.T, b *event.Bus, org identity.OrganizationID, last string) (*event.Subscription, []event.Event, bool) {
	t.Helper()
	s, replay, reset, err := b.Subscribe(context.Background(), org, last)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, replay, reset
}

func recv(t *testing.T, s *event.Subscription) event.Event {
	t.Helper()
	select {
	case e, ok := <-s.C:
		if !ok {
			t.Fatal("subscription closed")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return event.Event{}
}

func TestReplayAndReset(t *testing.T) {
	b := startBus(t, event.NewMemoryLog())
	s, replay, reset := subscribe(t, b, orgA, "")
	if replay != nil || reset {
		t.Fatalf("fresh subscribe: %v %v", replay, reset)
	}
	b.Publish(orgA, "a", map[string]int{"n": 1})
	b.Publish(orgA, "b", map[string]int{"n": 2})
	first, second := recv(t, s), recv(t, s)
	if first.Type != "a" || second.Type != "b" || first.Seq >= second.Seq {
		t.Fatalf("order: %+v %+v", first, second)
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
	_, replay, reset = subscribe(t, b, orgA, first.ID)
	if reset || len(replay) != 1 || replay[0].ID != second.ID {
		t.Fatalf("replay: %+v reset=%v", replay, reset)
	}
	if _, replay, reset := subscribe(t, b, orgA, second.ID); reset || len(replay) != 0 {
		t.Fatalf("up to date: %+v %v", replay, reset)
	}
	for _, id := range []string{"otherboot-1", "garbage", "-3", second.ID + "0"} {
		if _, _, reset := subscribe(t, b, orgA, id); !reset {
			t.Errorf("Subscribe(%q) must reset", id)
		}
	}
}

func TestSweptEventsReset(t *testing.T) {
	b := startBus(t, event.NewMemoryLog())
	s, _, _ := subscribe(t, b, orgA, "")
	b.Publish(orgA, "old", 1)
	old := recv(t, s)
	b.Publish(orgA, "newer", 2)
	recv(t, s)
	if _, err := b.Sweep(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, reset := subscribe(t, b, orgA, old.ID); !reset {
		t.Fatal("an id whose successors were swept must reset")
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	b := startBus(t, event.NewMemoryLog())
	s, _, _ := subscribe(t, b, orgA, "")
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
	// It reconnects: more than maxReplay behind resets, closer replays.
	if _, _, reset := subscribe(t, b, orgA, "0"); !reset {
		t.Fatal("2000 events behind must reset")
	}
	// (last, head] comes as replay and the rest on C (the fan-out may not
	// have reached 2000 yet): 1501..2000, no gap, no duplicate.
	s2, replay, reset := subscribe(t, b, orgA, "1500")
	if reset {
		t.Fatal("500 events behind must replay, not reset")
	}
	got := replay
	for len(got) < 500 {
		got = append(got, recv(t, s2))
	}
	for i, e := range got {
		if e.Seq != int64(1501+i) {
			t.Fatalf("replay after drop: event %d is seq %d (replayed %d)", i, e.Seq, len(replay))
		}
	}
}

func TestEventsStayInTheirOrganization(t *testing.T) {
	b := startBus(t, event.NewMemoryLog())
	a, _, _ := subscribe(t, b, orgA, "")
	bs, _, _ := subscribe(t, b, orgB, "")
	b.Publish(orgA, "a", 1)
	b.Publish(orgB, "b", 2)
	if e := recv(t, a); e.Type != "a" {
		t.Fatalf("org A got %s", e.Type)
	}
	if e := recv(t, bs); e.Type != "b" {
		t.Fatalf("org B got %s", e.Type)
	}
	select {
	case e := <-a.C:
		t.Fatalf("org A received another org's event %s", e.Type)
	case <-time.After(100 * time.Millisecond):
	}
	// Replay only returns the subscriber's organization.
	_, replay, reset := subscribe(t, b, orgB, "0")
	if reset || len(replay) != 1 || replay[0].Type != "b" {
		t.Fatalf("org B replay: %+v reset=%v", replay, reset)
	}
}

// TestBusesShareTheLog: two buses (two processes) on one log both see
// every event, and a client resumes on either with the other's ids.
func TestBusesShareTheLog(t *testing.T) {
	log := event.NewMemoryLog()
	one, two := startBus(t, log), startBus(t, log)
	s1, _, _ := subscribe(t, one, orgA, "")
	s2, _, _ := subscribe(t, two, orgA, "")
	one.Publish(orgA, "from one", 1)
	two.Publish(orgA, "from two", 2)
	for _, s := range []*event.Subscription{s1, s2} {
		if a, b := recv(t, s), recv(t, s); a.Type != "from one" || b.Type != "from two" {
			t.Fatalf("order: %s, %s", a.Type, b.Type)
		}
	}
	// A client of bus one moves to bus two after the first event.
	_, replay, reset := subscribe(t, two, orgA, "1")
	if reset || len(replay) != 1 || replay[0].Type != "from two" {
		t.Fatalf("resume on the other bus: %+v %v", replay, reset)
	}
}

// TestNoGapNoDuplicate: events published while clients subscribe reach
// each client exactly once and in order, through replay or C.
func TestNoGapNoDuplicate(t *testing.T) {
	b := startBus(t, event.NewMemoryLog())
	const n = 300
	go func() {
		for i := 1; i <= n; i++ {
			b.Publish(orgA, "x", i)
		}
	}()
	for c := 0; c < 20; c++ {
		s, replay, reset := subscribe(t, b, orgA, "0")
		if reset {
			t.Fatal("reset")
		}
		seen := map[int64]bool{}
		last := int64(0)
		take := func(e event.Event) {
			if seen[e.Seq] || e.Seq <= last {
				t.Fatalf("client %d: event %d twice or out of order (after %d)", c, e.Seq, last)
			}
			seen[e.Seq], last = true, e.Seq
		}
		for _, e := range replay {
			take(e)
		}
		for last < n {
			take(recv(t, s))
		}
		for i := int64(1); i <= n; i++ {
			if !seen[i] {
				t.Fatalf("client %d missed event %s", c, strconv.FormatInt(i, 10))
			}
		}
		s.Close()
	}
}
