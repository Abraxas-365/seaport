// Package schedulepg is the Postgres repository of schedules. Due is the
// runner's claim: rows are locked FOR UPDATE SKIP LOCKED and advanced in
// the same transaction, so two processes never take one slot.
package schedulepg

import (
	"context"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, conversation_id, agent_id, name, cron, run_at, created_by, done_at, timezone, text, enabled, next_at, last_at, last_ship_id, created_at, updated_at`

// createLock serializes one organization's schedule creations while its
// schedules are counted (second key: hashtext(organization)).
const createLock = 7_401_012

// purgeBatch bounds how many done one-shots one claim deletes.
const purgeBatch = 100

var messages = pgerr.Messages{NotFound: "schedule not found", Unique: "a schedule with this name already exists",
	ForeignKey: "conversation or agent not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ schedule.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, s schedule.Schedule, limit int) (schedule.Schedule, error) {
	var out schedule.Schedule
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return out, pgerr.Map(err, "begin create schedule")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
		return out, pgerr.Map(err, "lock schedules")
	}
	if limit > 0 {
		if err := checkOrgQuota(ctx, tx, org, limit); err != nil {
			return out, err
		}
	}
	if err := insert(ctx, tx, org, s, &out); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, pgerr.Map(err, "commit schedule")
	}
	return out, nil
}

// checkOrgQuota counts the organization's pending schedules (a one-shot
// that already ran is not one) under the create lock.
func checkOrgQuota(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID, limit int) error {
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM schedules WHERE organization_id = $1 AND done_at IS NULL`, org); err != nil {
		return pgerr.Map(err, "count schedules")
	}
	if n >= limit {
		return errx.QuotaExceeded("schedules", limit, "an organization can have at most "+strconv.Itoa(limit)+" schedules; delete one first")
	}
	return nil
}

func insert(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID, s schedule.Schedule, out *schedule.Schedule) error {
	if err := tx.GetContext(ctx, out, `INSERT INTO schedules
		(id, organization_id, conversation_id, agent_id, name, cron, run_at, created_by, timezone, text, enabled, next_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING `+columns,
		s.ID, org, s.Conversation, s.Agent, s.Name, s.Cron, s.RunAt, s.CreatedBy, s.Timezone, s.Text, s.Enabled, s.NextAt); err != nil {
		return pgerr.MapWith(err, "create schedule", messages)
	}
	return nil
}

func (r *Repository) CreateAlarm(ctx context.Context, org identity.OrganizationID, s schedule.Schedule, limits schedule.AlarmLimits, orgLimit int) (schedule.Schedule, error) {
	var out schedule.Schedule
	if s.CreatedBy == nil {
		return out, errx.Internal("an alarm has a creator")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return out, pgerr.Map(err, "begin create alarm")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
		return out, pgerr.Map(err, "lock schedules")
	}
	var n struct {
		Conversation int `db:"conversation"`
		Agent        int `db:"agent"`
		Day          int `db:"day"`
	}
	if err := tx.GetContext(ctx, &n, `SELECT
			count(*) FILTER (WHERE done_at IS NULL AND conversation_id = $3) AS conversation,
			count(*) FILTER (WHERE done_at IS NULL) AS agent,
			count(*) FILTER (WHERE created_at > now() - interval '24 hours') AS day
		FROM schedules WHERE organization_id = $1 AND created_by = $2`, org, *s.CreatedBy, s.Conversation); err != nil {
		return out, pgerr.Map(err, "count alarms")
	}
	switch {
	case limits.PerConversation > 0 && n.Conversation >= limits.PerConversation:
		return out, errx.QuotaExceeded("alarms_per_conversation", limits.PerConversation,
			"you already have "+strconv.Itoa(n.Conversation)+" pending alarms in this conversation (at most "+
				strconv.Itoa(limits.PerConversation)+"); cancel one first")
	case limits.PerAgent > 0 && n.Agent >= limits.PerAgent:
		return out, errx.QuotaExceeded("alarms_per_agent", limits.PerAgent,
			"you already have "+strconv.Itoa(n.Agent)+" pending alarms (at most "+strconv.Itoa(limits.PerAgent)+"); cancel one first")
	case limits.PerDay > 0 && n.Day >= limits.PerDay:
		return out, errx.QuotaExceeded("alarms_per_day", limits.PerDay,
			"you set "+strconv.Itoa(n.Day)+" alarms in the last 24 hours (at most "+strconv.Itoa(limits.PerDay)+
				"); wait before setting more, and don't set an alarm every time you wake up")
	}
	if orgLimit > 0 {
		if err := checkOrgQuota(ctx, tx, org, orgLimit); err != nil {
			return out, err
		}
	}
	if err := insert(ctx, tx, org, s, &out); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, pgerr.Map(err, "commit alarm")
	}
	return out, nil
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID) (schedule.Schedule, error) {
	var s schedule.Schedule
	if err := r.db.GetContext(ctx, &s, `SELECT `+columns+` FROM schedules WHERE organization_id = $1 AND id = $2`, org, id); err != nil {
		return s, pgerr.MapWith(err, "find schedule", messages)
	}
	return s, nil
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, f schedule.Filter, page query.Pagination) (query.Paginated[schedule.Schedule], error) {
	out := query.Paginated[schedule.Schedule]{Items: []schedule.Schedule{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	cond, args := "organization_id = $1", []any{org}
	if f.Conversation != nil {
		args = append(args, *f.Conversation)
		cond += " AND conversation_id = $" + strconv.Itoa(len(args))
	}
	if f.Agent != nil {
		args = append(args, *f.Agent)
		cond += " AND created_by = $" + strconv.Itoa(len(args))
	}
	if !f.IncludeDone {
		cond += " AND done_at IS NULL"
	}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM schedules WHERE `+cond, args...); err != nil {
		return out, pgerr.Map(err, "count schedules")
	}
	n := len(args)
	if err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM schedules WHERE `+cond+
		` ORDER BY created_at, id LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2),
		append(args, page.Limit, page.Offset)...); err != nil {
		return out, pgerr.Map(err, "list schedules")
	}
	return out, nil
}

func (r *Repository) Update(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID, in schedule.Update, next time.Time) (schedule.Schedule, error) {
	var s schedule.Schedule
	setAgent := in.Agent != nil
	var agent any
	if setAgent && !in.Agent.IsZero() {
		agent = *in.Agent
	}
	if err := r.db.GetContext(ctx, &s, `UPDATE schedules SET
			name = COALESCE($3, name), cron = COALESCE($4, cron), timezone = COALESCE($5, timezone),
			text = COALESCE($6, text), enabled = COALESCE($7, enabled),
			agent_id = CASE WHEN $8::boolean THEN $9::uuid ELSE agent_id END,
			run_at = COALESCE($11::timestamptz, run_at),
			done_at = CASE WHEN $11::timestamptz IS NULL THEN done_at ELSE NULL END,
			next_at = $10, updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns,
		org, id, in.Name, in.Cron, in.Timezone, in.Text, in.Enabled, setAgent, agent, next, in.At); err != nil {
		return s, pgerr.MapWith(err, "update schedule", messages)
	}
	return s, nil
}

func (r *Repository) Delete(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID) (schedule.Schedule, error) {
	var s schedule.Schedule
	if err := r.db.GetContext(ctx, &s, `DELETE FROM schedules WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, id); err != nil {
		return s, pgerr.MapWith(err, "delete schedule", messages)
	}
	return s, nil
}

func (r *Repository) Record(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID, at time.Time, ship identity.ShipID) error {
	res, err := r.db.ExecContext(ctx, `UPDATE schedules SET last_at = $3, last_ship_id = $4
		WHERE organization_id = $1 AND id = $2`, org, id, at, ship)
	if err != nil {
		return pgerr.MapWith(err, "record schedule run", messages)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "record schedule run", messages)
	}
	return nil
}

func (r *Repository) Due(ctx context.Context, now time.Time, limit int, plan func(schedule.Schedule) schedule.Advance) (schedule.Claim, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return schedule.Claim{}, pgerr.Map(err, "begin due schedules")
	}
	defer func() { _ = tx.Rollback() }()
	var rows []schedule.Schedule
	if err := tx.SelectContext(ctx, &rows, `SELECT `+columns+` FROM schedules
		WHERE enabled AND next_at <= $1 ORDER BY next_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit); err != nil {
		return schedule.Claim{}, pgerr.Map(err, "claim due schedules")
	}
	var out schedule.Claim
	for _, s := range rows {
		adv := plan(s)
		next, enabled := adv.Next, true
		var done *time.Time
		switch {
		case adv.Done: // a one-shot ran or was missed: it never runs again
			next, enabled, done = s.NextAt, false, &now
		case next.IsZero(): // it can never run again: switch it off
			next, enabled = s.NextAt, false
			s.Enabled = false
			out.Disabled = append(out.Disabled, s)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET next_at = $3, enabled = $4, done_at = COALESCE($5, done_at), updated_at = now()
			WHERE organization_id = $1 AND id = $2`, s.Organization, s.ID, next, enabled, done); err != nil {
			return schedule.Claim{}, pgerr.Map(err, "advance schedule")
		}
		if done != nil {
			s.Enabled, s.DoneAt = false, done
		}
		if adv.Done && !adv.Fire { // closed without firing: tell the web
			out.Disabled = append(out.Disabled, s)
		}
		if adv.Fire {
			out.Fire = append(out.Fire, schedule.Due{Schedule: s, Slot: s.NextAt})
		}
	}
	// Done one-shots older than schedule.DoneKeep go (a bounded delete: the
	// next round takes the rest).
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id IN (
			SELECT id FROM schedules WHERE done_at IS NOT NULL AND done_at < $1 LIMIT $2)`,
		now.Add(-schedule.DoneKeep), purgeBatch); err != nil {
		return schedule.Claim{}, pgerr.Map(err, "purge done schedules")
	}
	if err := tx.Commit(); err != nil {
		return schedule.Claim{}, pgerr.Map(err, "commit due schedules")
	}
	out.Claimed = len(rows)
	return out, nil
}
