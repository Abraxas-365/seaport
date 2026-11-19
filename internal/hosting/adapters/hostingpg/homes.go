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
)

// Homes: uploaded harness homes (hosting_homes, migration 00021). A home
// is counted and inserted under hostingLock; agent configs name them
// (agent_profiles.home, FK).

var homeMessages = pgerr.Messages{NotFound: "home not found"}

type homeRow struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Digest       string                  `db:"digest"`
	Harness      string                  `db:"harness"`
	Files        []byte                  `db:"files"`
	Size         int64                   `db:"size"`
	Key          string                  `db:"blob_key"`
	CreatedBy    string                  `db:"created_by"`
	CreatedAt    time.Time               `db:"created_at"`
}

const homeSelect = `SELECT organization_id, digest, harness, files, size, blob_key, created_by, created_at FROM hosting_homes`

func (row homeRow) home() (hosting.Home, error) {
	h := hosting.Home{Organization: row.Organization, Digest: row.Digest, Harness: row.Harness, Size: row.Size,
		Key: row.Key, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	if err := json.Unmarshal(row.Files, &h.Files); err != nil {
		return h, errx.Wrap(err, "decode home files", errx.TypeInternal)
	}
	return h, nil
}

func (r *Repository) AddHome(ctx context.Context, org identity.OrganizationID, h hosting.Home, limit int) (hosting.Home, bool, error) {
	files, err := json.Marshal(h.Files)
	if err != nil {
		return hosting.Home{}, false, errx.Wrap(err, "encode home files", errx.TypeInternal)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Home{}, false, pgerr.Map(err, "begin add home")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return hosting.Home{}, false, err
	}
	var row homeRow
	err = tx.GetContext(ctx, &row, `UPDATE hosting_homes SET touched_at = now()
		WHERE organization_id = $1 AND digest = $2
		RETURNING organization_id, digest, harness, files, size, blob_key, created_by, created_at`, org, h.Digest)
	switch {
	case err == nil:
		out, err := row.home()
		if err != nil {
			return out, false, err
		}
		return out, false, pgerr.Map(tx.Commit(), "commit add home")
	case !pgerr.IsNoRows(err):
		return hosting.Home{}, false, pgerr.Map(err, "touch home")
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM hosting_homes WHERE organization_id = $1`, org); err != nil {
		return hosting.Home{}, false, pgerr.Map(err, "count homes")
	}
	if n >= limit {
		return hosting.Home{}, false, errx.QuotaExceeded("homes", limit,
			"an organization can keep at most "+strconv.Itoa(limit)+" homes; unused ones are deleted after a day")
	}
	if err := tx.GetContext(ctx, &row, `INSERT INTO hosting_homes (organization_id, digest, harness, files, size, blob_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING organization_id, digest, harness, files, size, blob_key, created_by, created_at`,
		org, h.Digest, h.Harness, files, h.Size, h.Key, h.CreatedBy); err != nil {
		return hosting.Home{}, false, pgerr.Map(err, "add home")
	}
	// Its blob was staged in the trash before the Put: it's kept now.
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_trash WHERE blob_key = $1`, h.Key); err != nil {
		return hosting.Home{}, false, pgerr.Map(err, "keep home blob")
	}
	out, err := row.home()
	if err != nil {
		return out, false, err
	}
	return out, true, pgerr.Map(tx.Commit(), "commit add home")
}

func (r *Repository) StageHomeBlob(ctx context.Context, org identity.OrganizationID, key string, due time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO blob_trash (blob_key, organization_id, due_at) VALUES ($1, $2, $3)
		ON CONFLICT (blob_key) DO UPDATE SET due_at = excluded.due_at`, key, org, due)
	return pgerr.Map(err, "stage home blob")
}

func (r *Repository) Home(ctx context.Context, org identity.OrganizationID, digest string) (hosting.Home, error) {
	var row homeRow
	if err := r.db.GetContext(ctx, &row, homeSelect+` WHERE organization_id = $1 AND digest = $2`, org, digest); err != nil {
		return hosting.Home{}, pgerr.MapWith(err, "find home", homeMessages)
	}
	return row.home()
}

// unnamed: no agent config version names the home.
const unnamed = `NOT EXISTS (SELECT 1 FROM agent_profiles p
	WHERE p.organization_id = hosting_homes.organization_id AND p.home = hosting_homes.digest)`

func (r *Repository) StaleHomes(ctx context.Context, before time.Time, limit int) ([]hosting.Home, error) {
	var rows []homeRow
	if err := r.db.SelectContext(ctx, &rows, homeSelect+` WHERE touched_at < $1 AND `+unnamed+`
		ORDER BY touched_at LIMIT $2`, before, limit); err != nil {
		return nil, pgerr.Map(err, "list stale homes")
	}
	out := make([]hosting.Home, 0, len(rows))
	for _, row := range rows {
		h, err := row.home()
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// DeleteHome deletes the home under hostingLock, so a save naming it (it
// takes the same lock) and the sweep never cross.
func (r *Repository) DeleteHome(ctx context.Context, org identity.OrganizationID, digest string, before time.Time) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin delete home")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosting_homes
		WHERE organization_id = $1 AND digest = $2 AND touched_at < $3 AND `+unnamed, org, digest, before); err != nil {
		return pgerr.Map(err, "delete home")
	}
	return pgerr.Map(tx.Commit(), "commit delete home")
}

// TouchHome touches a home a config is about to name, under hostingLock
// (the sweep takes it too), so the sweep doesn't delete it meanwhile; the
// FK on agent_profiles.home keeps a named one regardless.
func (r *Repository) TouchHome(ctx context.Context, org identity.OrganizationID, digest string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin touch home")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE hosting_homes SET touched_at = now() WHERE organization_id = $1 AND digest = $2`,
		org, digest)
	if err != nil {
		return pgerr.Map(err, "touch home")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("home not found")
	}
	return pgerr.Map(tx.Commit(), "commit touch home")
}
