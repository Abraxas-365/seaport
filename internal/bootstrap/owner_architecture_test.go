package bootstrap_test

import (
	"go/ast"
	"path"
	"testing"
)

// ownerOnly: in docksvc, the gateway calls that open, drive or close a
// dock's connection run only where ownership was checked (cluster.go):
// route sends work to the owner, which runs it in local/sendHere; the
// owner's reconcile restores in restore/ensureRunning. A direct call
// elsewhere would let a non-owner connect a WhatsApp account a second
// time, which replaces the owner's connection.
var ownerOnly = struct{ methods, callers map[string]bool }{
	methods: map[string]bool{"Link": true, "Unlink": true, "Restore": true, "Send": true},
	callers: map[string]bool{"local": true, "sendHere": true, "ensureRunning": true, "restore": true},
}

func ownerViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if path.Dir(f.rel) != "dock/docksvc" {
			continue
		}
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || ownerOnly.callers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && ownerOnly.methods[sel.Sel.Name] {
					out = append(out, f.fset.Position(call.Pos()).String()+": "+fn.Name.Name+" calls "+sel.Sel.Name+
						" (a dock's connection is driven only by its owner: route the work, see ownerOnly)")
				}
				return true
			})
		}
	}
	return out
}

func TestOnlyTheOwnerDrivesADock(t *testing.T) {
	files := sources(t, false)
	for _, v := range ownerViolations(files) {
		t.Error(v)
	}
	// The rule must see the code it guards.
	found := map[string]bool{}
	for _, f := range files {
		if path.Dir(f.rel) != "dock/docksvc" {
			continue
		}
		for _, decl := range f.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && ownerOnly.callers[fn.Name.Name] {
				found[fn.Name.Name] = true
			}
		}
	}
	for name := range ownerOnly.callers {
		if !found[name] {
			t.Errorf("ownerOnly names docksvc.%s, which no longer exists", name)
		}
	}
}
