package hostingpg

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

type buildRow struct {
	Digest     string            `db:"digest"`
	Agent      *identity.AgentID `db:"agent_id"`
	State      string            `db:"state"`
	Base       string            `db:"base"`
	Image      string            `db:"image"`
	Detail     string            `db:"detail"`
	Log        string            `db:"log"`
	StartedAt  time.Time         `db:"started_at"`
	FinishedAt *time.Time        `db:"finished_at"`
	UpdatedAt  time.Time         `db:"updated_at"`
}

func (b buildRow) build() hosting.Build {
	out := hosting.Build{Digest: b.Digest, State: b.State, Base: b.Base, Image: b.Image,
		Detail: b.Detail, Log: b.Log, StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, UpdatedAt: b.UpdatedAt}
	if b.Agent != nil {
		out.Agent = *b.Agent
	}
	return out
}

const buildColumns = `digest, agent_id, state, base, image, detail, log, started_at, finished_at, updated_at`

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

func (r *Repository) ClaimBuild(ctx context.Context, org identity.OrganizationID, digest string, agentID identity.AgentID, base string, force bool) (hosting.Build, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Build{}, false, pgerr.Map(err, "begin claim build")
	}
	defer func() { _ = tx.Rollback() }()
	// Insert, or take over a build that failed, went stale or is forced
	// (never one in progress that is still reporting).
	var row buildRow
	err = tx.GetContext(ctx, &row, `INSERT INTO hosting_builds AS b (organization_id, digest, agent_id, state, base)
		VALUES ($1, $2, $3, 'building', $4)
		ON CONFLICT (organization_id, digest) DO UPDATE
		SET state = 'building', agent_id = EXCLUDED.agent_id, base = EXCLUDED.base, image = '', detail = '', log = '',
		    started_at = now(), finished_at = NULL, updated_at = now()
		WHERE b.state = 'failed'
		   OR (b.state = 'building' AND b.updated_at < now() - make_interval(secs => $5))
		   OR ($6 AND b.state = 'ready')
		RETURNING `+buildColumns, org, digest, agentID, base, hosting.BuildStale.Seconds(), force)
	if err == nil {
		return row.build(), true, pgerr.Map(tx.Commit(), "commit claim build")
	}
	if !pgerr.IsNoRows(err) {
		return hosting.Build{}, false, pgerr.Map(err, "claim build")
	}
	if err := tx.GetContext(ctx, &row, `SELECT `+buildColumns+` FROM hosting_builds
		WHERE organization_id = $1 AND digest = $2`, org, digest); err != nil {
		return hosting.Build{}, false, pgerr.Map(err, "read build")
	}
	return row.build(), false, pgerr.Map(tx.Commit(), "commit read build")
}

func (r *Repository) BuildProgress(ctx context.Context, org identity.OrganizationID, digest, log string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hosting_builds SET log = $3, updated_at = now()
		WHERE organization_id = $1 AND digest = $2 AND state = 'building'`, org, digest, tail(log))
	return pgerr.Map(err, "store build progress")
}

func (r *Repository) FinishBuild(ctx context.Context, org identity.OrganizationID, digest, image, detail, log string) (hosting.Build, error) {
	state := hosting.BuildReady
	if image == "" {
		state = hosting.BuildFailed
	}
	var row buildRow
	err := r.db.GetContext(ctx, &row, `UPDATE hosting_builds
		SET state = $3, image = $4, detail = $5, log = $6, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND digest = $2 AND state = 'building'
		RETURNING `+buildColumns, org, digest, state, image, detail, tail(log))
	if pgerr.IsNoRows(err) {
		return hosting.Build{}, errx.Conflict("the build is not in progress")
	}
	if err != nil {
		return hosting.Build{}, pgerr.Map(err, "finish build")
	}
	return row.build(), nil
}

func (r *Repository) Build(ctx context.Context, org identity.OrganizationID, digest string) (hosting.Build, error) {
	var row buildRow
	err := r.db.GetContext(ctx, &row, `SELECT `+buildColumns+` FROM hosting_builds
		WHERE organization_id = $1 AND digest = $2`, org, digest)
	if pgerr.IsNoRows(err) {
		return hosting.Build{}, errx.NotFound("the agent's image was never built")
	}
	if err != nil {
		return hosting.Build{}, pgerr.Map(err, "find build")
	}
	return row.build(), nil
}
