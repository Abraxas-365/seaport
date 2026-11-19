package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// An agent's config carries its harness and machine (versioned and
// optimistic with the rest of the config; an unchanged config makes no
// version); the machine is normalized and its digest decides builds and
// outdated slots. Hosting gives the agent slots (bounded per
// organization); the machine's credentials must be secrets the agent has,
// a person's token and hosting exclude each other, and deleting the agent
// unhosts it.
func TestAgentMachineAndHosting(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-hosting-secrets-key-0123456789abcdefXYZ"
		c.MaxSlots, c.HostMaxCPUs = 3, 4
	}, bootstrap.Options{})
	events := p.openEvents("")
	pm := p.agent("pm")

	machine := map[string]any{"packages": []string{"jq"},
		"repos":     []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${GITHUB_TOKEN}"}},
		"network":   map[string]any{"egress": []string{"registry.npmjs.org"}},
		"resources": map[string]string{"size": "M"}}
	// A machine needs a harness, a valid one, resources within the
	// operator's maximum and the secrets it names.
	if code, _ := p.saveConfig(pm.ID, func(b map[string]any) { b["machine"] = machine }); code != http.StatusBadRequest {
		t.Fatalf("machine without a harness: %d", code)
	}
	if code, _ := p.saveConfig(pm.ID, func(b map[string]any) { b["harness"], b["machine"] = "emacs", machine }); code != http.StatusBadRequest {
		t.Fatalf("unknown harness: %d", code)
	}
	if code, _ := p.saveConfig(pm.ID, func(b map[string]any) {
		b["harness"], b["machine"] = "rness", map[string]any{"resources": map[string]any{"cpus": 8, "memory_mb": 1024}}
	}); code != http.StatusBadRequest {
		t.Fatalf("cpus above the operator's maximum: %d", code)
	}
	if code, _ := p.saveConfig(pm.ID, func(b map[string]any) { b["machine"] = map[string]any{"imag": "x"} }); code != http.StatusBadRequest {
		t.Fatalf("unknown machine key: %d", code)
	}
	if code, _ := p.saveConfig(pm.ID, func(b map[string]any) { b["harness"], b["machine"] = "claude-code", machine }); code != http.StatusBadRequest {
		t.Fatalf("machine naming a secret the agent lacks: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/secrets/GITHUB_TOKEN", map[string]any{"value": secretValue}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	v1 := p.setMachine(pm.ID, "claude-code", machine)
	m := machineOf(t, v1)
	if v1.Version != 1 || v1.Harness != hosting.HarnessClaudeCode || m.Resources.CPUs != 2 || m.Repos[0].Path != "app" ||
		m.IdleStop != "30m" || len(v1.MachineDigest) != 64 || len(v1.MachineSecrets) != 1 {
		t.Fatalf("v1: %+v %+v", v1, m)
	}
	next(t, events, agent.EventUpdated, nil)

	// An unchanged machine makes no version; a stale base conflicts; an
	// instructions edit keeps the digest, a machine edit changes it.
	if code, same := p.saveConfig(pm.ID, func(b map[string]any) { b["machine"] = machine }); code != http.StatusOK || same.Version != 1 {
		t.Fatalf("unchanged: %d %+v", code, same)
	}
	_, v2 := p.saveConfig(pm.ID, func(b map[string]any) { b["instructions"] = "Be brief." })
	if v2.Version != 2 || v2.MachineDigest != v1.MachineDigest {
		t.Fatalf("instructions edit: %+v", v2)
	}
	machine["idle_stop"] = "2h"
	v3 := p.setMachine(pm.ID, "claude-code", machine)
	if v3.Version != 3 || v3.MachineDigest == v1.MachineDigest || machineOf(t, v3).IdleStop != "2h" {
		t.Fatalf("machine edit: %+v", v3)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/config", map[string]any{"base": 1}, nil); code != http.StatusConflict {
		t.Fatalf("stale base: %d", code)
	}
	var old agent.Profile
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+pm.ID.String()+"/config/versions/1", nil, &old); code != http.StatusOK ||
		machineOf(t, old).IdleStop != "30m" {
		t.Fatalf("version 1: %d %+v", code, old)
	}

	// Hosting needs a machine; a person's token and hosting exclude each other.
	bare := p.agent("bare")
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+bare.ID.String()+"/hosting", map[string]any{}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("host without a machine: %d", code)
	}
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	var tok struct {
		ID string `json:"id"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": "laptop", "agent_id": pm.ID}, &tok); code != http.StatusCreated {
		t.Fatalf("mint a person's token: %d", code)
	}
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, nil); code != http.StatusConflict {
		t.Fatalf("host with a person's token live: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/crew/tokens/"+tok.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke the token: %d", code)
	}
	var h hosting.Hosted
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"slots": 2}, &h); code != http.StatusOK ||
		h.Agent != pm.ID || len(h.Slots) != 2 || h.Slots[1].Index != 2 || h.Slots[0].State != hosting.SlotStopped {
		t.Fatalf("host: %d %+v", code, h)
	}
	next(t, events, hosting.EventSlotsUpdated, nil)
	if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": "laptop", "agent_id": pm.ID}, nil); code != http.StatusConflict {
		t.Fatalf("a person's token for a hosted agent: %d", code)
	}
	// The agent can't then lose the secret its machine names.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+pm.ID.String()+"/secrets/GITHUB_TOKEN", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete a secret the machine names: %d", code)
	}

	// Slots are bounded per organization.
	reviewer := p.agent("reviewer")
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/secrets/GITHUB_TOKEN", map[string]any{"value": "x"}, nil); code != http.StatusOK {
		t.Fatalf("set reviewer secret: %d", code)
	}
	p.setMachine(reviewer.ID, "claude-code", machine)
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", map[string]any{"slots": 2}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("slots above the organization's maximum: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("host reviewer: %d", code)
	}
	var all struct {
		Agents  []hosting.Hosted `json:"agents"`
		Runtime bool             `json:"runtime"`
	}
	// This port runs no hosted agents (no runtime): the web says so.
	if code := p.admin(http.MethodGet, "/api/v1/hosting", nil, &all); code != http.StatusOK || len(all.Agents) != 2 || all.Runtime {
		t.Fatalf("hosted agents: %d %+v", code, all)
	}
	// A hosted agent's config can't drop its machine.
	if code, _ := p.saveConfig(reviewer.ID, func(b map[string]any) { b["machine"] = nil }); code != http.StatusUnprocessableEntity {
		t.Fatalf("drop the machine of a hosted agent: %d", code)
	}

	// Unhosting and deleting the agent end its slots.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", nil, nil); code != http.StatusNoContent {
		t.Fatalf("unhost: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unhosted agent's hosting: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+pm.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete agent: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/hosting", nil, &all); code != http.StatusOK || len(all.Agents) != 0 {
		t.Fatalf("hosted agents after delete: %d %+v", code, all)
	}

	// Events and configs never carry a secret value.
	for {
		select {
		case ev := <-events:
			if strings.Contains(string(ev.Data), secretValue) {
				t.Fatalf("event %s carries a secret: %s", ev.Type, ev.Data)
			}
			continue
		default:
		}
		break
	}
	raw, _ := json.Marshal(v3)
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("config carries a secret")
	}
}
