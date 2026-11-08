package dockwa

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// Importing devices from whatsmeow's own sqlstore (whatsmeow_* tables,
// used before migration 00008): each linked dock whose device is still
// there and not yet in wa_devices is copied in, sealed, in one transaction.
// The whatsmeow_* rows are left untouched (rollback for one release; a
// later migration drops them).

// legacyExists: whatsmeow's tables are there (databases from before 00008).
func legacyExists(ctx context.Context, q queryer) (bool, error) {
	var exists bool
	if err := q.GetContext(ctx, &exists, `SELECT to_regclass('whatsmeow_device') IS NOT NULL`); err != nil {
		return false, pgerr.Map(err, "find whatsmeow store")
	}
	return exists, nil
}

// purgeLegacy deletes a device's clear-text copy in whatsmeow_* (its child
// rows cascade): unlinking or logging out must not leave usable keys there.
func purgeLegacy(ctx context.Context, q queryer, jid string) error {
	exists, err := legacyExists(ctx, q)
	if err != nil || !exists {
		return err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM whatsmeow_device WHERE jid = $1`, jid)
	return pgerr.Map(err, "delete whatsmeow device")
}

// ImportLegacy copies the remaining whatsmeow_* devices; it returns how
// many docks it imported. A device that fails to import is logged and
// skipped (its dock fails to restore, like any broken dock), so it doesn't
// keep the port down.
func (s *Store) ImportLegacy(ctx context.Context) (int, error) {
	exists, err := legacyExists(ctx, s.db)
	if err != nil || !exists {
		return 0, err
	}
	var todo []struct {
		Dock identity.DockID         `db:"id"`
		Org  identity.OrganizationID `db:"organization_id"`
		JID  string                  `db:"address"`
	}
	if err := s.db.SelectContext(ctx, &todo, `SELECT DISTINCT ON (d.address) d.id, d.organization_id, d.address
		FROM docks d JOIN whatsmeow_device w ON w.jid = d.address
		WHERE d.channel = 'whatsapp' AND d.status = 'linked'
		  AND NOT EXISTS (SELECT 1 FROM wa_devices x WHERE x.dock_id = d.id OR x.jid = d.address)
		ORDER BY d.address, d.updated_at DESC`); err != nil {
		return 0, pgerr.Map(err, "list whatsmeow devices to import")
	}
	imported := 0
	for _, t := range todo {
		err := s.inTx(ctx, func(ctx context.Context) error { return s.importDevice(ctx, t.Org, t.Dock, t.JID) })
		if err != nil {
			slog.Error("whatsapp device not moved to the sealed store; link the dock again", "dock", t.Dock, "err", err)
			continue
		}
		imported++
		slog.Info("whatsapp device moved to the sealed store", "dock", t.Dock)
	}
	return imported, nil
}

// key32 checks a stored key's length (a slice→array conversion would panic).
func key32(b []byte, what string) ([32]byte, error) {
	if len(b) != 32 {
		return [32]byte{}, errx.Internal("whatsmeow " + what + " has an invalid length")
	}
	return [32]byte(b), nil
}

type legacyDevice struct {
	LID                sql.NullString `db:"lid"`
	FacebookUUID       uuid.NullUUID  `db:"facebook_uuid"`
	RegistrationID     int64          `db:"registration_id"`
	Noise              []byte         `db:"noise_key"`
	Identity           []byte         `db:"identity_key"`
	SignedPreKey       []byte         `db:"signed_pre_key"`
	SignedPreKeyID     int64          `db:"signed_pre_key_id"`
	SignedPreKeySig    []byte         `db:"signed_pre_key_sig"`
	AdvKey             []byte         `db:"adv_key"`
	AdvDetails         []byte         `db:"adv_details"`
	AdvAccountSig      []byte         `db:"adv_account_sig"`
	AdvAccountSigKey   []byte         `db:"adv_account_sig_key"`
	AdvDeviceSig       []byte         `db:"adv_device_sig"`
	Platform           string         `db:"platform"`
	BusinessName       string         `db:"business_name"`
	PushName           string         `db:"push_name"`
	LIDMigrationTS     int64          `db:"lid_migration_ts"`
	CompanionMetaNonce string         `db:"companion_meta_nonce"`
}

func (s *Store) importDevice(ctx context.Context, org identity.OrganizationID, dock identity.DockID, jidText string) error {
	q := s.q(ctx)
	var w legacyDevice
	if err := q.GetContext(ctx, &w, `SELECT lid, facebook_uuid, registration_id, noise_key, identity_key, signed_pre_key,
		signed_pre_key_id, signed_pre_key_sig, adv_key, adv_details, adv_account_sig, adv_account_sig_key, adv_device_sig,
		platform, business_name, push_name, lid_migration_ts, companion_meta_nonce
		FROM whatsmeow_device WHERE jid = $1`, jidText); err != nil {
		return pgerr.Map(err, "read whatsmeow device")
	}
	jid, err := types.ParseJID(jidText)
	if err != nil {
		return errx.Wrap(err, "parse whatsmeow device jid", errx.TypeInternal)
	}
	noise, err := key32(w.Noise, "noise key")
	if err != nil {
		return err
	}
	ident, err := key32(w.Identity, "identity key")
	if err != nil {
		return err
	}
	spk, err := key32(w.SignedPreKey, "signed pre-key")
	if err != nil {
		return err
	}
	if len(w.SignedPreKeySig) != 64 {
		return errx.Internal("whatsmeow signed pre-key signature has an invalid length")
	}
	dev := &store.Device{
		ID:                    &jid,
		RegistrationID:        uint32(w.RegistrationID),
		NoiseKey:              keys.NewKeyPairFromPrivateKey(noise),
		IdentityKey:           keys.NewKeyPairFromPrivateKey(ident),
		SignedPreKey:          &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey(spk), KeyID: uint32(w.SignedPreKeyID), Signature: (*[64]byte)(w.SignedPreKeySig)},
		AdvSecretKey:          w.AdvKey,
		Account:               &waAdv.ADVSignedDeviceIdentity{Details: w.AdvDetails, AccountSignature: w.AdvAccountSig, AccountSignatureKey: w.AdvAccountSigKey, DeviceSignature: w.AdvDeviceSig},
		Platform:              w.Platform,
		BusinessName:          w.BusinessName,
		PushName:              w.PushName,
		FacebookUUID:          w.FacebookUUID.UUID,
		LIDMigrationTimestamp: w.LIDMigrationTS,
		CompanionMetaNonce:    w.CompanionMetaNonce,
		Initialized:           true, // don't attach stores to this throwaway copy
	}
	if w.LID.String != "" {
		if dev.LID, err = types.ParseJID(w.LID.String); err != nil {
			return errx.Wrap(err, "parse whatsmeow device lid", errx.TypeInternal)
		}
	}
	d := s.dockStore(org, dock)
	if err := d.PutDevice(ctx, dev); err != nil {
		return err
	}
	for _, step := range []func(context.Context, *dockStore, string) error{
		importSignal, importAppState, importMessages, importPlain,
	} {
		if err := step(ctx, d, jidText); err != nil {
			return err
		}
	}
	return nil
}

// selectRows runs a query into rows, mapping the error.
func selectRows[T any](ctx context.Context, d *dockStore, op, query string, args ...any) ([]T, error) {
	var rows []T
	if err := d.q(ctx).SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, pgerr.Map(err, op)
	}
	return rows, nil
}

func importSignal(ctx context.Context, d *dockStore, jid string) error {
	identities, err := selectRows[struct {
		ID  string `db:"their_id"`
		Key []byte `db:"identity"`
	}](ctx, d, "read whatsmeow identities", `SELECT their_id, identity FROM whatsmeow_identity_keys WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range identities {
		key, err := key32(r.Key, "identity of "+r.ID)
		if err != nil {
			return err
		}
		if err := d.PutIdentity(ctx, r.ID, key); err != nil {
			return err
		}
	}
	preKeys, err := selectRows[struct {
		ID       int64  `db:"key_id"`
		Key      []byte `db:"key"`
		Uploaded bool   `db:"uploaded"`
	}](ctx, d, "read whatsmeow pre-keys", `SELECT key_id, key, uploaded FROM whatsmeow_pre_keys WHERE jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range preKeys {
		key, err := key32(r.Key, "pre-key")
		if err != nil {
			return err
		}
		k := &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey(key), KeyID: uint32(r.ID)}
		if err := d.insertPreKey(ctx, k, r.Uploaded); err != nil {
			return err
		}
	}
	sessions, err := selectRows[struct {
		ID      string `db:"their_id"`
		Session []byte `db:"session"`
	}](ctx, d, "read whatsmeow sessions", `SELECT their_id, session FROM whatsmeow_sessions WHERE our_jid = $1 AND session IS NOT NULL`, jid)
	if err != nil {
		return err
	}
	for _, r := range sessions {
		if err := d.PutSession(ctx, r.ID, r.Session); err != nil {
			return err
		}
	}
	senderKeys, err := selectRows[struct {
		Chat   string `db:"chat_id"`
		Sender string `db:"sender_id"`
		Key    []byte `db:"sender_key"`
	}](ctx, d, "read whatsmeow sender keys", `SELECT chat_id, sender_id, sender_key FROM whatsmeow_sender_keys WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range senderKeys {
		if err := d.PutSenderKey(ctx, r.Chat, r.Sender, r.Key); err != nil {
			return err
		}
	}
	return nil
}

func importAppState(ctx context.Context, d *dockStore, jid string) error {
	syncKeys, err := selectRows[appKeyRow](ctx, d, "read whatsmeow app state keys",
		`SELECT key_id, key_data, timestamp, fingerprint FROM whatsmeow_app_state_sync_keys WHERE jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range syncKeys {
		if err := d.PutAppStateSyncKey(ctx, r.ID, store.AppStateSyncKey{Data: r.Data, Timestamp: r.Timestamp, Fingerprint: r.Fingerprint}); err != nil {
			return err
		}
	}
	return nil
}

func importMessages(ctx context.Context, d *dockStore, jid string) error {
	secrets, err := selectRows[struct {
		Chat, Sender, ID string
		Key              []byte
	}](ctx, d, "read whatsmeow message secrets", `SELECT chat_jid AS chat, sender_jid AS sender, message_id AS id, key
		FROM whatsmeow_message_secrets WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range secrets {
		if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_message_secrets (dock_id, chat_jid, sender_jid, message_id, key)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			d.dock, r.Chat, r.Sender, r.ID, d.st.seal.Seal(d.msgSecretPurpose(r.Chat, r.Sender, r.ID), r.Key)); err != nil {
			return pgerr.Map(err, "import whatsapp message secret")
		}
	}
	tokens, err := selectRows[struct {
		User      string        `db:"their_jid"`
		Token     []byte        `db:"token"`
		Timestamp int64         `db:"timestamp"`
		SenderTS  sql.NullInt64 `db:"sender_timestamp"`
	}](ctx, d, "read whatsmeow privacy tokens", `SELECT their_jid, token, timestamp, sender_timestamp
		FROM whatsmeow_privacy_tokens WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range tokens {
		if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_privacy_tokens (dock_id, their_jid, token, timestamp, sender_timestamp)
			VALUES ($1, $2, $3, $4, $5)`, d.dock, r.User, d.st.seal.Seal(purpose("privacy_token", d.dock, r.User), r.Token),
			r.Timestamp, r.SenderTS); err != nil {
			return pgerr.Map(err, "import whatsapp privacy token")
		}
	}
	var salt []byte
	err = d.q(ctx).GetContext(ctx, &salt, `SELECT salt FROM whatsmeow_nct_salt WHERE our_jid = $1`, jid)
	switch {
	case err == nil:
		if err := d.PutNCTSalt(ctx, salt); err != nil {
			return err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return pgerr.Map(err, "read whatsmeow nct salt")
	}
	events, err := selectRows[struct {
		Hash      []byte `db:"ciphertext_hash"`
		Plaintext []byte `db:"plaintext"`
		ServerTS  int64  `db:"server_timestamp"`
		InsertTS  int64  `db:"insert_timestamp"`
	}](ctx, d, "read whatsmeow event buffer", `SELECT ciphertext_hash, plaintext, server_timestamp, insert_timestamp
		FROM whatsmeow_event_buffer WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range events {
		var sealed []byte
		if r.Plaintext != nil {
			sealed = d.st.seal.Seal(d.eventPurpose(r.Hash), r.Plaintext)
		}
		if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_event_buffer (dock_id, ciphertext_hash, plaintext, server_timestamp, insert_timestamp)
			VALUES ($1, $2, $3, $4, $5)`, d.dock, r.Hash, sealed, r.ServerTS, r.InsertTS); err != nil {
			return pgerr.Map(err, "import whatsapp buffered event")
		}
	}
	retries, err := selectRows[struct {
		Chat      string `db:"chat_jid"`
		ID        string `db:"message_id"`
		Format    string `db:"format"`
		Plaintext []byte `db:"plaintext"`
		Timestamp int64  `db:"timestamp"`
	}](ctx, d, "read whatsmeow retry buffer", `SELECT chat_jid, message_id, format, plaintext, timestamp
		FROM whatsmeow_retry_buffer WHERE our_jid = $1`, jid)
	if err != nil {
		return err
	}
	for _, r := range retries {
		if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_retry_buffer (dock_id, chat_jid, message_id, format, plaintext, timestamp)
			VALUES ($1, $2, $3, $4, $5, $6)`, d.dock, r.Chat, r.ID, r.Format,
			d.st.seal.Seal(d.retryPurpose(r.Chat, r.ID), r.Plaintext), r.Timestamp); err != nil {
			return pgerr.Map(err, "import whatsapp outgoing event")
		}
	}
	return nil
}

// importPlain copies the non-secret tables as they are. whatsmeow's LID map
// is global (shared by every device); a dock gets only the mappings of the
// users its own device has talked to.
func importPlain(ctx context.Context, d *dockStore, jid string) error {
	for _, c := range []struct{ op, query string }{
		{"import whatsapp app state versions", `INSERT INTO wa_app_state_versions (dock_id, name, version, hash)
			SELECT $1, name, version, hash FROM whatsmeow_app_state_version WHERE jid = $2`},
		{"import whatsapp app state macs", `INSERT INTO wa_app_state_mutation_macs (dock_id, name, version, index_mac, value_mac)
			SELECT $1, name, version, index_mac, value_mac FROM whatsmeow_app_state_mutation_macs WHERE jid = $2`},
		{"import whatsapp contacts", `INSERT INTO wa_contacts (dock_id, their_jid, first_name, full_name, push_name, business_name, redacted_phone)
			SELECT $1, their_jid, first_name, full_name, push_name, business_name, redacted_phone FROM whatsmeow_contacts WHERE our_jid = $2`},
		{"import whatsapp chat settings", `INSERT INTO wa_chat_settings (dock_id, chat_jid, muted_until, pinned, archived, wasa_root_secret_id)
			SELECT $1, chat_jid, muted_until, pinned, archived, wasa_root_secret_id FROM whatsmeow_chat_settings WHERE our_jid = $2`},
		{"import whatsapp lid map", `WITH users AS (
				SELECT split_part(split_part(their_id, ':', 1), '_', 1) AS u FROM whatsmeow_sessions WHERE our_jid = $2
				UNION SELECT split_part(split_part(their_id, ':', 1), '_', 1) FROM whatsmeow_identity_keys WHERE our_jid = $2
				UNION SELECT split_part(their_jid, '@', 1) FROM whatsmeow_contacts WHERE our_jid = $2
				UNION SELECT split_part(their_jid, '@', 1) FROM whatsmeow_privacy_tokens WHERE our_jid = $2
				UNION SELECT split_part(sender_jid, '@', 1) FROM whatsmeow_message_secrets WHERE our_jid = $2)
			INSERT INTO wa_lid_map (dock_id, lid, pn)
			SELECT $1, m.lid, m.pn FROM whatsmeow_lid_map m
			WHERE m.lid IN (SELECT u FROM users) OR m.pn IN (SELECT u FROM users)`},
	} {
		if _, err := d.q(ctx).ExecContext(ctx, c.query, d.dock, jid); err != nil {
			return pgerr.Map(err, c.op)
		}
	}
	return nil
}
