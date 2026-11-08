package dockwa_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/identity"
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

func TestImportLegacyMovesDevicesSealed(t *testing.T) {
	db, legacy := testDB(t)
	ctx := context.Background()
	jid := types.NewJID("15550004444", types.DefaultUserServer)
	old := legacy.NewDevice()
	pair(old, jid)
	if err := old.Save(ctx); err != nil {
		t.Fatal(err)
	}
	peer, peerLID := types.NewJID("15550005555", types.DefaultUserServer), types.NewJID("5151", types.HiddenUserServer)
	stranger, strangerLID := types.NewJID("15550006666", types.DefaultUserServer), types.NewJID("6161", types.HiddenUserServer)
	group := types.NewJID("12036302", types.GroupServer)
	preKeys, err := old.PreKeys.GetOrGenPreKeys(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	hash := [128]byte(randomBytes(128))
	now := time.Unix(time.Now().Unix(), 0)
	for _, err := range []error{
		old.Identities.PutIdentity(ctx, peerLID.SignalAddress().String(), [32]byte(randomBytes(32))),
		old.Sessions.PutSession(ctx, peerLID.SignalAddress().String(), []byte("session")),
		old.SenderKeys.PutSenderKey(ctx, group.String(), peerLID.SignalAddress().String(), []byte("sender key")),
		old.PreKeys.MarkPreKeysAsUploaded(ctx, preKeys[1].KeyID),
		old.AppStateKeys.PutAppStateSyncKey(ctx, []byte("k1"), store.AppStateSyncKey{Data: []byte("app key"), Fingerprint: []byte("f"), Timestamp: 3}),
		old.AppState.PutAppStateVersion(ctx, "regular", 4, hash),
		old.AppState.PutAppStateMutationMACs(ctx, "regular", 4, []store.AppStateMutationMAC{{IndexMAC: randomBytes(32), ValueMAC: randomBytes(32)}}),
		old.Contacts.PutContactName(ctx, peer, "Ana", "Ana Bell"),
		old.ChatSettings.PutPinned(ctx, group, true),
		old.MsgSecrets.PutMessageSecret(ctx, group, peerLID, "M1", []byte("message secret")),
		old.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: peerLID, Token: []byte("token"), Timestamp: now}),
		old.NCTSalt.PutNCTSalt(ctx, []byte("salt")),
		old.EventBuffer.PutBufferedEvent(ctx, [32]byte(randomBytes(32)), []byte("buffered"), now),
		old.EventBuffer.AddOutgoingEvent(ctx, group, "R1", "proto", []byte("outgoing")),
		old.LIDs.PutLIDMapping(ctx, peerLID, peer),
		old.LIDs.PutLIDMapping(ctx, strangerLID, stranger), // another device's peer
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	dock := addDock(t, db, jid.String())
	addDock(t, db, "") // an unlinked dock: nothing to import
	// a corrupt device is skipped, it doesn't stop the others (or the port)
	badJID := types.NewJID("15550003333", types.DefaultUserServer)
	bad := legacy.NewDevice()
	pair(bad, badJID)
	if err := bad.Save(ctx); err != nil {
		t.Fatal(err)
	}
	// (whatsmeow's CHECK keeps lengths right; a damaged table must not panic us)
	if _, err := db.Exec(`ALTER TABLE whatsmeow_device DROP CONSTRAINT whatsmeow_device_noise_key_check;
		UPDATE whatsmeow_device SET noise_key = '\x00' WHERE jid = '` + badJID.String() + `'`); err != nil {
		t.Fatal(err)
	}
	badDock := addDock(t, db, badJID.String())

	st := newStore(t, db, testKey)
	n, err := st.ImportLegacy(ctx)
	if err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	if n, err := st.ImportLegacy(ctx); err != nil || n != 0 {
		t.Fatalf("second import: %d %v", n, err)
	}
	dev, err := st.Device(ctx, org, dock)
	if err != nil || dev == nil {
		t.Fatalf("imported device: %v %v", dev, err)
	}
	if *dev.ID != jid || *dev.NoiseKey.Priv != *old.NoiseKey.Priv || *dev.IdentityKey.Priv != *old.IdentityKey.Priv ||
		*dev.SignedPreKey.Priv != *old.SignedPreKey.Priv || dev.SignedPreKey.KeyID != old.SignedPreKey.KeyID ||
		!bytes.Equal(dev.AdvSecretKey, old.AdvSecretKey) || !bytes.Equal(dev.Account.Details, old.Account.Details) ||
		dev.RegistrationID != old.RegistrationID || dev.LID != old.LID || dev.Platform != old.Platform {
		t.Fatal("imported device differs from whatsmeow's")
	}
	check := func(what string, ok bool, err error) {
		t.Helper()
		if err != nil || !ok {
			t.Errorf("%s: ok=%v err=%v", what, ok, err)
		}
	}
	addr := peerLID.SignalAddress().String()
	s, err := dev.Sessions.GetSession(ctx, addr)
	check("session", string(s) == "session", err)
	sk, err := dev.SenderKeys.GetSenderKey(ctx, group.String(), addr)
	check("sender key", string(sk) == "sender key", err)
	up, err := dev.PreKeys.UploadedPreKeyCount(ctx)
	check("uploaded pre-keys", up == 2, err)
	pk, err := dev.PreKeys.GetPreKey(ctx, preKeys[3].KeyID)
	check("pre-key", pk != nil && *pk.Priv == *preKeys[3].Priv, err)
	ak, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, []byte("k1"))
	check("app state key", ak != nil && string(ak.Data) == "app key", err)
	v, h, err := dev.AppState.GetAppStateVersion(ctx, "regular")
	check("app state version", v == 4 && h == hash, err)
	c, err := dev.Contacts.GetContact(ctx, peer)
	check("contact", c.FullName == "Ana Bell", err)
	cs, err := dev.ChatSettings.GetChatSettings(ctx, group)
	check("chat settings", cs.Pinned, err)
	ms, _, err := dev.MsgSecrets.GetMessageSecret(ctx, group, peer, "M1")
	check("message secret (by phone, via the imported LID map)", string(ms) == "message secret", err)
	tok, err := dev.PrivacyTokens.GetPrivacyToken(ctx, peerLID)
	check("privacy token", tok != nil && string(tok.Token) == "token", err)
	salt, err := dev.NCTSalt.GetNCTSalt(ctx)
	check("nct salt", string(salt) == "salt", err)
	f, out, err := dev.EventBuffer.GetOutgoingEvent(ctx, group, types.EmptyJID, "R1")
	check("outgoing event", f == "proto" && string(out) == "outgoing", err)
	sl, err := dev.LIDs.GetLIDForPN(ctx, stranger)
	check("only this device's peers in its LID map", sl.IsEmpty(), err)

	// The whatsmeow_* rows stay (rollback) and every moved secret is sealed.
	var kept int
	if err := db.Get(&kept, `SELECT count(*) FROM whatsmeow_device WHERE jid = $1`, jid.String()); err != nil || kept != 1 {
		t.Fatalf("whatsmeow_device rows: %d %v", kept, err)
	}
	needles := [][]byte{[]byte("session"), []byte("sender key"), []byte("app key"), []byte("message secret"), []byte("token"),
		[]byte("salt"), []byte("buffered"), []byte("outgoing"), old.NoiseKey.Priv[:], old.IdentityKey.Priv[:], preKeys[3].Priv[:]}
	if hits := plaintextHits(t, db, "wa_", needles); len(hits) > 0 {
		t.Errorf("secrets in clear after import: %v", hits)
	}
	if dev, err := st.Device(ctx, org, badDock); err != nil || dev != nil {
		t.Fatalf("corrupt device: %v %v", dev, err)
	}

	// Logging out (or unlinking) also drops the clear-text copy: no usable
	// key stays behind in whatsmeow_*.
	if err := dev.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDock(ctx, org, badDock); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.Get(&left, `SELECT (SELECT count(*) FROM whatsmeow_device) + (SELECT count(*) FROM whatsmeow_sessions)
		+ (SELECT count(*) FROM whatsmeow_pre_keys) + (SELECT count(*) FROM wa_devices)`); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d device/key rows left after logout and unlink", left)
	}
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
