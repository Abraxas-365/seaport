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
//   - Both rules read net/http registrations (mux.Handle("GET /x", h)) and
//     the Fiber app's (app.Get("/x", h), app.All("/x", h): "METHOD /x"), so
//     a route moved to Fiber keeps being checked.
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
	"GET /healthz":                "public liveness: nothing about any organization (served by the Fiber shell, bootstrap/server.go)",
	"ALL /healthz":                "the 405 for every other method of /healthz, as the mux answered it (Fiber shell)",
	"ALL /*":                      "the fallback: hands every request the shell doesn't serve to the net/http routes, whose rules (this test) apply to each one",
	"GET /github/apps/created":    "GitHub's manifest redirect: checks a sealed, expiring state naming the dock",
	"GET /github/apps/installed":  "GitHub's install redirect: checks a sealed, expiring state, then the installation with GitHub",
	"POST /github/webhook/{dock}": "GitHub's webhooks: checks the HMAC signature with that dock's App secret",
	"POST /jira/webhook/{dock}":   "Jira's webhooks and Automation rules: checks the HMAC signature (X-Hub-Signature) or the bearer secret of that dock, in constant time",
	"GET /slack/oauth/callback":   "Slack's OAuth redirect: checks a sealed, expiring state naming the dock, then the code with Slack",
	"POST /slack/events/{dock}":   "Slack's Events API: checks the HMAC signature (and its five-minute timestamp window) with that dock's app signing secret; url_verification is answered before the secret is known",
	"POST /github/actions/{dock}": "GitHub Actions: checks the job's OIDC token (GitHub's signature, audience = this port) and that the dock's App is installed on its repository",
}

// routeCall is one route registration found in the source: a net/http
// mux.Handle/HandleFunc("METHOD /path", h) or a Fiber app.Get/Post/…/All
// ("/path", h) (the pattern is then "METHOD /path": ALL for All). A Fiber
// call is recognized by a literal path starting with "/" and a handler, so
// c.Get("X-Header") and the like never match.
type routeCall struct {
	call    *ast.CallExpr
	pattern string
	handler ast.Expr
}

var fiberMethods = map[string]string{"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH",
	"Delete": "DELETE", "Head": "HEAD", "Options": "OPTIONS", "All": "ALL"}

func routeCallOf(n ast.Node) (routeCall, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return routeCall{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return routeCall{}, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return routeCall{}, false
	}
	pattern, _ := strconv.Unquote(lit.Value)
	if (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") && len(call.Args) == 2 {
		return routeCall{call, pattern, call.Args[1]}, true
	}
	if m, ok := fiberMethods[sel.Sel.Name]; ok && strings.HasPrefix(pattern, "/") {
		return routeCall{call, m + " " + pattern, call.Args[len(call.Args)-1]}, true
	}
	return routeCall{}, false
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
			rc, ok := routeCallOf(n)
			if !ok {
				return true
			}
			if strings.Contains(rc.pattern, " /api/") || strings.HasPrefix(rc.pattern, "/api/") {
				return true
			}
			if _, ok := publicRoutes[rc.pattern]; ok || authedCall(rc) {
				return true
			}
			if f.rel == "bootstrap/bootstrap.go" && legacyRoot(rc) {
				// A route of the net/http mux behind the Fiber app: Fiber
				// authenticates it first (routes.go), which the check below
				// requires for each of them (removed with the mux).
				if !hasNativeTwin(files, rc.pattern) {
					out = append(out, f.fset.Position(rc.call.Pos()).String()+": route "+rc.pattern+
						" of the legacy mux needs a Fiber route behind auth.Admin/auth.Crew in bootstrap/routes.go")
				}
				return true
			}
			out = append(out, f.fset.Position(rc.call.Pos()).String()+": route "+rc.pattern+
				" must be behind auth.Admin/auth.Crew (or listed in publicRoutes with the proof it checks)")
			return true
		})
	}
	sort.Strings(out)
	return out
}

// legacyRoot: mux.Handle on the net/http mux the Fiber app falls back to.
func legacyRoot(rc routeCall) bool {
	sel, ok := rc.call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "root" && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc")
}

// routeKey is the form routes of both routers compare in: path only, with
// {param} for :param and a trailing "/" for "/*".
func routeKey(pattern string) string {
	_, p, ok := strings.Cut(pattern, " /")
	if ok {
		p = "/" + p
	} else {
		p = pattern
	}
	if strings.HasSuffix(p, "/*") {
		p = strings.TrimSuffix(p, "*")
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if name, ok := strings.CutPrefix(part, ":"); ok {
			parts[i] = "{" + name + "}"
		}
	}
	return strings.Join(parts, "/")
}

// hasNativeTwin: routes.go registers pattern's path behind auth.
func hasNativeTwin(files []goFile, pattern string) bool {
	for _, f := range files {
		if f.rel != "bootstrap/routes.go" {
			continue
		}
		found := false
		ast.Inspect(f.file, func(n ast.Node) bool {
			rc, ok := routeCallOf(n)
			if ok && authedCall(rc) && routeKey(rc.pattern) == routeKey(pattern) {
				found = true
			}
			return !found
		})
		return found
	}
	return false
}

// authedCall: the registration (a Fiber one lists middleware before its
// handler) calls auth.Admin or auth.Crew in any of its arguments.
func authedCall(rc routeCall) bool {
	for _, a := range rc.call.Args[1:] {
		if authed(a) {
			return true
		}
	}
	return false
}

// authed: the expression calls auth.Admin or auth.Crew.
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
			rc, ok := routeCallOf(n)
			if !ok {
				return true
			}
			if !strings.Contains(rc.pattern, " /api/v1/") && !strings.HasPrefix(rc.pattern, "/api/v1/") {
				return true
			}
			if _, ok := unguardedRoutes[rc.pattern]; ok {
				return true
			}
			if !guarded(rc.handler) {
				out = append(out, f.fset.Position(rc.call.Pos()).String()+": route "+rc.pattern+
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
	// stdhttp.Require is the net/http form, for the handlers not migrated to
	// Fiber yet (removed with the last of them).
	if !isSelector(call.Fun, "httpx", "Require") && !isSelector(call.Fun, "httpx", "RequireFunc") &&
		!isSelector(call.Fun, "stdhttp", "Require") && !isSelector(call.Fun, "stdhttp", "RequireFunc") {
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
			rc, ok := routeCallOf(n)
			if !ok || !guarded(rc.handler) {
				return true
			}
			out[rc.pattern] = rc.handler.(*ast.CallExpr).Args[0].(*ast.SelectorExpr).Sel.Name
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
}
func register(app *fiber.App) {
	app.Get("/api/v1/gadgets", httpx.RequireFunc(permission.DocksRead, g.list))
	app.Post("/api/v1/gadgets", g.create)
	app.Delete("/api/v1/gadgets/:id", g.del)
	app.Post("/hooks/gadget", g.hook)
	app.Get("/healthz", g.health)
	app.Get("/crew/gadgets", auth.Crew(), g.crew)
	app.Get("/crew/sprockets", limit, auth.Crew(), g.crew)
	_ = c.Get("X-Request-ID")
}`,
		"permission/permission_gen.go": `package permission
const DocksRead = "seaport:docks:read"`,
	})
	expectViolations(t, routeViolations(files),
		"route POST /api/v1/widgets must be wrapped",
		"route DELETE /api/v1/widgets/{id} must be wrapped",
		"route POST /api/v1/gadgets must be wrapped",
		"route DELETE /api/v1/gadgets/:id must be wrapped",
	)
	expectViolations(t, publicViolations(files),
		"route POST /hooks/widget must be behind auth.Admin/auth.Crew",
		"route POST /hooks/gadget must be behind auth.Admin/auth.Crew")
	expectViolations(t, literalViolations(files), `permission literal "seaport:widgets:write"`)
	expectViolations(t, orphanViolations(files, map[string]string{"DocksRead": "a", "DocksWrite": "b"}),
		"permission.DocksWrite (b) is in the manifest but no code checks it")
}
