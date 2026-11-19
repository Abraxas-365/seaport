package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const upgradeKey = "e2e-upgrade-secrets-key-0123456789abcdefXYZ"

// seedEnvironments wipes the test database, migrates it to the last
// release with environments (00045, before 00046 drops them) and seeds what
// a port of that release had: agents hosted on environments, environment
// secrets sealed with the old store's key, and a running slot.
type envSeed struct {
	db  *sqlx.DB
	org identity.OrganizationID
	t   *testing.T
}

func seedEnvironments(t *testing.T) *envSeed {
	t.Helper()
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.MigrateTo(ctx, db.DB, 45); err != nil {
		t.Fatal(err)
	}
	return &envSeed{db: db, org: config.DefaultOrganization, t: t}
}

func (s *envSeed) exec(q string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
}

func (s *envSeed) agent(name string) identity.AgentID {
	s.t.Helper()
	id := identity.NewAgentID()
	s.exec(`INSERT INTO agents (id, organization_id, name) VALUES ($1, $2, $3)`, id, s.org, name)
	return id
}

func (s *envSeed) environment(name string, spec map[string]any) string {
	s.t.Helper()
	id := uuid.NewString()
	raw, _ := json.Marshal(spec)
	s.exec(`INSERT INTO environments (id, organization_id, name, version) VALUES ($1, $2, $3, 1)`, id, s.org, name)
	s.exec(`INSERT INTO environment_versions (organization_id, environment_id, version, spec) VALUES ($1, $2, 1, $3)`, s.org, id, raw)
	return id
}

func (s *envSeed) envSecret(env string, name, value string, asEnv bool) {
	s.t.Helper()
	sealer, err := seal.New(upgradeKey, "seaport environment secrets v1")
	if err != nil {
		s.t.Fatal(err)
	}
	sealed := sealer.Seal("environment-secret/"+s.org.String()+"/"+env+"/"+name, []byte(value))
	s.exec(`INSERT INTO environment_secrets (organization_id, environment_id, name, sealed, env) VALUES ($1, $2, $3, $4, $5)`,
		s.org, env, name, sealed, asEnv)
}

func (s *envSeed) agentSecret(a identity.AgentID, name, value string, asEnv bool) {
	s.t.Helper()
	sealer, err := seal.New(upgradeKey, "seaport agent secrets v1")
	if err != nil {
		s.t.Fatal(err)
	}
	sealed := sealer.Seal("agent-secret/"+s.org.String()+"/"+a.String()+"/"+name, []byte(value))
	s.exec(`INSERT INTO agent_secrets (organization_id, agent_id, name, sealed, env) VALUES ($1, $2, $3, $4, $5)`,
		s.org, a, name, sealed, asEnv)
}

func (s *envSeed) host(a identity.AgentID, env string, slots ...string) {
	s.t.Helper()
	s.exec(`INSERT INTO agent_hosting (organization_id, agent_id, environment_id) VALUES ($1, $2, $3)`, s.org, a, env)
	for i, state := range slots {
		s.exec(`INSERT INTO agent_slots (organization_id, agent_id, idx, state) VALUES ($1, $2, $3, $4)`, s.org, a, i+1, state)
	}
}

// secretValue opens an agent secret as the agent store does.
func (s *envSeed) secretValue(a identity.AgentID, name string) string {
	s.t.Helper()
	var sealed []byte
	if err := s.db.Get(&sealed, `SELECT sealed FROM agent_secrets WHERE organization_id = $1 AND agent_id = $2 AND name = $3`,
		s.org, a, name); err != nil {
		s.t.Fatalf("secret %s: %v", name, err)
	}
	sealer, _ := seal.New(upgradeKey, "seaport agent secrets v1")
	plain, err := sealer.Open("agent-secret/"+s.org.String()+"/"+a.String()+"/"+name, sealed)
	if err != nil {
		s.t.Fatalf("open %s: %v", name, err)
	}
	return string(plain)
}

// A port upgraded from environments moves each hosted agent's environment
// into a new version of its config, its secrets into the agent's (a clash
// renamed, the machine's references following), leaves agents that weren't
// hosted alone, drops the environments and marks running slots outdated.
func TestUpgradeMovesEnvironmentsIntoAgents(t *testing.T) {
	s := seedEnvironments(t)

	// metrica: a profile, its own GITHUB_TOKEN (an env var), and an
	// environment naming a model key and a repo credential of its own.
	metrica := s.agent("metrica")
	s.exec(`INSERT INTO agent_profiles (organization_id, agent_id, version, instructions, model, browser) VALUES ($1, $2, 1, 'Be brief.', 'claude-opus-5-5', true)`, s.org, metrica)
	s.exec(`UPDATE agents SET profile_version = 1 WHERE id = $1`, metrica)
	s.agentSecret(metrica, "GITHUB_TOKEN", "agent-gh", true)
	work := s.environment("work", map[string]any{
		"harness":  "rness",
		"packages": []string{"git"},
		"models":   map[string]string{"anthropic": "${ANTHROPIC_KEY}"},
		"repos":    []map[string]string{{"url": "https://github.com/example/app", "credential": "${GITHUB_TOKEN}"}},
		"browser":  map[string]string{"engine": "chromium"},
	})
	s.envSecret(work, "ANTHROPIC_KEY", "model-key-value", false)
	s.envSecret(work, "GITHUB_TOKEN", "env-gh", true)
	s.envSecret(work, "TZ_NAME", "UTC", true)
	s.host(metrica, work, "running")

	// helper: no profile at all, hosted on a Claude Code environment with
	// no key (its sessions sign in to a subscription).
	helper := s.agent("helper")
	cc := s.environment("cc", map[string]any{"harness": "claude-code"})
	s.host(helper, cc)

	// remote: never hosted; unused: an environment nobody runs on.
	remote := s.agent("remote")
	s.exec(`INSERT INTO agent_profiles (organization_id, agent_id, version, instructions) VALUES ($1, $2, 1, 'Remote.')`, s.org, remote)
	s.exec(`UPDATE agents SET profile_version = 1 WHERE id = $1`, remote)
	unused := s.environment("unused", map[string]any{"harness": "rness"})
	s.envSecret(unused, "LONELY", "x", false)

	p := joinPort(t, func(c *config.Server) { c.SecretKey = upgradeKey }, bootstrap.Options{})

	// metrica: v2 = its profile + the environment's harness and machine.
	c := p.configOf(metrica)
	if c.Version != 2 || c.Harness != "rness" || c.Instructions != "Be brief." || c.Model != "claude-opus-5-5" || !c.Browser {
		t.Fatalf("metrica's config: %+v", c)
	}
	m := machineOf(t, c)
	if m.Models["anthropic"] != "${ANTHROPIC_KEY}" || len(m.Repos) != 1 || m.Repos[0].Credential != "${GITHUB_TOKEN_ENV}" ||
		m.Browser.Engine != "chromium" || len(m.Packages) != 1 {
		t.Fatalf("metrica's machine: %+v", m)
	}
	var secrets struct{ Secrets []agent.Secret }
	p.admin(http.MethodGet, "/api/v1/agents/"+metrica.String()+"/secrets", nil, &secrets)
	got := map[string]bool{}
	for _, sec := range secrets.Secrets {
		got[sec.Name] = sec.Env
	}
	want := map[string]bool{"ANTHROPIC_KEY": false, "GITHUB_TOKEN": true, "GITHUB_TOKEN_ENV": false, "TZ_NAME": true}
	if len(got) != len(want) {
		t.Fatalf("metrica's secrets: %v", got)
	}
	for name, env := range want {
		if e, ok := got[name]; !ok || e != env {
			t.Fatalf("metrica's secrets: %v, want %v", got, want)
		}
	}
	// References read the environment's value, env vars the agent's.
	for name, value := range map[string]string{"GITHUB_TOKEN": "agent-gh", "GITHUB_TOKEN_ENV": "env-gh", "ANTHROPIC_KEY": "model-key-value"} {
		if v := s.secretValue(metrica, name); v != value {
			t.Fatalf("%s = %q, want %q", name, v, value)
		}
	}
	// A named secret can't be deleted.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+metrica.String()+"/secrets/GITHUB_TOKEN_ENV", nil, nil); code < 400 {
		t.Fatalf("deleting a named secret: %d", code)
	}
	// Still hosted; the running slot runs no digest, so it is outdated.
	var hosted hosting.Hosted
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+metrica.String()+"/hosting", nil, &hosted); code != http.StatusOK ||
		len(hosted.Slots) != 1 || !hosted.Slots[0].Outdated {
		t.Fatalf("metrica's hosting: %d %+v", code, hosted)
	}

	// helper: v1 (it had none), Claude Code signing in to a subscription.
	h := p.configOf(helper)
	if h.Version != 1 || h.Harness != "claude-code" || machineOf(t, h).Models["anthropic"] != hosting.Subscription {
		t.Fatalf("helper's config: %+v %s", h, h.Machine)
	}

	// remote: untouched.
	if r := p.configOf(remote); r.Version != 1 || r.Harness != "" || len(r.Machine) > 0 && string(r.Machine) != "null" {
		t.Fatalf("remote's config: %+v", r)
	}

	// The environments are gone, and the step ran once.
	var left bool
	if err := s.db.Get(&left, `SELECT to_regclass('environments') IS NOT NULL OR to_regclass('environment_secrets') IS NOT NULL`); err != nil || left {
		t.Fatalf("environment tables left: %v %v", left, err)
	}
	p.stop()
	again := joinPort(t, func(c *config.Server) { c.SecretKey = upgradeKey }, bootstrap.Options{})
	if c := again.configOf(metrica); c.Version != 2 {
		t.Fatalf("a second start made version %d", c.Version)
	}
}

// Environment secrets can't move without the key that sealed them: the
// port refuses to start, and nothing changes (the next start tries again).
func TestUpgradeEnvironmentsNeedsTheSecretKey(t *testing.T) {
	s := seedEnvironments(t)
	a := s.agent("metrica")
	env := s.environment("work", map[string]any{"harness": "rness", "models": map[string]string{"anthropic": "${ANTHROPIC_KEY}"}})
	s.envSecret(env, "ANTHROPIC_KEY", "model-key-value", false)
	s.host(a, env)

	_, err := bootstrap.Build(context.Background(), config.Server{Addr: "127.0.0.1:0", DatabaseURL: testDatabaseURL(t), AdminToken: adminToken},
		bootstrap.Options{DisableWhatsApp: true})
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Fatalf("start without the key: %v", err)
	}
	var marks int
	if err := s.db.Get(&marks, `SELECT count(*) FROM migration_marks`); err != nil || marks != 0 {
		t.Fatalf("marks after a failed upgrade: %d %v", marks, err)
	}
	var version int
	if err := s.db.Get(&version, `SELECT profile_version FROM agents WHERE id = $1`, a); err != nil || version != 0 {
		t.Fatalf("profile version after a failed upgrade: %d %v", version, err)
	}

	// With the key, it moves.
	p := joinPort(t, func(c *config.Server) { c.SecretKey = upgradeKey }, bootstrap.Options{})
	if c := p.configOf(a); c.Version != 1 || machineOf(t, c).Models["anthropic"] != "${ANTHROPIC_KEY}" {
		t.Fatalf("config after the upgrade: %+v", c)
	}
	if v := s.secretValue(a, "ANTHROPIC_KEY"); v != "model-key-value" {
		t.Fatalf("ANTHROPIC_KEY = %q", v)
	}
}
