package bootstrap_test

import (
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/permission"
)

// Permission rules (AGENTS.md "Rules"): permissions come from
// iam/permissions.json through internal/permission, and every admin route
// checks one.
//
//   - Every route registered on a mux with a "/api/v1/" pattern wraps its
//     handler in httpx.Require / httpx.RequireFunc(permission.X, ...),
//     unless it is listed in unguardedRoutes with the reason.
//   - Every other route (outside "/api/v1/", outside the connector, which
//     serves its own machine) is behind auth.Admin / auth.Crew, or listed
//     in publicRoutes with the reason: it then checks its own proof.
//   - No "seaport:…" permission literal outside internal/permission (Go)
//     or web/src/lib/permissions.gen.ts (web): a typo must not compile.
//   - Every permission of the manifest is checked somewhere (no orphan).
//   - Every live event type has a read permission (EventPermissions).

// unguardedRoutes: pattern → why it checks no permission itself.
var unguardedRoutes = map[string]string{
	"GET /api/v1/config":        "public: how the web app signs in; nothing about any organization",
	"POST /api/v1/session":      "public: trades credentials for a session; checks them itself",
	"GET /api/v1/session":       "public: whether this browser is signed in (never 401)",
	"DELETE /api/v1/session":    "any principal may end its own session",
	"GET /api/v1/session/login": "public: starts the IAMKit sign-in (iam mode)",
	"GET /api/v1/events":        "filters every event by its read permission (EventPermissions)",
}

// publicRoutes: route outside /api/v1/ → why no Seaport credential guards it.
var publicRoutes = map[string]string{
	"/":                           "the web UI's static files (the admin mux's catch-all 404 sits behind auth)",
	"GET /healthz":                "public liveness: nothing about any organization",
	"GET /github/apps/created":    "GitHub's manifest redirect: checks a sealed, expiring state naming the dock",
	"GET /github/apps/installed":  "GitHub's install redirect: checks a sealed, expiring state, then the installation with GitHub",
	"POST /github/webhook/{dock}": "GitHub's webhooks: checks the HMAC signature with that dock's App secret",
	"GET /slack/oauth/callback":   "Slack's OAuth redirect: checks a sealed, expiring state naming the dock, then the code with Slack",
	"POST /slack/events/{dock}":   "Slack's Events API: checks the HMAC signature (and its five-minute timestamp window) with that dock's app signing secret; url_verification is answered before the secret is known",
	"POST /github/actions/{dock}": "GitHub Actions: checks the job's OIDC token (GitHub's signature, audience = this port) and that the dock's App is installed on its repository",
}

// publicViolations: literal routes outside /api/v1/ neither behind
// auth.Admin/auth.Crew nor listed in publicRoutes.
func publicViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if roleOf(path.Dir(f.rel)).kind == "client" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			if strings.Contains(pattern, " /api/") || strings.HasPrefix(pattern, "/api/") {
				return true
			}
			if _, ok := publicRoutes[pattern]; ok || authed(call.Args[1]) {
				return true
			}
			out = append(out, f.fset.Position(call.Pos()).String()+": route "+pattern+
				" must be behind auth.Admin/auth.Crew (or listed in publicRoutes with the proof it checks)")
			return true
		})
	}
	sort.Strings(out)
	return out
}

// authed: the handler expression calls auth.Admin or auth.Crew.
func authed(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && (isSelector(call.Fun, "auth", "Admin") || isSelector(call.Fun, "auth", "Crew")) {
			found = true
		}
		return !found
	})
	return found
}

func routeViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			if !strings.Contains(pattern, " /api/v1/") && !strings.HasPrefix(pattern, "/api/v1/") {
				return true
			}
			if _, ok := unguardedRoutes[pattern]; ok {
				return true
			}
			if !guarded(call.Args[1]) {
				out = append(out, f.fset.Position(call.Pos()).String()+": route "+pattern+
					" must be wrapped in httpx.Require/RequireFunc(permission.X, …) (or listed in unguardedRoutes with the reason)")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// guarded: httpx.Require(permission.X, h) or httpx.RequireFunc(permission.X, h).
func guarded(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	if !isSelector(call.Fun, "httpx", "Require") && !isSelector(call.Fun, "httpx", "RequireFunc") {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "permission"
}

// guardedRoutes maps each guarded "/api/v1/" route pattern to the
// permission constant name it requires (e2e tests check every one).
func guardedRoutes(files []goFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 || !guarded(call.Args[1]) {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			out[pattern] = call.Args[1].(*ast.CallExpr).Args[0].(*ast.SelectorExpr).Sel.Name
			return true
		})
	}
	return out
}

// literalViolations: "<prefix>:…" strings outside internal/permission.
func literalViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		if strings.HasPrefix(path.Dir(f.rel), "permission") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, permission.Prefix+":") {
				out = append(out, f.fset.Position(lit.Pos()).String()+": permission literal "+lit.Value+"; use the permission.X constant")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// orphanViolations: permissions nobody references as permission.X.
func orphanViolations(files []goFile, catalog map[string]string) []string {
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasPrefix(path.Dir(f.rel), "permission") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "permission" {
					used[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	var out []string
	for ident, name := range catalog {
		if !used[ident] {
			out = append(out, "permission."+ident+" ("+name+") is in the manifest but no code checks it")
		}
	}
	sort.Strings(out)
	return out
}

// catalogIdents maps each generated constant name to its permission.
func catalogIdents(t *testing.T) map[string]string {
	t.Helper()
	files := sources(t, false)
	out := map[string]string{}
	for _, f := range files {
		if f.rel != "permission/permission_gen.go" {
			continue
		}
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						if s, _ := strconv.Unquote(lit.Value); strings.Contains(s, ":") && strings.HasPrefix(s, permission.Prefix+":") {
							out[name.Name] = s
						}
					}
				}
			}
		}
	}
	if len(out) != len(permission.All) {
		t.Fatalf("found %d permission constants, permission.All has %d", len(out), len(permission.All))
	}
	return out
}

func TestAdminRoutesRequirePermission(t *testing.T) { report(t, routeViolations(sources(t, false))) }
func TestPublicRoutesAreListed(t *testing.T)        { report(t, publicViolations(sources(t, false))) }
func TestNoPermissionLiterals(t *testing.T)         { report(t, literalViolations(sources(t, false))) }
func TestEveryPermissionIsChecked(t *testing.T) {
	report(t, orphanViolations(sources(t, false), catalogIdents(t)))
}

// TestWebUsesGeneratedPermissions: the web app spells permissions only
// through the generated Permission type.
func TestWebUsesGeneratedPermissions(t *testing.T) {
	root := filepath.Join("..", "..", "web", "src")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "permissions.gen.ts" {
			return err
		}
		if ext := filepath.Ext(p); ext != ".ts" && ext != ".tsx" {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, q := range []string{`"`, `'`, "`"} {
			if strings.Contains(string(raw), q+permission.Prefix+":") {
				t.Errorf("%s: permission literal; import Permission from lib/permissions.gen and type it", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEveryEventTypeHasAPermission: each `Event… = "…"` constant of a
// domain root is in bootstrap.EventPermissions with a catalog permission.
func TestEveryEventTypeHasAPermission(t *testing.T) {
	found := 0
	for _, f := range sources(t, false) {
		if roleOf(path.Dir(f.rel)).kind != "domain" {
			continue
		}
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Event") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					typ, _ := strconv.Unquote(lit.Value)
					found++
					perm, ok := bootstrap.EventPermissions[typ]
					if !ok {
						t.Errorf("%s: event %q has no entry in bootstrap.EventPermissions (it would never stream)", f.rel, typ)
					} else if !permission.Has(permission.All, perm) {
						t.Errorf("event %q: %q is not in the catalog", typ, perm)
					}
				}
			}
		}
	}
	if found != len(bootstrap.EventPermissions) {
		t.Errorf("found %d event constants, EventPermissions has %d entries", found, len(bootstrap.EventPermissions))
	}
}

func TestPermissionRulesCatchViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"widget/adapters/widgethttp/handler.go": `package widgethttp
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/widgets", httpx.RequireFunc(permission.DocksRead, h.list))
	mux.Handle("POST /api/v1/widgets", httpx.Handle(h.create))
	mux.Handle("DELETE /api/v1/widgets/{id}", httpx.Require("seaport:widgets:write", h.del))
	mux.Handle("GET /api/v1/session", h.session)
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("POST /hooks/widget", httpx.Handle(h.hook))
	mux.Handle("GET /crew/widgets", auth.Crew(h.crew))
}`,
		"permission/permission_gen.go": `package permission
const DocksRead = "seaport:docks:read"`,
	})
	expectViolations(t, routeViolations(files),
		"route POST /api/v1/widgets must be wrapped",
		"route DELETE /api/v1/widgets/{id} must be wrapped",
	)
	expectViolations(t, publicViolations(files), "route POST /hooks/widget must be behind auth.Admin/auth.Crew")
	expectViolations(t, literalViolations(files), `permission literal "seaport:widgets:write"`)
	expectViolations(t, orphanViolations(files, map[string]string{"DocksRead": "a", "DocksWrite": "b"}),
		"permission.DocksWrite (b) is in the manifest but no code checks it")
}
