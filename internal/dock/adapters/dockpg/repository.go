// Package dockpg is the Postgres repository of docks.
package dockpg

import (
	"context"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, channel, name, address, status, created_at, updated_at`

var messages = pgerr.Messages{NotFound: "dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ dock.Repository = (*Repository)(nil)

// createLock serializes one organization's dock creations while its docks
// are counted (second key: hashtext(organization)).
const createLock = 7_401_004

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, id identity.DockID, in dock.Create, status dock.Status, limit int) (dock.Dock, error) {
	var d dock.Dock
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return d, pgerr.Map(err, "begin create dock")
	}
	defer func() { _ = tx.Rollback() }()
	if limit > 0 {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
			return d, pgerr.Map(err, "lock docks")
		}
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM docks WHERE organization_id = $1`, org); err != nil {
			return d, pgerr.Map(err, "count docks")
		}
		if n >= limit {
			return d, errx.QuotaExceeded("docks", limit, "an organization can have at most "+strconv.Itoa(limit)+" docks; delete one first")
		}
	}
	if err := tx.GetContext(ctx, &d, `INSERT INTO docks (id, organization_id, channel, name, status)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+columns, id, org, in.Channel, in.Name, status); err != nil {
		return d, pgerr.MapWith(err, "create dock", messages)
	}
	return d, pgerr.Map(tx.Commit(), "commit dock")
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `SELECT `+columns+` FROM docks WHERE organization_id = $1 AND id = $2`, org, id)
	return d, pgerr.MapWith(err, "find dock", messages)
}

func (r *Repository) Get(ctx context.Context, id identity.DockID) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `SELECT `+columns+` FROM docks WHERE id = $1`, id)
	return d, pgerr.MapWith(err, "get dock", messages)
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[dock.Dock], error) {
	out := query.Paginated[dock.Dock]{Items: []dock.Dock{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM docks WHERE organization_id = $1`, org); err != nil {
		return out, pgerr.Map(err, "count docks")
	}
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM docks WHERE organization_id = $1
		ORDER BY created_at, id LIMIT $2 OFFSET $3`, org, page.Limit, page.Offset)
	return out, pgerr.Map(err, "list docks")
}

func (r *Repository) ListByStatus(ctx context.Context, status dock.Status) ([]dock.Dock, error) {
	out := []dock.Dock{}
	err := r.db.SelectContext(ctx, &out, `SELECT `+columns+` FROM docks WHERE status = $1 ORDER BY created_at, id`, status)
	return out, pgerr.Map(err, "list docks by status")
}

func (r *Repository) SetStatus(ctx context.Context, id identity.DockID, status dock.Status) error {
	return r.exec(ctx, "set dock status", `UPDATE docks SET status = $2, updated_at = now() WHERE id = $1`, id, status)
}

func (r *Repository) SetLinked(ctx context.Context, id identity.DockID, address string) error {
	return r.exec(ctx, "set dock linked",
		`UPDATE docks SET status = 'linked', address = $2, updated_at = now() WHERE id = $1`, id, address)
}

func (r *Repository) Update(ctx context.Context, org identity.OrganizationID, id identity.DockID, in dock.Update) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `UPDATE docks SET name = COALESCE($3, name), updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, id, in.Name)
	return d, pgerr.MapWith(err, "update dock", messages)
}

func (r *Repository) Delete(ctx context.Context, org identity.OrganizationID, id identity.DockID) error {
	return r.exec(ctx, "delete dock", `DELETE FROM docks WHERE organization_id = $1 AND id = $2`, org, id)
}

func (r *Repository) exec(ctx context.Context, op, q string, args ...any) error {
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return pgerr.MapWith(err, op, messages)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, op, messages)
	}
	return nil
}
