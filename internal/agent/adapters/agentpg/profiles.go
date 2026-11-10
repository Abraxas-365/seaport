package agentpg

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/jmoiron/sqlx"
)

// secretPurpose binds a sealed secret to its row: a value copied to
// another agent, organization or name doesn't open.
func secretPurpose(org identity.OrganizationID, id identity.AgentID, name string) string {
	return "agent-secret/" + org.String() + "/" + id.String() + "/" + name
}

type profileRow struct {
	Version      int       `db:"version"`
	Instructions string    `db:"instructions"`
	Skills       []byte    `db:"skills"`
	MCPServers   []byte    `db:"mcp_servers"`
	Approve      string    `db:"approve"`
	Model        string    `db:"model"`
	Effort       string    `db:"effort"`
	CreatedBy    string    `db:"created_by"`
	CreatedAt    time.Time `db:"created_at"`
}

const profileColumns = `version, instructions, skills, mcp_servers, approve, model, effort, created_by, created_at`

func (row profileRow) profile(id identity.AgentID) (agent.Profile, error) {
	p := agent.Profile{Agent: id, Version: row.Version, Instructions: row.Instructions, Approve: row.Approve,
		Model: row.Model, Effort: row.Effort, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	if err := json.Unmarshal(row.Skills, &p.Skills); err != nil {
		return p, errx.Wrap(err, "decode profile skills", errx.TypeInternal)
	}
	if err := json.Unmarshal(row.MCPServers, &p.MCPServers); err != nil {
		return p, errx.Wrap(err, "decode profile mcp servers", errx.TypeInternal)
	}
	if p.Skills == nil {
		p.Skills = []agent.Skill{}
	}
	if p.MCPServers == nil {
		p.MCPServers = []agent.MCPServer{}
	}
	for i := range p.MCPServers {
		if p.MCPServers[i].Headers == nil {
			p.MCPServers[i].Headers = []agent.Header{}
		}
	}
	return p, nil
}

// lockAgent locks the agent's row (profile and secret writes serialize on
// it) and returns its current profile version.
func lockAgent(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID, id identity.AgentID) (int, error) {
	var version int
	err := tx.GetContext(ctx, &version, `SELECT profile_version FROM agents
		WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL FOR UPDATE`, org, id)
	return version, pgerr.MapWith(err, "lock agent", messages)
}

// missingSecrets are the names not among the agent's secrets.
func missingSecrets(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, id identity.AgentID, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var have []string
	if err := sqlx.SelectContext(ctx, q, &have, `SELECT name FROM agent_secrets
		WHERE organization_id = $1 AND agent_id = $2 AND name = ANY($3::text[])`, org, id, names); err != nil {
		return nil, pgerr.Map(err, "find secrets")
	}
	set := map[string]bool{}
	for _, n := range have {
		set[n] = true
	}
	var out []string
	for _, n := range names {
		if !set[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

func (r *Repository) SaveProfile(ctx context.Context, org identity.OrganizationID, id identity.AgentID, in agent.SaveProfile) (agent.Profile, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return agent.Profile{}, pgerr.Map(err, "begin save profile")
	}
	defer func() { _ = tx.Rollback() }()
	current, err := lockAgent(ctx, tx, org, id)
	if err != nil {
		return agent.Profile{}, err
	}
	if current != in.Base {
		return agent.Profile{}, errx.Conflict("the profile changed since it was loaded (now version " +
			strconv.Itoa(current) + "); reload it and apply your changes again")
	}
	missing, err := missingSecrets(ctx, tx, org, id, in.SecretRefs())
	if err != nil {
		return agent.Profile{}, err
	}
	if len(missing) > 0 {
		return agent.Profile{}, errx.InvalidField("mcp_servers", "the MCP headers use secret "+missing[0]+", which this agent doesn't have; set it first")
	}
	skills, _ := json.Marshal(in.Skills)
	servers, _ := json.Marshal(in.MCPServers)
	var row profileRow
	if err := tx.GetContext(ctx, &row, `INSERT INTO agent_profiles
		(organization_id, agent_id, version, instructions, skills, mcp_servers, approve, model, effort, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING `+profileColumns,
		org, id, current+1, in.Instructions, skills, servers, in.Approve, in.Model, in.Effort, in.CreatedBy); err != nil {
		return agent.Profile{}, pgerr.MapWith(err, "insert profile", messages)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET profile_version = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2`, org, id, current+1); err != nil {
		return agent.Profile{}, pgerr.Map(err, "point agent at profile")
	}
	if err := tx.Commit(); err != nil {
		return agent.Profile{}, pgerr.Map(err, "commit profile")
	}
	return row.profile(id)
}

func (r *Repository) Profile(ctx context.Context, org identity.OrganizationID, id identity.AgentID, version int) (agent.Profile, error) {
	if version == 0 {
		if err := r.db.GetContext(ctx, &version, `SELECT profile_version FROM agents
			WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, org, id); err != nil {
			return agent.Profile{}, pgerr.MapWith(err, "find agent", messages)
		}
		if version == 0 {
			return agent.Profile{Agent: id, Skills: []agent.Skill{}, MCPServers: []agent.MCPServer{}}, nil
		}
	}
	var row profileRow
	err := r.db.GetContext(ctx, &row, `SELECT `+profileColumns+` FROM agent_profiles p
		WHERE p.organization_id = $1 AND p.agent_id = $2 AND p.version = $3
		  AND EXISTS (SELECT 1 FROM agents a WHERE a.organization_id = $1 AND a.id = $2 AND a.deleted_at IS NULL)`,
		org, id, version)
	if err != nil {
		return agent.Profile{}, pgerr.MapWith(err, "find profile", pgerr.Messages{NotFound: "profile version not found"})
	}
	return row.profile(id)
}

func (r *Repository) Versions(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]agent.Version, error) {
	if _, err := r.Find(ctx, org, id); err != nil {
		return nil, err
	}
	out := []agent.Version{}
	err := r.db.SelectContext(ctx, &out, `SELECT version, created_by, created_at FROM agent_profiles
		WHERE organization_id = $1 AND agent_id = $2 ORDER BY version DESC`, org, id)
	return out, pgerr.Map(err, "list profile versions")
}

func (r *Repository) SetSecret(ctx context.Context, org identity.OrganizationID, id identity.AgentID, in agent.SetSecret, limit int) (agent.Secret, error) {
	if r.sealer == nil {
		return agent.Secret{}, errx.Business("this port has no secret key: set SEAPORT_SECRET_KEY (or SEAPORT_SECRET_KEY_FILE) to store agent secrets")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return agent.Secret{}, pgerr.Map(err, "begin set secret")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockAgent(ctx, tx, org, id); err != nil {
		return agent.Secret{}, err
	}
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM agent_secrets
		WHERE organization_id = $1 AND agent_id = $2 AND name <> $3`, org, id, in.Name); err != nil {
		return agent.Secret{}, pgerr.Map(err, "count secrets")
	}
	if n >= limit {
		return agent.Secret{}, errx.QuotaExceeded("agent_secrets", limit, "an agent can have at most "+strconv.Itoa(limit)+" secrets")
	}
	sealed := r.sealer.Seal(secretPurpose(org, id, in.Name), []byte(in.Value))
	var s agent.Secret
	if err := tx.GetContext(ctx, &s, `INSERT INTO agent_secrets (organization_id, agent_id, name, sealed, env)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (organization_id, agent_id, name) DO UPDATE SET sealed = EXCLUDED.sealed, env = EXCLUDED.env, updated_at = now()
		RETURNING name, env, updated_at`, org, id, in.Name, sealed, in.Env); err != nil {
		return agent.Secret{}, pgerr.Map(err, "set secret")
	}
	return s, pgerr.Map(tx.Commit(), "commit secret")
}

func (r *Repository) DeleteSecret(ctx context.Context, org identity.OrganizationID, id identity.AgentID, name string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin delete secret")
	}
	defer func() { _ = tx.Rollback() }()
	version, err := lockAgent(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if version > 0 {
		var raw []byte
		if err := tx.GetContext(ctx, &raw, `SELECT mcp_servers FROM agent_profiles
			WHERE organization_id = $1 AND agent_id = $2 AND version = $3`, org, id, version); err != nil {
			return pgerr.Map(err, "read profile")
		}
		var p agent.Profile
		if err := json.Unmarshal(raw, &p.MCPServers); err != nil {
			return errx.Wrap(err, "decode profile mcp servers", errx.TypeInternal)
		}
		for _, ref := range p.SecretRefs() {
			if ref == name {
				return errx.Conflict("the agent's profile uses " + name + " in an MCP header; change the profile first")
			}
		}
	}
	// Nor one its environment names (hosted agent): a repo credential or a
	// model key.
	var envName string
	err = tx.GetContext(ctx, &envName, `SELECT e.name FROM agent_hosting h
		JOIN environments e ON e.organization_id = h.organization_id AND e.id = h.environment_id
		JOIN environment_versions v ON v.organization_id = e.organization_id AND v.environment_id = e.id AND v.version = e.version
		WHERE h.organization_id = $1 AND h.agent_id = $2
		  AND (v.spec->'repos' @> jsonb_build_array(jsonb_build_object('credential', '${' || $3 || '}'))
		    OR v.spec->'models'->>'anthropic' = '${' || $3 || '}')`, org, id, name)
	if err == nil {
		return errx.Conflict("the agent's environment " + envName + " uses " + name + " (a repo credential or model key); change it first")
	}
	if !pgerr.IsNoRows(err) {
		return pgerr.Map(err, "check environment credentials")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_secrets WHERE organization_id = $1 AND agent_id = $2 AND name = $3`, org, id, name)
	if err != nil {
		return pgerr.Map(err, "delete secret")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("secret not found")
	}
	return pgerr.Map(tx.Commit(), "commit delete secret")
}

func (r *Repository) Secrets(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]agent.Secret, error) {
	if _, err := r.Find(ctx, org, id); err != nil {
		return nil, err
	}
	out := []agent.Secret{}
	err := r.db.SelectContext(ctx, &out, `SELECT name, env, updated_at FROM agent_secrets
		WHERE organization_id = $1 AND agent_id = $2 ORDER BY name`, org, id)
	return out, pgerr.Map(err, "list secrets")
}

func (r *Repository) OpenSecrets(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]agent.SecretValue, error) {
	var rows []struct {
		Name   string `db:"name"`
		Sealed []byte `db:"sealed"`
		Env    bool   `db:"env"`
	}
	if err := r.db.SelectContext(ctx, &rows, `SELECT name, sealed, env FROM agent_secrets
		WHERE organization_id = $1 AND agent_id = $2 ORDER BY name`, org, id); err != nil {
		return nil, pgerr.Map(err, "read secrets")
	}
	out := make([]agent.SecretValue, 0, len(rows))
	if len(rows) > 0 && r.sealer == nil {
		return nil, errx.Internal("agent secrets are stored but this port has no secret key")
	}
	for _, row := range rows {
		plain, err := r.sealer.Open(secretPurpose(org, id, row.Name), row.Sealed)
		if err != nil {
			return nil, errx.Internal("agent secret " + row.Name + " doesn't open with this port's secret key (was the key changed?)")
		}
		out = append(out, agent.SecretValue{Name: row.Name, Value: string(plain), Env: row.Env})
	}
	return out, nil
}
