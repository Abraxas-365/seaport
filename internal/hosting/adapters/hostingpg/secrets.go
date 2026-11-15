package hostingpg

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/jmoiron/sqlx"
)

// secretPurpose binds a sealed secret to its row: a value copied to
// another environment's row doesn't open.
func secretPurpose(org identity.OrganizationID, id identity.EnvironmentID, name string) string {
	return "environment-secret/" + org.String() + "/" + id.String() + "/" + name
}

// lockEnvironment locks the environment's row (secret writes serialize on
// it, like spec saves) and answers its current version.
func lockEnvironment(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID, id identity.EnvironmentID) (int, error) {
	var version int
	if err := tx.GetContext(ctx, &version, `SELECT version FROM environments
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, id); err != nil {
		return 0, pgerr.MapWith(err, "lock environment", messages)
	}
	return version, nil
}

func (r *Repository) SetSecret(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, in hosting.SetEnvironmentSecret, limit int) (hosting.EnvironmentSecret, error) {
	if r.sealer == nil {
		return hosting.EnvironmentSecret{}, errx.Business("this port has no secret key: set SEAPORT_SECRET_KEY (or SEAPORT_SECRET_KEY_FILE) to store secrets")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.EnvironmentSecret{}, pgerr.Map(err, "begin set secret")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockEnvironment(ctx, tx, org, id); err != nil {
		return hosting.EnvironmentSecret{}, err
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM environment_secrets
		WHERE organization_id = $1 AND environment_id = $2 AND name <> $3`, org, id, in.Name); err != nil {
		return hosting.EnvironmentSecret{}, pgerr.Map(err, "count secrets")
	}
	if n >= limit {
		return hosting.EnvironmentSecret{}, errx.QuotaExceeded("environment_secrets", limit, "an environment can have at most "+strconv.Itoa(limit)+" secrets")
	}
	sealed := r.sealer.Seal(secretPurpose(org, id, in.Name), []byte(in.Value))
	var s hosting.EnvironmentSecret
	if err := tx.GetContext(ctx, &s, `INSERT INTO environment_secrets (organization_id, environment_id, name, sealed, env)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (organization_id, environment_id, name) DO UPDATE SET sealed = EXCLUDED.sealed, env = EXCLUDED.env, updated_at = now()
		RETURNING name, env, updated_at`, org, id, in.Name, sealed, in.Env); err != nil {
		return hosting.EnvironmentSecret{}, pgerr.Map(err, "set secret")
	}
	return s, pgerr.Map(tx.Commit(), "commit secret")
}

func (r *Repository) DeleteSecret(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, name string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin delete secret")
	}
	defer func() { _ = tx.Rollback() }()
	version, err := lockEnvironment(ctx, tx, org, id)
	if err != nil {
		return err
	}
	var raw []byte
	if err := tx.GetContext(ctx, &raw, `SELECT spec FROM environment_versions
		WHERE organization_id = $1 AND environment_id = $2 AND version = $3`, org, id, version); err != nil {
		return pgerr.Map(err, "read spec")
	}
	var spec hosting.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return errx.Wrap(err, "decode environment spec", errx.TypeInternal)
	}
	if slices.Contains(spec.Secrets(), name) {
		return errx.Conflict("the environment's spec uses " + name + " (a repo credential or model key); change the spec first")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM environment_secrets WHERE organization_id = $1 AND environment_id = $2 AND name = $3`, org, id, name)
	if err != nil {
		return pgerr.Map(err, "delete secret")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("secret not found")
	}
	return pgerr.Map(tx.Commit(), "commit delete secret")
}

func (r *Repository) Secrets(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) ([]hosting.EnvironmentSecret, error) {
	if _, err := r.Environment(ctx, org, id, 0); err != nil {
		return nil, err
	}
	out := []hosting.EnvironmentSecret{}
	err := r.db.SelectContext(ctx, &out, `SELECT name, env, updated_at FROM environment_secrets
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY name`, org, id)
	return out, pgerr.Map(err, "list secrets")
}

func (r *Repository) OpenEnvironmentSecrets(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) ([]hosting.EnvironmentSecretValue, error) {
	var rows []struct {
		Name   string `db:"name"`
		Sealed []byte `db:"sealed"`
		Env    bool   `db:"env"`
	}
	if err := r.db.SelectContext(ctx, &rows, `SELECT name, sealed, env FROM environment_secrets
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY name`, org, id); err != nil {
		return nil, pgerr.Map(err, "read secrets")
	}
	out := make([]hosting.EnvironmentSecretValue, 0, len(rows))
	if len(rows) > 0 && r.sealer == nil {
		return nil, errx.Internal("environment secrets are stored but this port has no secret key")
	}
	for _, row := range rows {
		plain, err := r.sealer.Open(secretPurpose(org, id, row.Name), row.Sealed)
		if err != nil {
			return nil, errx.Internal("environment secret " + row.Name + " doesn't open with this port's secret key (was the key changed?)")
		}
		out = append(out, hosting.EnvironmentSecretValue{Name: row.Name, Value: string(plain), Env: row.Env})
	}
	return out, nil
}
