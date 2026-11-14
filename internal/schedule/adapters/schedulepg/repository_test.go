package schedulepg_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/schedule/adapters/schedulepg"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func freshDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Migrate(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	return db
}

// conversation stores a dock with one chat and returns the chat.
func conversation(t *testing.T, db *sqlx.DB) identity.ConversationID {
	t.Helper()
	dock, conv := identity.NewDockID(), identity.NewConversationID()
	if _, err := db.Exec(`INSERT INTO docks (id, organization_id, channel, name, address, status)
		VALUES ($1, $2, 'api', 'test', '', 'linked')`, dock, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations (id, organization_id, dock_id, channel, external_id, kind, access, status, thread_id)
		VALUES ($1, $2, $3, 'api', 'chat', 'direct', 'allow', 'allowed', $1)`, conv, org, dock); err != nil {
		t.Fatal(err)
	}
	return conv
}

func row(conv identity.ConversationID, name string, next time.Time) schedule.Schedule {
	return schedule.Schedule{ID: identity.NewScheduleID(), Conversation: conv, Name: name, Cron: "0 9 * * *",
		Timezone: "UTC", Text: "hola", Enabled: true, NextAt: next}
}

// Two callers claiming at once never get the same row (FOR UPDATE SKIP
// LOCKED), and a claimed row has moved on, so a later call finds nothing.
func TestDueNeverGivesARowTwice(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := repo.Create(ctx, org, row(conv, "s"+string(rune('a'+i)), past), 0); err != nil {
			t.Fatal(err)
		}
	}
	// One future schedule and one switched off are never claimed.
	if _, err := repo.Create(ctx, org, row(conv, "later", time.Now().Add(time.Hour)), 0); err != nil {
		t.Fatal(err)
	}
	off := row(conv, "off", past)
	off.Enabled = false
	if _, err := repo.Create(ctx, org, off, 0); err != nil {
		t.Fatal(err)
	}

	next := time.Now().Add(24 * time.Hour)
	plan := func(schedule.Schedule) schedule.Advance {
		time.Sleep(20 * time.Millisecond) // widen the window two claimers overlap in
		return schedule.Advance{Next: next, Fire: true}
	}
	var (
		mu   sync.Mutex
		seen = map[identity.ScheduleID]int{}
		wg   sync.WaitGroup
	)
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire, _, err := repo.Due(ctx, time.Now(), 3, plan)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range fire {
				seen[d.ID]++
			}
		}()
	}
	wg.Wait()
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("schedule %s claimed %d times", id, c)
		}
	}
	// Drain what the four callers left.
	for {
		fire, claimed, err := repo.Due(ctx, time.Now(), 5, func(schedule.Schedule) schedule.Advance { return schedule.Advance{Next: next, Fire: true} })
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range fire {
			seen[d.ID]++
			if seen[d.ID] != 1 {
				t.Fatalf("schedule %s claimed twice", d.ID)
			}
		}
		if claimed == 0 {
			break
		}
	}
	if len(seen) != n {
		t.Fatalf("claimed %d schedules, want %d", len(seen), n)
	}
	if fire, claimed, err := repo.Due(ctx, time.Now(), 5, plan); err != nil || claimed != 0 || len(fire) != 0 {
		t.Fatalf("after the claims: %d %d %v", claimed, len(fire), err)
	}
}

// A schedule that can't run again is switched off instead of claimed forever;
// one the plan skips (a missed slot) is advanced without being fired.
func TestDueAdvancesSkippedAndDisablesBroken(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	skipped, _ := repo.Create(ctx, org, row(conv, "skipped", past), 0)
	broken, _ := repo.Create(ctx, org, row(conv, "broken", past), 0)
	next := time.Now().Add(time.Hour)
	fire, claimed, err := repo.Due(ctx, time.Now(), 10, func(s schedule.Schedule) schedule.Advance {
		if s.ID == skipped.ID {
			return schedule.Advance{Next: next}
		}
		return schedule.Advance{}
	})
	if err != nil || claimed != 2 || len(fire) != 0 {
		t.Fatalf("due: %d %d %v", claimed, len(fire), err)
	}
	got, err := repo.Find(ctx, org, skipped.ID)
	if err != nil || !got.Enabled || got.NextAt.Before(time.Now()) {
		t.Fatalf("skipped: %+v %v", got, err)
	}
	got, err = repo.Find(ctx, org, broken.ID)
	if err != nil || got.Enabled {
		t.Fatalf("broken should be off: %+v %v", got, err)
	}
}

// The quota is counted and enforced under the advisory lock: concurrent
// creates never overshoot it.
func TestCreateQuotaUnderTheLock(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	ctx := context.Background()
	const limit, tries = 3, 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		refused int
	)
	for i := 0; i < tries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Create(ctx, org, row(conv, "n"+string(rune('a'+i)), time.Now().Add(time.Hour)), limit)
			mu.Lock()
			defer mu.Unlock()
			var e *errx.Error
			switch {
			case err == nil:
				ok++
			case errors.As(err, &e) && e.HTTPStatus == 422:
				refused++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok != limit || refused != tries-limit {
		t.Fatalf("created %d refused %d, want %d and %d", ok, refused, limit, tries-limit)
	}
	var n int
	if err := db.Get(&n, `SELECT count(*) FROM schedules`); err != nil || n != limit {
		t.Fatalf("rows %d (%v), want %d", n, err, limit)
	}
}

// A name is unique per organization; a conversation's schedules go with it;
// Find/List/Delete never cross organizations.
func TestNamesTenancyAndCascade(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	ctx := context.Background()
	a, err := repo.Create(ctx, org, row(conv, "standup", time.Now().Add(time.Hour)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, org, row(conv, "standup", time.Now().Add(time.Hour)), 0); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	other := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	if _, err := repo.Find(ctx, other, a.ID); err == nil {
		t.Fatal("another organization found the schedule")
	}
	if page, err := repo.List(ctx, other, schedule.Filter{}, query.Pagination{Limit: 10}); err != nil || page.Page.Total != 0 {
		t.Fatalf("other organization lists %d (%v)", page.Page.Total, err)
	}
	if _, err := repo.Delete(ctx, other, a.ID); err == nil {
		t.Fatal("another organization deleted the schedule")
	}
	if _, err := db.Exec(`DELETE FROM conversations WHERE id = $1`, conv); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Find(ctx, org, a.ID); err == nil {
		t.Fatal("the schedule outlived its conversation")
	}
}
