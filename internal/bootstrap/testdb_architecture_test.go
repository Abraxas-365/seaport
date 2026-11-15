package bootstrap_test

import (
	"go/ast"
	"path"
	"strconv"
	"strings"
	"testing"
)

// testdbViolations: internal/testdb is test support (it starts Docker and
// creates databases), so only _test.go files import it; and the database
// URL variable is read there alone, since a test that read it itself would
// share one database with every other package (and need -p 1 again).
func testdbViolations(files []goFile) []string {
	const (
		testdbPkg = "github.com/Abraxas-365/seaport/internal/testdb"
		envName   = "SEAPORT_TEST_" + "DATABASE_URL" // split: this file is not a violation of its own rule
	)
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		isTest := strings.HasSuffix(f.rel, "_test.go")
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == testdbPkg && !isTest {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+f.rel+
					" imports internal/testdb; only _test.go files may")
			}
		}
		if dir == "testdb" {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok {
				return true
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && v == envName {
				out = append(out, f.fset.Position(lit.Pos()).String()+": "+dir+
					" names "+envName+"; ask testdb.URL(t) for the package's own database")
			}
			return true
		})
	}
	return out
}

func TestOnlyTestsImportTestdb(t *testing.T) { report(t, testdbViolations(sources(t, true))) }

func TestTestdbRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"ship/shipsvc/s.go":         `package shipsvc; import _ "github.com/Abraxas-365/seaport/internal/testdb"`,
		"dock/adapters/x/x_test.go": `package x_test; import _ "github.com/Abraxas-365/seaport/internal/testdb"`,
		"pglock/p_test.go":          `package pglock_test; import "os"; var _ = os.Getenv("SEAPORT_TEST_DATABASE_URL")`,
		"testdb/testdb.go":          `package testdb; const Env = "SEAPORT_TEST_DATABASE_URL"`,
	})
	expectViolations(t, testdbViolations(files), "shipsvc/s.go", "pglock/p_test.go")
}
