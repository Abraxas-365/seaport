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
	"strconv"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, name, description, created_by, created_at, updated_at`

// createLock serializes one organization's agent creations while its
// agents are counted (second key: hashtext(organization)).
const createLock = 7_401_008

var messages = pgerr.Messages{NotFound: "agent not found", Unique: "an agent with this name already exists",
	ForeignKey: "dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

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
