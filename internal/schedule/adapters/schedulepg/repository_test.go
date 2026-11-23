package schedulepg_test

import (
	"context"
	"errors"
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
	"github.com/Abraxas-365/seaport/internal/testdb"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func freshDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := testdb.URL(t)
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
			claim, err := repo.Due(ctx, time.Now(), 3, plan)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range claim.Fire {
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
		claim, err := repo.Due(ctx, time.Now(), 5, func(schedule.Schedule) schedule.Advance { return schedule.Advance{Next: next, Fire: true} })
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range claim.Fire {
			seen[d.ID]++
			if seen[d.ID] != 1 {
				t.Fatalf("schedule %s claimed twice", d.ID)
			}
		}
		if claim.Claimed == 0 {
			break
		}
	}
	if len(seen) != n {
		t.Fatalf("claimed %d schedules, want %d", len(seen), n)
	}
	if claim, err := repo.Due(ctx, time.Now(), 5, plan); err != nil || claim.Claimed != 0 || len(claim.Fire) != 0 {
		t.Fatalf("after the claims: %d %d %v", claim.Claimed, len(claim.Fire), err)
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
	claim, err := repo.Due(ctx, time.Now(), 10, func(s schedule.Schedule) schedule.Advance {
		if s.ID == skipped.ID {
			return schedule.Advance{Next: next}
		}
		return schedule.Advance{}
	})
	if err != nil || claim.Claimed != 2 || len(claim.Fire) != 0 {
		t.Fatalf("due: %d %d %v", claim.Claimed, len(claim.Fire), err)
	}
	if len(claim.Disabled) != 1 || claim.Disabled[0].ID != broken.ID || claim.Disabled[0].Enabled {
		t.Fatalf("disabled: %+v", claim.Disabled)
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

// agentRow stores an agent (the creator of alarms).
func agentRow(t *testing.T, db *sqlx.DB, name string) identity.AgentID {
	t.Helper()
	id := identity.NewAgentID()
	if _, err := db.Exec(`INSERT INTO agents (id, organization_id, name) VALUES ($1, $2, $3)`, id, org, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func alarmRow(conv identity.ConversationID, agent identity.AgentID, name string, at time.Time) schedule.Schedule {
	return schedule.Schedule{ID: identity.NewScheduleID(), Conversation: conv, Agent: &agent, CreatedBy: &agent, Name: name,
		RunAt: &at, Timezone: "UTC", Text: "note", Enabled: true, NextAt: at}
}

// The alarm limits are counted under the organization's lock: per
// conversation and per agent pending, per agent per 24 hours (done ones
// count), and the organization's quota counts pending ones only.
func TestCreateAlarmLimits(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv, conv2 := conversation(t, db), conversation(t, db)
	a, b := agentRow(t, db, "a"), agentRow(t, db, "b")
	ctx := context.Background()
	at := time.Now().Add(time.Hour)
	limits := schedule.AlarmLimits{PerConversation: 2, PerAgent: 3, PerDay: 5}

	mk := func(c identity.ConversationID, ag identity.AgentID, name string) error {
		_, err := repo.CreateAlarm(ctx, org, alarmRow(c, ag, name, at), limits, 0)
		return err
	}
	quota := func(err error) {
		t.Helper()
		var e *errx.Error
		if !errors.As(err, &e) || e.Type != errx.TypeBusiness {
			t.Fatalf("want a quota error, got %v", err)
		}
	}
	for _, n := range []string{"a1", "a2"} {
		if err := mk(conv, a, n); err != nil {
			t.Fatal(err)
		}
	}
	quota(mk(conv, a, "a3")) // per conversation
	if err := mk(conv2, a, "a3"); err != nil {
		t.Fatal(err)
	}
	quota(mk(conv2, a, "a4")) // per agent
	if err := mk(conv, b, "b1"); err != nil {
		t.Fatalf("another agent has its own limits: %v", err)
	}

	// Done ones leave the pending limits but count for the day.
	if _, err := db.Exec(`UPDATE schedules SET done_at = now(), enabled = false WHERE created_by = $1`, a); err != nil {
		t.Fatal(err)
	}
	if err := mk(conv, a, "a5"); err != nil { // the 4th created today
		t.Fatalf("pending limits count only pending alarms: %v", err)
	}
	if err := mk(conv, a, "a6"); err != nil { // the 5th
		t.Fatal(err)
	}
	quota(mk(conv2, a, "a7")) // the 6th in 24 hours
	if _, err := db.Exec(`UPDATE schedules SET created_at = now() - interval '25 hours' WHERE created_by = $1 AND done_at IS NOT NULL`, a); err != nil {
		t.Fatal(err)
	}
	if err := mk(conv2, a, "a7"); err != nil {
		t.Fatalf("the day is rolling: %v", err)
	}

	// The organization's quota counts pending schedules (done ones don't).
	var pending int
	if err := db.Get(&pending, `SELECT count(*) FROM schedules WHERE done_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	_, err := repo.CreateAlarm(ctx, org, alarmRow(conv2, b, "b2", at), limits, pending)
	quota(err)
	if _, err := repo.CreateAlarm(ctx, org, alarmRow(conv2, b, "b2", at), limits, pending+1); err != nil {
		t.Fatalf("done alarms don't count against the quota: %v", err)
	}
}

// A one-shot is done when it runs (or is missed), not claimed again, hidden
// from lists unless asked, purged after DoneKeep; moving it arms it again.
func TestOneShotLifecycle(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	ag := agentRow(t, db, "a")
	ctx := context.Background()
	now := time.Now()
	fire, missed, later := alarmRow(conv, ag, "fire", now.Add(-time.Minute)), alarmRow(conv, ag, "missed", now.Add(-3*time.Hour)), alarmRow(conv, ag, "later", now.Add(time.Hour))
	for _, s := range []schedule.Schedule{fire, missed, later} {
		if _, err := repo.Create(ctx, org, s, 0); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := repo.Due(ctx, now, 10, func(s schedule.Schedule) schedule.Advance {
		return schedule.Advance{Fire: s.Name == "fire", Done: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Claimed != 2 || len(claim.Fire) != 1 || claim.Fire[0].Name != "fire" || claim.Fire[0].DoneAt == nil || claim.Fire[0].Enabled {
		t.Fatalf("claim: %+v", claim)
	}
	if len(claim.Disabled) != 1 || claim.Disabled[0].Name != "missed" {
		t.Fatalf("a missed one-shot is announced: %+v", claim.Disabled)
	}
	if again, err := repo.Due(ctx, now.Add(time.Minute), 10, func(schedule.Schedule) schedule.Advance { return schedule.Advance{Fire: true, Done: true} }); err != nil || again.Claimed != 0 {
		t.Fatalf("done one-shots are claimed again: %+v %v", again, err)
	}

	names := func(f schedule.Filter) []string {
		page, err := repo.List(ctx, org, f, query.Pagination{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range page.Items {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names(schedule.Filter{}); len(got) != 1 || got[0] != "later" {
		t.Fatalf("default list: %v", got)
	}
	if got := names(schedule.Filter{IncludeDone: true, Agent: &ag}); len(got) != 3 {
		t.Fatalf("with done: %v", got)
	}
	other := agentRow(t, db, "other")
	if got := names(schedule.Filter{IncludeDone: true, Agent: &other}); len(got) != 0 {
		t.Fatalf("another agent's: %v", got)
	}

	// A done one-shot frees its name; re-arming it while the name is taken
	// again is a conflict.
	again := alarmRow(conv, ag, "fire", now.Add(time.Hour))
	if _, err := repo.Create(ctx, org, again, 0); err != nil {
		t.Fatalf("a done one-shot's name is free: %v", err)
	}
	at := now.Add(2 * time.Hour)
	on := true
	if _, err := repo.Update(ctx, org, fire.ID, schedule.Update{At: &at, Enabled: &on}, at); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("re-arm with the name taken: %v", err)
	}
	if _, err := repo.Delete(ctx, org, again.ID); err != nil {
		t.Fatal(err)
	}

	// Moving a done one-shot arms it again.
	moved, err := repo.Update(ctx, org, fire.ID, schedule.Update{At: &at, Enabled: &on}, at)
	if err != nil || moved.DoneAt != nil || !moved.Enabled || moved.RunAt == nil || !moved.RunAt.Equal(at) {
		t.Fatalf("move: %+v %v", moved, err)
	}

	// The purge removes what was done more than DoneKeep ago.
	if _, err := db.Exec(`UPDATE schedules SET done_at = now() - interval '8 days' WHERE name = 'missed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Due(ctx, time.Now(), 10, func(schedule.Schedule) schedule.Advance { return schedule.Advance{} }); err != nil {
		t.Fatal(err)
	}
	if got := names(schedule.Filter{IncludeDone: true}); len(got) != 2 {
		t.Fatalf("after the purge: %v", got)
	}
}

// The database refuses a schedule that is both recurring and one-shot, or
// neither.
func TestSchedulesWhenConstraint(t *testing.T) {
	db := freshDB(t)
	repo := schedulepg.New(db)
	conv := conversation(t, db)
	both := row(conv, "both", time.Now())
	at := time.Now().Add(time.Hour)
	both.RunAt = &at
	if _, err := repo.Create(context.Background(), org, both, 0); err == nil {
		t.Fatal("a schedule with cron and run_at was stored")
	}
	none := row(conv, "none", time.Now())
	none.Cron = ""
	if _, err := repo.Create(context.Background(), org, none, 0); err == nil {
		t.Fatal("a schedule with neither cron nor run_at was stored")
	}
}
