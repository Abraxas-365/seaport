package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Cloning copies an agent's current config (profile, harness, machine,
// home reference, digest, named secrets) as the clone's version 1 and its
// hosting (same slots, stopped), and never its docks, takeovers or tokens.
// Secrets are copied only on request, resealed for the clone: no value
// ever leaves, and a clone without them fails clearly wherever the config
// resolves one (kit, workspace, model key, slot start).
func TestCloneAgent(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-clone-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
	}, bootstrap.Options{Runtime: rt})
	events := p.openEvents("")

	// A hosted source: a repo credential, an MCP header secret and an env
	// secret, two slots, a dock.
	pm := p.agent("pm")
	base := "/api/v1/agents/" + pm.ID.String()
	for name, body := range map[string]map[string]any{
		"GITHUB_TOKEN": {"value": secretValue},
		"LINEAR_TOKEN": {"value": secretValue + "-linear"},
		"REGION":       {"value": "sa-east-1", "env": true},
	} {
		if code := p.admin(http.MethodPut, base+"/secrets/"+name, body, nil); code != http.StatusOK {
			t.Fatalf("set %s: %d", name, code)
		}
	}
	machine := map[string]any{"packages": []string{"jq"},
		"repos": []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${GITHUB_TOKEN}"}}}
	header := []map[string]any{{"name": "linear", "url": "https://mcp.example.com/mcp",
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer ${LINEAR_TOKEN}"}}}}
	code, src := p.saveConfig(pm.ID, func(b map[string]any) {
		b["harness"], b["machine"], b["mcp_servers"], b["instructions"] = "rness", machine, header, "Eres el PM."
	})
	if code != http.StatusOK || src.Version != 1 {
		t.Fatalf("source config: %d %+v", code, src)
	}
	p.setMachine(pm.ID, "rness", machine) // unchanged: still version 1
	if code := p.admin(http.MethodPut, base+"/hosting", map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host source: %d", code)
	}
	d := p.openDock("clone-dock")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	if code := p.admin(http.MethodPatch, base, map[string]any{"description": "El PM"}, nil); code != http.StatusOK {
		t.Fatalf("describe: %d", code)
	}

	// Without secrets (the default): the config is copied whole, the
	// secrets are not.
	var bare agent.Agent
	var raw json.RawMessage
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{"name": "pm-copy"}, &raw); code != http.StatusCreated {
		t.Fatalf("clone: %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &bare); err != nil {
		t.Fatal(err)
	}
	if bare.Name != "pm-copy" || bare.ID == pm.ID || bare.Description != "El PM" || bare.ProfileVersion != 1 ||
		len(bare.Docks) != 0 || len(bare.Takeovers) != 0 {
		t.Fatalf("clone: %+v", bare)
	}
	next(t, events, agent.EventUpdated, func(d json.RawMessage) bool { return strings.Contains(string(d), `"pm-copy"`) })
	cfg := p.configOf(bare.ID)
	if cfg.Version != 1 || cfg.Instructions != "Eres el PM." || cfg.Harness != "rness" || cfg.MachineDigest != src.MachineDigest ||
		cfg.MachineDigest == "" || string(cfg.Machine) != string(p.configOf(pm.ID).Machine) ||
		strings.Join(cfg.MachineSecrets, ",") != "GITHUB_TOKEN" || len(cfg.MCPServers) != 1 {
		t.Fatalf("clone config: %+v", cfg)
	}
	var secrets struct {
		Secrets []agent.Secret `json:"secrets"`
	}
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+bare.ID.String()+"/secrets", nil, &secrets); code != http.StatusOK || len(secrets.Secrets) != 0 {
		t.Fatalf("clone secrets: %d %+v", code, secrets)
	}
	// Hosted like the source: two slots, stopped.
	var h hosting.Hosted
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+bare.ID.String()+"/hosting", nil, &h); code != http.StatusOK ||
		len(h.Slots) != 2 || h.Slots[0].State != hosting.SlotStopped || h.Slots[1].State != hosting.SlotStopped {
		t.Fatalf("clone hosting: %d %+v", code, h)
	}
	// The source is untouched and the clone carries no dock.
	if got := p.configOf(pm.ID); got.Version != 1 || got.CreatedBy == "" {
		t.Fatalf("source changed: %+v", got)
	}
	// Starting the clone's slot names the missing secret instead of failing later.
	code, msg := p.cloneStart(bare.ID.String())
	if code != http.StatusUnprocessableEntity || !strings.Contains(msg, "GITHUB_TOKEN") || strings.Contains(msg, secretValue) {
		t.Fatalf("start without secrets: %d %q", code, msg)
	}
	// ... and so does the kit of a non-hosted clone (below).

	// With secrets: every one is resealed for the clone, env flags kept.
	var full agent.Agent
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{"name": "pm-full", "description": "Otra", "secrets": true}, &full); code != http.StatusCreated ||
		full.Description != "Otra" {
		t.Fatalf("clone with secrets: %d %+v", code, full)
	}
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+full.ID.String()+"/secrets", nil, &secrets); code != http.StatusOK || len(secrets.Secrets) != 3 {
		t.Fatalf("clone secrets: %d %+v", code, secrets)
	}
	for _, s := range secrets.Secrets {
		if s.Env != (s.Name == "REGION") {
			t.Fatalf("env flag of %s: %+v", s.Name, s)
		}
	}
	raw, _ = json.Marshal(full)
	if strings.Contains(string(raw), secretValue) {
		t.Fatal("clone answer carries a secret")
	}
	rt.results <- "sha256:clone1"
	if code, msg := p.cloneStart(full.ID.String()); code != http.StatusAccepted {
		t.Fatalf("start with secrets: %d %q", code, msg)
	}

	// A plain (not hosted) agent: its kit resolves for the clone with
	// secrets, and fails naming the secret for the clone without.
	plain := p.agent("plain")
	pbase := "/api/v1/agents/" + plain.ID.String()
	if code := p.admin(http.MethodPut, pbase+"/secrets/LINEAR_TOKEN", map[string]any{"value": secretValue}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	if code, _ := p.saveConfig(plain.ID, func(b map[string]any) { b["mcp_servers"] = header }); code != http.StatusOK {
		t.Fatalf("plain config: %d", code)
	}
	var withKit, noKit agent.Agent
	if code := p.admin(http.MethodPost, pbase+"/clone", map[string]any{"name": "plain-full", "secrets": true}, &withKit); code != http.StatusCreated {
		t.Fatalf("clone plain: %d", code)
	}
	if code := p.admin(http.MethodPost, pbase+"/clone", map[string]any{"name": "plain-bare"}, &noKit); code != http.StatusCreated {
		t.Fatalf("clone plain bare: %d", code)
	}
	if k, code := p.kit(p.issueAs(adminToken, withKit.ID, "t").Value); code != http.StatusOK ||
		len(k.MCPServers) != 1 || k.MCPServers[0].Headers[0].Value != "Bearer "+secretValue {
		t.Fatalf("kit of the clone with secrets: %d %+v", code, k)
	}
	var e struct {
		Error errx.Response `json:"error"`
	}
	if code := p.call(http.MethodGet, "/crew/profile", p.issueAs(adminToken, noKit.ID, "t").Value, nil, &e); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e.Error.Message, "LINEAR_TOKEN") {
		t.Fatalf("kit of the clone without secrets: %d %+v", code, e)
	}
	// The clone's own secret is its own: changing the source's leaves it.
	if code := p.admin(http.MethodPut, pbase+"/secrets/LINEAR_TOKEN", map[string]any{"value": "changed"}, nil); code != http.StatusOK {
		t.Fatalf("change the source's secret: %d", code)
	}
	if k, code := p.kit(p.issueAs(adminToken, withKit.ID, "t2").Value); code != http.StatusOK || k.MCPServers[0].Headers[0].Value != "Bearer "+secretValue {
		t.Fatalf("clone's secret followed the source's: %d %+v", code, k)
	}

	// A source with no config makes a clone with none; a description of
	// "" is a description.
	empty := p.agent("empty")
	var ec agent.Agent
	if code := p.admin(http.MethodPost, "/api/v1/agents/"+empty.ID.String()+"/clone", map[string]any{"name": "empty-copy", "description": ""}, &ec); code != http.StatusCreated ||
		ec.ProfileVersion != 0 || p.configOf(ec.ID).Version != 0 {
		t.Fatalf("clone without config: %d %+v", code, ec)
	}
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+ec.ID.String()+"/hosting", nil, nil); code != http.StatusNotFound {
		t.Fatalf("a clone of an unhosted agent is hosted: %d", code)
	}

	// Refusals: a taken name, a bad name, an unknown source, a bad body.
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{"name": "pm-copy"}, nil); code != http.StatusConflict {
		t.Fatalf("name taken: %d", code)
	}
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{"name": "Bad Name"}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", code)
	}
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("no name: %d", code)
	}
	if code := p.admin(http.MethodPost, base+"/clone", map[string]any{"name": "x", "bogus": 1}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/agents/00000000-0000-4000-8000-000000000999/clone", map[string]any{"name": "ghost"}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown source: %d", code)
	}
	// The failed attempts left no agent behind.
	var list query.Paginated[agent.Agent]
	p.admin(http.MethodGet, "/api/v1/agents?limit=100", nil, &list)
	for _, a := range list.Items {
		if a.Name == "x" || a.Name == "ghost" || a.Name == "Bad Name" {
			t.Fatalf("a refused clone exists: %+v", a)
		}
	}
}

// cloneStart starts slot 1 of the agent and answers the status and error message.
func (p *port) cloneStart(id string) (int, string) {
	p.t.Helper()
	var e struct {
		Error errx.Response `json:"error"`
	}
	code := p.admin(http.MethodPost, "/api/v1/agents/"+id+"/hosting/slots/1/start", nil, &e)
	return code, e.Error.Message
}

// A clone that can't be hosted like its source (here: the organization's
// slots are used up) is not kept half-made: the error is answered and no
// agent is left behind.
func TestCloneAgentHostingFails(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.MaxSlots = 2 }, bootstrap.Options{})
	pm := p.agent("pm")
	p.setMachine(pm.ID, "rness", map[string]any{})
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/hosting", map[string]any{"slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host source: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/agents/"+pm.ID.String()+"/clone", map[string]any{"name": "pm-copy"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("clone over the slot limit: %d", code)
	}
	var list query.Paginated[agent.Agent]
	p.admin(http.MethodGet, "/api/v1/agents?limit=100", nil, &list)
	for _, a := range list.Items {
		if a.Name == "pm-copy" {
			t.Fatalf("a clone that couldn't be hosted exists: %+v", a)
		}
	}
	// The name is free again.
	if code := p.admin(http.MethodPost, "/api/v1/agents", map[string]any{"name": "pm-copy"}, nil); code != http.StatusCreated {
		t.Fatalf("name after a failed clone: %d", code)
	}
}
