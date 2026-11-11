package eventpg_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/event/eventpg"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/jmoiron/sqlx"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func openDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	db, err := bootstrap.OpenDB(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestTwoProcesses: two buses, each with its own pool (as two replicas),
// get every event of both in one order, concurrent appends included, and
// a client resumes on the other one.
func TestTwoProcesses(t *testing.T) {
	ctx := context.Background()
	first := openDB(t)
	if _, err := first.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Migrate(ctx, first.DB); err != nil {
		t.Fatal(err)
	}
	second := openDB(t)
	buses := []*event.Bus{event.NewBus(eventpg.New(first)), event.NewBus(eventpg.New(second))}
	var subs []*event.Subscription
	for _, b := range buses {
		if err := b.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.Close)
		s, _, _, err := b.Subscribe(ctx, org, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		subs = append(subs, s)
	}

	const each = 50
	var wg sync.WaitGroup
	for _, b := range buses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				b.Publish(org, "x", i)
			}
		}()
	}
	wg.Wait()
	var orders [2][]int64
	for i, s := range subs {
		for len(orders[i]) < 2*each {
			select {
			case e, ok := <-s.C:
				if !ok {
					t.Fatal("subscription dropped")
				}
				if n := len(orders[i]); n > 0 && e.Seq <= orders[i][n-1] {
					t.Fatalf("bus %d: %d after %d", i, e.Seq, orders[i][n-1])
				}
				orders[i] = append(orders[i], e.Seq)
			case <-time.After(5 * time.Second):
				t.Fatalf("bus %d got %d of %d events", i, len(orders[i]), 2*each)
			}
		}
	}
	for j := range orders[0] {
		if orders[0][j] != orders[1][j] {
			t.Fatalf("the buses disagree at %d: %d vs %d", j, orders[0][j], orders[1][j])
		}
	}

	// A client of bus 0 that saw half reconnects to bus 1: it replays the rest.
	mid := orders[0][each-1]
	_, replay, reset, err := buses[1].Subscribe(ctx, org, itoa(mid))
	if err != nil || reset || len(replay) != each || replay[0].Seq <= mid {
		t.Fatalf("resume on the other process: %d events, reset=%v, err=%v", len(replay), reset, err)
	}
	// Other organizations replay nothing of this one.
	other := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	if _, replay, _, _ := buses[1].Subscribe(ctx, other, "0"); len(replay) != 0 {
		t.Fatalf("another organization replayed %d events", len(replay))
	}

	// After the retention sweep, an old id resets.
	if n, err := buses[0].Sweep(ctx, time.Now().Add(time.Minute)); err != nil || n != 2*each {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, _, reset, _ := buses[1].Subscribe(ctx, org, itoa(mid)); !reset {
		t.Fatal("a swept id must reset")
	}
	// The head survives the sweep: new events continue the sequence.
	buses[0].Publish(org, "after sweep", 0)
	select {
	case e := <-subs[1].C:
		if e.Seq <= orders[1][len(orders[1])-1] {
			t.Fatalf("sequence went back: %d", e.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event after the sweep")
	}

	// A flash crosses to the other process once (not back to its own
	// subscribers twice) and is never stored.
	var before int
	if err := first.GetContext(ctx, &before, `SELECT count(*) FROM events`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // both LISTEN connections up
	for len(subs[0].C) > 0 {
		<-subs[0].C // "after sweep"
	}
	buses[0].Flash(org, "live", map[string]int{"n": 7})
	for i, s := range subs {
		select {
		case e := <-s.C:
			if e.Type != "live" || e.ID != "" || string(e.Data) != `{"n":7}` {
				t.Fatalf("bus %d: flash %+v %s", i, e, e.Data)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("bus %d got no flash", i)
		}
	}
	select {
	case e := <-subs[0].C:
		t.Fatalf("the sender's bus got its flash twice: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
	var after int
	if err := first.GetContext(ctx, &after, `SELECT count(*) FROM events`); err != nil || after != before {
		t.Fatalf("a flash was stored: %d → %d (%v)", before, after, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
