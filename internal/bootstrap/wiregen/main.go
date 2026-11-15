// Command wiregen generates web/src/lib/types.gen.ts: the TypeScript types of
// what the admin API and the live events send, read by reflection from the Go
// types listed in internal/bootstrap/wirespec (internal/wire does the work).
// It fails on a type it can't name or type exactly.
//
// Run through `go generate ./internal/wire`. The test in this package fails
// when the generated file is stale.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Abraxas-365/seaport/internal/bootstrap/wirespec"
	"github.com/Abraxas-365/seaport/internal/wire"
)

const tsPath = "web/src/lib/types.gen.ts"

func main() {
	root := "."
	if len(os.Args) == 3 && os.Args[1] == "-root" {
		root = os.Args[2]
	}
	out, err := generate(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wiregen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(root, tsPath), out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "wiregen:", err)
		os.Exit(1)
	}
}

// generate renders the file for the repository at root.
func generate(root string) ([]byte, error) {
	return wire.Render(wirespec.Spec(), os.DirFS(root))
}
