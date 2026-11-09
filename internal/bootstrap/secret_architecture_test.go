package bootstrap_test

import (
	"go/ast"
	"path"
	"testing"
)

// secretCalls: the calls that hand out an agent's secrets in clear, and
// the only packages allowed to make them. Equip (the agent's kit) is
// served to the agent's own crew credential by crewhttp (GET
// /crew/profile); OpenSecrets is how agentsvc builds that kit. An admin
// handler, an event or the MCP tools calling either would show secrets to
// someone else (the web never gets one back once written).
var secretCalls = map[string]string{
	"Equip":       "crew/adapters/crewhttp",
	"OpenSecrets": "agent/agentsvc",
}

func secretViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if allowed, guarded := secretCalls[sel.Sel.Name]; guarded && dir != allowed {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+sel.Sel.Name+
					" hands out agent secrets; only "+allowed+" may call it (secretCalls)")
			}
			return true
		})
	}
	return out
}

func TestOnlyTheCrewEquips(t *testing.T) {
	files := sources(t, false)
	report(t, secretViolations(files))
	// The rule must see the calls it allows.
	seen := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && secretCalls[sel.Sel.Name] == path.Dir(f.rel) {
					seen[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	for name, dir := range secretCalls {
		if !seen[name] {
			t.Errorf("secretCalls: %s no longer calls %s; update the rule", dir, name)
		}
	}
}

func TestSecretRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"agent/adapters/agenthttp/h.go": `package agenthttp; func f(k interface{ Equip() }) { k.Equip() }`,
		"crew/adapters/crewmcp/t.go":    `package crewmcp; func f(r interface{ OpenSecrets() }) { r.OpenSecrets() }`,
		"crew/adapters/crewhttp/k.go":   `package crewhttp; func f(k interface{ Equip() }) { k.Equip() }`,
	})
	expectViolations(t, secretViolations(files), "agenthttp/h.go", "crewmcp/t.go")
}
