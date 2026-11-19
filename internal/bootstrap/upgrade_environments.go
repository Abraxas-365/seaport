package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// Environments moved into their agents (plan agent-centric): migration
// 00045 adds the agent config's machine, this data step moves every hosted
// agent's environment into it, and 00046 drops the environments. Build
// runs goose up to beforeEnvironmentsDrop, then the step, then the rest.
const (
	beforeEnvironmentsDrop = 45
	// environmentsMark is the step's row in migration_marks.
	environmentsMark = "environments"
	// upgradeLock serializes the step across processes starting together
	// (second key: the mark).
	upgradeLock = 7_401_016
	// The seal infos of the two secret stores (hostingmodule's, gone, and
	// agentmodule.secretsInfo): environment secrets are opened with the
	// first and sealed again with the second under the agent's purpose.
	environmentSecretsInfo = "seaport environment secrets v1"
	agentSecretsInfo       = "seaport agent secrets v1"
	// upgradedBy names the step in created_by.
	upgradedBy = "seaport upgrade"
)

// oldSpec is an environment version's spec: the harness and the machine
// (the description went with the environment).
type oldSpec struct {
	Harness string `json:"harness"`
	hosting.Machine
}

type hostedEnv struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Agent        identity.AgentID        `db:"agent_id"`
	Environment  string                  `db:"environment_id"`
	EnvName      string                  `db:"env_name"`
	Spec         []byte                  `db:"spec"`
	Version      int                     `db:"profile_version"`
}

type envSecret struct {
	Name   string `db:"name"`
	Sealed []byte `db:"sealed"`
	Env    bool   `db:"env"`
}

// upgradeEnvironments moves each hosted agent's environment into its
// config and secrets, in one transaction with the mark (so a failure
// leaves everything as it was and the next start tries again):
//   - a new config version = the current profile + the environment's
//     harness and machine, normalized (an old spec with no model key for
//     the harness's sessions reads as anthropic: subscription);
//   - each environment secret is opened and sealed again as the agent's;
//     one whose name the agent already has is stored as <NAME>_ENV (then
//     _ENV_2…) with env off, and the machine's references (models, repo
//     credentials) follow it, so both resolutions stay what they were
//     (references read the environment's value, env vars the agent's);
//   - running slots are left alone: their machine digest (”) is not the
//     new one, so the supervisor restarts them once idle.
//
// Agents that weren't hosted keep their profile; environments no agent
// used are dropped by 00046. Nothing to do once 00046 ran or the mark is
// there.
func upgradeEnvironments(ctx context.Context, db *sqlx.DB, secretKey string) error {
	var present bool
	if err := db.GetContext(ctx, &present, `SELECT to_regclass('environments') IS NOT NULL`); err != nil {
		return errx.Wrap(err, "check environments", errx.TypeInternal)
	}
	if !present {
		return nil
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin environments upgrade")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, upgradeLock, environmentsMark); err != nil {
		return pgerr.Map(err, "lock environments upgrade")
	}
	var done bool
	if err := tx.GetContext(ctx, &done, `SELECT EXISTS (SELECT 1 FROM migration_marks WHERE name = $1)`, environmentsMark); err != nil {
		return pgerr.Map(err, "read migration mark")
	}
	if done {
		return nil
	}
	var hosted []hostedEnv
	if err := tx.SelectContext(ctx, &hosted, `SELECT h.organization_id, h.agent_id,
		  e.id::text AS environment_id, e.name AS env_name, v.spec, a.profile_version
		FROM agent_hosting h
		JOIN agents a ON a.organization_id = h.organization_id AND a.id = h.agent_id
		JOIN environments e ON e.organization_id = h.organization_id AND e.id = h.environment_id
		JOIN environment_versions v ON v.organization_id = e.organization_id AND v.environment_id = e.id AND v.version = e.version
		ORDER BY h.organization_id, h.agent_id
		FOR UPDATE OF a`); err != nil {
		return pgerr.Map(err, "read hosted agents")
	}
	var sealed int
	if err := tx.GetContext(ctx, &sealed, `SELECT count(*) FROM environment_secrets s
		JOIN agent_hosting h ON h.organization_id = s.organization_id AND h.environment_id = s.environment_id`); err != nil {
		return pgerr.Map(err, "count environment secrets")
	}
	var from, to seal.Sealer
	if sealed > 0 {
		if secretKey == "" {
			return errx.Validation("environment secrets must move into their agents and this port has no secret key: start it with the key that sealed them (SEAPORT_SECRET_KEY or SEAPORT_SECRET_KEY_FILE)")
		}
		if from, err = seal.New(secretKey, environmentSecretsInfo); err != nil {
			return err
		}
		if to, err = seal.New(secretKey, agentSecretsInfo); err != nil {
			return err
		}
	}
	for _, h := range hosted {
		if err := upgradeAgent(ctx, tx, h, from, to); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO migration_marks (name) VALUES ($1)`, environmentsMark); err != nil {
		return pgerr.Map(err, "mark environments upgrade")
	}
	if err := tx.Commit(); err != nil {
		return pgerr.Map(err, "commit environments upgrade")
	}
	if len(hosted) > 0 {
		slog.Info("moved the hosted agents' environments into their configs", "agents", len(hosted))
	}
	return nil
}

func upgradeAgent(ctx context.Context, tx *sqlx.Tx, h hostedEnv, from, to seal.Sealer) error {
	org := h.Organization
	log := slog.With("organization", org.String(), "agent", h.Agent.String(), "environment", h.EnvName)
	var spec oldSpec
	if err := json.Unmarshal(h.Spec, &spec); err != nil {
		return errx.Wrap(err, "decode environment "+h.EnvName+"'s spec", errx.TypeInternal)
	}
	var secrets []envSecret
	if err := tx.SelectContext(ctx, &secrets, `SELECT name, sealed, env FROM environment_secrets
		WHERE organization_id = $1 AND environment_id = $2 ORDER BY name`, org, h.Environment); err != nil {
		return pgerr.Map(err, "read environment secrets")
	}
	var names []string
	if err := tx.SelectContext(ctx, &names, `SELECT name FROM agent_secrets
		WHERE organization_id = $1 AND agent_id = $2`, org, h.Agent); err != nil {
		return pgerr.Map(err, "read agent secrets")
	}
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	renamed := map[string]string{}
	for _, s := range secrets {
		plain, err := from.Open("environment-secret/"+org.String()+"/"+h.Environment+"/"+s.Name, s.Sealed)
		if err != nil {
			return errx.Wrap(err, "open environment "+h.EnvName+"'s secret "+s.Name+" (is this the key that sealed it?)", errx.TypeInternal)
		}
		name, env := s.Name, s.Env
		if taken[name] {
			name, env = freeSecretName(s.Name, taken), false
			renamed[s.Name] = name
			log.Warn("an environment secret has the name of one of the agent's: the environment's is kept as a new name (not an env var); the machine's references follow it",
				"secret", s.Name, "renamed", name)
		}
		taken[name] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_secrets (organization_id, agent_id, name, sealed, env)
			VALUES ($1, $2, $3, $4, $5)`, org, h.Agent, name,
			to.Seal("agent-secret/"+org.String()+"/"+h.Agent.String()+"/"+name, plain), env); err != nil {
			return pgerr.Map(err, "move secret "+s.Name)
		}
	}
	if len(taken) > 50 {
		log.Warn("the agent has more secrets than a save allows now; delete the ones it no longer needs", "secrets", len(taken))
	}
	m := spec.Machine
	for p, ref := range m.Models {
		if n := m.Models.KeyName(p); renamed[n] != "" {
			m.Models[p] = "${" + renamed[n] + "}"
		} else {
			m.Models[p] = ref
		}
	}
	for i, r := range m.Repos {
		if n := r.CredentialName(); renamed[n] != "" {
			m.Repos[i].Credential = "${" + renamed[n] + "}"
		}
	}
	full := hosting.Spec{Harness: spec.Harness, Machine: m}
	if err := full.Validate(); err != nil {
		// The environment ran with it: keep it, and say why a save of the
		// config will ask for a fix.
		log.Warn("the environment's spec doesn't pass today's checks; the agent keeps it until its config is saved again", "err", err)
	}
	full = hosting.Normalize(full)
	machine, err := json.Marshal(full.Machine)
	if err != nil {
		return errx.Wrap(err, "encode machine", errx.TypeInternal)
	}
	used, _ := json.Marshal(full.Secrets())
	var home any
	if full.Home != "" {
		home = full.Home
	}
	next := h.Version + 1
	// A copy of the current profile (or the defaults when it has none)
	// with the environment's harness and machine.
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_profiles
		(organization_id, agent_id, version, instructions, skills, mcp_servers, approve, model, effort, browser, browser_assist,
		 browser_detect, browser_ignore_hosts, harness, machine, machine_digest, machine_secrets, home, created_by)
		SELECT $1, $2, $3, instructions, skills, mcp_servers, approve, model, effort, browser, browser_assist,
		 browser_detect, browser_ignore_hosts, $5, $6, $7, ARRAY(SELECT jsonb_array_elements_text($8::jsonb)), $9, $10
		FROM agent_profiles WHERE organization_id = $1 AND agent_id = $2 AND version = $4`,
		org, h.Agent, next, h.Version, full.Harness, machine, full.Digest(), used, home, upgradedBy)
	if err != nil {
		return pgerr.Map(err, "copy profile")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_profiles
			(organization_id, agent_id, version, harness, machine, machine_digest, machine_secrets, home, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, ARRAY(SELECT jsonb_array_elements_text($7::jsonb)), $8, $9)`,
			org, h.Agent, next, full.Harness, machine, full.Digest(), used, home, upgradedBy); err != nil {
			return pgerr.Map(err, "create profile")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET profile_version = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2`, org, h.Agent, next); err != nil {
		return pgerr.Map(err, "point agent at its config")
	}
	log.Info("moved the environment into the agent's config", "version", next, "secrets", len(secrets), "renamed", len(renamed))
	return nil
}

// freeSecretName is <name>_ENV, else <name>_ENV_2, _ENV_3…: the first one
// not taken that is still a valid secret name (at most 64 characters: the
// base is cut to fit).
func freeSecretName(name string, taken map[string]bool) string {
	for i := 1; ; i++ {
		suffix := "_ENV"
		if i > 1 {
			suffix += "_" + strconv.Itoa(i)
		}
		base := name
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		if c := base + suffix; !taken[c] {
			return c
		}
	}
}
