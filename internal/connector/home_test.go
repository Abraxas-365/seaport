package connector

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A seeded dir follows the image: changed files are replaced, files the
// image dropped are deleted, the harness's own files stay.
func TestSeedDirFollowsTheImage(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	write := func(p, v string, mode os.FileMode) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(v), mode); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	write(filepath.Join(from, "CLAUDE.md"), "v1", 0o644)
	write(filepath.Join(from, "skills", "go", "SKILL.md"), "go", 0o644)
	write(filepath.Join(from, "hooks", "pre.sh"), "#!/bin/sh", 0o755)
	if err := seedDir(from, to, []string{"projects"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(to, "hooks", "pre.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("hook not executable: %v %v", fi, err)
	}
	// The harness writes its own files; the next image drops the skill.
	write(filepath.Join(to, "projects", "s.jsonl"), "x", 0o600)
	write(filepath.Join(to, "todos", "t.json"), "x", 0o600)
	from2 := t.TempDir()
	write(filepath.Join(from2, "CLAUDE.md"), "v2", 0o644)
	write(filepath.Join(from2, "hooks", "pre.sh"), "#!/bin/sh", 0o755)
	if err := seedDir(from2, to, []string{"projects"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(to, "CLAUDE.md")); string(raw) != "v2" {
		t.Fatalf("not replaced: %q", raw)
	}
	if exists(filepath.Join(to, "skills", "go", "SKILL.md")) || exists(filepath.Join(to, "skills")) {
		t.Fatal("dropped skill still there")
	}
	if !exists(filepath.Join(to, "projects", "s.jsonl")) || !exists(filepath.Join(to, "todos", "t.json")) {
		t.Fatal("the harness's own files were deleted")
	}
	// A list naming a kept or escaping path deletes nothing there.
	write(filepath.Join(to, seededFile), "projects/s.jsonl\n../../etc/x\nCLAUDE.md", 0o600)
	if err := seedDir(t.TempDir(), to, []string{"projects"}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(to, "projects", "s.jsonl")) || exists(filepath.Join(to, "CLAUDE.md")) {
		t.Fatal("seed list")
	}
}

func TestKeepSeaportRequire(t *testing.T) {
	p := filepath.Join(t.TempDir(), "init.lua")
	for in, want := range map[string]string{
		"provider('x')\n":                                  seaportRequire + "\nprovider('x')\n",
		"provider('x')\nrequire(\"seaport\")\n":            seaportRequire + "\nprovider('x')\n",
		seaportRequire + "\nprovider('x')\n":               seaportRequire + "\nprovider('x')\n",
		"  require('seaport') -- mine\nrequire('other')\n": seaportRequire + "\nrequire('other')\n",
	} {
		_ = os.WriteFile(p, []byte(in), 0o644)
		if err := keepSeaportRequire(p); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(p); string(raw) != want {
			t.Errorf("%q → %q, want %q", in, raw, want)
		}
	}
	if err := keepSeaportRequire(filepath.Join(t.TempDir(), "none.lua")); err != nil {
		t.Fatal(err)
	}
}

func TestPlanPlugins(t *testing.T) {
	plan, err := planPlugins([]byte(`{"enabledPlugins": {"gopls-lsp@claude-plugins-official": true, "off@team": false,
		"review@team": true}, "extraKnownMarketplaces": {"team": {"source": {"source": "github", "repo": "acme/plugins"}},
		"unused": {"source": {"source": "directory", "path": "/x"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gopls-lsp@claude-plugins-official", "review@team"}; !reflect.DeepEqual(plan.plugins, want) {
		t.Fatalf("plugins %v", plan.plugins)
	}
	if want := [][2]string{{"claude-plugins-official", officialMarketplaceSource}, {"team", "acme/plugins"}}; !reflect.DeepEqual(plan.marketplaces, want) {
		t.Fatalf("marketplaces %v", plan.marketplaces)
	}
	for in, want := range map[string]string{
		`{"enabledPlugins": {"x@nowhere": true}}`: "not declared",
		`{"enabledPlugins": {"x": true}}`:         "plugin@marketplace",
		`{"enabledPlugins": {"x@m": true}, "extraKnownMarketplaces": {"m": {"source": {"source": "directory", "path": "/p"}}}}`: "one machine's",
		`{"enabledPlugins": {"x@m": true}, "extraKnownMarketplaces": {"m": {"source": {"source": "git", "url": "git@h:x"}}}}`:   "https",
		`{`: "valid JSON",
	} {
		if _, err := planPlugins([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	if plan, err := planPlugins([]byte(`{"model": "sonnet"}`)); err != nil || len(plan.plugins) != 0 {
		t.Fatalf("no plugins: %+v %v", plan, err)
	}
}
