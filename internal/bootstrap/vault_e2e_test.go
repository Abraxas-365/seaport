package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool calls an MCP tool and answers whether it failed, the error text and
// the structured result.
func tool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (bool, string, map[string]any) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	out := map[string]any{}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &out)
	}
	return res.IsError, text, out
}

// A vault granted write through dock D1 and read to the agent directly:
// the agent writes it from a conversation of D1 and only reads it from one
// of D2 (and with no conversation); a dock it doesn't work gives nothing;
// an agent with no grants has no vault tools; versions can be reverted.
func TestVaultsThroughMCP(t *testing.T) {
	p := startPort(t)
	agentA, agentB := p.agent("vault-a"), p.agent("vault-b")
	d1, d2, d3 := p.openDock("v1"), p.openDock("v2"), p.openDock("v3")
	p.assignAs(adminToken, d1.ID, agentA.ID, true)
	p.assignAs(adminToken, d2.ID, agentA.ID, true)
	p.assignAs(adminToken, d3.ID, agentB.ID, true)
	c1, c2, c3 := p.inject(d1, "1", "m1", "hi").Conversation.String(), p.inject(d2, "2", "m2", "hi").Conversation.String(),
		p.inject(d3, "3", "m3", "hi").Conversation.String()
	tokA, tokB := p.issueAs(adminToken, agentA.ID, "a").Value, p.issueAs(adminToken, agentB.ID, "b").Value

	// No grants yet: no vault tools.
	for _, tl := range []string{"vaults", "memory_search", "memory_read", "memory_save", "memory_delete"} {
		if toolNames(t, mcpSession(t, p, tokA))[tl] {
			t.Fatalf("%s listed for an agent with no vault", tl)
		}
	}

	var v vault.Vault
	if code := p.admin(http.MethodPost, "/api/v1/vaults", map[string]string{"name": "company", "title": "Company", "description": "what we know"}, &v); code != http.StatusCreated {
		t.Fatalf("create vault: %d", code)
	}
	id := "/api/v1/vaults/" + v.ID.String()
	if code := p.admin(http.MethodPost, "/api/v1/vaults", map[string]string{"name": "Bad Name"}, nil); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("bad vault name: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/vaults", map[string]string{"name": "company"}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate vault name: %d", code)
	}
	if code := p.admin(http.MethodPut, id+"/docks/"+d1.ID.String(), map[string]string{"mode": "write"}, nil); code != http.StatusOK {
		t.Fatalf("grant dock: %d", code)
	}
	if code := p.admin(http.MethodPut, id+"/agents/"+agentA.ID.String(), map[string]string{"mode": "read"}, nil); code != http.StatusOK {
		t.Fatalf("grant agent: %d", code)
	}
	if code := p.admin(http.MethodPut, id+"/agents/"+agentA.ID.String(), map[string]string{"mode": "nope"}, nil); code == http.StatusOK {
		t.Fatal("a bad mode was accepted")
	}
	// A dock the agent doesn't work also holds the vault.
	if code := p.admin(http.MethodPut, id+"/docks/"+d3.ID.String(), map[string]string{"mode": "write"}, nil); code != http.StatusOK {
		t.Fatalf("grant dock 3: %d", code)
	}

	a := mcpSession(t, p, tokA)
	names := toolNames(t, a)
	for _, tl := range []string{"vaults", "memory_search", "memory_read", "memory_save", "memory_delete"} {
		if !names[tl] {
			t.Fatalf("%s not listed for an agent with a vault", tl)
		}
	}
	// Agent B reaches the vault only through D3, a dock it works: it has tools.
	if !toolNames(t, mcpSession(t, p, tokB))["memory_save"] {
		t.Fatal("agent B works a dock with a vault but has no tools")
	}

	// Write from D1's conversation: allowed.
	failed, msg, out := tool(t, a, "memory_save", map[string]any{"vault": "company", "name": "hours", "body": "# Hours\nWe open at 9.", "conversation_id": c1})
	if failed {
		t.Fatalf("save from D1: %s", msg)
	}
	if out["version"] != float64(1) || !strings.Contains(out["source"].(map[string]any)["description"].(string), "written by agent vault-a") {
		t.Fatalf("save result: %+v", out)
	}
	// Same vault from D2's conversation, or none: read-only.
	for name, conv := range map[string]any{"D2": c2, "none": nil, "D3 (not its dock)": c3} {
		args := map[string]any{"vault": "company", "name": "hours2", "body": "x"}
		if conv != nil {
			args["conversation_id"] = conv
		}
		if failed, msg, _ := tool(t, a, "memory_save", args); !failed || !strings.Contains(msg, "read-only") {
			t.Fatalf("save from %s: failed=%v %q", name, failed, msg)
		}
		del := map[string]any{"vault": "company", "name": "hours"}
		if conv != nil {
			del["conversation_id"] = conv
		}
		if failed, msg, _ := tool(t, a, "memory_delete", del); !failed || !strings.Contains(msg, "read-only") {
			t.Fatalf("delete from %s: failed=%v %q", name, failed, msg)
		}
	}
	// Reading and searching work everywhere the agent holds the vault.
	if failed, msg, out := tool(t, a, "memory_read", map[string]any{"vault": "company", "name": "hours", "conversation_id": c2}); failed || !strings.Contains(out["body"].(string), "open at 9") {
		t.Fatalf("read from D2: %v %s %+v", failed, msg, out)
	}
	if failed, msg, out := tool(t, a, "memory_search", map[string]any{"query": "open", "conversation_id": c2}); failed || len(out["hits"].([]any)) != 1 {
		t.Fatalf("search: %v %s %+v", failed, msg, out)
	}
	if failed, _, out := tool(t, a, "vaults", map[string]any{"conversation_id": c2}); failed {
		t.Fatal("vaults failed")
	} else if vs := out["vaults"].([]any); len(vs) != 1 || vs[0].(map[string]any)["mode"] != "read" || len(vs[0].(map[string]any)["index"].([]any)) != 1 {
		t.Fatalf("vaults on D2: %+v", out)
	}
	if _, _, out := tool(t, a, "vaults", map[string]any{"conversation_id": c1}); out["vaults"].([]any)[0].(map[string]any)["mode"] != "write" {
		t.Fatalf("vaults on D1: %+v", out)
	}

	// Edit: needs the base; a stale or missing one conflicts.
	if failed, _, _ := tool(t, a, "memory_save", map[string]any{"vault": "company", "name": "hours", "body": "new", "conversation_id": c1}); !failed {
		t.Fatal("overwriting without base was accepted")
	}
	if failed, msg, _ := tool(t, a, "memory_save", map[string]any{"vault": "company", "name": "hours", "body": "# Hours\nOpen at 10.", "base": 1, "conversation_id": c1}); failed {
		t.Fatalf("edit: %s", msg)
	}
	if failed, _, _ := tool(t, a, "memory_save", map[string]any{"vault": "company", "name": "hours", "body": "stale", "base": 1, "conversation_id": c1}); !failed {
		t.Fatal("a stale base was accepted")
	}
	// A credential-looking body is refused without echoing it.
	secret := "sk-ant-" + strings.Repeat("a", 40)
	if failed, msg, _ := tool(t, a, "memory_save", map[string]any{"vault": "company", "name": "keys", "body": "use " + secret, "conversation_id": c1}); !failed || strings.Contains(msg, secret) {
		t.Fatalf("secret body: failed=%v %q", failed, msg)
	}

	// The admin API sees the entry, its versions, and reverts.
	var list query.Paginated[vault.Summary]
	if code := p.admin(http.MethodGet, id+"/entries", nil, &list); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Version != 2 {
		t.Fatalf("entries: %d %+v", code, list)
	}
	e := id + "/entries/" + list.Items[0].ID.String()
	var vs struct{ Versions []vault.Version }
	if code := p.admin(http.MethodGet, e+"/versions", nil, &vs); code != http.StatusOK || len(vs.Versions) != 2 || vs.Versions[0].Provenance.By != "vault-a" {
		t.Fatalf("versions: %d %+v", code, vs)
	}
	var reverted vault.Entry
	if code := p.admin(http.MethodPost, e+"/revert", map[string]int{"version": 1}, &reverted); code != http.StatusOK || reverted.Version != 3 || !strings.Contains(reverted.Body, "open at 9") || reverted.Provenance.Kind != vault.WrittenByUser {
		t.Fatalf("revert: %d %+v", code, reverted)
	}
	var detail struct {
		Entries int
		Agents  []vault.AgentGrant
		Docks   []vault.DockGrant
	}
	if code := p.admin(http.MethodGet, id, nil, &detail); code != http.StatusOK || detail.Entries != 1 || len(detail.Agents) != 1 || len(detail.Docks) != 2 {
		t.Fatalf("detail: %d %+v", code, detail)
	}

	// Delete from D1; the versions stay for people.
	if failed, msg, _ := tool(t, a, "memory_delete", map[string]any{"vault": "company", "name": "hours", "conversation_id": c1}); failed {
		t.Fatalf("delete: %s", msg)
	}
	if failed, _, _ := tool(t, a, "memory_read", map[string]any{"vault": "company", "name": "hours"}); !failed {
		t.Fatal("deleted entry still reads")
	}
	if code := p.admin(http.MethodGet, e+"/versions", nil, nil); code != http.StatusNotFound && code != http.StatusOK {
		t.Fatalf("versions after delete: %d", code)
	}

	// Revoking the agent's own grant leaves it D1's; revoking that too ends its tools.
	if code := p.admin(http.MethodDelete, id+"/agents/"+agentA.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke agent: %d", code)
	}
	if code := p.admin(http.MethodDelete, id+"/docks/"+d1.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke dock: %d", code)
	}
	if toolNames(t, mcpSession(t, p, tokA))["memory_save"] {
		t.Fatal("tools listed after every grant was revoked")
	}
	if code := p.admin(http.MethodDelete, id, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete vault: %d", code)
	}
	if code := p.admin(http.MethodGet, id, nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleted vault: %d", code)
	}
}
