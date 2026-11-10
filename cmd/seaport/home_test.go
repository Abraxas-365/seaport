package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/hosting"
)

// push packs only the harness's config: sessions, sign-in, plugin
// installs and junk are left out (and listed); a linked skill is followed;
// pull writes the same files back.
func TestPackHome(t *testing.T) {
	dir, skill := t.TempDir(), t.TempDir()
	write := func(p, v string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "CLAUDE.md"), "be terse")
	write(filepath.Join(dir, "settings.json"), `{"model":"sonnet"}`)
	write(filepath.Join(dir, "projects", "x", "s.jsonl"), "{}")
	write(filepath.Join(dir, ".credentials.json"), "{}")
	write(filepath.Join(dir, "plugins", "installed_plugins.json"), "{}")
	write(filepath.Join(dir, "history.jsonl"), "{}")
	write(filepath.Join(dir, "skills", "go", ".git", "HEAD"), "ref")
	write(filepath.Join(skill, "SKILL.md"), "review")
	if err := os.Symlink(skill, filepath.Join(dir, "skills", "review")); err != nil {
		t.Fatal(err)
	}
	tarball, skipped, err := packHome(hosting.HarnessClaudeCode, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hosting.ReadHome(hosting.HarnessClaudeCode, tarball)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range b.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, " "); got != "CLAUDE.md settings.json skills/review/SKILL.md" {
		t.Fatalf("packed %s", got)
	}
	if got := strings.Join(skipped, " "); got != ".credentials.json history.jsonl plugins/ projects/ skills/" {
		t.Fatalf("skipped %s", got)
	}
	out := filepath.Join(t.TempDir(), "pulled")
	if n, err := extractHome(b.Tar, hosting.HarnessClaudeCode, out); err != nil || n != 3 {
		t.Fatalf("pull: %d %v", n, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(out, "skills", "review", "SKILL.md")); string(raw) != "review" {
		t.Fatalf("pulled %q", raw)
	}
	if _, err := extractHome(b.Tar, hosting.HarnessClaudeCode, out); err == nil {
		t.Fatal("pulled into a non-empty dir")
	}
}
