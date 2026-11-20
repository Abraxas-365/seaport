package dockwa_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// A dock without a row has the defaults; a patch changes only what it
// names; another organization can't read or write the dock's settings, and
// deleting the dock deletes them.
func TestSettingsStore(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	s := dockwa.NewSettingsStore(db)
	d := addDock(t, db, "")

	got, err := s.Get(ctx, org, d)
	if err != nil || got != dockwa.DefaultSettings() {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	on, off, check := true, false, "✅"
	if got, err = s.Update(ctx, org, d, dockwa.SettingsPatch{MarkRead: &on}); err != nil ||
		got != (dockwa.Settings{MarkRead: true, Emoji: dockwa.DefaultEmoji}) {
		t.Fatalf("mark read on: %+v %v", got, err)
	}
	if got, err = s.Update(ctx, org, d, dockwa.SettingsPatch{React: &on, Emoji: &check}); err != nil ||
		got != (dockwa.Settings{React: true, Emoji: "✅", MarkRead: true}) {
		t.Fatalf("react with ✅: %+v %v", got, err)
	}
	if got, err = s.Update(ctx, org, d, dockwa.SettingsPatch{MarkRead: &off}); err != nil ||
		got != (dockwa.Settings{React: true, Emoji: "✅"}) {
		t.Fatalf("mark read off: %+v %v", got, err)
	}
	if again, err := s.Get(ctx, org, d); err != nil || again != got {
		t.Fatalf("read back: %+v %v", again, err)
	}

	other := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	if _, err := s.Update(ctx, other, d, dockwa.SettingsPatch{React: &off}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization's update: %v", err)
	}
	if theirs, err := s.Get(ctx, other, d); err != nil || theirs != dockwa.DefaultSettings() {
		t.Fatalf("another organization reads nothing: %+v %v", theirs, err)
	}
	if mine, _ := s.Get(ctx, org, d); mine != got {
		t.Fatalf("their update changed nothing: %+v", mine)
	}
	if _, err := s.Update(ctx, org, identity.NewDockID(), dockwa.SettingsPatch{React: &on}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("a missing dock: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM docks WHERE id = $1`, d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Get(&n, `SELECT count(*) FROM whatsapp_docks`); err != nil || n != 0 {
		t.Fatalf("rows after deleting the dock: %d %v", n, err)
	}
}
