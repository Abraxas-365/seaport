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

// columns reads a dock; the tool switches land in Dock.Tools (sqlx's
// "tools.<field>" names).
const columns = `id, organization_id, channel, name, address, status, replies,
	tool_history AS "tools.history", tool_conversations AS "tools.conversations",
	tool_attachments AS "tools.attachments", mirror_dock, mirror_destination, created_at, updated_at`

// row is a dock as stored: the mirror is two nullable columns.
type row struct {
	dock.Dock
	MirrorDock *identity.DockID `db:"mirror_dock"`
	MirrorDest *string          `db:"mirror_destination"`
}

func (r row) dock() dock.Dock {
	d := r.Dock
	if r.MirrorDock != nil && r.MirrorDest != nil {
		d.Mirror = &dock.Mirror{Dock: *r.MirrorDock, Destination: *r.MirrorDest}
	}
	return d
}

func docks(rows []row) []dock.Dock {
	out := make([]dock.Dock, len(rows))
	for i, r := range rows {
		out[i] = r.dock()
	}
	return out
}

var messages = pgerr.Messages{NotFound: "dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ dock.Repository = (*Repository)(nil)

// createLock serializes one organization's dock creations while its docks
// are counted (second key: hashtext(organization)).
const createLock = 7_401_004

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, id identity.DockID, in dock.Create, status dock.Status, limit int) (dock.Dock, error) {
	var d row
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return dock.Dock{}, pgerr.Map(err, "begin create dock")
	}
	defer func() { _ = tx.Rollback() }()
	if limit > 0 {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
			return dock.Dock{}, pgerr.Map(err, "lock docks")
		}
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM docks WHERE organization_id = $1`, org); err != nil {
			return dock.Dock{}, pgerr.Map(err, "count docks")
		}
		if n >= limit {
			return dock.Dock{}, errx.QuotaExceeded("docks", limit, "an organization can have at most "+strconv.Itoa(limit)+" docks; delete one first")
		}
	}
	if err := tx.GetContext(ctx, &d, `INSERT INTO docks (id, organization_id, channel, name, status)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+columns, id, org, in.Channel, in.Name, status); err != nil {
		return dock.Dock{}, pgerr.MapWith(err, "create dock", messages)
	}
	return d.dock(), pgerr.Map(tx.Commit(), "commit dock")
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	var d row
	err := r.db.GetContext(ctx, &d, `SELECT `+columns+` FROM docks WHERE organization_id = $1 AND id = $2`, org, id)
	return d.dock(), pgerr.MapWith(err, "find dock", messages)
}

func (r *Repository) Get(ctx context.Context, id identity.DockID) (dock.Dock, error) {
	var d row
	err := r.db.GetContext(ctx, &d, `SELECT `+columns+` FROM docks WHERE id = $1`, id)
	return d.dock(), pgerr.MapWith(err, "get dock", messages)
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[dock.Dock], error) {
	out := query.Paginated[dock.Dock]{Items: []dock.Dock{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM docks WHERE organization_id = $1`, org); err != nil {
		return out, pgerr.Map(err, "count docks")
	}
	var rows []row
	err := r.db.SelectContext(ctx, &rows, `SELECT `+columns+` FROM docks WHERE organization_id = $1
		ORDER BY created_at, id LIMIT $2 OFFSET $3`, org, page.Limit, page.Offset)
	if err == nil {
		out.Items = docks(rows)
	}
	return out, pgerr.Map(err, "list docks")
}

func (r *Repository) ListByStatus(ctx context.Context, status dock.Status) ([]dock.Dock, error) {
	var rows []row
	err := r.db.SelectContext(ctx, &rows, `SELECT `+columns+` FROM docks WHERE status = $1 ORDER BY created_at, id`, status)
	return docks(rows), pgerr.Map(err, "list docks by status")
}

func (r *Repository) SetStatus(ctx context.Context, id identity.DockID, status dock.Status) error {
	return r.exec(ctx, "set dock status", `UPDATE docks SET status = $2, updated_at = now() WHERE id = $1`, id, status)
}

func (r *Repository) SetLinked(ctx context.Context, id identity.DockID, address string) error {
	return r.exec(ctx, "set dock linked",
		`UPDATE docks SET status = 'linked', address = $2, updated_at = now() WHERE id = $1`, id, address)
}

func (r *Repository) Update(ctx context.Context, org identity.OrganizationID, id identity.DockID, in dock.Update) (dock.Dock, error) {
	var d row
	var t dock.ToolsPatch
	if in.Tools != nil {
		t = *in.Tools
	}
	err := r.db.GetContext(ctx, &d, `UPDATE docks SET name = COALESCE($3, name), replies = COALESCE($4, replies),
		tool_history = COALESCE($5, tool_history), tool_conversations = COALESCE($6, tool_conversations),
		tool_attachments = COALESCE($7, tool_attachments), updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, id, in.Name, in.Replies,
		t.History, t.Conversations, t.Attachments)
	// The only check an update can break is docks_mirror_trigger_only
	// (replies turned on while a mirror was set meanwhile).
	return d.dock(), pgerr.MapWith(err, "update dock", pgerr.Messages{NotFound: messages.NotFound,
		Check: "this dock has a mirror: remove the mirror first, then turn its replies on"})
}

func (r *Repository) SetMirror(ctx context.Context, org identity.OrganizationID, id identity.DockID, m *dock.Mirror) (dock.Dock, error) {
	var mdock *identity.DockID
	var mdest *string
	if m != nil {
		mdock, mdest = &m.Dock, &m.Destination
	}
	var d row
	err := r.db.GetContext(ctx, &d, `UPDATE docks SET mirror_dock = $3, mirror_destination = $4, updated_at = now()
		WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, id, mdock, mdest)
	return d.dock(), pgerr.MapWith(err, "set dock mirror", pgerr.Messages{NotFound: "dock not found", ForeignKey: "that destination does not exist",
		Check: "only a dock that doesn't reply can have a mirror, and not itself"})
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
