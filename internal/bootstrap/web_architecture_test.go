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
