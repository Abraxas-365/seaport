package bootstrap_test

import (
	"go/ast"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Fiber rules (AGENTS.md "HTTP server"):
//
//   - A fiber.Ctx is pooled: fasthttp reuses it for the next request once the
//     handler returns, so a goroutine that outlives the handler and touches
//     it reads (or writes) another request. No `go` statement uses the
//     fiber.Ctx of the function it sits in, neither in a closure nor as the
//     call's receiver or argument. Take what the goroutine needs first
//     (c.Context(), the principal, copies of the values) and hand it over.

// ctxViolations: `go` statements capturing a fiber.Ctx parameter.
func ctxViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		seen := map[ast.Node]bool{}
		check := func(typ *ast.FuncType, body *ast.BlockStmt) {
			if body == nil {
				return
			}
			ctxs := fiberCtxParams(typ)
			if len(ctxs) == 0 {
				return
			}
			ast.Inspect(body, func(n ast.Node) bool {
				g, ok := n.(*ast.GoStmt)
				if !ok || seen[g] {
					return true
				}
				if name := capturedCtx(g.Call, ctxs); name != "" {
					seen[g] = true
					out = append(out, f.fset.Position(g.Pos()).String()+": go statement uses the fiber.Ctx "+name+
						", which fasthttp reuses once the handler returns; hand the goroutine what it needs (c.Context(), copied values), not the Ctx")
				}
				return true
			})
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				check(fn.Type, fn.Body)
			case *ast.FuncLit:
				check(fn.Type, fn.Body)
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// fiberCtxParams: names of the parameters typed fiber.Ctx or *fiber.Ctx.
func fiberCtxParams(typ *ast.FuncType) map[string]bool {
	names := map[string]bool{}
	for _, field := range typ.Params.List {
		t := field.Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		if isSelector(t, "fiber", "Ctx") {
			for _, n := range field.Names {
				names[n.Name] = true
			}
		}
	}
	return names
}

// capturedCtx: the Ctx name a go statement's call uses, or "". A closure
// uses it by naming it anywhere in its body (unless it declares its own
// parameter of that name); another call by naming it as the receiver or as
// an argument itself (c.Context() as an argument is evaluated before the
// goroutine starts, so it is fine).
func capturedCtx(call *ast.CallExpr, ctxs map[string]bool) string {
	found := ""
	if lit, ok := call.Fun.(*ast.FuncLit); ok {
		own := map[string]bool{}
		for _, field := range lit.Type.Params.List {
			for _, n := range field.Names {
				own[n.Name] = true
			}
		}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && ctxs[id.Name] && !own[id.Name] && found == "" {
				found = id.Name
			}
			return found == ""
		})
		return found
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && ctxs[id.Name] {
			return id.Name
		}
	}
	for _, arg := range call.Args {
		if id, ok := arg.(*ast.Ident); ok && ctxs[id.Name] {
			return id.Name
		}
	}
	return ""
}

func TestNoFiberCtxInGoroutines(t *testing.T) { report(t, ctxViolations(sources(t, true))) }

func TestFiberCtxRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"widget/adapters/widgethttp/ok.go": `package widgethttp
func (h *H) ok(c fiber.Ctx) error {
	ctx, id := c.Context(), c.Params("id")
	go h.work(ctx, id)
	go h.work(c.Context(), c.Params("id"))
	go func(c int) { _ = c }(1)
	go func() { h.other(ctx, id) }()
	return nil
}
func (h *H) notAHandler(c *gin.Context) { go func() { _ = c }() }`,
		"widget/adapters/widgethttp/bad.go": `package widgethttp
func (h *H) closure(c fiber.Ctx) error {
	go func() { _ = c.Params("id") }()
	return nil
}
func (h *H) arg(c fiber.Ctx) error {
	go h.work(c)
	return nil
}
func (h *H) receiver(c *fiber.Ctx) error {
	go c.Next()
	return nil
}
func register(app *fiber.App) {
	app.Get("/x", func(ctx fiber.Ctx) error {
		go func() { ctx.Send(nil) }()
		return nil
	})
}`,
	})
	expectViolations(t, ctxViolations(files),
		"bad.go:3",
		"bad.go:7",
		"bad.go:11",
		"bad.go:16",
	)
}

// netHTTPServers: where net/http may route or listen, and why.
var netHTTPServers = map[string]string{
	"connector":                      "the loopback MCP relay of `seaport agent-run`, on the user's machine",
	"hosting/adapters/hostingegress": "the slots' egress proxy: CONNECT tunnels and plain-http forwarding on its own port",
	"hosting/hostingmodule":          "runs the egress proxy's http.Server for the adapter above",
	"httpx/stdhttp":                  "the adapter that mounts a net/http handler on a Fiber route",
}

// stdhttpUsers: who imports httpx/stdhttp (the adapter for vendor handlers).
var stdhttpUsers = []string{
	"crew/adapters/crewmcp", "crew/crewmodule", "hosting/adapters/hostinggateway", "hosting/hostingmodule",
}

var routingNames = map[string]bool{"NewServeMux": true, "ServeMux": true, "Server": true, "ListenAndServe": true, "ListenAndServeTLS": true}

// routingViolations: net/http routing or serving outside netHTTPServers, and
// stdhttp imported outside stdhttpUsers.
func routingViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := pathDir(f.rel)
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == modulePath+"httpx/stdhttp" && dir != "httpx/stdhttp" && !ownedBy(dir, stdhttpUsers) {
				out = append(out, f.fset.Position(imp.Pos()).String()+": httpx/stdhttp is for the packages that build or mount a vendor net/http handler ("+
					strings.Join(stdhttpUsers, ", ")+"); "+dir+" must not import it")
			}
		}
		if ownedBy(dir, mapKeys(netHTTPServers)) {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && isSelector(sel, "http", sel.Sel.Name) && routingNames[sel.Sel.Name] {
				out = append(out, f.fset.Position(sel.Pos()).String()+": http."+sel.Sel.Name+
					" routes or serves with net/http; routes belong to the Fiber app (bootstrap/routes.go), see netHTTPServers for the exceptions")
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

func pathDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestNoNetHTTPRouting(t *testing.T) { report(t, routingViolations(sources(t, false))) }

func TestRoutingRuleCatchesViolations(t *testing.T) {
	m := modulePath
	files := parseFixtures(t, map[string]string{
		"connector/relay.go": `package connector
func serve() { mux := http.NewServeMux(); srv := &http.Server{Handler: mux}; _ = srv }`,
		"hosting/adapters/hostingegress/p.go": `package hostingegress
func s() *http.Server { return &http.Server{} }`,
		"crew/crewmodule/m.go": `package crewmodule; import _ "` + m + `httpx/stdhttp"`,
		"widget/adapters/widgethttp/ok.go": `package widgethttp
func h(w http.ResponseWriter, r *http.Request) { _ = http.StatusOK }`,
		"widget/adapters/widgethttp/mux.go": `package widgethttp
func r() { mux := http.NewServeMux(); _ = mux }`,
		"widget/adapters/widgethttp/srv.go": `package widgethttp
func s() { _ = http.ListenAndServe(":80", nil) }`,
		"widget/adapters/widgethttp/std.go": `package widgethttp; import _ "` + m + `httpx/stdhttp"`,
	})
	expectViolations(t, routingViolations(files),
		"widgethttp/mux.go:2:19: http.NewServeMux routes or serves with net/http",
		"widgethttp/srv.go:2:16: http.ListenAndServe routes or serves with net/http",
		"widgethttp/std.go:1:28: httpx/stdhttp is for the packages that build or mount a vendor net/http handler",
	)
}
