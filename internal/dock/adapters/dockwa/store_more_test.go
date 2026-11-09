package dockwa_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/jmoiron/sqlx"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func linkedDevice(t *testing.T, st interface {
	NewDevice(identity.OrganizationID, identity.DockID) *store.Device
}, dock identity.DockID, jid types.JID) *store.Device {
	t.Helper()
	dev := st.NewDevice(org, dock)
	pair(dev, jid)
	if err := dev.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestStoreIsolatesDocksAndBindsRows(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	st := newStore(t, db, testKey)
	jidA, jidB := types.NewJID("15550000001", types.DefaultUserServer), types.NewJID("15550000002", types.DefaultUserServer)
	dockA, dockB := addDock(t, db, jidA.String()), addDock(t, db, jidB.String())
	a, b := linkedDevice(t, st, dockA, jidA), linkedDevice(t, st, dockB, jidB)

	peer := types.NewJID("15550007777", types.DefaultUserServer)
	if err := a.Sessions.PutSession(ctx, "777:0", []byte("session of A")); err != nil {
		t.Fatal(err)
	}
	if err := a.LIDs.PutLIDMapping(ctx, types.NewJID("4242", types.HiddenUserServer), peer); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Sessions.GetSession(ctx, "777:0"); err != nil || got != nil {
		t.Fatalf("dock B sees A's session: %q %v", got, err)
	}
	if lid, err := b.LIDs.GetLIDForPN(ctx, peer); err != nil || !lid.IsEmpty() {
		t.Fatalf("dock B sees A's LID map: %v %v", lid, err)
	}

	// A sealed value copied to another row (or dock) doesn't open there.
	if _, err := db.Exec(`INSERT INTO wa_sessions (dock_id, their_id, session)
		SELECT $2, their_id, session FROM wa_sessions WHERE dock_id = $1`, dockA, dockB); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sessions.GetSession(ctx, "777:0"); err == nil {
		t.Fatal("a session moved to another dock opened")
	}
	if _, err := db.Exec(`UPDATE wa_sessions SET their_id = '778:0' WHERE dock_id = $1`, dockA); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sessions.GetSession(ctx, "778:0"); err == nil {
		t.Fatal("a session moved to another address opened")
	}

	// Another key opens nothing: the device doesn't load.
	if _, err := newStore(t, db, "another-secret-key-0123456789abcdefgh").Device(ctx, org, dockA); err == nil {
		t.Fatal("device loaded with another key")
	}
	// The right key loads the same device, with working stores.
	loaded, err := st.Device(ctx, org, dockA)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v %v", loaded, err)
	}
	if *loaded.ID != jidA || *loaded.NoiseKey.Priv != *a.NoiseKey.Priv || *loaded.IdentityKey.Priv != *a.IdentityKey.Priv ||
		*loaded.SignedPreKey.Signature != *a.SignedPreKey.Signature || !bytes.Equal(loaded.Account.DeviceSignature, a.Account.DeviceSignature) ||
		loaded.RegistrationID != a.RegistrationID || loaded.LID != a.LID || loaded.PushName != a.PushName {
		t.Fatal("loaded device differs from the saved one")
	}
	if lid, err := loaded.LIDs.GetLIDForPN(ctx, peer); err != nil || lid.User != "4242" {
		t.Fatalf("loaded device's LID map: %v %v", lid, err)
	}
	// Another organization's dock is not found.
	if dev, err := st.Device(ctx, identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002"), dockA); err != nil || dev != nil {
		t.Fatalf("device of another organization: %v %v", dev, err)
	}

	// Relinking replaces the dock's device; the stale client can't save over it.
	jidA2 := types.NewJID("15550000003", types.DefaultUserServer)
	a2 := linkedDevice(t, st, dockA, jidA2)
	if loaded, _ := st.Device(ctx, org, dockA); loaded == nil || *loaded.ID != jidA2 {
		t.Fatal("relink did not replace the device")
	}
	a.PushName = "stale"
	if err := a.Save(ctx); err == nil {
		t.Fatal("stale device saved over the new pairing")
	}
	if got, _ := a2.Sessions.GetSession(ctx, "778:0"); got != nil {
		t.Fatal("the new pairing inherited the old device's sessions")
	}

	// Logout (Device.Delete) and dock deletion drop every key (cascade).
	if err := a2.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if dev, err := st.Device(ctx, org, dockA); err != nil || dev != nil {
		t.Fatalf("device after logout: %v %v", dev, err)
	}
	if _, err := db.Exec(`DELETE FROM docks WHERE id = $1`, dockB); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.Get(&left, `SELECT (SELECT count(*) FROM wa_devices) + (SELECT count(*) FROM wa_sessions) + (SELECT count(*) FROM wa_lid_map)`); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d rows left after logout and dock deletion", left)
	}
}

// Migration 00012 drops whatsmeow's clear-text tables, but refuses while
// a linked dock's device is only there (it would lose its link).
func TestMigrationDropsTheOldStore(t *testing.T) {
	db, legacy := testDBAt(t, 11)
	ctx := context.Background()
	jid := types.NewJID("15550004444", types.DefaultUserServer)
	old := legacy.NewDevice()
	pair(old, jid)
	if err := old.Save(ctx); err != nil {
		t.Fatal(err)
	}
	dock := addDock(t, db, jid.String())
	if err := bootstrap.Migrate(ctx, db.DB); err == nil || !strings.Contains(err.Error(), "run migrations") {
		t.Fatalf("migration with a stranded device: %v", err)
	}
	if !exists(t, db, "whatsmeow_device") {
		t.Fatal("the old store was dropped while a dock still needed it")
	}

	// once the device is in the sealed store, the old tables go
	linkedDevice(t, newStore(t, db, testKey), dock, jid)
	if err := bootstrap.Migrate(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.Get(&left, `SELECT count(*) FROM pg_tables WHERE tablename LIKE 'whatsmeow\_%'`); err != nil || left != 0 {
		t.Fatalf("whatsmeow tables left: %d %v", left, err)
	}
	if dev, err := newStore(t, db, testKey).Device(ctx, org, dock); err != nil || dev == nil || *dev.ID != jid {
		t.Fatalf("sealed device after the migration: %v %v", dev, err)
	}
}

func exists(t *testing.T, db *sqlx.DB, table string) bool {
	t.Helper()
	var ok bool
	if err := db.Get(&ok, `SELECT to_regclass($1) IS NOT NULL`, table); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestOpenRefusesAnotherKey(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	linkedDevice(t, newStore(t, db, testKey), addDock(t, db, jid.String()), jid)
	if err := newStore(t, db, testKey).CheckKey(ctx); err != nil {
		t.Fatalf("right key: %v", err)
	}
	if _, err := dockwa.Open(ctx, db, "another-secret-key-0123456789abcdefgh"); err == nil || !strings.Contains(err.Error(), "another SEAPORT_SECRET_KEY") {
		t.Fatalf("open with another key: %v", err)
	}
}
