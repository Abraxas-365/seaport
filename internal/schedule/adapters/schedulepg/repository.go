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

const columns = `id, organization_id, conversation_id, agent_id, name, cron, timezone, text, enabled, next_at, last_at, last_ship_id, created_at, updated_at`

// createLock serializes one organization's schedule creations while its
// schedules are counted (second key: hashtext(organization)).
const createLock = 7_401_012

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
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM schedules WHERE organization_id = $1`, org); err != nil {
			return out, pgerr.Map(err, "count schedules")
		}
		if n >= limit {
			return out, errx.QuotaExceeded("schedules", limit, "an organization can have at most "+strconv.Itoa(limit)+" schedules; delete one first")
		}
	}
	if err := tx.GetContext(ctx, &out, `INSERT INTO schedules
		(id, organization_id, conversation_id, agent_id, name, cron, timezone, text, enabled, next_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING `+columns,
		s.ID, org, s.Conversation, s.Agent, s.Name, s.Cron, s.Timezone, s.Text, s.Enabled, s.NextAt); err != nil {
		return out, pgerr.MapWith(err, "create schedule", messages)
	}
	if err := tx.Commit(); err != nil {
		return out, pgerr.Map(err, "commit schedule")
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
		cond += " AND conversation_id = $2"
		args = append(args, *f.Conversation)
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
			next_at = $10, updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns,
		org, id, in.Name, in.Cron, in.Timezone, in.Text, in.Enabled, setAgent, agent, next); err != nil {
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
		if next.IsZero() { // it can never run again: switch it off
			next, enabled = s.NextAt, false
			s.Enabled = false
			out.Disabled = append(out.Disabled, s)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET next_at = $3, enabled = $4, updated_at = now()
			WHERE organization_id = $1 AND id = $2`, s.Organization, s.ID, next, enabled); err != nil {
			return schedule.Claim{}, pgerr.Map(err, "advance schedule")
		}
		if adv.Fire {
			out.Fire = append(out.Fire, schedule.Due{Schedule: s, Slot: s.NextAt})
		}
	}
	if err := tx.Commit(); err != nil {
		return schedule.Claim{}, pgerr.Map(err, "commit due schedules")
	}
	out.Claimed = len(rows)
	return out, nil
}
