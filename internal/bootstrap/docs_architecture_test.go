package bootstrap_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
)

// Documentation drift (AGENTS.md "Rules"): the repository is public, and
// docs/reference/*.md is the contract people read, so each reference page is
// compared with the code it describes. No database needed.
//
//   - http-api.md     every Fiber route (admin /api/v1, crew, public) ↔ the
//     "Method | Path" tables, both ways.
//   - configuration.md  every SEAPORT_*/IAMKIT_*/DATABASE_URL variable the
//     code reads ↔ the first column of its tables, both ways.
//   - mcp-tools.md    every tool crewmcp registers ↔ the Tools table.
//   - cli.md          every cmd/seaport subcommand (the cases of its dispatch
//     switches) ↔ the `seaport …` commands the page names.
//   - events.md       bootstrap.EventPermissions ↔ the Event types table.
//   - permissions.md  iam/permissions.json (permissions, roles) ↔ the
//     Catalog and Roles tables.
//   - crew-protocol.md  the crew.Frame… constants ↔ the Frames table.
//   - limits.md       the Go names the doc cites exist, and every
//     configurable limit variable is listed (the doc chooses which constants
//     to list, so a new constant is not required there: one direction).
//
// Each check is a function over (code facts, document text) returning
// violations, so it is proven on a wrong document
// (TestDocsRulesCatchViolations) and run on the real ones.

const docsRefDir = "../../docs/reference"

func readDoc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(docsRefDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ---- markdown helpers ----

type mdTable struct {
	section string     // the nearest "## " heading
	header  []string   // cells of the header row
	rows    [][]string // cells of each body row
}

var fence = regexp.MustCompile("(?s)```.*?```")

// mdCells splits one table row into trimmed cells; "\|" stays inside a cell.
func mdCells(line string) []string {
	line = strings.ReplaceAll(strings.TrimSpace(line), `\|`, "\x00")
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(strings.ReplaceAll(c, "\x00", "|"))
	}
	return cells
}

// mdTables reads every pipe table of a document (fenced code ignored).
func mdTables(doc string) []mdTable {
	doc = fence.ReplaceAllString(doc, "")
	var out []mdTable
	section := ""
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if h, ok := strings.CutPrefix(line, "## "); ok {
			section = strings.TrimSpace(h)
			continue
		}
		if !strings.HasPrefix(line, "|") || i+1 >= len(lines) || !strings.Contains(lines[i+1], "---") {
			continue
		}
		t := mdTable{section: section, header: mdCells(line)}
		i += 2
		for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
			t.rows = append(t.rows, mdCells(lines[i]))
		}
		i--
		out = append(out, t)
	}
	return out
}

// tablesWith: the tables whose header starts with the given cells.
func tablesWith(doc string, header ...string) []mdTable {
	var out []mdTable
	for _, t := range mdTables(doc) {
		if len(t.header) < len(header) {
			continue
		}
		match := true
		for i, h := range header {
			match = match && t.header[i] == h
		}
		if match {
			out = append(out, t)
		}
	}
	return out
}

var codeSpan = regexp.MustCompile("`([^`\n]+)`")

func spans(s string) []string {
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// missingFrom: each element of want absent from got, as format with %s
// replaced by the element, sorted.
func missingFrom(want, got map[string]bool, format string) []string {
	var out []string
	for x := range want {
		if !got[x] {
			out = append(out, strings.ReplaceAll(format, "%s", x))
		}
	}
	sort.Strings(out)
	return out
}

func setOf(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

func keys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// bothWays: code ∖ doc and doc ∖ code, with the two message formats.
func bothWays(code, doc map[string]bool, undocumented, unknown string) []string {
	out := append(missingFrom(code, doc, undocumented), missingFrom(doc, code, unknown)...)
	sort.Strings(out)
	return out
}

// ---- Go helpers ----

// stringConsts: package-level string constants by "pkg.Name" (pkg = the
// package clause), so a route or env name given as hosting.GatewayPath or
// hosting.EnvURL resolves.
func stringConsts(files []goFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		pkg := f.file.Name.Name
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							out[pkg+"."+name.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}
	return out
}

// constString evaluates a string expression made of literals, constants
// (pkg.Name, or Name in package pkg) and "+".
func constString(e ast.Expr, pkg string, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return constString(e.X, pkg, consts)
	case *ast.Ident:
		s, ok := consts[pkg+"."+e.Name]
		return s, ok
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			s, ok := consts[id.Name+"."+e.Sel.Name]
			return s, ok
		}
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok1 := constString(e.X, pkg, consts)
		r, ok2 := constString(e.Y, pkg, consts)
		return l + r, ok1 && ok2
	}
	return "", false
}

// cmdSources parses the non-test files of cmd/seaport (rel "cmd/seaport/…").
func cmdSources(t *testing.T) []goFile {
	t.Helper()
	paths, err := filepath.Glob("../../cmd/seaport/*.go")
	if err != nil || len(paths) == 0 {
		t.Fatal("no cmd/seaport sources", err)
	}
	fset := token.NewFileSet()
	var out []goFile
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, goFile{rel: "cmd/seaport/" + filepath.Base(p), file: f, fset: fset})
	}
	return out
}

// ---- 1. http-api.md ----

// Routes registered but not listed by pattern: the catch-alls, and the
// routes the doc names in another form.
var (
	docSkipRoutes = map[string]string{
		"ALL /*":       "the web app (documented in prose: everything else is the web app)",
		"ALL /api/*":   "the 404 of unknown /api/ routes (documented in Requests)",
		"ALL /healthz": "the 405 of /healthz (documented in Requests)",
	}
	docAllRoutes = map[string]string{
		"ALL /mcp":       "POST /mcp",
		"ALL /gateway/*": "ALL /gateway/anthropic/…",
	}
)

var (
	docParam = regexp.MustCompile(`\{[^}/]*\}`)
	docRest  = regexp.MustCompile(`\{[^}/]*…\}`)
)

// normRoute spells "METHOD /path" with every parameter as {} and a
// trailing wildcard as *, so Fiber's :id, net/http's {id} and the doc's
// {path…} compare (parameter names are not compared).
func normRoute(method, path string) string {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "{}"
		}
	}
	path = strings.Join(parts, "/")
	path = docRest.ReplaceAllString(path, "*")
	path = docParam.ReplaceAllString(path, "{}")
	return method + " " + path
}

// codeRoutes: every Fiber registration with a literal or constant path.
func codeRoutes(files []goFile) map[string]string {
	consts := stringConsts(files)
	out := map[string]string{}
	for _, f := range files {
		if roleOf(path.Dir(f.rel)).kind == "client" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method, ok := fiberMethods[sel.Sel.Name]
			if !ok {
				return true
			}
			p, ok := constString(call.Args[0], f.file.Name.Name, consts)
			if !ok || !strings.HasPrefix(p, "/") {
				return true
			}
			out[normRoute(method, p)] = f.fset.Position(call.Pos()).String()
			return true
		})
	}
	return out
}

// docRoutes: the rows of the "Method | Path" tables. Under "Admin routes"
// paths are relative to /api/v1.
func docRoutes(doc string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tablesWith(doc, "Method", "Path") {
		for _, row := range t.rows {
			m, p := spans(row[0]), spans(row[1])
			if len(m) != 1 || len(p) != 1 {
				continue
			}
			method, path := m[0], p[0]
			if method == "*" {
				method = "ALL"
			}
			if t.section == "Admin routes" && !strings.HasPrefix(path, "/api/") {
				path = "/api/v1" + path
			}
			out[normRoute(method, path)] = true
		}
	}
	return out
}

func routeDocViolations(code map[string]string, doc string) []string {
	want := map[string]bool{}
	for pattern := range code {
		if _, skip := docSkipRoutes[pattern]; skip {
			continue
		}
		if listed, ok := docAllRoutes[pattern]; ok {
			pattern = listed
		} else if strings.HasPrefix(pattern, "ALL ") {
			want[pattern+" (a catch-all route: map it in docAllRoutes or docSkipRoutes)"] = true
			continue
		}
		want[pattern] = true
	}
	return bothWays(want, docRoutes(doc),
		"http-api.md: route %s is registered but not documented",
		"http-api.md: route %s is documented but not registered")
}

func TestDocsHTTPAPI(t *testing.T) {
	code := codeRoutes(sources(t, false))
	if len(code) < 100 {
		t.Fatalf("found only %d routes: the rule checks nothing", len(code))
	}
	report(t, routeDocViolations(code, readDoc(t, "http-api.md")))
}

// ---- 2. configuration.md ----

var envName = regexp.MustCompile(`^(SEAPORT_[A-Z0-9_]+|IAMKIT_[A-Z0-9_]+|DATABASE_URL)$`)

// testOnlyEnv: the test suite's own switches (SEAPORT_TEST_DATABASE_URL,
// SEAPORT_TEST_IAMKIT, the acptest echo agent's variables). They are read by
// test support packages, not by a shipped binary, and live in AGENTS.md and
// docs/contributing, not in configuration.md.
var testOnlyEnv = regexp.MustCompile(`^SEAPORT_TEST_`)

var envReaders = map[string]bool{"Getenv": true, "LookupEnv": true, "env": true, "envInt": true, "envDuration": true, "envOr": true}

// codeEnvKeys: the variables the code reads outside tests: an argument of
// os.Getenv/LookupEnv or of config's/cmd's env helpers (a literal or a
// constant, like hosting.EnvURL), plus every exact literal of internal/config
// (its limits are read through a table of keys).
func codeEnvKeys(files []goFile) map[string]string {
	consts := stringConsts(files)
	out := map[string]string{}
	for _, f := range files {
		pkg := f.file.Name.Name
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				var name string
				switch fn := n.Fun.(type) {
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				case *ast.Ident:
					name = fn.Name
				}
				if !envReaders[name] || len(n.Args) == 0 {
					return true
				}
				if s, ok := constString(n.Args[0], pkg, consts); ok && envName.MatchString(s) && !testOnlyEnv.MatchString(s) {
					out[s] = f.fset.Position(n.Pos()).String()
				}
			case *ast.BasicLit:
				if pkg == "config" && n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil && envName.MatchString(s) && !testOnlyEnv.MatchString(s) {
						out[s] = f.fset.Position(n.Pos()).String()
					}
				}
			}
			return true
		})
	}
	return out
}

// docEnvKeys: the variables named in the first column of the doc's tables.
func docEnvKeys(doc string) map[string]bool {
	out := map[string]bool{}
	for _, t := range mdTables(doc) {
		for _, row := range t.rows {
			for _, s := range spans(row[0]) {
				if envName.MatchString(s) {
					out[s] = true
				}
			}
		}
	}
	return out
}

func envDocViolations(code map[string]string, doc string) []string {
	return bothWays(keys(code), docEnvKeys(doc),
		"configuration.md: variable %s is read by the code but not documented",
		"configuration.md: variable %s is documented but nothing reads it")
}

func TestDocsConfiguration(t *testing.T) {
	files := append(sources(t, false), cmdSources(t)...)
	code := codeEnvKeys(files)
	if len(code) < 40 {
		t.Fatalf("found only %d variables: the rule checks nothing", len(code))
	}
	report(t, envDocViolations(code, readDoc(t, "configuration.md")))
}

// ---- 3. mcp-tools.md ----

// codeMCPTools: the Name of every &mcp.Tool{…} crewmcp registers.
func codeMCPTools(files []goFile) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if filepath.ToSlash(filepath.Dir(f.rel)) != "crew/adapters/crewmcp" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !isSelectorType(cl.Type, "mcp", "Tool") {
				return true
			}
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Name" {
					if lit, ok := kv.Value.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						out[s] = true
					}
				}
			}
			return true
		})
	}
	return out
}

func isSelectorType(e ast.Expr, pkg, name string) bool {
	return e != nil && types.ExprString(e) == pkg+"."+name
}

func firstColumn(doc string, header ...string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tablesWith(doc, header...) {
		for _, row := range t.rows {
			if s := spans(row[0]); len(s) == 1 {
				out[s[0]] = true
			}
		}
	}
	return out
}

func mcpDocViolations(code map[string]bool, doc string) []string {
	return bothWays(code, firstColumn(doc, "Tool", "Always there"),
		"mcp-tools.md: tool %s is registered by crewmcp but not in the Tools table",
		"mcp-tools.md: tool %s is in the Tools table but crewmcp registers none")
}

func TestDocsMCPTools(t *testing.T) {
	code := codeMCPTools(sources(t, false))
	if len(code) < 8 {
		t.Fatalf("found only %d tools: the rule checks nothing", len(code))
	}
	report(t, mcpDocViolations(code, readDoc(t, "mcp-tools.md")))
}

// ---- 4. cli.md ----

// cliSwitches: the switches of cmd/seaport that dispatch commands, as
// "function/tag" → the command words that lead to them. A new dispatch
// switch (tag sub, cmd, args[0] or rest[0]) must be listed, so a new group
// of commands can't skip the check.
var cliSwitches = map[string]string{
	"run/cmd":                 "seaport",
	"run/rest[0]":             "seaport dock",
	"dockDestination/args[0]": "seaport dock destination",
	"thread/args[0]":          "seaport thread",
	"agents/sub":              "seaport agents",
	"hostCmd/sub":             "seaport agents",
	"profileCmd/sub":          "seaport agents profile",
	"secretsCmd/sub":          "seaport agents secrets",
	"env/sub":                 "seaport env",
	"envHome/sub":             "seaport env home",
	"schedules/sub":           "seaport schedules",
	"crewTokens/sub":          "seaport crew tokens",
}

// cliExtra: commands dispatched without a switch case.
var cliExtra = []string{
	"seaport help",        // run: args[0] == "help"
	"seaport crew tokens", // run: crew + rest[0] == "tokens"
	"seaport customs rm",  // customs: rest[0] == "rm"
}

// cliAliases: names that stand for another command; the doc may leave them
// to prose.
var cliAliases = map[string]string{
	"seaport envs": "seaport env", "seaport environments": "seaport env",
	"seaport schedule": "seaport schedules", "seaport cron": "seaport schedules",
	"seaport agent": "seaport agents", "seaport convs": "seaport conversations",
	"seaport threads": "seaport thread",
	// subcommand aliases
	"seaport dock destination remove": "seaport dock destination rm",
	"seaport env show":                "seaport env export",
}

var cliTags = map[string]bool{"sub": true, "cmd": true, "args[0]": true, "rest[0]": true}

// codeCLI: every command path ("seaport dock add") of the dispatch
// switches, and the dispatch switches that aren't listed in cliSwitches.
func codeCLI(files []goFile) (map[string]bool, []string) {
	paths := setOf(cliExtra...)
	var stray []string
	for _, f := range files {
		for _, decl := range f.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok || sw.Tag == nil {
					return true
				}
				tag := types.ExprString(sw.Tag)
				prefix, listed := cliSwitches[fd.Name.Name+"/"+tag]
				if !listed {
					if cliTags[tag] {
						stray = append(stray, f.fset.Position(sw.Pos()).String()+": switch on "+tag+" in "+fd.Name.Name+
							" dispatches commands: list it in cliSwitches")
					}
					return true
				}
				for _, cc := range sw.Body.List {
					for _, e := range cc.(*ast.CaseClause).List {
						if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							name, _ := strconv.Unquote(lit.Value)
							paths[prefix+" "+name] = true
						}
					}
				}
				return true
			})
		}
	}
	for alias := range cliAliases {
		delete(paths, alias)
	}
	sort.Strings(stray)
	return paths, stray
}

var cliWord = regexp.MustCompile(`^\[?([a-z][a-z-]*)\]*$`)

// docCLI: for each `seaport …` span of the page, its command words: up to
// the first token that isn't a lowercase word (a flag, <arg>, "text").
func docCLI(doc string) [][]string {
	doc = fence.ReplaceAllString(doc, "")
	var out [][]string
	for _, s := range spans(doc) {
		fields := strings.Fields(s)
		if len(fields) < 2 || fields[0] != "seaport" {
			continue
		}
		words := []string{"seaport"}
		for _, f := range fields[1:] {
			m := cliWord.FindStringSubmatch(f)
			if m == nil {
				break
			}
			w := "seaport " + m[1]
			if len(words) > 1 {
				w = words[len(words)-1] + " " + m[1]
			}
			if alias, ok := cliAliases[w]; ok {
				w = alias
			}
			words = append(words, w)
		}
		out = append(out, words)
	}
	return out
}

// cliDocViolations: every command path of the code is named by a doc
// command, and every doc command walks down existing paths (it may go on
// into arguments once its path reaches a command that has no subcommands).
func cliDocViolations(paths map[string]bool, doc string) []string {
	groups := map[string]bool{}
	for p := range paths {
		parts := strings.Split(p, " ")
		for i := 1; i < len(parts); i++ {
			groups[strings.Join(parts[:i], " ")] = true
		}
	}
	covered := map[string]bool{}
	var out []string
	for _, words := range docCLI(doc) {
		full := words[len(words)-1]
		for p := range paths {
			if full == p || strings.HasPrefix(full, p+" ") {
				covered[p] = true
			}
		}
		// `seaport dock add whatsapp`: walk while the path is a group.
		cur := "seaport"
		for _, w := range words[1:] {
			if !groups[cur] {
				break
			}
			if !paths[w] {
				out = append(out, "cli.md: `"+w+"` is documented but cmd/seaport has no such command")
				break
			}
			cur = w
		}
	}
	for p := range paths {
		covered[p] = covered[p] || func() bool {
			for c := range covered {
				if strings.HasPrefix(c, p+" ") {
					return true
				}
			}
			return false
		}()
	}
	out = append(out, missingFrom(paths, covered, "cli.md: command `%s` is in cmd/seaport but not documented")...)
	sort.Strings(out)
	return out
}

func TestDocsCLI(t *testing.T) {
	paths, stray := codeCLI(cmdSources(t))
	if len(paths) < 60 {
		t.Fatalf("found only %d commands: the rule checks nothing", len(paths))
	}
	report(t, stray)
	report(t, cliDocViolations(paths, readDoc(t, "cli.md")))
}

// ---- 5. events.md ----

func docEvents(doc string) map[string]string {
	out := map[string]string{}
	for _, t := range tablesWith(doc, "Event type", "Reading permission") {
		for _, row := range t.rows {
			e, p := spans(row[0]), spans(row[1])
			if len(e) == 1 && len(p) == 1 {
				out[e[0]] = p[0]
			}
		}
	}
	return out
}

func eventDocViolations(code map[string]string, doc string) []string {
	docEv := docEvents(doc)
	out := bothWays(keys(code), keys(docEv),
		"events.md: event type %s is streamed but not documented",
		"events.md: event type %s is documented but never streamed")
	for e, perm := range code {
		if got, ok := docEv[e]; ok && got != perm {
			out = append(out, "events.md: event "+e+" is read with "+perm+", the doc says "+got)
		}
	}
	sort.Strings(out)
	return out
}

func TestDocsEvents(t *testing.T) {
	report(t, eventDocViolations(bootstrap.EventPermissions, readDoc(t, "events.md")))
}

// ---- 6. permissions.md ----

type permManifest struct {
	Permissions []struct {
		Name string `json:"name"`
	} `json:"permissions"`
	Roles []struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	} `json:"roles"`
}

func readManifest(t *testing.T) permManifest {
	t.Helper()
	raw, err := os.ReadFile("../../iam/permissions.json")
	if err != nil {
		t.Fatal(err)
	}
	var m permManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var holdsAll = regexp.MustCompile(`\*\*Admin\*\* holds all (\d+) permissions`)

func permDocViolations(m permManifest, doc string) []string {
	catalog := map[string]bool{}
	for _, p := range m.Permissions {
		catalog[p.Name] = true
	}
	var out []string
	out = append(out, bothWays(catalog, firstColumn(doc, "Permission", "Allows"),
		"permissions.md: permission %s is in iam/permissions.json but not in the Catalog",
		"permissions.md: permission %s is in the Catalog but not in iam/permissions.json")...)

	tables := tablesWith(doc, "Permission", "Admin")
	if len(tables) != 1 {
		return append(out, "permissions.md: expected one Roles table (Permission | Admin | …), found "+strconv.Itoa(len(tables)))
	}
	roles := tables[0]
	granted := map[string]bool{} // "role permission" cells marked yes
	for _, row := range roles.rows {
		s := spans(row[0])
		if len(s) != 1 {
			continue
		}
		for i, cell := range row[1:] {
			if cell == "yes" && i+1 < len(roles.header) {
				granted[roles.header[i+1]+" "+s[0]] = true
			}
		}
	}
	want := map[string]bool{}
	var names []string
	for _, r := range m.Roles {
		names = append(names, r.Name)
		for _, p := range r.Permissions {
			want[r.Name+" "+p] = true
		}
	}
	if strings.Join(roles.header[1:], ",") != strings.Join(names, ",") {
		out = append(out, "permissions.md: Roles columns are "+strings.Join(roles.header[1:], ", ")+", the manifest's roles are "+strings.Join(names, ", "))
	}
	out = append(out, bothWays(want, granted,
		"permissions.md: role grant [%s] is in iam/permissions.json but not in the Roles table",
		"permissions.md: role grant [%s] is in the Roles table but not in iam/permissions.json")...)
	rowsListed := map[string]bool{}
	for _, row := range roles.rows {
		if s := spans(row[0]); len(s) == 1 {
			rowsListed[s[0]] = true
		}
	}
	out = append(out, missingFrom(catalog, rowsListed, "permissions.md: permission %s has no row in the Roles table")...)
	if m := holdsAll.FindStringSubmatch(doc); m == nil {
		out = append(out, "permissions.md: the \"Admin holds all N permissions\" sentence is gone")
	} else if n, _ := strconv.Atoi(m[1]); n != len(catalog) {
		out = append(out, "permissions.md: says Admin holds all "+m[1]+" permissions, the manifest has "+strconv.Itoa(len(catalog)))
	}
	sort.Strings(out)
	return out
}

func TestDocsPermissions(t *testing.T) {
	m := readManifest(t)
	if len(m.Permissions) == 0 || len(m.Roles) == 0 {
		t.Fatal("empty manifest: the rule checks nothing")
	}
	report(t, permDocViolations(m, readDoc(t, "permissions.md")))
}

// ---- 7. crew-protocol.md ----

// codeFrames: the crew.Frame… constants (type FrameType) of the wire protocol.
func codeFrames(files []goFile) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if f.rel != "crew/protocol.go" {
			continue
		}
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "FrameType" {
					continue
				}
				for _, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						out[s] = true
					}
				}
			}
		}
	}
	return out
}

func frameDocViolations(code map[string]bool, doc string) []string {
	return bothWays(code, firstColumn(doc, "Frame", "Direction"),
		"crew-protocol.md: frame type %s is defined in crew/protocol.go but not in the Frames table",
		"crew-protocol.md: frame type %s is in the Frames table but crew/protocol.go defines none")
}

func TestDocsCrewProtocol(t *testing.T) {
	code := codeFrames(sources(t, false))
	if len(code) < 8 {
		t.Fatalf("found only %d frame types: the rule checks nothing", len(code))
	}
	report(t, frameDocViolations(code, readDoc(t, "crew-protocol.md")))
}

// ---- 8. limits.md ----

// goNames: for each package name, its package-level identifiers (consts,
// vars, types, funcs), and for each "pkg.Type", the struct's fields.
func goNames(files []goFile) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		pkg := f.file.Name.Name
		for _, decl := range f.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out[pkg+"."+d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							out[pkg+"."+n.Name] = true
						}
					case *ast.TypeSpec:
						out[pkg+"."+s.Name.Name] = true
						if st, ok := s.Type.(*ast.StructType); ok {
							for _, fld := range st.Fields.List {
								for _, n := range fld.Names {
									out[pkg+"."+s.Name.Name+"."+n.Name] = true
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// limitsExternal: packages outside internal/ the Constant column may cite.
var limitsExternal = map[string]bool{"http": true}

var goRef = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[A-Za-z][A-Za-z0-9]*)+$`)

var limitVar = regexp.MustCompile(`^SEAPORT_(MAX|SEND|HOST_MAX|API)_[A-Z0-9_]+$`)

// limitsDocViolations: every `pkg.Name` (or `pkg.Type.Field`) of a Constant
// column exists; every SEAPORT_MAX_*/SEND_*/HOST_MAX_*/API_* variable the
// code reads is listed in a Configurable column, and every variable listed
// there is read. One direction for the constants: the page picks which to cite.
func limitsDocViolations(names map[string]bool, envKeys map[string]string, doc string) []string {
	var out []string
	listed := map[string]bool{}
	for _, t := range tablesWith(doc, "Limit", "Value", "Constant", "Configurable") {
		for _, row := range t.rows {
			if len(row) < 4 {
				continue
			}
			for _, s := range spans(row[2]) {
				if !goRef.MatchString(s) {
					continue
				}
				if pkg, _, _ := strings.Cut(s, "."); limitsExternal[pkg] {
					continue
				}
				if !names[s] {
					out = append(out, "limits.md: `"+s+"` is cited but no such name exists in the code")
				}
			}
			for _, s := range spans(row[3]) {
				if envName.MatchString(s) {
					listed[s] = true
					if _, ok := envKeys[s]; !ok {
						out = append(out, "limits.md: `"+s+"` is listed but nothing reads it")
					}
				}
			}
		}
	}
	for k := range envKeys {
		if limitVar.MatchString(k) && !listed[k] {
			out = append(out, "limits.md: variable "+k+" is read by the code but not listed in a Configurable column")
		}
	}
	sort.Strings(out)
	return out
}

func TestDocsLimits(t *testing.T) {
	files := sources(t, false)
	envKeys := codeEnvKeys(append(append([]goFile{}, files...), cmdSources(t)...))
	names := goNames(files)
	if !names["config.Server.MaxDocks"] || !names["crew.MaxTokens"] {
		t.Fatal("goNames misses known names: the rule checks nothing")
	}
	report(t, limitsDocViolations(names, envKeys, readDoc(t, "limits.md")))
}

// ---- fixtures: each check fires on a wrong document ----

func TestDocsRulesCatchViolations(t *testing.T) {
	t.Run("http-api", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"widget/widgethttp/h.go": `package widgethttp
const base = "/api/v1/widgets"
func routes(r R) {
	r.Get(base, h)
	r.Get(base+"/:id", h)
	r.Delete("/api/v1/widgets/:id", h)
	r.Post("/hooks/:dock", h)
	r.All("/mcp", h)
	r.All("/*", h)
	_ = c.Get("X-Request-ID")
}`,
		})
		code := codeRoutes(files)
		doc := "## Public routes\n\n| Method | Path | Response |\n| :--- | :--- | :--- |\n" +
			"| `POST` | `/hooks/{dock}` | ok |\n| `GET` | `/api/v1/ghosts` | gone |\n\n" +
			"## Admin routes\n\n| Method | Path | Permission | Body |\n| :--- | :--- | :--- | :--- |\n" +
			"| `GET` | `/widgets` | `widgets:read` | list |\n| `GET` | `/widgets/{id}` | `widgets:read` | one |\n"
		expectViolations(t, routeDocViolations(code, doc),
			"route DELETE /api/v1/widgets/{} is registered but not documented",
			"route POST /mcp is registered but not documented",
			"route GET /api/v1/ghosts is documented but not registered")
		files = parseFixtures(t, map[string]string{"widget/widgethttp/h.go": `package widgethttp
func routes(r R) { r.All("/odd/*", h) }`})
		expectViolations(t, routeDocViolations(codeRoutes(files), ""), "ALL /odd/* (a catch-all route")
	})

	t.Run("configuration", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"config/config.go": `package config
func f() {
	_ = env("SEAPORT_KNOWN", "x")
	_ = envInt("SEAPORT_NEW", 1)
	_ = os.Getenv("SEAPORT_ALSO_NEW")
	_ = os.Getenv(hosting.EnvURL)
	_ = []string{"SEAPORT_TABLE_KEY"}
	_ = "SEAPORT_ONLY_IN_A_MESSAGE is not a name"
}`,
			"hosting/hosting.go": `package hosting
const EnvURL = "SEAPORT_URL"`,
			"connector/c.go": `package connector
func g() { _ = os.Getenv("SEAPORT_GH"); _ = Getenv("OTHER") }`,
			"connector/acptest/a.go": `package acptest
func h() { _ = os.Getenv("SEAPORT_TEST_ACP_AGENT") }`, // the suite's own switch: not a product variable
		})
		doc := "| Variable | Default | Meaning |\n| :--- | :--- | :--- |\n" +
			"| `SEAPORT_KNOWN` | x | y |\n| `SEAPORT_URL` | x | y |\n| `SEAPORT_STALE` | x | y |\n"
		expectViolations(t, envDocViolations(codeEnvKeys(files), doc),
			"variable SEAPORT_NEW is read by the code but not documented",
			"variable SEAPORT_ALSO_NEW is read",
			"variable SEAPORT_TABLE_KEY is read",
			"variable SEAPORT_GH is read",
			"variable SEAPORT_STALE is documented but nothing reads it")
	})

	t.Run("mcp-tools", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"crew/adapters/crewmcp/t.go": `package crewmcp
func add(srv S) {
	mcp.AddTool(srv, &mcp.Tool{Name: "reply"}, h)
	mcp.AddTool(srv, &mcp.Tool{Name: "fresh", Description: "d"}, h)
}`,
			"crew/adapters/other/t.go": `package other
var _ = &mcp.Tool{Name: "elsewhere"}`,
		})
		doc := "| Tool | Always there | What it does |\n| :--- | :--- | :--- |\n" +
			"| `reply` | yes | x |\n| `retired` | yes | x |\n"
		expectViolations(t, mcpDocViolations(codeMCPTools(files), doc),
			"tool fresh is registered by crewmcp but not in the Tools table",
			"tool retired is in the Tools table but crewmcp registers none")
	})

	t.Run("cli", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"cmd/seaport/main.go": `package main
func run(args []string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
	case "dock":
		switch rest[0] {
		case "add":
		case "rename":
		}
	case "agents", "agent":
	case "mystery":
	}
	return nil
}
func agents(args []string) error {
	switch sub {
	case "list":
	case "add":
	}
	return nil
}
func newGroup(args []string) error {
	switch args[0] {
	case "x":
	}
	return nil
}
func other(d D) { switch d.Channel { case "ignored": } }`,
		})
		paths, stray := codeCLI(files)
		expectViolations(t, stray, "switch on args[0] in newGroup dispatches commands: list it in cliSwitches")
		doc := "| Command | Does |\n| :--- | :--- |\n" +
			"| `seaport serve` | x |\n" +
			"| `seaport dock add whatsapp [-name N]` | x |\n" +
			"| `seaport dock vanish <id>` | x |\n" +
			"| `seaport agents [list]` | x |\n" +
			"| `seaport agent add <name>` | an alias |\n" +
			"Run `seaport help` or `seaport: oops` or `seaport serve -h`.\n"
		expectViolations(t, cliDocViolations(paths, doc),
			"`seaport dock vanish` is documented but cmd/seaport has no such command",
			"command `seaport crew tokens` is in cmd/seaport but not documented",
			"command `seaport customs rm` is in cmd/seaport but not documented",
			"command `seaport dock rename` is in cmd/seaport but not documented",
			"command `seaport mystery` is in cmd/seaport but not documented")
	})

	t.Run("events", func(t *testing.T) {
		code := map[string]string{"a.created": "seaport:a:read", "a.deleted": "seaport:a:read", "b.fired": "seaport:b:read"}
		doc := "| Event type | Reading permission | Payload | When |\n| :--- | :--- | :--- | :--- |\n" +
			"| `a.created` | `seaport:a:read` | x | y |\n| `b.fired` | `seaport:a:read` | x | y |\n| `c.gone` | `seaport:c:read` | x | y |\n"
		expectViolations(t, eventDocViolations(code, doc),
			"event type a.deleted is streamed but not documented",
			"event type c.gone is documented but never streamed",
			"event b.fired is read with seaport:b:read, the doc says seaport:a:read")
	})

	t.Run("permissions", func(t *testing.T) {
		var m permManifest
		if err := json.Unmarshal([]byte(`{"permissions":[{"name":"seaport:a:read"},{"name":"seaport:a:write"},{"name":"seaport:b:use"}],
			"roles":[{"name":"Admin","permissions":["seaport:a:read","seaport:a:write","seaport:b:use"]},
			         {"name":"Viewer","permissions":["seaport:a:read"]}]}`), &m); err != nil {
			t.Fatal(err)
		}
		doc := "| Permission | Allows |\n| :--- | :--- |\n| `seaport:a:read` | x |\n| `seaport:a:write` | x |\n| `seaport:old:read` | x |\n\n" +
			"| Permission | Admin | Viewer |\n| :--- | :---: | :---: |\n" +
			"| `seaport:a:read` | yes | yes |\n| `seaport:a:write` | yes | yes |\n\n" +
			"- **Admin** holds all 2 permissions.\n"
		expectViolations(t, permDocViolations(m, doc),
			"permission seaport:b:use is in iam/permissions.json but not in the Catalog",
			"permission seaport:old:read is in the Catalog but not in iam/permissions.json",
			"role grant [Admin seaport:b:use] is in iam/permissions.json but not in the Roles table",
			"role grant [Viewer seaport:a:write] is in the Roles table but not in iam/permissions.json",
			"permission seaport:b:use has no row in the Roles table",
			"says Admin holds all 2 permissions, the manifest has 3")
	})

	t.Run("crew-protocol", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"crew/protocol.go": `package crew
type FrameType string
const (
	FrameHello FrameType = "hello"
	FrameNew   FrameType = "new"
)
const Other = "not a frame"`,
		})
		doc := "| Frame | Direction | Fields |\n| :--- | :--- | :--- |\n| `hello` | a | b |\n| `gone` | a | b |\n"
		expectViolations(t, frameDocViolations(codeFrames(files), doc),
			"frame type new is defined in crew/protocol.go but not in the Frames table",
			"frame type gone is in the Frames table but crew/protocol.go defines none")
	})

	t.Run("limits", func(t *testing.T) {
		files := parseFixtures(t, map[string]string{
			"config/config.go": `package config
type Server struct{ MaxDocks int }
const SessionTTL = 1`,
			"crew/c.go": `package crew
const MaxTokens = 100`,
		})
		doc := "| Limit | Value | Constant | Configurable |\n| :--- | :--- | :--- | :--- |\n" +
			"| Docks | 10 | `config.Server.MaxDocks` | `SEAPORT_MAX_DOCKS` |\n" +
			"| Tokens | 100 | `crew.MaxTokens` | No |\n" +
			"| Gone | 1 | `crew.MaxGone` | No |\n" +
			"| Header time | 10 s | `http.Server.ReadHeaderTimeout` in `bootstrap` | No |\n" +
			"| Stale | 1 | `config.Server.MaxStale` | `SEAPORT_MAX_STALE` |\n"
		keysRead := map[string]string{"SEAPORT_MAX_DOCKS": "x", "SEAPORT_MAX_NEW": "x", "SEAPORT_ADDR": "x"}
		expectViolations(t, limitsDocViolations(goNames(files), keysRead, doc),
			"`crew.MaxGone` is cited but no such name exists",
			"`config.Server.MaxStale` is cited but no such name exists",
			"`SEAPORT_MAX_STALE` is listed but nothing reads it",
			"variable SEAPORT_MAX_NEW is read by the code but not listed")
	})
}
