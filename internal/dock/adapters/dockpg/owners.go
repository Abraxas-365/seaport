package dockpg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/jmoiron/sqlx"
)

// Owners leases docks to processes in dock_owners (migration 00011). The
// database clock decides expiry, so processes need not agree on time.
type Owners struct{ db *sqlx.DB }

func NewOwners(db *sqlx.DB) *Owners { return &Owners{db: db} }

var _ dock.Owners = (*Owners)(nil)

var ownerMessages = pgerr.Messages{ForeignKey: "dock not found", NotFound: "dock not found"}

func (o *Owners) Acquire(ctx context.Context, org identity.OrganizationID, id identity.DockID, process string, ttl time.Duration) (string, error) {
	// Two tries: the row may vanish between the upsert and the read.
	for range 2 {
		var owner string
		err := o.db.GetContext(ctx, &owner, `INSERT INTO dock_owners (dock_id, organization_id, process, expires_at)
			VALUES ($2, $1, $3, now() + make_interval(secs => $4))
			ON CONFLICT (dock_id) DO UPDATE SET process = EXCLUDED.process, expires_at = EXCLUDED.expires_at
			WHERE dock_owners.process = EXCLUDED.process OR dock_owners.expires_at <= now()
			RETURNING process`, org, id, process, ttl.Seconds())
		if err == nil {
			return owner, nil
		}
		if !pgerr.IsNoRows(err) {
			return "", pgerr.MapWith(err, "acquire dock", ownerMessages)
		}
		// A live lease of another process.
		err = o.db.GetContext(ctx, &owner, `SELECT process FROM dock_owners
			WHERE organization_id = $1 AND dock_id = $2 AND expires_at > now()`, org, id)
		if err == nil {
			return owner, nil
		}
		if !pgerr.IsNoRows(err) {
			return "", pgerr.MapWith(err, "find dock owner", ownerMessages)
		}
	}
	return "", pgerr.MapWith(pgerr.ErrNoRows, "acquire dock", ownerMessages)
}

func (o *Owners) Release(ctx context.Context, org identity.OrganizationID, id identity.DockID, process string) error {
	_, err := o.db.ExecContext(ctx, `DELETE FROM dock_owners WHERE organization_id = $1 AND dock_id = $2 AND process = $3`,
		org, id, process)
	return pgerr.Map(err, "release dock")
}

func (o *Owners) Renew(ctx context.Context, process string, docks []identity.DockID, ttl time.Duration) ([]identity.DockID, error) {
	out := []identity.DockID{}
	if len(docks) == 0 {
		return out, nil
	}
	ids := make([]string, len(docks))
	for i, d := range docks {
		ids[i] = d.String()
	}
	err := o.db.SelectContext(ctx, &out, `UPDATE dock_owners SET expires_at = now() + make_interval(secs => $2)
		WHERE process = $1 AND dock_id = ANY($3::uuid[]) AND expires_at > now() RETURNING dock_id`, process, ttl.Seconds(), ids)
	return out, pgerr.Map(err, "renew docks")
}

func (o *Owners) Claim(ctx context.Context, process string, ttl time.Duration, channels []dock.Channel, statuses []dock.Status, limit int) ([]dock.Dock, error) {
	out := []dock.Dock{}
	if len(channels) == 0 || len(statuses) == 0 || limit <= 0 {
		return out, nil
	}
	chs, sts := make([]string, len(channels)), make([]string, len(statuses))
	for i, c := range channels {
		chs[i] = string(c)
	}
	for i, s := range statuses {
		sts[i] = string(s)
	}
	// Concurrent claims of one dock: the upsert's WHERE lets one win.
	err := o.db.SelectContext(ctx, &out, `WITH free AS (
			SELECT d.id, d.organization_id FROM docks d
			LEFT JOIN dock_owners o ON o.dock_id = d.id
			WHERE d.channel = ANY($3::text[]) AND d.status = ANY($4::text[])
			  AND (o.dock_id IS NULL OR o.expires_at <= now())
			ORDER BY d.created_at, d.id LIMIT $5
		), taken AS (
			INSERT INTO dock_owners (dock_id, organization_id, process, expires_at)
			SELECT id, organization_id, $1, now() + make_interval(secs => $2) FROM free
			ON CONFLICT (dock_id) DO UPDATE SET process = EXCLUDED.process, expires_at = EXCLUDED.expires_at
			WHERE dock_owners.expires_at <= now()
			RETURNING dock_id
		)
		SELECT `+columns+` FROM docks WHERE id IN (SELECT dock_id FROM taken) ORDER BY created_at, id`,
		process, ttl.Seconds(), chs, sts, limit)
	return out, pgerr.Map(err, "claim docks")
}

func (o *Owners) ReleaseAll(ctx context.Context, process string) error {
	_, err := o.db.ExecContext(ctx, `DELETE FROM dock_owners WHERE process = $1`, process)
	return pgerr.Map(err, "release docks")
}
