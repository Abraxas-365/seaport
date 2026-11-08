package dockwa_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

const testKey = "dockwa-test-secret-0123456789abcdefghij"

// testDB is the wiped integration database with Seaport's migrations and
// whatsmeow's own sqlstore tables (the reference implementation).
func testDB(t *testing.T) (*sqlx.DB, *sqlstore.Container) {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Migrate(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	legacy := sqlstore.NewWithDB(db.DB, "pgx", nil)
	if err := legacy.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	return db, legacy
}

func newStore(t *testing.T, db *sqlx.DB, key string) *dockwa.Store {
	t.Helper()
	s, err := seal.New(key, dockwa.SealInfo)
	if err != nil {
		t.Fatal(err)
	}
	return dockwa.NewStore(db, s)
}

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func addDock(t *testing.T, db *sqlx.DB, address string) identity.DockID {
	t.Helper()
	id := identity.NewDockID()
	if _, err := db.Exec(`INSERT INTO docks (id, organization_id, channel, name, address, status)
		VALUES ($1, $2, 'whatsapp', 'test', $3, 'linked')`, id, org, address); err != nil {
		t.Fatal(err)
	}
	return id
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// pair fills in what a pairing sets, as whatsmeow does before Save.
func pair(dev *store.Device, jid types.JID) {
	dev.ID = &jid
	dev.LID = types.NewJID("1000"+jid.User, types.HiddenUserServer)
	dev.Platform, dev.PushName = "test", "Test"
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: randomBytes(40), AccountSignature: randomBytes(64),
		AccountSignatureKey: randomBytes(32), DeviceSignature: randomBytes(64)}
}

// script runs the same operations on a device and records what the store
// answers (randomly generated keys are recorded by id only).
func script(t *testing.T, dev *store.Device, secrets [][]byte) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	rec := func(op string, v ...any) { out = append(out, op+": "+fmt.Sprint(v...)) }
	pn := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("98765", types.HiddenUserServer)
	pnAddr, lidAddr := pn.SignalAddress().String(), lid.SignalAddress().String()
	pnDev := types.JID{User: pn.User, Device: 3, Server: types.DefaultUserServer}
	group := types.NewJID("12036302", types.GroupServer)
	ident, sess, sk, appKey, msgSecret, token, salt, event, retry := secrets[0], secrets[1], secrets[2], secrets[3], secrets[4], secrets[5], secrets[6], secrets[7], secrets[8]

	// identities
	trusted, err := dev.Identities.IsTrustedIdentity(ctx, pnAddr, [32]byte(ident))
	rec("trust unknown", trusted, err)
	rec("put identity", dev.Identities.PutIdentity(ctx, pnAddr, [32]byte(ident)))
	trusted, err = dev.Identities.IsTrustedIdentity(ctx, pnAddr, [32]byte(ident))
	rec("trust same", trusted, err)
	trusted, err = dev.Identities.IsTrustedIdentity(ctx, pnAddr, [32]byte(randomBytes(32)))
	rec("trust other", trusted, err)

	// sessions
	has, err := dev.Sessions.HasSession(ctx, pnAddr)
	rec("has none", has, err)
	got, err := dev.Sessions.GetSession(ctx, pnAddr)
	rec("get none", got == nil, err)
	rec("put session", dev.Sessions.PutSession(ctx, pnAddr, sess))
	got, err = dev.Sessions.GetSession(ctx, pnAddr)
	rec("get session", bytes.Equal(got, sess), err)
	rec("put many", dev.Sessions.PutManySessions(ctx, map[string][]byte{"222:0": []byte("s2"), "333:1": []byte("s3")}))
	many, err := dev.Sessions.GetManySessions(ctx, []string{pnAddr, "222:0", "444:0"})
	rec("get many", len(many), string(many["222:0"]), many["444:0"] == nil, bytes.Equal(many[pnAddr], sess), err)
	rec("delete session", dev.Sessions.DeleteSession(ctx, "333:1"))
	has, err = dev.Sessions.HasSession(ctx, "333:1")
	rec("has deleted", has, err)

	// sender keys, then the phone → LID migration of all three
	rec("put sender key", dev.SenderKeys.PutSenderKey(ctx, group.String(), pnAddr, sk))
	got, err = dev.SenderKeys.GetSenderKey(ctx, group.String(), pnAddr)
	rec("get sender key", bytes.Equal(got, sk), err)
	rec("migrate", dev.Sessions.MigratePNToLID(ctx, pn, lid))
	has, err = dev.Sessions.HasSession(ctx, pnAddr)
	rec("pn session gone", has, err)
	got, err = dev.Sessions.GetSession(ctx, lidAddr)
	rec("lid session", bytes.Equal(got, sess), err)
	trusted, err = dev.Identities.IsTrustedIdentity(ctx, lidAddr, [32]byte(ident))
	rec("lid identity", trusted, err)
	got, err = dev.SenderKeys.GetSenderKey(ctx, group.String(), lidAddr)
	rec("lid sender key", bytes.Equal(got, sk), err)
	got, err = dev.SenderKeys.GetSenderKey(ctx, group.String(), pnAddr)
	rec("pn sender key gone", got == nil, err)
	rec("delete all identities", dev.Identities.DeleteAllIdentities(ctx, lid.SignalAddressUser()))
	trusted, err = dev.Identities.IsTrustedIdentity(ctx, lidAddr, [32]byte(randomBytes(32)))
	rec("identity forgotten", trusted, err)
	rec("delete all sessions", dev.Sessions.DeleteAllSessions(ctx, "222"))
	has, err = dev.Sessions.HasSession(ctx, "222:0")
	rec("sessions forgotten", has, err)

	// pre-keys
	keys, err := dev.PreKeys.GetOrGenPreKeys(ctx, 5)
	rec("gen pre-keys", keyIDs(keys), err)
	again, err := dev.PreKeys.GetOrGenPreKeys(ctx, 3)
	rec("same pre-keys", keyIDs(again), samePriv(keys[:3], again), err)
	count, err := dev.PreKeys.UploadedPreKeyCount(ctx)
	rec("uploaded none", count, err)
	rec("mark uploaded", dev.PreKeys.MarkPreKeysAsUploaded(ctx, keys[2].KeyID))
	count, err = dev.PreKeys.UploadedPreKeyCount(ctx)
	rec("uploaded 3", count, err)
	more, err := dev.PreKeys.GetOrGenPreKeys(ctx, 3)
	rec("after upload", keyIDs(more), err)
	one, err := dev.PreKeys.GenOnePreKey(ctx)
	rec("gen one", one.KeyID, err)
	count, err = dev.PreKeys.UploadedPreKeyCount(ctx)
	rec("uploaded 4", count, err)
	k, err := dev.PreKeys.GetPreKey(ctx, keys[1].KeyID)
	rec("get pre-key", k.KeyID, *k.Priv == *keys[1].Priv, err)
	rec("remove pre-key", dev.PreKeys.RemovePreKey(ctx, keys[1].KeyID))
	k, err = dev.PreKeys.GetPreKey(ctx, keys[1].KeyID)
	rec("removed pre-key", k == nil, err)

	// app state
	latest, err := dev.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	rec("latest none", latest == nil, err)
	rec("put app key 1", dev.AppStateKeys.PutAppStateSyncKey(ctx, []byte("k1"), store.AppStateSyncKey{Data: appKey, Fingerprint: []byte("f1"), Timestamp: 10}))
	rec("put app key 2", dev.AppStateKeys.PutAppStateSyncKey(ctx, []byte("k2"), store.AppStateSyncKey{Data: []byte("d2"), Fingerprint: []byte("f2"), Timestamp: 20}))
	rec("older app key ignored", dev.AppStateKeys.PutAppStateSyncKey(ctx, []byte("k1"), store.AppStateSyncKey{Data: []byte("old"), Fingerprint: []byte("f0"), Timestamp: 5}))
	ak, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, []byte("k1"))
	rec("get app key", bytes.Equal(ak.Data, appKey), string(ak.Fingerprint), ak.Timestamp, err)
	latest, err = dev.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	rec("latest", string(latest), err)
	all, err := dev.AppStateKeys.GetAllAppStateSyncKeys(ctx)
	rec("all app keys", len(all), all[0].Timestamp, err)
	ak, err = dev.AppStateKeys.GetAppStateSyncKey(ctx, []byte("nope"))
	rec("missing app key", ak == nil, err)
	v, h, err := dev.AppState.GetAppStateVersion(ctx, "regular")
	rec("version none", v, h == [128]byte{}, err)
	hash := [128]byte(randomBytes(128))
	rec("put version", dev.AppState.PutAppStateVersion(ctx, "regular", 7, hash))
	v, h, err = dev.AppState.GetAppStateVersion(ctx, "regular")
	rec("version", v, h == hash, err)
	i1, i2, v1, v2 := randomBytes(32), randomBytes(32), randomBytes(32), randomBytes(32)
	rec("put macs", dev.AppState.PutAppStateMutationMACs(ctx, "regular", 7, []store.AppStateMutationMAC{{IndexMAC: i1, ValueMAC: v1}, {IndexMAC: i2, ValueMAC: v2}}))
	rec("put macs newer", dev.AppState.PutAppStateMutationMACs(ctx, "regular", 8, []store.AppStateMutationMAC{{IndexMAC: i1, ValueMAC: v2}}))
	mac, err := dev.AppState.GetAppStateMutationMAC(ctx, "regular", i1)
	rec("newest mac", bytes.Equal(mac, v2), err)
	rec("delete macs", dev.AppState.DeleteAppStateMutationMACs(ctx, "regular", [][]byte{i2}))
	mac, err = dev.AppState.GetAppStateMutationMAC(ctx, "regular", i2)
	rec("deleted mac", mac == nil, err)
	rec("delete version", dev.AppState.DeleteAppStateVersion(ctx, "regular"))
	mac, err = dev.AppState.GetAppStateMutationMAC(ctx, "regular", i1)
	rec("macs cascade", mac == nil, err)

	// contacts
	changed, prev, err := dev.Contacts.PutPushName(ctx, pn, "Ana")
	rec("push name", changed, prev, err)
	changed, prev, err = dev.Contacts.PutPushName(ctx, pn, "Ana")
	rec("push name same", changed, prev, err)
	changed, prev, err = dev.Contacts.PutPushName(ctx, pn, "Ana B")
	rec("push name changed", changed, prev, err)
	changed, prev, err = dev.Contacts.PutBusinessName(ctx, pn, "Shop")
	rec("business name", changed, prev, err)
	rec("contact name", dev.Contacts.PutContactName(ctx, pn, "Ana", "Ana Bell"))
	other := types.NewJID("15550002222", types.DefaultUserServer)
	rec("all names", dev.Contacts.PutAllContactNames(ctx, []store.ContactEntry{{JID: other, FirstName: "x", FullName: "X"}, {JID: other, FirstName: "Bo", FullName: "Bo Z"}}))
	rec("redacted", dev.Contacts.PutManyRedactedPhones(ctx, []store.RedactedPhoneEntry{{JID: lid, RedactedPhone: "+1•••11"}}))
	c, err := dev.Contacts.GetContact(ctx, pn)
	rec("contact", c, err)
	c, err = dev.Contacts.GetContact(ctx, types.NewJID("1", types.DefaultUserServer))
	rec("unknown contact", c, err)
	contacts, err := dev.Contacts.GetAllContacts(ctx)
	rec("all contacts", sortedContacts(contacts), err)

	// chat settings
	cs, err := dev.ChatSettings.GetChatSettings(ctx, group)
	rec("settings none", cs, err)
	until := time.Unix(time.Now().Add(time.Hour).Unix(), 0)
	rec("mute", dev.ChatSettings.PutMutedUntil(ctx, group, until))
	rec("pin", dev.ChatSettings.PutPinned(ctx, group, true))
	rec("archive", dev.ChatSettings.PutArchived(ctx, group, true))
	rec("wasa", dev.ChatSettings.PutWASARootSecretID(ctx, group, "ABC"))
	cs, err = dev.ChatSettings.GetChatSettings(ctx, group)
	rec("settings", cs.Found, cs.MutedUntil.Equal(until), cs.Pinned, cs.Archived, err)
	rec("mute forever", dev.ChatSettings.PutMutedUntil(ctx, group, store.MutedForever))
	cs, err = dev.ChatSettings.GetChatSettings(ctx, group)
	rec("forever", cs.MutedUntil.Equal(store.MutedForever), err)
	wasa, err := dev.ChatSettings.GetWASARootSecretID(ctx, group)
	rec("get wasa", wasa, err)

	// LID map
	l, err := dev.LIDs.GetLIDForPN(ctx, pn)
	rec("lid none", l, err)
	rec("put lid", dev.LIDs.PutLIDMapping(ctx, lid, pn))
	l, err = dev.LIDs.GetLIDForPN(ctx, pnDev)
	rec("lid for pn (device kept)", l, err)
	p, err := dev.LIDs.GetPNForLID(ctx, lid)
	rec("pn for lid", p, err)
	newLID := types.NewJID("55555", types.HiddenUserServer)
	rec("remap", dev.LIDs.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: newLID, PN: pn}, {LID: types.NewJID("777", types.HiddenUserServer), PN: other}}))
	l, err = dev.LIDs.GetLIDForPN(ctx, pn)
	rec("remapped", l, err)
	manyLIDs, err := dev.LIDs.GetManyLIDsForPNs(ctx, []types.JID{pn, pnDev, other, types.NewJID("1", types.DefaultUserServer)})
	rec("many lids", len(manyLIDs), manyLIDs[pnDev], manyLIDs[other], err)
	_, err = dev.LIDs.GetPNForLID(ctx, pn)
	rec("pn for non-lid fails", err != nil)

	// message secrets (found under the other identity of the sender)
	rec("put secret", dev.MsgSecrets.PutMessageSecret(ctx, group, newLID, "M1", msgSecret))
	rec("put secrets", dev.MsgSecrets.PutMessageSecrets(ctx, []store.MessageSecretInsert{{Chat: group, Sender: pn, ID: "M2", Secret: []byte("s2")}, {Chat: group, Sender: pn, ID: "M1", Secret: []byte("dup")}}))
	sec, who, err := dev.MsgSecrets.GetMessageSecret(ctx, group, newLID, "M1")
	rec("get secret", bytes.Equal(sec, msgSecret), who, err)
	sec, who, err = dev.MsgSecrets.GetMessageSecret(ctx, group, pn, "M1")
	rec("get secret by pn", bytes.Equal(sec, msgSecret), who, err)
	sec, who, err = dev.MsgSecrets.GetMessageSecret(ctx, group, pn, "nope")
	rec("missing secret", sec == nil, who, err)

	// privacy tokens
	now := time.Unix(time.Now().Unix(), 0)
	rec("put tokens", dev.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: newLID, Token: token, Timestamp: now, SenderTimestamp: now.Add(-time.Minute)}))
	tok, err := dev.PrivacyTokens.GetPrivacyToken(ctx, pn)
	rec("token by pn", tok != nil && bytes.Equal(tok.Token, token), tok != nil && tok.Timestamp.Equal(now), err)
	rec("put old token", dev.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: newLID, Token: []byte("old"), Timestamp: now.Add(-time.Hour)}))
	tok, err = dev.PrivacyTokens.GetPrivacyToken(ctx, newLID)
	rec("token kept", bytes.Equal(tok.Token, token), tok.SenderTimestamp.Equal(now.Add(-time.Minute)), err)
	n, err := dev.PrivacyTokens.DeleteExpiredPrivacyTokens(ctx, now.Add(time.Second))
	rec("expire tokens", n, err)
	tok, err = dev.PrivacyTokens.GetPrivacyToken(ctx, newLID)
	rec("token gone", tok == nil, err)

	// NCT salt
	s, err := dev.NCTSalt.GetNCTSalt(ctx)
	rec("salt none", s == nil, err)
	rec("put salt", dev.NCTSalt.PutNCTSalt(ctx, salt))
	s, err = dev.NCTSalt.GetNCTSalt(ctx)
	rec("salt", bytes.Equal(s, salt), err)
	rec("delete salt", dev.NCTSalt.DeleteNCTSalt(ctx))
	s, err = dev.NCTSalt.GetNCTSalt(ctx)
	rec("salt gone", s == nil, err)

	// event buffer
	h1, h2 := [32]byte(randomBytes(32)), [32]byte(randomBytes(32))
	be, err := dev.EventBuffer.GetBufferedEvent(ctx, h1)
	rec("event none", be == nil, err)
	rec("buffer in txn", dev.EventBuffer.DoDecryptionTxn(ctx, func(ctx context.Context) error {
		return dev.EventBuffer.PutBufferedEvent(ctx, h1, event, now)
	}))
	rec("failed txn", dev.EventBuffer.DoDecryptionTxn(ctx, func(ctx context.Context) error {
		if err := dev.EventBuffer.PutBufferedEvent(ctx, h2, []byte("x"), now); err != nil {
			return err
		}
		return fmt.Errorf("decryption failed")
	}))
	be, err = dev.EventBuffer.GetBufferedEvent(ctx, h2)
	rec("rolled back", be == nil, err)
	be, err = dev.EventBuffer.GetBufferedEvent(ctx, h1)
	rec("event", bytes.Equal(be.Plaintext, event), be.ServerTime.Equal(now), err)
	rec("clear event", dev.EventBuffer.ClearBufferedEventPlaintext(ctx, h1))
	be, err = dev.EventBuffer.GetBufferedEvent(ctx, h1)
	rec("cleared", be.Plaintext == nil, err)
	rec("put empty", dev.EventBuffer.PutBufferedEvent(ctx, h2, []byte{}, now))
	be, err = dev.EventBuffer.GetBufferedEvent(ctx, h2)
	rec("empty plaintext", be.Plaintext != nil, len(be.Plaintext), err)
	rec("delete old", dev.EventBuffer.DeleteOldBufferedHashes(ctx))

	// retry buffer
	_, _, err = dev.EventBuffer.GetOutgoingEvent(ctx, group, types.EmptyJID, "R1")
	rec("outgoing none fails", err != nil)
	rec("add outgoing", dev.EventBuffer.AddOutgoingEvent(ctx, pn, "R1", "proto", retry))
	format, plain, err := dev.EventBuffer.GetOutgoingEvent(ctx, newLID, pn, "R1")
	rec("outgoing by alt", format, bytes.Equal(plain, retry), err)
	rec("delete old outgoing", dev.EventBuffer.DeleteOldOutgoingEvents(ctx))
	return out
}

func keyIDs(ks []*keys.PreKey) []uint32 {
	out := make([]uint32, len(ks))
	for i, k := range ks {
		out[i] = k.KeyID
	}
	return out
}

func samePriv(a, b []*keys.PreKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if *a[i].Priv != *b[i].Priv {
			return false
		}
	}
	return true
}

func sortedContacts(m map[types.JID]types.ContactInfo) []string {
	var out []string
	for j, c := range m {
		out = append(out, fmt.Sprint(j, c))
	}
	slices.Sort(out)
	return out
}

func secretsFor() [][]byte {
	out := make([][]byte, 9)
	for i := range out {
		out[i] = randomBytes(32)
	}
	return out
}

func TestStoreBehavesLikeSQLStore(t *testing.T) {
	db, legacy := testDB(t)
	ctx := context.Background()
	secrets := secretsFor()

	ref := legacy.NewDevice()
	pair(ref, types.NewJID("15550009999", types.DefaultUserServer))
	if err := ref.Save(ctx); err != nil {
		t.Fatal(err)
	}
	want := script(t, ref, secrets)

	st := newStore(t, db, testKey)
	jid := types.NewJID("15550008888", types.DefaultUserServer)
	dock := addDock(t, db, jid.String())
	dev := st.NewDevice(org, dock)
	pair(dev, jid)
	if err := dev.Save(ctx); err != nil {
		t.Fatal(err)
	}
	got := script(t, dev, secrets)

	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d:\n got  %s\n want %s", i, got[i], want[i])
		}
	}

	t.Logf("%d steps agree", len(want))

	// Nothing secret is stored in clear: no wa_* column holds any of them,
	// while sqlstore's tables (the reference) hold them all (proving the
	// scan finds them).
	// The script deleted the token, salt and buffered event: store them again.
	now := time.Now()
	if err := errors.Join(
		dev.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: jid, Token: secrets[5], Timestamp: now}),
		dev.NCTSalt.PutNCTSalt(ctx, secrets[6]),
		dev.EventBuffer.PutBufferedEvent(ctx, [32]byte(randomBytes(32)), secrets[7], now),
	); err != nil {
		t.Fatal(err)
	}
	// secrets[0] is a peer's identity key: public, stored as is.
	needles := append(slices.Clone(secrets[1:]), dev.NoiseKey.Priv[:], dev.IdentityKey.Priv[:], dev.SignedPreKey.Priv[:], dev.AdvSecretKey)
	if hits := plaintextHits(t, db, "wa_", needles); len(hits) > 0 {
		t.Errorf("secrets in clear: %v", hits)
	}
	// (the reference still holds what the script did not delete)
	refNeedles := [][]byte{secrets[1], secrets[2], secrets[3], secrets[4], secrets[8],
		ref.NoiseKey.Priv[:], ref.IdentityKey.Priv[:], ref.SignedPreKey.Priv[:], ref.AdvSecretKey}
	if hits := plaintextHits(t, db, "whatsmeow_", refNeedles); len(hits) != len(refNeedles) {
		t.Errorf("the scan found %d of %d secrets in sqlstore's tables: %v", len(hits), len(refNeedles), hits)
	}
}

// plaintextHits scans every bytea and text column of the tables named
// prefix* for the needles; it returns the indexes of the needles found.
func plaintextHits(t *testing.T, db *sqlx.DB, prefix string, needles [][]byte) map[int]string {
	t.Helper()
	hits := map[int]string{}
	var cols []struct {
		Table  string `db:"table_name"`
		Column string `db:"column_name"`
	}
	if err := db.Select(&cols, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND starts_with(table_name, $1) AND data_type IN ('bytea', 'text')`, prefix); err != nil {
		t.Fatal(err)
	}
	if len(cols) == 0 {
		t.Fatal("no columns to scan")
	}
	for _, c := range cols {
		var values [][]byte
		if err := db.Select(&values, fmt.Sprintf(`SELECT %q::bytea FROM %q WHERE %q IS NOT NULL`, c.Column, c.Table, c.Column)); err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			for i, n := range needles {
				if bytes.Contains(v, n) {
					hits[i] = c.Table + "." + c.Column
				}
			}
		}
	}
	return hits
}
