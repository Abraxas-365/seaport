package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// secretValue is a secret only the agent's connections may ever see.
const secretValue = "sk-e2e-never-leaves-7f3a91"

func (p *port) saveProfile(id identity.AgentID, body map[string]any) (agent.Profile, int) {
	p.t.Helper()
	var out agent.Profile
	code := p.admin(http.MethodPut, "/api/v1/agents/"+id.String()+"/profile", body, &out)
	return out, code
}

// kit fetches GET /crew/profile with a crew token.
func (p *port) kit(token string) (agent.Kit, int) {
	p.t.Helper()
	var k agent.Kit
	code := p.call(http.MethodGet, "/crew/profile", token, nil, &k)
	return k, code
}

// crewAgent waits until the crew status shows a connection of the agent
// matching ok.
func (p *port) crewAgent(name string, ok func(crew.Agent) bool) crew.Agent {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last crew.Status
	for time.Now().Before(deadline) {
		last = crew.Status{}
		p.admin(http.MethodGet, "/api/v1/crew", nil, &last)
		for _, a := range last.Agents {
			if a.AgentName == name && ok(a) {
				return a
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("crew status of %s never matched: %+v", name, last.Agents)
	return crew.Agent{}
}

// An agent's profile is versioned (optimistic: a stale base conflicts),
// its secrets are written but never read back by the admin API, events or
// storage, and only its own crew credential gets the kit with them
// resolved. Saving tells its connections (profile frame, outdated in the
// crew status), skills reach any harness through the MCP skill tool, and a
// draining connection takes no new work.
func TestAgentProfiles(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "e2e-agent-secrets-key-0123456789abcdefXYZ" }, bootstrap.Options{})
	ctx := context.Background()
	pm, reviewer := p.agent("pm"), p.agent("reviewer")
	pmTok, revTok := p.issueAs(adminToken, pm.ID, "pm"), p.issueAs(adminToken, reviewer.ID, "reviewer")
	base := "/api/v1/agents/" + pm.ID.String()

	// No profile yet: version 0, and an empty kit.
	var none agent.Profile
	if code := p.admin(http.MethodGet, base+"/profile", nil, &none); code != http.StatusOK || none.Version != 0 {
		t.Fatalf("empty profile: %d %+v", code, none)
	}
	if k, code := p.kit(pmTok.Value); code != http.StatusOK || k.Version != 0 || k.Name != "pm" || k.Revision == "" {
		t.Fatalf("empty kit: %d %+v", code, k)
	}

	// A connection running no profile.
	conn := p.dialCrewAs(pmTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 1})
	p.crewAgent("pm", func(a crew.Agent) bool { return a.ProfileVersion == 0 && !a.Outdated })

	// A header can only name secrets the agent has.
	header := []map[string]any{{"name": "linear", "url": "https://mcp.example.com/mcp",
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer ${LINEAR_TOKEN}"}}}}
	profile := map[string]any{"base": 0, "instructions": "Eres el PM.", "approve": "allow", "effort": "high",
		"skills":      []map[string]string{{"name": "bug-triage", "description": "Cuando reportan un bug", "body": "Pide pasos para reproducir."}},
		"mcp_servers": header}
	if _, code := p.saveProfile(pm.ID, profile); code != http.StatusBadRequest {
		t.Fatalf("profile naming a missing secret: %d", code)
	}

	// Secrets: written, answered by name only.
	var sec agent.Secret
	if code := p.admin(http.MethodPut, base+"/secrets/LINEAR_TOKEN", map[string]any{"value": secretValue}, &sec); code != http.StatusOK || sec.Name != "LINEAR_TOKEN" || sec.Env {
		t.Fatalf("set secret: %d %+v", code, sec)
	}
	if code := p.admin(http.MethodPut, base+"/secrets/REGION", map[string]any{"value": "sa-east-1", "env": true}, nil); code != http.StatusOK {
		t.Fatalf("set env secret: %d", code)
	}
	if code := p.admin(http.MethodPut, base+"/secrets/SEAPORT_TOKEN", map[string]any{"value": "x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("reserved secret name: %d", code)
	}

	v1, code := p.saveProfile(pm.ID, profile)
	if code != http.StatusOK || v1.Version != 1 || len(v1.Skills) != 1 || v1.Approve != agent.ApproveAllow {
		t.Fatalf("save v1: %d %+v", code, v1)
	}
	// The connection hears of it, and shows as outdated until it runs v1.
	if f := conn.read(); f.Type != crew.FrameProfile {
		t.Fatalf("expected a profile frame, got %+v", f)
	}
	p.crewAgent("pm", func(a crew.Agent) bool { return a.Outdated })
	var listed struct {
		Items []agent.Agent `json:"items"`
	}
	p.admin(http.MethodGet, "/api/v1/agents?limit=100", nil, &listed)
	for _, a := range listed.Items {
		if a.ID == pm.ID && a.ProfileVersion != 1 {
			t.Fatalf("agent list profile version: %+v", a)
		}
	}

	// Optimistic: an edit from a stale base conflicts.
	if _, code := p.saveProfile(pm.ID, profile); code != http.StatusConflict {
		t.Fatalf("stale base: %d", code)
	}
	profile["base"], profile["instructions"] = 1, "Eres el PM. Sé breve."
	if v2, code := p.saveProfile(pm.ID, profile); code != http.StatusOK || v2.Version != 2 {
		t.Fatalf("save v2: %d %+v", code, v2)
	}
	var versions struct {
		Versions []agent.Version `json:"versions"`
	}
	if code := p.admin(http.MethodGet, base+"/profile/versions", nil, &versions); code != http.StatusOK || len(versions.Versions) != 2 || versions.Versions[0].Version != 2 {
		t.Fatalf("versions: %d %+v", code, versions)
	}
	var old agent.Profile
	if code := p.admin(http.MethodGet, base+"/profile/versions/1", nil, &old); code != http.StatusOK || old.Instructions != "Eres el PM." {
		t.Fatalf("version 1: %d %+v", code, old)
	}

	// The kit: secrets resolved in headers, env secrets as env, for pm's
	// token only.
	k, code := p.kit(pmTok.Value)
	if code != http.StatusOK || k.Version != 2 || k.Instructions != "Eres el PM. Sé breve." || k.Effort != "high" ||
		len(k.MCPServers) != 1 || k.MCPServers[0].Headers[0].Value != "Bearer "+secretValue ||
		len(k.Env) != 1 || k.Env["REGION"] != "sa-east-1" {
		t.Fatalf("kit: %d %+v", code, k)
	}
	if rk, code := p.kit(revTok.Value); code != http.StatusOK || rk.Name != "reviewer" || rk.Version != 0 {
		t.Fatalf("reviewer's kit: %d %+v", code, rk)
	}
	if code := p.call(http.MethodGet, "/crew/profile?agent=pm", revTok.Value, nil, nil); code != http.StatusForbidden {
		t.Fatalf("another agent's kit: %d", code)
	}
	if code := p.call(http.MethodGet, "/crew/profile", adminToken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("kit with the admin token: %d", code)
	}

	// The value never comes back to the admin side.
	for _, path := range []string{base + "/secrets", base + "/profile", base + "/profile/versions/2", base, "/api/v1/crew"} {
		var raw json.RawMessage
		p.admin(http.MethodGet, path, nil, &raw)
		if strings.Contains(string(raw), secretValue) {
			t.Fatalf("%s returns the secret: %s", path, raw)
		}
	}
	db, err := bootstrap.OpenDB(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var leaks int
	if err := db.GetContext(ctx, &leaks, `SELECT (SELECT count(*) FROM events e WHERE e::text LIKE '%'||$1||'%')
		+ (SELECT count(*) FROM agent_profiles pr WHERE pr::text LIKE '%'||$1||'%')
		+ (SELECT count(*) FROM agent_secrets s WHERE encode(s.sealed, 'escape') LIKE '%'||$1||'%')`, secretValue); err != nil || leaks != 0 {
		t.Fatalf("secret stored or published in clear: %d %v", leaks, err)
	}

	// A connection running the current version is up to date.
	conn.ws.CloseNow()
	fresh := p.dialCrewAs(pmTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 1, ProfileVersion: 2})
	p.crewAgent("pm", func(a crew.Agent) bool { return a.ProfileVersion == 2 && !a.Outdated })

	// Skills reach the harness through MCP: pm sees the skill tool with
	// its skills listed, the reviewer (no skills) doesn't.
	pmMCP := mcpSession(t, p, pmTok.Value)
	tools, err := pmMCP.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		found = found || (tool.Name == "skill" && strings.Contains(tool.Description, "bug-triage: Cuando reportan un bug"))
	}
	if !found {
		t.Fatal("pm has no skill tool listing bug-triage")
	}
	res, err := pmMCP.CallTool(ctx, &mcp.CallToolParams{Name: "skill", Arguments: map[string]any{"name": "bug-triage"}})
	if raw, _ := json.Marshal(res.StructuredContent); err != nil || res.IsError || !strings.Contains(string(raw), "Pide pasos para reproducir.") {
		t.Fatalf("skill: %v %s", err, raw)
	}
	revTools, err := mcpSession(t, p, revTok.Value).ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range revTools.Tools {
		if tool.Name == "skill" {
			t.Fatal("reviewer has a skill tool without skills")
		}
	}

	// A draining connection (restarting with a new profile) takes no new
	// work; the ship waits for the next connection.
	d := p.openDock("profiles")
	p.assignAs(adminToken, d.ID, pm.ID, true)
	fresh.send(crew.Frame{Type: crew.FrameDrain})
	time.Sleep(100 * time.Millisecond)
	s := p.inject(d, "500", "p1", "hola")
	fresh.quiet(300 * time.Millisecond)
	next := p.dialCrewAs(pmTok.Value, crew.Frame{Type: crew.FrameHello, Agent: "pm-laptop", Concurrency: 1, ProfileVersion: 2})
	ack(next, delivered(t, next, s))

	// A secret a header uses can't go; deleting the agent takes its
	// secrets and versions with it.
	if code := p.admin(http.MethodDelete, base+"/secrets/LINEAR_TOKEN", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete a used secret: %d", code)
	}
	if code := p.admin(http.MethodDelete, base+"/secrets/REGION", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete secret: %d", code)
	}
	if k, _ := p.kit(pmTok.Value); len(k.Env) != 0 {
		t.Fatalf("deleted env secret still in the kit: %+v", k.Env)
	}
	if code := p.admin(http.MethodDelete, base, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete agent: %d", code)
	}
	var left int
	if err := db.GetContext(ctx, &left, `SELECT count(*) FROM agent_secrets WHERE agent_id = $1`, pm.ID); err != nil || left != 0 {
		t.Fatalf("secrets of a deleted agent: %d %v", left, err)
	}
}
