package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedTypesAreFresh fails when a Go wire type (or the list in
// internal/bootstrap/wirespec) changed without `go generate ./internal/wire`.
func TestGeneratedTypesAreFresh(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	want, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, tsPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: run `go generate ./internal/wire`", tsPath)
	}
}
