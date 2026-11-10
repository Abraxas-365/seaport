// Package crewpg is the Postgres repository of crew tokens.
package crewpg

import (
	"context"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/jmoiron/sqlx"
)

// columns are read from crew_tokens t joined with agents a.
const columns = `t.id, t.organization_id, t.name, t.agent_id, a.name AS agent_name, t.hint, t.slot, t.created_by,
	t.created_at, t.expires_at, t.last_used_at`

const from = ` FROM crew_tokens t JOIN agents a ON a.organization_id = t.organization_id AND a.id = t.agent_id `

var messages = pgerr.Messages{NotFound: "crew token not found", ForeignKey: "agent not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ crew.TokenRepository = (*Repository)(nil)

// tokenLock serializes one organization's crew token creations while the
// active ones are counted (second key: hashtext(organization)).
const tokenLock = 7_401_003

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, id identity.CrewTokenID, in crew.CreateToken, hash, hint string, limit int) (crew.Token, error) {
	var t crew.Token
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return t, pgerr.Map(err, "begin create crew token")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, tokenLock, org); err != nil {
		return t, pgerr.Map(err, "lock crew tokens")
	}
	// Slot tokens are not counted: the slots are bounded on their own.
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM crew_tokens WHERE organization_id = $1 AND slot = 0
		AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, org); err != nil {
		return t, pgerr.Map(err, "count active crew tokens")
	}
	if in.Slot == 0 && n >= limit {
		return t, errx.QuotaExceeded("crew_tokens", limit, "an organization can have at most "+strconv.Itoa(limit)+" crew tokens; revoke one first")
	}
	// The agent row is locked FOR SHARE: a concurrent delete (which
	// revokes the agent's tokens) can't miss this one.
	var found int
	err = tx.GetContext(ctx, &found, `SELECT 1 FROM agents WHERE organization_id = $1 AND id = $2
		AND deleted_at IS NULL FOR SHARE`, org, in.Agent)
	if pgerr.IsNoRows(err) {
		return t, errx.NotFound("agent not found")
	}
	if err != nil {
		return t, pgerr.Map(err, "find agent")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO crew_tokens (id, organization_id, name, agent_id, token_hash, hint, created_by, expires_at, slot)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, org, in.Name, in.Agent, hash, hint, in.CreatedBy, in.ExpiresAt, in.Slot); err != nil {
		return t, pgerr.MapWith(err, "create crew token", messages)
	}
	if err := tx.GetContext(ctx, &t, `SELECT `+columns+from+`WHERE t.organization_id = $1 AND t.id = $2`, org, id); err != nil {
		return t, pgerr.MapWith(err, "read crew token", messages)
	}
	return t, pgerr.Map(tx.Commit(), "commit crew token")
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, f crew.TokenFilter, page query.Pagination) (query.Paginated[crew.Token], error) {
	out := query.Paginated[crew.Token]{Items: []crew.Token{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	var agent any // nil: every agent
	if f.Agent != nil {
		agent = *f.Agent
	}
	// Slot tokens belong to the slots' runs, not to people: not listed.
	const where = `WHERE t.organization_id = $1 AND t.revoked_at IS NULL AND t.slot = 0 AND ($2::uuid IS NULL OR t.agent_id = $2)`
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*)`+from+where, org, agent); err != nil {
		return out, pgerr.Map(err, "count crew tokens")
	}
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+from+where+`
		ORDER BY t.created_at, t.id LIMIT $3 OFFSET $4`, org, agent, page.Limit, page.Offset)
	return out, pgerr.Map(err, "list crew tokens")
}

func (r *Repository) Revoke(ctx context.Context, org identity.OrganizationID, id identity.CrewTokenID) error {
	res, err := r.db.ExecContext(ctx, `UPDATE crew_tokens SET revoked_at = now()
		WHERE organization_id = $1 AND id = $2 AND revoked_at IS NULL`, org, id)
	if err != nil {
		return pgerr.Map(err, "revoke crew token")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "revoke crew token", messages)
	}
	return nil
}

func (r *Repository) FindByHash(ctx context.Context, hash string) (crew.Token, error) {
	var t crew.Token
	err := r.db.GetContext(ctx, &t, `SELECT `+columns+from+`WHERE t.token_hash = $1 AND t.revoked_at IS NULL`, hash)
	return t, pgerr.MapWith(err, "find crew token", messages)
}

func (r *Repository) Touch(ctx context.Context, id identity.CrewTokenID, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE crew_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return pgerr.Map(err, "touch crew token")
}
