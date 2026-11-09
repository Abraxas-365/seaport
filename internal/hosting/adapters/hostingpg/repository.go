// Package hostingpg is the Postgres repository of environments and hosted
// agents (their slots).
package hostingpg

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/jmoiron/sqlx"
)

// hostingLock serializes one organization's environment creations and
// slot changes while they are counted (second key:
// hashtext(organization)).
const hostingLock = 7_401_009

var messages = pgerr.Messages{NotFound: "environment not found", Unique: "an environment with this name already exists",
	ForeignKey: "environment not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ hosting.Repository = (*Repository)(nil)

type envRow struct {
	ID        identity.EnvironmentID `db:"id"`
	Name      string                 `db:"name"`
	Version   int                    `db:"version"`
	Spec      []byte                 `db:"spec"`
	CreatedBy string                 `db:"created_by"`
	CreatedAt time.Time              `db:"created_at"`
	UpdatedAt time.Time              `db:"updated_at"`
	Agents    int                    `db:"agents"`
}

// envSelect reads an environment at a version ($3; 0 = current).
const envSelect = `SELECT e.id, e.name, v.version, v.spec, v.created_by, v.created_at, e.updated_at,
	(SELECT count(*) FROM agent_hosting h WHERE h.organization_id = e.organization_id AND h.environment_id = e.id) AS agents
	FROM environments e JOIN environment_versions v
	  ON v.organization_id = e.organization_id AND v.environment_id = e.id`

func (row envRow) environment(org identity.OrganizationID) (hosting.Environment, error) {
	e := hosting.Environment{ID: row.ID, Organization: org, Name: row.Name, Version: row.Version,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Agents: row.Agents}
	if err := json.Unmarshal(row.Spec, &e.Spec); err != nil {
		return e, errx.Wrap(err, "decode environment spec", errx.TypeInternal)
	}
	e.Spec = hosting.Normalize(e.Spec)
	return e, nil
}

func lock(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, hostingLock, org)
	return pgerr.Map(err, "lock hosting")
}

func (r *Repository) CreateEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, in hosting.CreateEnvironment, limit int) (hosting.Environment, error) {
	spec, err := json.Marshal(in.Spec)
	if err != nil {
		return hosting.Environment{}, errx.Wrap(err, "encode environment spec", errx.TypeInternal)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Environment{}, pgerr.Map(err, "begin create environment")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return hosting.Environment{}, err
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM environments WHERE organization_id = $1`, org); err != nil {
		return hosting.Environment{}, pgerr.Map(err, "count environments")
	}
	if n >= limit {
		return hosting.Environment{}, errx.QuotaExceeded("environments", limit,
			"an organization can have at most "+strconv.Itoa(limit)+" environments; delete one first")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environments (id, organization_id, name, version, created_by)
		VALUES ($1, $2, $3, 1, $4)`, id, org, in.Name, in.CreatedBy); err != nil {
		return hosting.Environment{}, pgerr.MapWith(err, "create environment", messages)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environment_versions (organization_id, environment_id, version, spec, created_by)
		VALUES ($1, $2, 1, $3, $4)`, org, id, spec, in.CreatedBy); err != nil {
		return hosting.Environment{}, pgerr.Map(err, "create environment version")
	}
	e, err := environment(ctx, tx, org, id, 0)
	if err != nil {
		return e, err
	}
	return e, pgerr.Map(tx.Commit(), "commit environment")
}

func (r *Repository) SaveEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, in hosting.SaveEnvironment) (hosting.Environment, error) {
	spec, err := json.Marshal(in.Spec)
	if err != nil {
		return hosting.Environment{}, errx.Wrap(err, "encode environment spec", errx.TypeInternal)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Environment{}, pgerr.Map(err, "begin save environment")
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	if err := tx.GetContext(ctx, &current, `SELECT version FROM environments
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, id); err != nil {
		return hosting.Environment{}, pgerr.MapWith(err, "lock environment", messages)
	}
	if current != in.Base {
		return hosting.Environment{}, errx.Conflict("the environment changed meanwhile (version " + strconv.Itoa(current) +
			" is current, the edit started from " + strconv.Itoa(in.Base) + "); reload and apply again")
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO environment_versions (organization_id, environment_id, version, spec, created_by)
		VALUES ($1, $2, $3, $4, $5)`, org, id, next, spec, in.CreatedBy); err != nil {
		return hosting.Environment{}, pgerr.Map(err, "save environment version")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE environments SET version = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2`, org, id, next); err != nil {
		return hosting.Environment{}, pgerr.Map(err, "point environment at version")
	}
	e, err := environment(ctx, tx, org, id, 0)
	if err != nil {
		return e, err
	}
	return e, pgerr.Map(tx.Commit(), "commit environment version")
}

func (r *Repository) DeleteEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin delete environment")
	}
	defer func() { _ = tx.Rollback() }()
	var found int
	if err := tx.GetContext(ctx, &found, `SELECT 1 FROM environments WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, id); err != nil {
		return pgerr.MapWith(err, "lock environment", messages)
	}
	var hosted int
	if err := tx.GetContext(ctx, &hosted, `SELECT count(*) FROM agent_hosting
		WHERE organization_id = $1 AND environment_id = $2`, org, id); err != nil {
		return pgerr.Map(err, "count hosted agents")
	}
	if hosted > 0 {
		return errx.Conflict(strconv.Itoa(hosted) + " agent(s) run on this environment; move or unhost them first")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM environments WHERE organization_id = $1 AND id = $2`, org, id); err != nil {
		return pgerr.Map(err, "delete environment")
	}
	return pgerr.Map(tx.Commit(), "commit delete environment")
}

func environment(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, id identity.EnvironmentID, version int) (hosting.Environment, error) {
	var row envRow
	err := sqlx.GetContext(ctx, q, &row, envSelect+`
		WHERE e.organization_id = $1 AND e.id = $2 AND v.version = CASE WHEN $3 = 0 THEN e.version ELSE $3 END`, org, id, version)
	if err != nil {
		if version > 0 && pgerr.IsNoRows(err) {
			return hosting.Environment{}, errx.NotFound("environment version not found")
		}
		return hosting.Environment{}, pgerr.MapWith(err, "find environment", messages)
	}
	return row.environment(org)
}

func (r *Repository) Environment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, version int) (hosting.Environment, error) {
	return environment(ctx, r.db, org, id, version)
}

func (r *Repository) EnvironmentByName(ctx context.Context, org identity.OrganizationID, name string) (hosting.Environment, error) {
	var row envRow
	if err := r.db.GetContext(ctx, &row, envSelect+`
		WHERE e.organization_id = $1 AND e.name = $2 AND v.version = e.version`, org, name); err != nil {
		return hosting.Environment{}, pgerr.MapWith(err, "find environment by name", messages)
	}
	return row.environment(org)
}

func (r *Repository) Environments(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[hosting.Environment], error) {
	out := query.Paginated[hosting.Environment]{Items: []hosting.Environment{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM environments WHERE organization_id = $1`, org); err != nil {
		return out, pgerr.Map(err, "count environments")
	}
	var rows []envRow
	if err := r.db.SelectContext(ctx, &rows, envSelect+`
		WHERE e.organization_id = $1 AND v.version = e.version ORDER BY e.name LIMIT $2 OFFSET $3`,
		org, page.Limit, page.Offset); err != nil {
		return out, pgerr.Map(err, "list environments")
	}
	for _, row := range rows {
		e, err := row.environment(org)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

func (r *Repository) Versions(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) ([]hosting.Version, error) {
	out := []hosting.Version{}
	if err := r.db.SelectContext(ctx, &out, `SELECT version, created_by, created_at FROM environment_versions
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY version DESC`, org, id); err != nil {
		return out, pgerr.Map(err, "list environment versions")
	}
	if len(out) == 0 {
		return out, errx.NotFound("environment not found")
	}
	return out, nil
}

func (r *Repository) Host(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, in hosting.Host, maxSlots int) (hosting.Hosted, error) {
	slots := in.SlotsOrDefault()
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "begin host agent")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return hosting.Hosted{}, err
	}
	var found int
	err = tx.GetContext(ctx, &found, `SELECT 1 FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL FOR SHARE`, org, agentID)
	if pgerr.IsNoRows(err) {
		return hosting.Hosted{}, errx.NotFound("agent not found")
	}
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "find agent")
	}
	err = tx.GetContext(ctx, &found, `SELECT 1 FROM environments
		WHERE organization_id = $1 AND id = $2 FOR SHARE`, org, in.Environment)
	if err != nil {
		return hosting.Hosted{}, pgerr.MapWith(err, "find environment", messages)
	}
	if maxSlots > 0 {
		var others int
		if err := tx.GetContext(ctx, &others, `SELECT count(*) FROM agent_slots
			WHERE organization_id = $1 AND agent_id <> $2`, org, agentID); err != nil {
			return hosting.Hosted{}, pgerr.Map(err, "count slots")
		}
		if others+slots > maxSlots {
			return hosting.Hosted{}, errx.QuotaExceeded("slots", maxSlots,
				"an organization can run at most "+strconv.Itoa(maxSlots)+" slots across its hosted agents")
		}
	}
	var busy int
	if err := tx.GetContext(ctx, &busy, `SELECT count(*) FROM agent_slots
		WHERE organization_id = $1 AND agent_id = $2 AND idx > $3 AND state <> 'stopped'`, org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "check dropped slots")
	}
	if busy > 0 {
		return hosting.Hosted{}, errx.Conflict("stop the slots above " + strconv.Itoa(slots) + " first")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_hosting (organization_id, agent_id, environment_id, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (organization_id, agent_id) DO UPDATE
		SET environment_id = EXCLUDED.environment_id, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		org, agentID, in.Environment, in.By); err != nil {
		return hosting.Hosted{}, pgerr.MapWith(err, "host agent", messages)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx > $3`,
		org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "drop slots")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_slots (organization_id, agent_id, idx)
		SELECT $1, $2, g FROM generate_series(1, $3::int) g
		ON CONFLICT (organization_id, agent_id, idx) DO NOTHING`, org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "add slots")
	}
	h, err := hosted(ctx, tx, org, agentID)
	if err != nil {
		return h, err
	}
	return h, pgerr.Map(tx.Commit(), "commit host agent")
}

func (r *Repository) Unhost(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin unhost agent")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return err
	}
	var busy int
	if err := tx.GetContext(ctx, &busy, `SELECT count(*) FROM agent_slots
		WHERE organization_id = $1 AND agent_id = $2 AND state <> 'stopped'`, org, agentID); err != nil {
		return pgerr.Map(err, "check slots")
	}
	if busy > 0 {
		return errx.Conflict("stop the agent's slots first")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_hosting WHERE organization_id = $1 AND agent_id = $2`, org, agentID)
	if err != nil {
		return pgerr.Map(err, "unhost agent")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("the agent is not hosted")
	}
	return pgerr.Map(tx.Commit(), "commit unhost agent")
}

type hostedRow struct {
	Agent       identity.AgentID       `db:"agent_id"`
	Environment identity.EnvironmentID `db:"environment_id"`
	UpdatedBy   string                 `db:"updated_by"`
	UpdatedAt   time.Time              `db:"updated_at"`
}

const slotColumns = `agent_id, idx, state, detail, image, environment_version, started_at, active_at, updated_at`

type slotRow struct {
	Agent identity.AgentID `db:"agent_id"`
	hosting.Slot
}

func hosted(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	all, err := hostedWhere(ctx, q, org, &agentID)
	if err != nil {
		return hosting.Hosted{}, err
	}
	if len(all) == 0 {
		return hosting.Hosted{}, errx.NotFound("the agent is not hosted")
	}
	return all[0], nil
}

// hostedWhere reads the organization's hosted agents (one when agentID is
// set), live agents only, with their slots.
func hostedWhere(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, agentID *identity.AgentID) ([]hosting.Hosted, error) {
	var rows []hostedRow
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT h.agent_id, h.environment_id, h.updated_by, h.updated_at
		FROM agent_hosting h JOIN agents a ON a.organization_id = h.organization_id AND a.id = h.agent_id
		WHERE h.organization_id = $1 AND ($2::uuid IS NULL OR h.agent_id = $2) AND a.deleted_at IS NULL
		ORDER BY a.name`, org, agentID); err != nil {
		return nil, pgerr.Map(err, "list hosted agents")
	}
	out := make([]hosting.Hosted, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	var slots []slotRow
	if err := sqlx.SelectContext(ctx, q, &slots, `SELECT `+slotColumns+` FROM agent_slots
		WHERE organization_id = $1 AND ($2::uuid IS NULL OR agent_id = $2) ORDER BY agent_id, idx`, org, agentID); err != nil {
		return nil, pgerr.Map(err, "list slots")
	}
	byAgent := map[identity.AgentID][]hosting.Slot{}
	for _, s := range slots {
		byAgent[s.Agent] = append(byAgent[s.Agent], s.Slot)
	}
	for _, row := range rows {
		h := hosting.Hosted{Agent: row.Agent, Environment: row.Environment, UpdatedBy: row.UpdatedBy,
			UpdatedAt: row.UpdatedAt, Slots: byAgent[row.Agent]}
		if h.Slots == nil {
			h.Slots = []hosting.Slot{}
		}
		out = append(out, h)
	}
	return out, nil
}

func (r *Repository) Hosted(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	return hosted(ctx, r.db, org, agentID)
}

func (r *Repository) HostedAgents(ctx context.Context, org identity.OrganizationID) ([]hosting.Hosted, error) {
	return hostedWhere(ctx, r.db, org, nil)
}
