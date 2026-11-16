package bootstrap_test

import (
	"go/ast"
	"go/token"
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

// Hold reasons (AGENTS.md "Rules"): a held ship carries a reason string
// (`ship.hold_reason`) set by customs (`conversation.Hold…`) or routing
// (`agent.Hold…`). The web shows them translated through `holdLabel` in
// ship-bubble.tsx, so every non-empty `Hold… = "…"` constant of a domain
// root must have an entry there (TestEveryHoldReasonIsTranslated).

const holdLabelsFile = "components/seaport/ship-bubble.tsx"

// holdReasons: the non-empty `Hold…` string constants of domain roots → file.
func holdReasons(files []goFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
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
					if !strings.HasPrefix(name.Name, "Hold") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if s, _ := strconv.Unquote(lit.Value); s != "" {
						out[s] = f.rel
					}
				}
			}
		}
	}
	return out
}

func untranslatedHolds(reasons map[string]string, labels string) []string {
	var out []string
	for reason, file := range reasons {
		if !strings.Contains(labels, strconv.Quote(reason)+":") {
			out = append(out, file+": hold reason "+strconv.Quote(reason)+" has no entry in holdLabel ("+holdLabelsFile+")")
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryHoldReasonIsTranslated(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", filepath.FromSlash(holdLabelsFile)))
	if err != nil {
		t.Fatal(err)
	}
	reasons := holdReasons(sources(t, false))
	if len(reasons) == 0 {
		t.Fatal("found no Hold… constants: the rule checks nothing")
	}
	report(t, untranslatedHolds(reasons, string(raw)))
}

func TestHoldRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"widget/widget.go": `package widget
const (
	HoldNone  = ""
	HoldKnown = "known reason"
	HoldNew   = "new reason"
)`,
		"widget/widgetsvc/service.go": `package widgetsvc
const HoldIgnored = "not a domain root"`,
	})
	labels := `const holdLabel = { "known reason": "motivo conocido" }`
	expectViolations(t, untranslatedHolds(holdReasons(files), labels),
		`widget/widget.go: hold reason "new reason" has no entry`,
	)
}

// Live handlers (AGENTS.md "Rules"): the web patches its cache from
// /api/v1/events with one handler per event type, the keys of `handlers` in
// web/src/lib/live.ts (typed over EventMap, and registered by LiveStream.start
// through Object.keys). An event type published by the server that has no
// handler is silently dropped by the browser, so every key of
// bootstrap.EventPermissions has a handler or is listed in
// unhandledEvents with the reason the web ignores it
// (TestEveryEventTypeHasALiveHandler).

const liveFile = "lib/live.ts"

// unhandledEvents: event types the web deliberately has no handler for.
var unhandledEvents = map[string]string{
	"slot.browser":   "an audit trail of who opened the browser viewer; the viewer is its own WebSocket and nothing in the cache depends on it",
	"agent.takeover": "an audit trail of who asked a takeover and who ended it; the Navegador card follows agent.updated, which carries the open takeovers",
}

var handlerKey = regexp.MustCompile(`(?m)^  "([a-z_]+\.[a-z_]+)":`)

// liveHandlers: the event types keyed in the `handlers` object of live.ts.
func liveHandlers(src string) map[string]bool {
	out := map[string]bool{}
	_, body, ok := strings.Cut(src, "export const handlers")
	if !ok {
		return out
	}
	// The object ends at the first closing brace in column 0.
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	for _, m := range handlerKey.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func unhandledLiveEvents(events []string, handlers map[string]bool, allowed map[string]string) []string {
	var out []string
	known := map[string]bool{}
	for _, typ := range events {
		known[typ] = true
		_, listed := allowed[typ]
		switch {
		case !handlers[typ] && !listed:
			out = append(out, "event "+strconv.Quote(typ)+" has no handler in "+liveFile+" (add one, or list it in unhandledEvents with the reason)")
		case handlers[typ] && listed:
			out = append(out, "event "+strconv.Quote(typ)+" is listed in unhandledEvents but has a handler: remove the entry")
		}
	}
	for typ := range allowed {
		if !known[typ] {
			out = append(out, "unhandledEvents lists "+strconv.Quote(typ)+", which is not an event type")
		}
	}
	for typ := range handlers {
		if !known[typ] {
			out = append(out, liveFile+" handles "+strconv.Quote(typ)+", which the server never publishes")
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryEventTypeHasALiveHandler(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", filepath.FromSlash(liveFile)))
	if err != nil {
		t.Fatal(err)
	}
	handlers := liveHandlers(string(raw))
	if len(handlers) < 10 {
		t.Fatalf("found %d handlers in %s: the rule checks nothing", len(handlers), liveFile)
	}
	var events []string
	for typ := range bootstrap.EventPermissions {
		events = append(events, typ)
	}
	for typ, why := range unhandledEvents {
		if strings.TrimSpace(why) == "" {
			t.Errorf("unhandledEvents[%q] needs a reason", typ)
		}
	}
	report(t, unhandledLiveEvents(events, handlers, unhandledEvents))
}

func TestLiveHandlerRuleCatchesViolations(t *testing.T) {
	src := `import x from "y"
export const handlers: Handlers = {
  "dock.updated": patchDock,
  "dock.deleted": (qc, { id }) => {
    qc.removeQueries({ queryKey: keys.dock(id) })
  },
  "ghost.event": (qc) => {},
}
const other = {
  "not.counted": 1,
}
`
	got := liveHandlers(src)
	if len(got) != 3 || !got["dock.updated"] || !got["dock.deleted"] || !got["ghost.event"] || got["not.counted"] {
		t.Fatalf("liveHandlers = %v", got)
	}
	events := []string{"dock.updated", "dock.deleted", "ship.created", "slot.browser", "audit.opened"}
	expectViolations(t, unhandledLiveEvents(events, got, map[string]string{
		"slot.browser": "audit only",
		"dock.updated": "listed although handled",
		"never.exists": "stale entry",
	}),
		`event "audit.opened" has no handler`,
		`event "dock.updated" is listed in unhandledEvents but has a handler`,
		`event "ship.created" has no handler`,
		`handles "ghost.event"`,
		`lists "never.exists"`,
	)
}
