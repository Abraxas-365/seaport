package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkill(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "triage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\r\nname: bug-triage\r\ndescription: \"When a customer reports a bug\"\r\n---\r\n\r\n# Triage\r\nAsk for steps.\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := readSkill(path)
	if err != nil || s.Name != "bug-triage" || s.Description != "When a customer reports a bug" || s.Body != "# Triage\nAsk for steps." {
		t.Fatalf("skill = %+v, err = %v", s, err)
	}
	if err := os.WriteFile(path, []byte("no front matter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSkill(path); err == nil {
		t.Fatal("a skill without a description was accepted")
	}
}
