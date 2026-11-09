package hostingpg

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

type buildRow struct {
	Environment identity.EnvironmentID `db:"environment_id"`
	Version     int                    `db:"version"`
	State       string                 `db:"state"`
	Base        string                 `db:"base"`
	Image       string                 `db:"image"`
	Detail      string                 `db:"detail"`
	Log         string                 `db:"log"`
	StartedAt   time.Time              `db:"started_at"`
	FinishedAt  *time.Time             `db:"finished_at"`
	UpdatedAt   time.Time              `db:"updated_at"`
}

func (b buildRow) build() hosting.Build {
	return hosting.Build{Environment: b.Environment, Version: b.Version, State: b.State, Base: b.Base, Image: b.Image,
		Detail: b.Detail, Log: b.Log, StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, UpdatedAt: b.UpdatedAt}
}

const buildColumns = `environment_id, version, state, base, image, detail, log, started_at, finished_at, updated_at`

// tail keeps the end of a log within hosting.MaxBuildLog, starting at a
// line and as valid text (Postgres refuses NUL bytes).
func tail(log string) string {
	log = strings.ToValidUTF8(strings.ReplaceAll(log, "\x00", ""), "")
	if len(log) <= hosting.MaxBuildLog {
		return log
	}
	log = log[len(log)-hosting.MaxBuildLog:]
	if i := strings.IndexByte(log, '\n'); i >= 0 {
		log = log[i+1:]
	}
	return strings.ToValidUTF8(log, "")
}

func (r *Repository) ClaimBuild(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, version int, base string, force bool) (hosting.Build, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Build{}, false, pgerr.Map(err, "begin claim build")
	}
	defer func() { _ = tx.Rollback() }()
	var found int
	if err := tx.GetContext(ctx, &found, `SELECT 1 FROM environment_versions
		WHERE organization_id = $1 AND environment_id = $2 AND version = $3 FOR SHARE`, org, env, version); err != nil {
		if pgerr.IsNoRows(err) {
			return hosting.Build{}, false, errx.NotFound("environment version not found")
		}
		return hosting.Build{}, false, pgerr.Map(err, "find environment version")
	}
	// Insert, or take over a build that failed, went stale or is forced
	// (never one in progress that is still reporting).
	var row buildRow
	err = tx.GetContext(ctx, &row, `INSERT INTO environment_builds AS b (organization_id, environment_id, version, state, base)
		VALUES ($1, $2, $3, 'building', $4)
		ON CONFLICT (organization_id, environment_id, version) DO UPDATE
		SET state = 'building', base = EXCLUDED.base, image = '', detail = '', log = '',
		    started_at = now(), finished_at = NULL, updated_at = now()
		WHERE b.state = 'failed'
		   OR (b.state = 'building' AND b.updated_at < now() - make_interval(secs => $5))
		   OR ($6 AND b.state = 'ready')
		RETURNING `+buildColumns, org, env, version, base, hosting.BuildStale.Seconds(), force)
	if err == nil {
		return row.build(), true, pgerr.Map(tx.Commit(), "commit claim build")
	}
	if !pgerr.IsNoRows(err) {
		return hosting.Build{}, false, pgerr.Map(err, "claim build")
	}
	if err := tx.GetContext(ctx, &row, `SELECT `+buildColumns+` FROM environment_builds
		WHERE organization_id = $1 AND environment_id = $2 AND version = $3`, org, env, version); err != nil {
		return hosting.Build{}, false, pgerr.Map(err, "read build")
	}
	return row.build(), false, pgerr.Map(tx.Commit(), "commit read build")
}

func (r *Repository) BuildProgress(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, version int, log string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE environment_builds SET log = $4, updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND version = $3 AND state = 'building'`, org, env, version, tail(log))
	return pgerr.Map(err, "store build progress")
}

func (r *Repository) FinishBuild(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, version int, image, detail, log string) (hosting.Build, error) {
	state := hosting.BuildReady
	if image == "" {
		state = hosting.BuildFailed
	}
	var row buildRow
	err := r.db.GetContext(ctx, &row, `UPDATE environment_builds
		SET state = $4, image = $5, detail = $6, log = $7, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND environment_id = $2 AND version = $3 AND state = 'building'
		RETURNING `+buildColumns, org, env, version, state, image, detail, tail(log))
	if pgerr.IsNoRows(err) {
		return hosting.Build{}, errx.Conflict("build of version " + strconv.Itoa(version) + " is not in progress")
	}
	if err != nil {
		return hosting.Build{}, pgerr.Map(err, "finish build")
	}
	return row.build(), nil
}

func (r *Repository) Build(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, version int) (hosting.Build, error) {
	var row buildRow
	err := r.db.GetContext(ctx, &row, `SELECT `+buildColumns+` FROM environment_builds b
		WHERE organization_id = $1 AND environment_id = $2
		  AND version = CASE WHEN $3 = 0 THEN (SELECT e.version FROM environments e
		                     WHERE e.organization_id = $1 AND e.id = $2) ELSE $3 END`, org, env, version)
	if pgerr.IsNoRows(err) {
		return hosting.Build{}, errx.NotFound("the environment version was never built")
	}
	if err != nil {
		return hosting.Build{}, pgerr.Map(err, "find build")
	}
	return row.build(), nil
}
