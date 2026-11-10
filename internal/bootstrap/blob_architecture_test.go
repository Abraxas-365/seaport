package bootstrap_test

import (
	"path"
	"strconv"
	"strings"
	"testing"
)

// blobOwners: the packages that may import the blob store
// (internal/blob). What sits in it is sealed per tenant and served only
// through the owning service's checked reads (ship.Queries.OpenAttachment,
// hosting.Queries.HomeFile/HomeArchive); any other package reaching the
// store would bypass them. The modules only hand the store to their
// service; bootstrap opens it.
var blobOwners = map[string]bool{
	"blob": true, "blob/adapters/gocloudblob": true, "bootstrap": true,
	"ship/shipsvc": true, "ship/shipmodule": true,
	"hosting/hostingsvc": true, "hosting/hostingmodule": true,
}

func blobViolations(files []goFile) []string {
	const blobPkg = "github.com/Abraxas-365/seaport/internal/blob"
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if (p == blobPkg || strings.HasPrefix(p, blobPkg+"/")) && !blobOwners[dir] {
				out = append(out, f.fset.Position(imp.Pos()).String()+": "+dir+
					" must not reach the blob store; read through the owning service (blobOwners)")
			}
		}
	}
	return out
}

func TestOnlyOwnersReachBlobs(t *testing.T) { report(t, blobViolations(sources(t, false))) }

func TestBlobRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"hosting/adapters/hostinghttp/h.go": `package hostinghttp; import _ "github.com/Abraxas-365/seaport/internal/blob"`,
		"crew/adapters/crewmcp/t.go":        `package crewmcp; import _ "github.com/Abraxas-365/seaport/internal/blob/adapters/gocloudblob"`,
		"hosting/hostingsvc/s.go":           `package hostingsvc; import _ "github.com/Abraxas-365/seaport/internal/blob"`,
	})
	expectViolations(t, blobViolations(files), "hostinghttp/h.go", "crewmcp/t.go")
}
