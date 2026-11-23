// Package agentpg is the Postgres repository of agents and their dock
// assignments.
//
// Deleting or unassigning an agent also holds the ships waiting for it and
// (delete) revokes its crew tokens, in the same transaction: nothing may
// stay queued for an agent no dock routes to. shippg.CreateInbound queues
// a ship only while its agent's assignment row exists (read FOR SHARE), so
// an arrival racing an unassign is either held by it or seen by it.
package agentpg

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, name, description, created_by, created_at, updated_at, profile_version`

// takeovers is agents.takeovers (jsonb) in Go.
type takeovers agent.Takeovers

func (l *takeovers) Scan(src any) error {
	*l = takeovers{}
	var raw []byte
	switch v := src.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errx.Internal("takeovers: unexpected column type")
	}
	if len(raw) == 0 {
		return nil
	}
	var out []agent.Takeover
	if err := json.Unmarshal(raw, &out); err != nil {
		return errx.Wrap(err, "decode takeovers", errx.TypeInternal)
	}
	if out != nil {
		*l = out
	}
	return nil
}

func (l takeovers) Value() (driver.Value, error) {
	if l == nil {
		l = takeovers{}
	}
	b, err := json.Marshal([]agent.Takeover(l))
	if err != nil {
		return nil, errx.Wrap(err, "encode takeovers", errx.TypeInternal)
	}
	return string(b), nil
}

// createLock serializes one organization's agent creations while its
// agents are counted (second key: hashtext(organization)).
const createLock = 7_401_008

var messages = pgerr.Messages{NotFound: "agent not found", Unique: "an agent with this name already exists",
	ForeignKey: "dock not found"}

type Repository struct {
	db *sqlx.DB
	// sealer seals agent secrets; nil when the port has no secret key
	// (secrets are refused).
	sealer *seal.Sealer
}

func New(db *sqlx.DB, sealer *seal.Sealer) *Repository { return &Repository{db: db, sealer: sealer} }

var _ agent.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, id identity.AgentID, in agent.Create, limit int) (agent.Agent, error) {
	var a agent.Agent
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return a, pgerr.Map(err, "begin create agent")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
		return a, pgerr.Map(err, "lock agents")
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM agents WHERE organization_id = $1 AND deleted_at IS NULL`, org); err != nil {
		return a, pgerr.Map(err, "count agents")
	}
	if n >= limit {
		return a, errx.QuotaExceeded("agents", limit, "an organization can have at most "+strconv.Itoa(limit)+" agents; delete one first")
	}
	if err := tx.GetContext(ctx, &a, `INSERT INTO agents (id, organization_id, name, description, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+columns, id, org, in.Name, in.Description, in.CreatedBy); err != nil {
		return a, pgerr.MapWith(err, "create agent", messages)
	}
	if err := tx.Commit(); err != nil {
		return a, pgerr.Map(err, "commit agent")
	}
	a.Docks = []agent.Assignment{}
	return a, nil
}

// Clone: Create's lock, count and insert, then the source's current config
// as version 1 and, with in.Secrets, its secrets resealed for the clone,
// all in one transaction. The config is copied as it is, with no check
// that the secrets it names exist (SaveProfile's rule): a clone without
// secrets keeps naming them, and what resolves them answers a Business
// error naming the missing one. The source's row is locked (shared) so a
// concurrent SaveProfile (FOR UPDATE) doesn't tear the copy. Plaintext
// exists only inside this method, between Open and Seal.
func (r *Repository) Clone(ctx context.Context, org identity.OrganizationID, source, id identity.AgentID, in agent.Clone, limit int) (agent.Agent, error) {
	var a agent.Agent
	if in.Secrets && r.sealer == nil {
		return a, errx.Business("this port has no secret key: set SEAPORT_SECRET_KEY (or SEAPORT_SECRET_KEY_FILE) to copy agent secrets")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return a, pgerr.Map(err, "begin clone agent")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
		return a, pgerr.Map(err, "lock agents")
	}
	var src struct {
		Description string `db:"description"`
		Version     int    `db:"profile_version"`
	}
	if err := tx.GetContext(ctx, &src, `SELECT description, profile_version FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL FOR SHARE`, org, source); err != nil {
		return a, pgerr.MapWith(err, "find agent to clone", messages)
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM agents WHERE organization_id = $1 AND deleted_at IS NULL`, org); err != nil {
		return a, pgerr.Map(err, "count agents")
	}
	if n >= limit {
		return a, errx.QuotaExceeded("agents", limit, "an organization can have at most "+strconv.Itoa(limit)+" agents; delete one first")
	}
	description := src.Description
	if in.Description != nil {
		description = *in.Description
	}
	if err := tx.GetContext(ctx, &a, `INSERT INTO agents (id, organization_id, name, description, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+columns, id, org, in.Name, description, in.CreatedBy); err != nil {
		return a, pgerr.MapWith(err, "create agent", messages)
	}
	if src.Version > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_profiles
			(organization_id, agent_id, version, instructions, skills, mcp_servers, approve, model, effort, browser, browser_assist, browser_detect, browser_ignore_hosts,
			 harness, machine, machine_digest, machine_secrets, home, created_by, alarms)
			SELECT organization_id, $3, 1, instructions, skills, mcp_servers, approve, model, effort, browser, browser_assist, browser_detect, browser_ignore_hosts,
			 harness, machine, machine_digest, machine_secrets, home, $5, alarms
			FROM agent_profiles WHERE organization_id = $1 AND agent_id = $2 AND version = $4`,
			org, source, id, src.Version, in.CreatedBy); err != nil {
			return a, pgerr.MapWith(err, "copy profile", profileMessages)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET profile_version = 1 WHERE organization_id = $1 AND id = $2`, org, id); err != nil {
			return a, pgerr.Map(err, "point clone at profile")
		}
		a.ProfileVersion = 1
	}
	if in.Secrets {
		var rows []struct {
			Name   string `db:"name"`
			Sealed []byte `db:"sealed"`
			Env    bool   `db:"env"`
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT name, sealed, env FROM agent_secrets
			WHERE organization_id = $1 AND agent_id = $2 ORDER BY name`, org, source); err != nil {
			return a, pgerr.Map(err, "read secrets to clone")
		}
		for _, row := range rows {
			plain, err := r.sealer.Open(secretPurpose(org, source, row.Name), row.Sealed)
			if err != nil {
				return a, errx.Internal("agent secret " + row.Name + " doesn't open with this port's secret key (was the key changed?)")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_secrets (organization_id, agent_id, name, sealed, env)
				VALUES ($1, $2, $3, $4, $5)`, org, id, row.Name, r.sealer.Seal(secretPurpose(org, id, row.Name), plain), row.Env); err != nil {
				return a, pgerr.Map(err, "copy secret")
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return a, pgerr.Map(err, "commit clone agent")
	}
	a.Docks, a.Takeovers = []agent.Assignment{}, agent.Takeovers{}
	return a, nil
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Agent, error) {
	var a agent.Agent
	err := r.db.GetContext(ctx, &a, `SELECT `+columns+` FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, org, id)
	if err != nil {
		return a, pgerr.MapWith(err, "find agent", messages)
	}
	return a, r.withDocks(ctx, org, []*agent.Agent{&a})
}

func (r *Repository) FindByName(ctx context.Context, org identity.OrganizationID, name string) (agent.Agent, error) {
	var a agent.Agent
	err := r.db.GetContext(ctx, &a, `SELECT `+columns+` FROM agents
		WHERE organization_id = $1 AND name = $2 AND deleted_at IS NULL`, org, name)
	if err != nil {
		return a, pgerr.MapWith(err, "find agent by name", messages)
	}
	return a, r.withDocks(ctx, org, []*agent.Agent{&a})
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[agent.Agent], error) {
	out := query.Paginated[agent.Agent]{Items: []agent.Agent{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total,
		`SELECT count(*) FROM agents WHERE organization_id = $1 AND deleted_at IS NULL`, org); err != nil {
		return out, pgerr.Map(err, "count agents")
	}
	if err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM agents
		WHERE organization_id = $1 AND deleted_at IS NULL ORDER BY name LIMIT $2 OFFSET $3`,
		org, page.Limit, page.Offset); err != nil {
		return out, pgerr.Map(err, "list agents")
	}
	ptrs := make([]*agent.Agent, len(out.Items))
	for i := range out.Items {
		ptrs[i] = &out.Items[i]
	}
	return out, r.withDocks(ctx, org, ptrs)
}

// withDocks fills each agent's assignments.
func (r *Repository) withDocks(ctx context.Context, org identity.OrganizationID, as []*agent.Agent) error {
	if len(as) == 0 {
		return nil
	}
	ids := make([]string, len(as))
	byID := make(map[identity.AgentID]*agent.Agent, len(as))
	for i, a := range as {
		ids[i] = a.ID.String()
		a.Docks = []agent.Assignment{}
		byID[a.ID] = a
	}
	var rows []struct {
		Agent identity.AgentID `db:"agent_id"`
		agent.Assignment
	}
	if err := r.db.SelectContext(ctx, &rows, `SELECT agent_id, dock_id, is_default FROM dock_agents
		WHERE organization_id = $1 AND agent_id = ANY($2::uuid[]) ORDER BY created_at, dock_id`, org, ids); err != nil {
		return pgerr.Map(err, "list agent docks")
	}
	for _, row := range rows {
		if a := byID[row.Agent]; a != nil {
			a.Docks = append(a.Docks, row.Assignment)
		}
	}
	var open []struct {
		Agent     identity.AgentID `db:"id"`
		Takeovers takeovers        `db:"takeovers"`
	}
	if err := r.db.SelectContext(ctx, &open, `SELECT id, takeovers FROM agents
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`, org, ids); err != nil {
		return pgerr.Map(err, "list takeovers")
	}
	for _, row := range open {
		if a := byID[row.Agent]; a != nil {
			a.Takeovers = agent.Takeovers(row.Takeovers)
		}
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, org identity.OrganizationID, id identity.AgentID, in agent.Update) (agent.Agent, error) {
	var a agent.Agent
	err := r.db.GetContext(ctx, &a, `UPDATE agents SET name = COALESCE($3, name),
		description = COALESCE($4, description), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL RETURNING `+columns, org, id, in.Name, in.Description)
	if err != nil {
		return a, pgerr.MapWith(err, "update agent", messages)
	}
	return a, r.withDocks(ctx, org, []*agent.Agent{&a})
}

func (r *Repository) SetTakeovers(ctx context.Context, org identity.OrganizationID, id identity.AgentID, change func(agent.Takeovers) agent.Takeovers) (agent.Agent, error) {
	var a agent.Agent
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return a, pgerr.Map(err, "begin takeovers")
	}
	defer func() { _ = tx.Rollback() }()
	var current takeovers
	if err := tx.GetContext(ctx, &current, `SELECT takeovers FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL FOR UPDATE`, org, id); err != nil {
		return a, pgerr.MapWith(err, "lock agent", messages)
	}
	if err := tx.GetContext(ctx, &a, `UPDATE agents SET takeovers = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, id, takeovers(change(agent.Takeovers(current)))); err != nil {
		return a, pgerr.MapWith(err, "set takeovers", messages)
	}
	if err := tx.Commit(); err != nil {
		return a, pgerr.Map(err, "commit takeovers")
	}
	return a, r.withDocks(ctx, org, []*agent.Agent{&a})
}

// StaleTakeovers: agents with a takeover whose `at` is at or before
// cutoff (the jsonb is small: at most agent.MaxTakeovers per agent).
func (r *Repository) StaleTakeovers(ctx context.Context, cutoff time.Time) ([]agent.AgentRef, error) {
	refs := []agent.AgentRef{}
	err := r.db.SelectContext(ctx, &refs, `SELECT organization_id, id FROM agents
		WHERE deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(takeovers) t
			WHERE (t->>'at')::timestamptz <= $1)
		ORDER BY organization_id, id`, cutoff)
	if err != nil {
		return nil, pgerr.Map(err, "list stale takeovers")
	}
	return refs, nil
}

// holdWaiting holds the inbound ships waiting for the agent (of one dock,
// or of all when dock is zero), released from any delivery in flight: the
// agent's connection keeps working what it pushed already but its ack
// finds nothing claimed.
const holdWaiting = `UPDATE ships s SET status = 'held', hold_reason = $4,
	  delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
	WHERE s.organization_id = $1 AND s.agent_id = $2 AND s.status IN ('queued', 'claimed')
	  AND ($3::uuid IS NULL OR s.conversation_id IN
	       (SELECT c.id FROM conversations c WHERE c.organization_id = $1 AND c.dock_id = $3))`

func (r *Repository) Delete(ctx context.Context, org identity.OrganizationID, id identity.AgentID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin delete agent")
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET deleted_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, org, id)
	if err != nil {
		return pgerr.Map(err, "delete agent")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "delete agent", messages)
	}
	// Assignments first: arrivals waiting on their rows (FOR SHARE) then
	// find none and hold their ship.
	if _, err := tx.ExecContext(ctx, `DELETE FROM dock_agents WHERE organization_id = $1 AND agent_id = $2`, org, id); err != nil {
		return pgerr.Map(err, "unassign deleted agent")
	}
	if _, err := tx.ExecContext(ctx, holdWaiting, org, id, nil, agent.HoldAgentGone); err != nil {
		return pgerr.Map(err, "hold ships of deleted agent")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crew_tokens SET revoked_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND revoked_at IS NULL`, org, id); err != nil {
		return pgerr.Map(err, "revoke tokens of deleted agent")
	}
	// Its secrets go with it (profiles stay: they are history), and so
	// does its hosting (slots cascade).
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_secrets WHERE organization_id = $1 AND agent_id = $2`, org, id); err != nil {
		return pgerr.Map(err, "delete secrets of deleted agent")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_hosting WHERE organization_id = $1 AND agent_id = $2`, org, id); err != nil {
		return pgerr.Map(err, "unhost deleted agent")
	}
	return pgerr.Map(tx.Commit(), "commit delete agent")
}

func (r *Repository) Assign(ctx context.Context, org identity.OrganizationID, in agent.Assign) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin assign agent")
	}
	defer func() { _ = tx.Rollback() }()
	var found int
	err = tx.GetContext(ctx, &found, `SELECT 1 FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL FOR SHARE`, org, in.Agent)
	if pgerr.IsNoRows(err) {
		return errx.NotFound("agent not found")
	}
	if err != nil {
		return pgerr.Map(err, "find agent")
	}
	if in.Default {
		// One default per dock: the unique index would refuse a second.
		if _, err := tx.ExecContext(ctx, `UPDATE dock_agents SET is_default = false
			WHERE organization_id = $1 AND dock_id = $2 AND agent_id <> $3 AND is_default`, org, in.Dock, in.Agent); err != nil {
			return pgerr.Map(err, "demote dock default")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dock_agents (organization_id, dock_id, agent_id, is_default)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (organization_id, dock_id, agent_id) DO UPDATE SET is_default = EXCLUDED.is_default`,
		org, in.Dock, in.Agent, in.Default); err != nil {
		return pgerr.MapWith(err, "assign agent", messages)
	}
	return pgerr.Map(tx.Commit(), "commit assign agent")
}

func (r *Repository) Unassign(ctx context.Context, org identity.OrganizationID, dock identity.DockID, id identity.AgentID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin unassign agent")
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM dock_agents WHERE organization_id = $1 AND dock_id = $2 AND agent_id = $3`, org, dock, id)
	if err != nil {
		return pgerr.Map(err, "unassign agent")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("the agent is not assigned to this dock")
	}
	if _, err := tx.ExecContext(ctx, holdWaiting, org, id, dock, agent.HoldAgentGone); err != nil {
		return pgerr.Map(err, "hold ships of unassigned agent")
	}
	return pgerr.Map(tx.Commit(), "commit unassign agent")
}

func (r *Repository) Team(ctx context.Context, org identity.OrganizationID, dock identity.DockID) ([]agent.Member, error) {
	out := []agent.Member{}
	var found int
	err := r.db.GetContext(ctx, &found, `SELECT 1 FROM docks WHERE organization_id = $1 AND id = $2`, org, dock)
	if pgerr.IsNoRows(err) {
		return out, errx.NotFound("dock not found")
	}
	if err != nil {
		return out, pgerr.Map(err, "find dock")
	}
	err = r.db.SelectContext(ctx, &out, `SELECT d.agent_id, a.name, d.is_default FROM dock_agents d
		JOIN agents a ON a.organization_id = d.organization_id AND a.id = d.agent_id
		WHERE d.organization_id = $1 AND d.dock_id = $2 ORDER BY d.created_at, a.name`, org, dock)
	return out, pgerr.Map(err, "dock team")
}
