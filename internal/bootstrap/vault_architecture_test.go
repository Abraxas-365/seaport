package bootstrap_test

import (
	"go/ast"
	"path"
	"strings"
	"testing"
)

// vaultWriteCalls: the vault.Commands methods that write entries without
// asking whether the caller may. People reach them through the admin API
// (behind vaults:write); agents must go through vault.Writer's WriteAs and
// DeleteAs, which check the agent's write grant on the vault (its own, or
// its dock's for the conversation it works). The crew's adapters (MCP
// tools, REST) calling the Commands directly would let an agent write to a
// vault it can only read.
var vaultWriteCalls = map[string]bool{"SaveEntry": true, "DeleteEntry": true, "Revert": true}

// vaultAgentCalls: the access-checked calls the crew's MCP tools make.
var vaultAgentCalls = []string{"WriteAs", "DeleteAs"}

func vaultViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if !strings.HasPrefix(path.Dir(f.rel), "crew/adapters/") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && vaultWriteCalls[sel.Sel.Name] {
				out = append(out, f.fset.Position(call.Pos()).String()+": "+sel.Sel.Name+
					" writes a vault without checking the agent's access; call vault.Writer's WriteAs/DeleteAs (vaultWriteCalls)")
			}
			return true
		})
	}
	return out
}

func TestVaultWritesCheckAccess(t *testing.T) {
	files := sources(t, false)
	report(t, vaultViolations(files))
	// The rule must see the calls it points to.
	seen := map[string]bool{}
	for _, f := range files {
		if path.Dir(f.rel) != "crew/adapters/crewmcp" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					seen[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	for _, name := range vaultAgentCalls {
		if !seen[name] {
			t.Errorf("crewmcp no longer calls %s; update the rule", name)
		}
	}
}

func TestVaultRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"crew/adapters/crewmcp/t.go":    `package crewmcp; func f(c interface{ SaveEntry() }) { c.SaveEntry() }`,
		"crew/adapters/crewhttp/h.go":   `package crewhttp; func f(c interface{ DeleteEntry() }) { c.DeleteEntry() }`,
		"crew/adapters/crewmcp/r.go":    `package crewmcp; func f(c interface{ Revert() }) { c.Revert() }`,
		"crew/adapters/crewmcp/ok.go":   `package crewmcp; func f(c interface{ WriteAs() }) { c.WriteAs() }`,
		"vault/adapters/vaulthttp/h.go": `package vaulthttp; func f(c interface{ SaveEntry() }) { c.SaveEntry() }`,
	})
	expectViolations(t, vaultViolations(files), "crewmcp/t.go", "crewhttp/h.go", "crewmcp/r.go")
}
