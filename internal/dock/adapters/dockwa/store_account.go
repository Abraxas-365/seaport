package dockwa

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// App state (contacts, chat settings… synced from the phone) and the
// account's local data.

func (d *dockStore) appKeyPurpose(id []byte) string {
	return purpose("app_state_sync_key", d.dock, hexKey(id))
}

// PutAppStateSyncKey keeps the newest key per id.
func (d *dockStore) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_app_state_sync_keys (dock_id, key_id, key_data, timestamp, fingerprint)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dock_id, key_id) DO UPDATE SET key_data = excluded.key_data, timestamp = excluded.timestamp,
			fingerprint = excluded.fingerprint
		WHERE excluded.timestamp > wa_app_state_sync_keys.timestamp`,
		d.dock, id, d.st.seal.Seal(d.appKeyPurpose(id), key.Data), key.Timestamp, key.Fingerprint)
	return pgerr.Map(err, "put whatsapp app state key")
}

type appKeyRow struct {
	ID          []byte `db:"key_id"`
	Data        []byte `db:"key_data"`
	Timestamp   int64  `db:"timestamp"`
	Fingerprint []byte `db:"fingerprint"`
}

func (d *dockStore) appKey(r appKeyRow) (*store.AppStateSyncKey, error) {
	data, err := d.st.open(d.appKeyPurpose(r.ID), r.Data)
	if err != nil {
		return nil, err
	}
	return &store.AppStateSyncKey{Data: data, Fingerprint: r.Fingerprint, Timestamp: r.Timestamp}, nil
}

func (d *dockStore) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	var r appKeyRow
	err := d.q(ctx).GetContext(ctx, &r, `SELECT key_id, key_data, timestamp, fingerprint FROM wa_app_state_sync_keys
		WHERE dock_id = $1 AND key_id = $2`, d.dock, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp app state key")
	}
	return d.appKey(r)
}

func (d *dockStore) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	var id []byte
	err := d.q(ctx).GetContext(ctx, &id, `SELECT key_id FROM wa_app_state_sync_keys WHERE dock_id = $1
		ORDER BY timestamp DESC LIMIT 1`, d.dock)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return id, pgerr.Map(err, "latest whatsapp app state key")
}

func (d *dockStore) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	var rows []appKeyRow
	if err := d.q(ctx).SelectContext(ctx, &rows, `SELECT key_id, key_data, timestamp, fingerprint FROM wa_app_state_sync_keys
		WHERE dock_id = $1 ORDER BY timestamp DESC`, d.dock); err != nil {
		return nil, pgerr.Map(err, "list whatsapp app state keys")
	}
	out := make([]*store.AppStateSyncKey, 0, len(rows))
	for _, r := range rows {
		k, err := d.appKey(r)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func (d *dockStore) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_app_state_versions (dock_id, name, version, hash) VALUES ($1, $2, $3, $4)
		ON CONFLICT (dock_id, name) DO UPDATE SET version = excluded.version, hash = excluded.hash`,
		d.dock, name, int64(version), hash[:])
	return pgerr.Map(err, "put whatsapp app state version")
}

// GetAppStateVersion: 0 and an empty hash when unknown (the initial state).
func (d *dockStore) GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error) {
	var row struct {
		Version int64  `db:"version"`
		Hash    []byte `db:"hash"`
	}
	err := d.q(ctx).GetContext(ctx, &row, `SELECT version, hash FROM wa_app_state_versions WHERE dock_id = $1 AND name = $2`, d.dock, name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, [128]byte{}, nil
	case err != nil:
		return 0, [128]byte{}, pgerr.Map(err, "get whatsapp app state version")
	case len(row.Hash) != 128:
		return 0, [128]byte{}, errx.Internal("whatsapp app state hash has an invalid length")
	case row.Version == 0:
		return 0, [128]byte{}, errx.Internal("whatsapp app state " + name + " has version 0")
	}
	return uint64(row.Version), [128]byte(row.Hash), nil
}

func (d *dockStore) DeleteAppStateVersion(ctx context.Context, name string) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_app_state_versions WHERE dock_id = $1 AND name = $2`, d.dock, name)
	return pgerr.Map(err, "delete whatsapp app state version")
}

func (d *dockStore) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	if len(mutations) == 0 {
		return nil
	}
	return d.st.inTx(ctx, func(ctx context.Context) error {
		for chunk := range slices.Chunk(mutations, 500) {
			index, value := make([][]byte, len(chunk)), make([][]byte, len(chunk))
			for i, m := range chunk {
				index[i], value[i] = m.IndexMAC, m.ValueMAC
			}
			if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_app_state_mutation_macs (dock_id, name, version, index_mac, value_mac)
				SELECT $1, $2, $3, i, v FROM unnest($4::bytea[], $5::bytea[]) AS m(i, v)`,
				d.dock, name, int64(version), index, value); err != nil {
				return pgerr.Map(err, "put whatsapp app state macs")
			}
		}
		return nil
	})
}

func (d *dockStore) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	if len(indexMACs) == 0 {
		return nil
	}
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_app_state_mutation_macs
		WHERE dock_id = $1 AND name = $2 AND index_mac = ANY($3::bytea[])`, d.dock, name, indexMACs)
	return pgerr.Map(err, "delete whatsapp app state macs")
}

func (d *dockStore) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) ([]byte, error) {
	var mac []byte
	err := d.q(ctx).GetContext(ctx, &mac, `SELECT value_mac FROM wa_app_state_mutation_macs
		WHERE dock_id = $1 AND name = $2 AND index_mac = $3 ORDER BY version DESC LIMIT 1`, d.dock, name, indexMAC)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mac, pgerr.Map(err, "get whatsapp app state mac")
}

// Contacts: cached per device store (whatsmeow reads them on every message).

type contactRow struct {
	JID           string         `db:"their_jid"`
	FirstName     sql.NullString `db:"first_name"`
	FullName      sql.NullString `db:"full_name"`
	PushName      sql.NullString `db:"push_name"`
	BusinessName  sql.NullString `db:"business_name"`
	RedactedPhone sql.NullString `db:"redacted_phone"`
}

func (r contactRow) info(found bool) *types.ContactInfo {
	return &types.ContactInfo{Found: found, FirstName: r.FirstName.String, FullName: r.FullName.String,
		PushName: r.PushName.String, BusinessName: r.BusinessName.String, RedactedPhone: r.RedactedPhone.String}
}

const contactColumns = `their_jid, first_name, full_name, push_name, business_name, redacted_phone`

// contact reads through the cache; d.contactsMu must be held.
func (d *dockStore) contact(ctx context.Context, user types.JID) (*types.ContactInfo, error) {
	if c, ok := d.contacts[user]; ok {
		return c, nil
	}
	var r contactRow
	err := d.q(ctx).GetContext(ctx, &r, `SELECT `+contactColumns+` FROM wa_contacts WHERE dock_id = $1 AND their_jid = $2`, d.dock, user.String())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, pgerr.Map(err, "get whatsapp contact")
	}
	c := r.info(err == nil)
	d.contacts[user] = c
	return c, nil
}

// putContactField sets one column if it changed, returning the old value.
func (d *dockStore) putContactField(ctx context.Context, user types.JID, column, value string, current func(*types.ContactInfo) *string) (bool, string, error) {
	d.contactsMu.Lock()
	defer d.contactsMu.Unlock()
	c, err := d.contact(ctx, user)
	if err != nil {
		return false, "", err
	}
	field := current(c)
	if *field == value {
		return false, "", nil
	}
	// column is one of the fixed names passed by the callers below.
	if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_contacts (dock_id, their_jid, `+column+`) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id, their_jid) DO UPDATE SET `+column+` = excluded.`+column, d.dock, user.String(), value); err != nil {
		return false, "", pgerr.Map(err, "put whatsapp contact")
	}
	old := *field
	*field, c.Found = value, true
	return true, old, nil
}

func (d *dockStore) PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error) {
	return d.putContactField(ctx, user, "push_name", pushName, func(c *types.ContactInfo) *string { return &c.PushName })
}

func (d *dockStore) PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error) {
	return d.putContactField(ctx, user, "business_name", businessName, func(c *types.ContactInfo) *string { return &c.BusinessName })
}

func (d *dockStore) PutContactName(ctx context.Context, user types.JID, firstName, fullName string) error {
	d.contactsMu.Lock()
	defer d.contactsMu.Unlock()
	c, err := d.contact(ctx, user)
	if err != nil {
		return err
	}
	if c.FirstName == firstName && c.FullName == fullName {
		return nil
	}
	if err := d.upsertNames(ctx, []store.ContactEntry{{JID: user, FirstName: firstName, FullName: fullName}}); err != nil {
		return err
	}
	c.FirstName, c.FullName, c.Found = firstName, fullName, true
	return nil
}

func (d *dockStore) upsertNames(ctx context.Context, contacts []store.ContactEntry) error {
	jids, first, full := make([]string, len(contacts)), make([]string, len(contacts)), make([]string, len(contacts))
	for i, c := range contacts {
		jids[i], first[i], full[i] = c.JID.String(), c.FirstName, c.FullName
	}
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_contacts (dock_id, their_jid, first_name, full_name)
		SELECT $1, j, f, n FROM unnest($2::text[], $3::text[], $4::text[]) AS c(j, f, n)
		ON CONFLICT (dock_id, their_jid) DO UPDATE SET first_name = excluded.first_name, full_name = excluded.full_name`,
		d.dock, jids, first, full)
	return pgerr.Map(err, "put whatsapp contact names")
}

// lastPerJID keeps the last entry of each JID (the newest), in order.
func lastPerJID[T any](entries []T, jid func(T) types.JID) []T {
	seen := map[types.JID]bool{}
	out := make([]T, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		if j := jid(entries[i]); !seen[j] {
			seen[j] = true
			out = append(out, entries[i])
		}
	}
	slices.Reverse(out)
	return out
}

func (d *dockStore) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	if len(contacts) == 0 {
		return nil
	}
	contacts = lastPerJID(contacts, func(c store.ContactEntry) types.JID { return c.JID })
	err := d.st.inTx(ctx, func(ctx context.Context) error {
		for chunk := range slices.Chunk(contacts, 500) {
			if err := d.upsertNames(ctx, chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.contactsMu.Lock()
	d.contacts = map[types.JID]*types.ContactInfo{}
	d.contactsMu.Unlock()
	return nil
}

func (d *dockStore) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	if len(entries) == 0 {
		return nil
	}
	entries = lastPerJID(entries, func(e store.RedactedPhoneEntry) types.JID { return e.JID })
	err := d.st.inTx(ctx, func(ctx context.Context) error {
		for chunk := range slices.Chunk(entries, 500) {
			jids, phones := make([]string, len(chunk)), make([]string, len(chunk))
			for i, e := range chunk {
				jids[i], phones[i] = e.JID.String(), e.RedactedPhone
			}
			if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_contacts (dock_id, their_jid, redacted_phone)
				SELECT $1, j, p FROM unnest($2::text[], $3::text[]) AS c(j, p)
				ON CONFLICT (dock_id, their_jid) DO UPDATE SET redacted_phone = excluded.redacted_phone`,
				d.dock, jids, phones); err != nil {
				return pgerr.Map(err, "put whatsapp redacted phones")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.contactsMu.Lock()
	for _, e := range entries {
		if c, ok := d.contacts[e.JID]; !ok || c.RedactedPhone != e.RedactedPhone {
			delete(d.contacts, e.JID)
		}
	}
	d.contactsMu.Unlock()
	return nil
}

func (d *dockStore) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	d.contactsMu.Lock()
	defer d.contactsMu.Unlock()
	c, err := d.contact(ctx, user)
	if err != nil {
		return types.ContactInfo{}, err
	}
	return *c, nil
}

func (d *dockStore) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	d.contactsMu.Lock()
	defer d.contactsMu.Unlock()
	var rows []contactRow
	if err := d.q(ctx).SelectContext(ctx, &rows, `SELECT `+contactColumns+` FROM wa_contacts WHERE dock_id = $1`, d.dock); err != nil {
		return nil, pgerr.Map(err, "list whatsapp contacts")
	}
	out := make(map[types.JID]types.ContactInfo, len(rows))
	for _, r := range rows {
		jid, err := types.ParseJID(r.JID)
		if err != nil {
			return nil, errx.Wrap(err, "parse whatsapp contact jid", errx.TypeInternal)
		}
		c := r.info(true)
		out[jid] = *c
		d.contacts[jid] = c
	}
	return out, nil
}

// Chat settings.

func (d *dockStore) putChatSetting(ctx context.Context, chat types.JID, column string, value any) error {
	// column is one of the fixed names passed by the callers below.
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_chat_settings (dock_id, chat_jid, `+column+`) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id, chat_jid) DO UPDATE SET `+column+` = excluded.`+column, d.dock, chat.String(), value)
	return pgerr.Map(err, "put whatsapp chat setting")
}

// PutMutedUntil stores -1 for "forever", 0 for "not muted".
func (d *dockStore) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	var v int64
	switch {
	case mutedUntil.Equal(store.MutedForever):
		v = -1
	case !mutedUntil.IsZero():
		v = mutedUntil.Unix()
	}
	return d.putChatSetting(ctx, chat, "muted_until", v)
}

func (d *dockStore) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	return d.putChatSetting(ctx, chat, "pinned", pinned)
}

func (d *dockStore) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	return d.putChatSetting(ctx, chat, "archived", archived)
}

func (d *dockStore) PutWASARootSecretID(ctx context.Context, chat types.JID, id types.MessageID) error {
	return d.putChatSetting(ctx, chat, "wasa_root_secret_id", string(id))
}

func (d *dockStore) GetWASARootSecretID(ctx context.Context, chat types.JID) (types.MessageID, error) {
	var id string
	err := d.q(ctx).GetContext(ctx, &id, `SELECT wasa_root_secret_id FROM wa_chat_settings WHERE dock_id = $1 AND chat_jid = $2`, d.dock, chat.String())
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return types.MessageID(id), pgerr.Map(err, "get whatsapp wasa root secret id")
}

func (d *dockStore) GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error) {
	var row struct {
		MutedUntil int64 `db:"muted_until"`
		Pinned     bool  `db:"pinned"`
		Archived   bool  `db:"archived"`
	}
	err := d.q(ctx).GetContext(ctx, &row, `SELECT muted_until, pinned, archived FROM wa_chat_settings
		WHERE dock_id = $1 AND chat_jid = $2`, d.dock, chat.String())
	if errors.Is(err, sql.ErrNoRows) {
		return types.LocalChatSettings{}, nil
	}
	if err != nil {
		return types.LocalChatSettings{}, pgerr.Map(err, "get whatsapp chat settings")
	}
	out := types.LocalChatSettings{Found: true, Pinned: row.Pinned, Archived: row.Archived}
	switch {
	case row.MutedUntil < 0:
		out.MutedUntil = store.MutedForever
	case row.MutedUntil > 0:
		out.MutedUntil = time.Unix(row.MutedUntil, 0)
	}
	return out, nil
}

// NCT salt.

func (d *dockStore) PutNCTSalt(ctx context.Context, salt []byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_nct_salts (dock_id, salt) VALUES ($1, $2)
		ON CONFLICT (dock_id) DO UPDATE SET salt = excluded.salt`, d.dock, d.st.seal.Seal(purpose("nct_salt", d.dock), salt))
	return pgerr.Map(err, "put whatsapp nct salt")
}

func (d *dockStore) GetNCTSalt(ctx context.Context) ([]byte, error) {
	var sealed []byte
	err := d.q(ctx).GetContext(ctx, &sealed, `SELECT salt FROM wa_nct_salts WHERE dock_id = $1`, d.dock)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp nct salt")
	}
	return d.st.open(purpose("nct_salt", d.dock), sealed)
}

func (d *dockStore) DeleteNCTSalt(ctx context.Context) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_nct_salts WHERE dock_id = $1`, d.dock)
	return pgerr.Map(err, "delete whatsapp nct salt")
}

func unixOrZero(sec sql.NullInt64) time.Time {
	if !sec.Valid {
		return time.Time{}
	}
	return time.Unix(sec.Int64, 0)
}
