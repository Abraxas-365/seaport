package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanPush(t *testing.T) {
	local := map[string]string{"new": "a\n", "same": "text\n", "edited": "two\n"}
	remote := map[string]string{"same": "text", "edited": "one", "gone": "x"}

	got := planPush(local, remote, false)
	want := []pushStep{{pushUpdate, "edited"}, {pushCreate, "new"}, {pushUnchanged, "same"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without prune: %v", got)
	}
	got = planPush(local, remote, true)
	want = []pushStep{{pushUpdate, "edited"}, {pushDelete, "gone"}, {pushCreate, "new"}, {pushUnchanged, "same"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with prune: %v", got)
	}
}

func TestReadEntryFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"customers.md": "Ana Torres\n", "Bad Name.md": "x", "notes.txt": "x", "UPPER.md": "x", "ok_1-a.md": "y",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, skipped, err := readEntryFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files["customers"] != "Ana Torres\n" || files["ok_1-a"] != "y" {
		t.Fatalf("files %v", files)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped %v", skipped)
	}
}
