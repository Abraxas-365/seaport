package dockpg

import (
	"context"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

const destinationColumns = `organization_id, dock_id, name, target, title, created_at`

var destinationMessages = pgerr.Messages{NotFound: "destination not found"}

// PutDestination counts and inserts under the dock's row lock (FOR UPDATE
// on the dock, which also proves it is the organization's), so concurrent
// puts can't overshoot the limit.
func (r *Repository) PutDestination(ctx context.Context, org identity.OrganizationID, in dock.AddDestination, limit int) (dock.Destination, error) {
	var d dock.Destination
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return d, pgerr.Map(err, "begin put destination")
	}
	defer func() { _ = tx.Rollback() }()
	var dockID identity.DockID
	if err := tx.GetContext(ctx, &dockID, `SELECT id FROM docks WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, in.Dock); err != nil {
		return d, pgerr.MapWith(err, "lock dock", messages)
	}
	if limit > 0 {
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM dock_destinations WHERE organization_id = $1 AND dock_id = $2 AND name <> $3`,
			org, in.Dock, in.Name); err != nil {
			return d, pgerr.Map(err, "count destinations")
		}
		if n >= limit {
			return d, errx.QuotaExceeded("destinations", limit, "a dock can have at most "+strconv.Itoa(limit)+" destinations; remove one first")
		}
	}
	err = tx.GetContext(ctx, &d, `INSERT INTO dock_destinations (organization_id, dock_id, name, target, title)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (organization_id, dock_id, name) DO UPDATE SET target = EXCLUDED.target, title = EXCLUDED.title
		RETURNING `+destinationColumns, org, in.Dock, in.Name, in.Target, in.Title)
	if err != nil {
		return d, pgerr.MapWith(err, "put destination", messages)
	}
	return d, pgerr.Map(tx.Commit(), "commit destination")
}

func (r *Repository) RemoveDestination(ctx context.Context, org identity.OrganizationID, id identity.DockID, name string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM dock_destinations WHERE organization_id = $1 AND dock_id = $2 AND name = $3`, org, id, name)
	if err != nil {
		return pgerr.Map(err, "remove destination")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "remove destination", destinationMessages)
	}
	return nil
}

func (r *Repository) Destinations(ctx context.Context, org identity.OrganizationID, id identity.DockID) ([]dock.Destination, error) {
	out := []dock.Destination{}
	err := r.db.SelectContext(ctx, &out, `SELECT `+destinationColumns+` FROM dock_destinations
		WHERE organization_id = $1 AND dock_id = $2 ORDER BY name`, org, id)
	return out, pgerr.Map(err, "list destinations")
}

func (r *Repository) Destination(ctx context.Context, org identity.OrganizationID, id identity.DockID, name string) (dock.Destination, error) {
	var d dock.Destination
	err := r.db.GetContext(ctx, &d, `SELECT `+destinationColumns+` FROM dock_destinations
		WHERE organization_id = $1 AND dock_id = $2 AND name = $3`, org, id, name)
	return d, pgerr.MapWith(err, "find destination", destinationMessages)
}
