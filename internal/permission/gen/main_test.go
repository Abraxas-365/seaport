package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedFilesAreFresh fails when iam/permissions.json changed
// without `go generate ./internal/permission`.
func TestGeneratedFilesAreFresh(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	files, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale: run `go generate ./internal/permission`", path)
		}
	}
}

func TestGeneratorRefusesBadManifests(t *testing.T) {
	const roles = `"roles":[{"name":"Admin","permissions":["s:a:read"]}]`
	cases := map[string]string{
		"format":       `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:A:read","label":"x"}],` + roles + `}`,
		"prefix":       `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"t:a:read","label":"x"}],` + roles + `}`,
		"duplicate":    `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"},{"name":"s:a:read","label":"y"}],` + roles + `}`,
		"label":        `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":" "}],` + roles + `}`,
		"same ident":   `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"},{"name":"s:a-:read","label":"y"}],` + roles + `}`,
		"outside":      `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"}],"roles":[{"name":"Admin","permissions":["s:a:read","s:b:read"]}]}`,
		"admin misses": `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"},{"name":"s:b:read","label":"y"}],` + roles + `}`,
		"no admin":     `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"}],"roles":[{"name":"Viewer","permissions":["s:a:read"]}]}`,
		"empty role":   `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"}],"roles":[{"name":"Admin","permissions":["s:a:read"]},{"name":"V","permissions":[]}]}`,
		"unknown key":  `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:a:read","label":"x"}],` + roles + `,"extra":1}`,
	}
	for name, raw := range cases {
		if _, err := generateFrom([]byte(raw)); err == nil {
			t.Errorf("%s: manifest accepted", name)
		}
	}
	ok := `{"resource":{"name":"S","prefix":"s"},"permissions":[{"name":"s:crew-tokens:read","label":"x \"y\""}],"roles":[{"name":"Admin","permissions":["s:crew-tokens:read"]}]}`
	files, err := generateFrom([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[goPath]), "CrewTokensRead") || !strings.Contains(string(files[tsPath]), `"x \"y\""`) {
		t.Errorf("unexpected output:\n%s\n%s", files[goPath], files[tsPath])
	}
}
