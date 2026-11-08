// Package dockpg is the Postgres repository of docks.
package dockpg

import (
	"context"

	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/pgerr"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/jmoiron/sqlx"
)

const columns = `id, channel, name, address, status, created_at, updated_at`

var messages = pgerr.Messages{NotFound: "dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ dock.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, id identity.DockID, in dock.Create, status dock.Status) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `INSERT INTO docks (id, channel, name, status) VALUES ($1, $2, $3, $4) RETURNING `+columns,
		id, in.Channel, in.Name, status)
	return d, pgerr.MapWith(err, "create dock", messages)
}

func (r *Repository) Find(ctx context.Context, id identity.DockID) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `SELECT `+columns+` FROM docks WHERE id = $1`, id)
	return d, pgerr.MapWith(err, "find dock", messages)
}

func (r *Repository) List(ctx context.Context, page query.Pagination) (query.Paginated[dock.Dock], error) {
	out := query.Paginated[dock.Dock]{Items: []dock.Dock{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM docks`); err != nil {
		return out, pgerr.Map(err, "count docks")
	}
	err := r.db.SelectContext(ctx, &out.Items,
		`SELECT `+columns+` FROM docks ORDER BY created_at, id LIMIT $1 OFFSET $2`, page.Limit, page.Offset)
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

func (r *Repository) Update(ctx context.Context, id identity.DockID, in dock.Update) (dock.Dock, error) {
	var d dock.Dock
	err := r.db.GetContext(ctx, &d, `UPDATE docks SET name = COALESCE($2, name), updated_at = now()
		WHERE id = $1 RETURNING `+columns, id, in.Name)
	return d, pgerr.MapWith(err, "update dock", messages)
}

func (r *Repository) Delete(ctx context.Context, id identity.DockID) error {
	return r.exec(ctx, "delete dock", `DELETE FROM docks WHERE id = $1`, id)
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
