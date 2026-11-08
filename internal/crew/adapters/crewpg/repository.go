// Package crewpg is the Postgres repository of crew tokens.
package crewpg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, name, hint, created_by, created_at, expires_at, last_used_at`

var messages = pgerr.Messages{NotFound: "crew token not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ crew.TokenRepository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, id identity.CrewTokenID, in crew.CreateToken, hash, hint string) (crew.Token, error) {
	var t crew.Token
	err := r.db.GetContext(ctx, &t, `INSERT INTO crew_tokens (id, organization_id, name, token_hash, hint, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+columns, id, org, in.Name, hash, hint, in.CreatedBy, in.ExpiresAt)
	return t, pgerr.MapWith(err, "create crew token", messages)
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[crew.Token], error) {
	out := query.Paginated[crew.Token]{Items: []crew.Token{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total,
		`SELECT count(*) FROM crew_tokens WHERE organization_id = $1 AND revoked_at IS NULL`, org); err != nil {
		return out, pgerr.Map(err, "count crew tokens")
	}
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM crew_tokens
		WHERE organization_id = $1 AND revoked_at IS NULL ORDER BY created_at, id LIMIT $2 OFFSET $3`, org, page.Limit, page.Offset)
	return out, pgerr.Map(err, "list crew tokens")
}

func (r *Repository) CountActive(ctx context.Context, org identity.OrganizationID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM crew_tokens WHERE organization_id = $1
		AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, org)
	return n, pgerr.Map(err, "count active crew tokens")
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
	err := r.db.GetContext(ctx, &t, `SELECT `+columns+` FROM crew_tokens WHERE token_hash = $1 AND revoked_at IS NULL`, hash)
	return t, pgerr.MapWith(err, "find crew token", messages)
}

func (r *Repository) Touch(ctx context.Context, id identity.CrewTokenID, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE crew_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return pgerr.Map(err, "touch crew token")
}
