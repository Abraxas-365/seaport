package hostingpg

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// Slot runs: every transition is one conditional UPDATE, so processes
// sharing the database (and the Docker engine) never both start or stop
// one slot.

func (r *Repository) BeginStart(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, wake bool) (hosting.SlotStart, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.SlotStart{}, false, pgerr.Map(err, "begin start slot")
	}
	defer func() { _ = tx.Rollback() }()
	run := identity.NewCrewTokenID().String() // any random id
	var row struct {
		OldToken    *identity.CrewTokenID  `db:"old_token"`
		Environment identity.EnvironmentID `db:"environment_id"`
		AgentName   string                 `db:"name"`
	}
	// cur locks the row first: a concurrent start waits, then sees it
	// starting and claims nothing.
	err = tx.GetContext(ctx, &row, `WITH cur AS (
			SELECT token_id FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 FOR UPDATE)
		UPDATE agent_slots s SET state = 'starting', detail = 'starting', run = $4, token_id = NULL, updated_at = now()
		FROM cur, agent_hosting h, agents a
		WHERE s.organization_id = $1 AND s.agent_id = $2 AND s.idx = $3
		  AND h.organization_id = s.organization_id AND h.agent_id = s.agent_id
		  AND a.organization_id = s.organization_id AND a.id = s.agent_id AND a.deleted_at IS NULL
		  AND (s.state IN ('stopped', 'failed') OR (s.state = 'starting' AND s.updated_at < now() - make_interval(secs => $5::float8)))
		  AND (NOT $6::bool OR NOT EXISTS (SELECT 1 FROM agent_slots o WHERE o.organization_id = s.organization_id
		        AND o.agent_id = s.agent_id AND o.idx <> s.idx AND o.state IN ('starting', 'running')))
		RETURNING cur.token_id AS old_token, h.environment_id, a.name`,
		org, agentID, slot, run, hosting.StartStale.Seconds(), wake)
	if pgerr.IsNoRows(err) {
		h, herr := hosted(ctx, tx, org, agentID)
		if herr != nil {
			return hosting.SlotStart{}, false, herr
		}
		for _, s := range h.Slots {
			if s.Index == slot {
				return hosting.SlotStart{}, false, nil
			}
		}
		return hosting.SlotStart{}, false, errx.NotFound("slot not found")
	}
	if err != nil {
		return hosting.SlotStart{}, false, pgerr.Map(err, "start slot")
	}
	env, err := environment(ctx, tx, org, row.Environment, 0)
	if err != nil {
		return hosting.SlotStart{}, false, err
	}
	out := hosting.SlotStart{Run: run, AgentName: row.AgentName, Environment: env}
	if row.OldToken != nil {
		out.OldToken = *row.OldToken
	}
	return out, true, pgerr.Map(tx.Commit(), "commit start slot")
}

func (r *Repository) StartProgress(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run, detail string, token identity.CrewTokenID) error {
	var tok any
	if !token.IsZero() {
		tok = token
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET detail = $5, token_id = COALESCE($6, token_id), updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state = 'starting' AND run = $4`,
		org, agentID, slot, run, detail, tok)
	if err != nil {
		return pgerr.Map(err, "slot start progress")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.Conflict("the slot is no longer starting")
	}
	return nil
}

func (r *Repository) StartDone(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run string, started hosting.SlotRun) (hosting.Hosted, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET state = 'running', detail = '', image = $5,
		environment_id = $6, environment_version = $7, started_at = now(), active_at = now(), updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state = 'starting' AND run = $4`,
		org, agentID, slot, run, started.Image, started.Environment, started.Version)
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "slot started")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return hosting.Hosted{}, errx.Conflict("the slot is no longer starting")
	}
	return hosted(ctx, r.db, org, agentID)
}

func (r *Repository) BeginStop(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run string) (hosting.Hosted, bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET state = 'stopping', detail = 'stopping', run = '', updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state <> 'stopped'
		  AND ($4::text = '' OR (state = 'running' AND run = $4))`, org, agentID, slot, run)
	if err != nil {
		return hosting.Hosted{}, false, pgerr.Map(err, "stop slot")
	}
	h, err := hosted(ctx, r.db, org, agentID)
	if err != nil {
		return h, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 && !hasSlot(h, slot) {
		return h, false, errx.NotFound("slot not found")
	}
	return h, n > 0, nil
}

func hasSlot(h hosting.Hosted, slot int) bool {
	for _, s := range h.Slots {
		if s.Index == slot {
			return true
		}
	}
	return false
}

func (r *Repository) EndRun(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, end hosting.EndRun) (hosting.Hosted, identity.CrewTokenID, bool, error) {
	var token *identity.CrewTokenID
	err := r.db.GetContext(ctx, &token, `WITH cur AS (
			SELECT token_id FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 FOR UPDATE)
		UPDATE agent_slots s SET state = $4, detail = $5, run = '', token_id = NULL, updated_at = now()
		FROM cur
		WHERE s.organization_id = $1 AND s.agent_id = $2 AND s.idx = $3 AND s.state = ANY($6::text[])
		  AND ($7::text = '' OR s.run = $7) AND ($8::float8 = 0 OR s.updated_at < now() - make_interval(secs => $8))
		RETURNING cur.token_id`, org, agentID, slot, end.State, end.Detail, end.From, end.Run, end.StaleFor.Seconds())
	if pgerr.IsNoRows(err) {
		return hosting.Hosted{}, identity.CrewTokenID{}, false, nil
	}
	if err != nil {
		return hosting.Hosted{}, identity.CrewTokenID{}, false, pgerr.Map(err, "end slot run")
	}
	var tok identity.CrewTokenID
	if token != nil {
		tok = *token
	}
	h, err := hosted(ctx, r.db, org, agentID)
	if errx.IsType(err, errx.TypeNotFound) { // unhosted meanwhile
		return hosting.Hosted{}, tok, true, nil
	}
	return h, tok, true, err
}

func (r *Repository) Supervise(ctx context.Context) ([]hosting.Supervised, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, pgerr.Map(err, "begin supervise")
	}
	defer func() { _ = tx.Rollback() }()
	// Agents with work keep their running slots active.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_slots s SET active_at = now()
		WHERE s.state = 'running' AND EXISTS (SELECT 1 FROM ships sh WHERE sh.organization_id = s.organization_id
		  AND sh.agent_id = s.agent_id AND sh.status IN ('queued', 'claimed'))`); err != nil {
		return nil, pgerr.Map(err, "touch active slots")
	}
	out := []hosting.Supervised{}
	if err := tx.SelectContext(ctx, &out, `WITH work AS (
			SELECT organization_id, agent_id,
			  count(*) FILTER (WHERE status = 'queued') AS queued,
			  count(*) FILTER (WHERE status = 'claimed') AS claimed
			FROM ships WHERE status IN ('queued', 'claimed') AND agent_id IS NOT NULL GROUP BY organization_id, agent_id)
		SELECT s.organization_id, s.agent_id, s.idx, s.state, s.run, s.updated_at, s.active_at, s.started_at,
		  COALESCE(v.spec->>'idle_stop', '') AS idle_stop,
		  (s.state = 'running' AND (s.environment_id IS DISTINCT FROM h.environment_id OR s.environment_version <> e.version)) AS outdated,
		  COALESCE(w.queued, 0) AS queued, COALESCE(w.claimed, 0) AS claimed
		FROM agent_slots s
		JOIN agent_hosting h ON h.organization_id = s.organization_id AND h.agent_id = s.agent_id
		JOIN agents a ON a.organization_id = s.organization_id AND a.id = s.agent_id AND a.deleted_at IS NULL
		JOIN environments e ON e.organization_id = h.organization_id AND e.id = h.environment_id
		JOIN environment_versions v ON v.organization_id = e.organization_id AND v.environment_id = e.id AND v.version = e.version
		LEFT JOIN work w ON w.organization_id = s.organization_id AND w.agent_id = s.agent_id
		ORDER BY s.organization_id, s.agent_id, s.idx`); err != nil {
		return nil, pgerr.Map(err, "list slots to supervise")
	}
	return out, pgerr.Map(tx.Commit(), "commit supervise")
}

func (r *Repository) PortID(ctx context.Context) (string, error) {
	var id string
	err := r.db.GetContext(ctx, &id, `SELECT id::text FROM port_identity`)
	return id, pgerr.Map(err, "read port id")
}
