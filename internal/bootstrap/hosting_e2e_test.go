package bootstrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Environments are versioned specs (optimistic like profiles; an unchanged
// spec makes no version), keyed by name too; an agent is hosted on one in
// slots (bounded per organization), its repo credentials must be secrets
// the agent has, and an environment with agents can't be deleted.
// Deleting the agent unhosts it.
func TestEnvironmentsAndHosting(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-hosting-secrets-key-0123456789abcdefXYZ"
		c.MaxSlots, c.HostMaxCPUs = 3, 4
	}, bootstrap.Options{})
	events := p.openEvents("")
	pm := p.agent("pm")

	spec := map[string]any{"harness": "claude-code", "packages": []string{"jq"},
		"repos":     []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${GITHUB_TOKEN}"}},
		"network":   map[string]any{"egress": []string{"registry.npmjs.org"}},
		"resources": map[string]string{"size": "M"}}
	var e hosting.Environment
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, &e); code != http.StatusCreated ||
		e.Version != 1 || e.Spec.Resources.CPUs != 2 || e.Spec.Repos[0].Path != "app" || e.Spec.IdleStop != "30m" {
		t.Fatalf("create: %d %+v", code, e)
	}
	next(t, events, hosting.EventEnvironmentUpdated, nil)
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate name: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "big",
		"spec": map[string]any{"harness": "rness", "resources": map[string]any{"cpus": 8, "memory_mb": 1024}}}, nil); code != http.StatusBadRequest {
		t.Fatalf("cpus above the operator's maximum: %d", code)
	}

	// Found by name; an unchanged spec makes no version; a stale base
	// conflicts.
	var byName hosting.Environment
	if code := p.admin(http.MethodGet, "/api/v1/environments/web", nil, &byName); code != http.StatusOK || byName.ID != e.ID {
		t.Fatalf("by name: %d %+v", code, byName)
	}
	var same hosting.Environment
	if code := p.admin(http.MethodPut, "/api/v1/environments/web", map[string]any{"base": 1, "spec": spec}, &same); code != http.StatusOK || same.Version != 1 {
		t.Fatalf("unchanged: %d %+v", code, same)
	}
	spec["idle_stop"] = "2h"
	var v2 hosting.Environment
	if code := p.admin(http.MethodPut, "/api/v1/environments/"+e.ID.String(), map[string]any{"base": 1, "spec": spec}, &v2); code != http.StatusOK ||
		v2.Version != 2 || v2.Spec.IdleStop != "2h" {
		t.Fatalf("v2: %d %+v", code, v2)
	}
	if code := p.admin(http.MethodPut, "/api/v1/environments/web", map[string]any{"base": 1, "spec": spec}, nil); code != http.StatusConflict {
		t.Fatalf("stale base: %d", code)
	}
	var old hosting.Environment
	if code := p.admin(http.MethodGet, "/api/v1/environments/web/versions/1", nil, &old); code != http.StatusOK || old.Spec.IdleStop != "30m" {
		t.Fatalf("version 1: %d %+v", code, old)
	}
	var versions struct {
		Versions []hosting.Version `json:"versions"`
	}
	if code := p.admin(http.MethodGet, "/api/v1/environments/web/versions", nil, &versions); code != http.StatusOK || len(versions.Versions) != 2 {
		t.Fatalf("versions: %d %+v", code, versions)
	}

	// Hosting needs the secrets the repos name.
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "web"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("host without the credential secret: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+pm.ID.String()+"/secrets/GITHUB_TOKEN", map[string]any{"value": secretValue}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	var h hosting.Hosted
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "web", "slots": 2}, &h); code != http.StatusOK ||
		h.Environment != e.ID || len(h.Slots) != 2 || h.Slots[1].Index != 2 || h.Slots[0].State != hosting.SlotStopped {
		t.Fatalf("host: %d %+v", code, h)
	}
	next(t, events, hosting.EventSlotsUpdated, nil)
	// The agent can't then lose the secret its environment uses, nor get
	// a spec naming one it lacks.
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+pm.ID.String()+"/secrets/GITHUB_TOKEN", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete a secret the environment uses: %d", code)
	}
	spec["repos"] = []map[string]string{{"url": "https://github.com/acme/app.git", "credential": "${OTHER_TOKEN}"}}
	if code := p.admin(http.MethodPut, "/api/v1/environments/web", map[string]any{"base": 2, "spec": spec}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("spec naming a secret a hosted agent lacks: %d", code)
	}

	// Slots are bounded per organization.
	reviewer := p.agent("reviewer")
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/secrets/GITHUB_TOKEN", map[string]any{"value": "x"}, nil); code != http.StatusOK {
		t.Fatalf("set reviewer secret: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", map[string]any{"environment": "web", "slots": 2}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("slots above the organization's maximum: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", map[string]any{"environment": e.ID.String()}, nil); code != http.StatusOK {
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
	var listed query.Paginated[hosting.Environment]
	if code := p.admin(http.MethodGet, "/api/v1/environments", nil, &listed); code != http.StatusOK || len(listed.Items) != 1 || listed.Items[0].Agents != 2 {
		t.Fatalf("list: %d %+v", code, listed)
	}

	// An environment in use can't be deleted; unhosting and deleting the
	// agent free it.
	if code := p.admin(http.MethodDelete, "/api/v1/environments/web", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete in use: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", nil, nil); code != http.StatusNoContent {
		t.Fatalf("unhost: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+reviewer.ID.String()+"/hosting", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unhosted agent's hosting: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/agents/"+pm.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete agent: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/environments/web", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	next(t, events, hosting.EventEnvironmentDeleted, nil)

	// Events and specs never carry a secret value.
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
	raw, _ := json.Marshal(v2)
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("environment carries a secret")
	}
}
