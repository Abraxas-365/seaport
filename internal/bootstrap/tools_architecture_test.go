package bootstrap_test

import (
	"go/ast"
	"path"
	"strings"
	"testing"
)

// unscopedReads: the ship.Queries methods that read a conversation's
// history without asking whether the reader is an agent allowed to. People
// reach them through the admin API (behind ships:read); the crew's
// adapters (MCP tools, crew routes) read through AgentHistory, which keeps
// an agent to its docks and their tool switches (dock.Tools: history off,
// files withheld). Attachments need no rule: OpenAttachment itself checks
// the agent it is given. The rule reads calls (dock.Tools has a History
// field, which isn't a read) and method values passed along (a read
// handed to a helper).
var unscopedReads = map[string]bool{"History": true, "ThreadHistory": true}

func crewReadViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if !strings.HasPrefix(path.Dir(f.rel), "crew/adapters/") {
			continue
		}
		flag := func(sel *ast.SelectorExpr) {
			out = append(out, f.fset.Position(sel.Pos()).String()+": "+sel.Sel.Name+
				" reads history without the agent's dock switches; call ship.Queries' AgentHistory (unscopedReads)")
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && unscopedReads[sel.Sel.Name] {
					flag(sel)
				}
				// A method value as an argument: f(q.History).
				for _, a := range n.Args {
					if sel, ok := a.(*ast.SelectorExpr); ok && unscopedReads[sel.Sel.Name] {
						flag(sel)
					}
				}
			case *ast.AssignStmt:
				// read := q.ThreadHistory
				for _, r := range n.Rhs {
					if sel, ok := r.(*ast.SelectorExpr); ok && unscopedReads[sel.Sel.Name] && len(n.Lhs) == len(n.Rhs) {
						if id, ok := n.Lhs[0].(*ast.Ident); ok && id.Name != "_" || n.Tok.String() == ":=" {
							flag(sel)
						}
					}
				}
			}
			return true
		})
	}
	return out
}

func TestCrewReadsCheckTools(t *testing.T) {
	files := sources(t, false)
	report(t, crewReadViolations(files))
	// The rule must see the call it points to.
	seen := false
	for _, f := range files {
		if path.Dir(f.rel) != "crew/adapters/crewmcp" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "AgentHistory" {
				seen = true
			}
			return true
		})
	}
	if !seen {
		t.Error("crewmcp no longer calls AgentHistory; update the rule")
	}
}

func TestCrewReadRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"crew/adapters/crewmcp/t.go":  `package crewmcp; func f(q interface{ History() }) { q.History() }`,
		"crew/adapters/crewhttp/h.go": `package crewhttp; func f(q interface{ ThreadHistory() }) { read := q.ThreadHistory; read() }`,
		"crew/adapters/crewmcp/ok.go": `package crewmcp; func f(q interface{ AgentHistory() }) { q.AgentHistory() }`,
		"crew/adapters/crewmcp/v.go":  `package crewmcp; func g(func()) {}; func f(q interface{ History() }) { g(q.History) }`,
		"crew/adapters/crewmcp/fd.go": `package crewmcp; type T struct{ History bool }; func f(t T) bool { return t.History }`,
		"ship/adapters/shiphttp/h.go": `package shiphttp; func f(q interface{ History() }) { q.History() }`,
	})
	expectViolations(t, crewReadViolations(files), "crewmcp/t.go", "crewhttp/h.go", "crewmcp/v.go")
}
