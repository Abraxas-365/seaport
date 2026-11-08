package dockwa

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// Signal protocol state: peer identities, sessions, pre-keys, sender keys.
// Addresses are signal addresses ("<user>[_<agent>]:<device>").

func (d *dockStore) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_identity_keys (dock_id, their_id, identity) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id, their_id) DO UPDATE SET identity = excluded.identity`, d.dock, address, key[:])
	return pgerr.Map(err, "put whatsapp identity")
}

func (d *dockStore) DeleteAllIdentities(ctx context.Context, phone string) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_identity_keys WHERE dock_id = $1 AND starts_with(their_id, $2)`, d.dock, phone+":")
	return pgerr.Map(err, "delete whatsapp identities")
}

func (d *dockStore) DeleteIdentity(ctx context.Context, address string) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_identity_keys WHERE dock_id = $1 AND their_id = $2`, d.dock, address)
	return pgerr.Map(err, "delete whatsapp identity")
}

// IsTrustedIdentity trusts unknown peers (their key is saved afterwards).
func (d *dockStore) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	var known []byte
	err := d.q(ctx).GetContext(ctx, &known, `SELECT identity FROM wa_identity_keys WHERE dock_id = $1 AND their_id = $2`, d.dock, address)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, pgerr.Map(err, "get whatsapp identity")
	}
	if len(known) != 32 {
		return false, errx.Internal("whatsapp identity key has an invalid length")
	}
	return [32]byte(known) == key, nil
}

func (d *dockStore) sealSession(address string, session []byte) []byte {
	return d.st.seal.Seal(purpose("session", d.dock, address), session)
}

func (d *dockStore) GetSession(ctx context.Context, address string) ([]byte, error) {
	var sealed []byte
	err := d.q(ctx).GetContext(ctx, &sealed, `SELECT session FROM wa_sessions WHERE dock_id = $1 AND their_id = $2`, d.dock, address)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp session")
	}
	return d.st.open(purpose("session", d.dock, address), sealed)
}

func (d *dockStore) HasSession(ctx context.Context, address string) (bool, error) {
	var has bool
	err := d.q(ctx).GetContext(ctx, &has, `SELECT EXISTS (SELECT 1 FROM wa_sessions WHERE dock_id = $1 AND their_id = $2)`, d.dock, address)
	return has, pgerr.Map(err, "has whatsapp session")
}

// GetManySessions maps every requested address, nil when it has no session.
func (d *dockStore) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	var rows []struct {
		Address string `db:"their_id"`
		Session []byte `db:"session"`
	}
	if err := d.q(ctx).SelectContext(ctx, &rows, `SELECT their_id, session FROM wa_sessions
		WHERE dock_id = $1 AND their_id = ANY($2::text[])`, d.dock, addresses); err != nil {
		return nil, pgerr.Map(err, "get whatsapp sessions")
	}
	out := make(map[string][]byte, len(addresses))
	for _, a := range addresses {
		out[a] = nil
	}
	for _, r := range rows {
		plain, err := d.st.open(purpose("session", d.dock, r.Address), r.Session)
		if err != nil {
			return nil, err
		}
		out[r.Address] = plain
	}
	return out, nil
}

func (d *dockStore) PutSession(ctx context.Context, address string, session []byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_sessions (dock_id, their_id, session) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id, their_id) DO UPDATE SET session = excluded.session`, d.dock, address, d.sealSession(address, session))
	return pgerr.Map(err, "put whatsapp session")
}

func (d *dockStore) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	return d.st.inTx(ctx, func(ctx context.Context) error {
		for address, session := range sessions {
			if err := d.PutSession(ctx, address, session); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *dockStore) DeleteAllSessions(ctx context.Context, phone string) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_sessions WHERE dock_id = $1 AND starts_with(their_id, $2)`, d.dock, phone+":")
	return pgerr.Map(err, "delete whatsapp sessions")
}

func (d *dockStore) DeleteSession(ctx context.Context, address string) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_sessions WHERE dock_id = $1 AND their_id = $2`, d.dock, address)
	return pgerr.Map(err, "delete whatsapp session")
}

// MigratePNToLID moves a user's sessions, identities and sender keys from
// their phone-number address to their LID address (once per user and
// device store). Sealed values are bound to their address, so they are
// opened and sealed again under the new one.
func (d *dockStore) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	from, to := pn.SignalAddressUser(), lid.SignalAddressUser()
	d.migratedMu.Lock()
	done := d.migrated[from]
	d.migrated[from] = true
	d.migratedMu.Unlock()
	if done {
		return nil
	}
	moved := func(address string) string { return to + strings.TrimPrefix(address, from) }
	return d.st.inTx(ctx, func(ctx context.Context) error {
		q := d.q(ctx)
		var sessions []struct {
			Address string `db:"their_id"`
			Session []byte `db:"session"`
		}
		if err := q.SelectContext(ctx, &sessions, `SELECT their_id, session FROM wa_sessions
			WHERE dock_id = $1 AND starts_with(their_id, $2)`, d.dock, from+":"); err != nil {
			return pgerr.Map(err, "list whatsapp sessions to migrate")
		}
		for _, s := range sessions {
			plain, err := d.st.open(purpose("session", d.dock, s.Address), s.Session)
			if err != nil {
				return err
			}
			if err := d.PutSession(ctx, moved(s.Address), plain); err != nil {
				return err
			}
		}
		if err := d.DeleteAllSessions(ctx, from); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO wa_identity_keys (dock_id, their_id, identity)
			SELECT dock_id, $3 || substr(their_id, length($2) + 1), identity FROM wa_identity_keys
			WHERE dock_id = $1 AND starts_with(their_id, $2 || ':')
			ON CONFLICT (dock_id, their_id) DO UPDATE SET identity = excluded.identity`, d.dock, from, to); err != nil {
			return pgerr.Map(err, "migrate whatsapp identities")
		}
		if err := d.DeleteAllIdentities(ctx, from); err != nil {
			return err
		}
		var senderKeys []struct {
			Chat   string `db:"chat_id"`
			Sender string `db:"sender_id"`
			Key    []byte `db:"sender_key"`
		}
		if err := q.SelectContext(ctx, &senderKeys, `SELECT chat_id, sender_id, sender_key FROM wa_sender_keys
			WHERE dock_id = $1 AND starts_with(sender_id, $2)`, d.dock, from+":"); err != nil {
			return pgerr.Map(err, "list whatsapp sender keys to migrate")
		}
		for _, k := range senderKeys {
			plain, err := d.st.open(purpose("sender_key", d.dock, k.Chat, k.Sender), k.Key)
			if err != nil {
				return err
			}
			if err := d.PutSenderKey(ctx, k.Chat, moved(k.Sender), plain); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(ctx, `DELETE FROM wa_sender_keys WHERE dock_id = $1 AND starts_with(sender_id, $2)`, d.dock, from+":")
		return pgerr.Map(err, "delete migrated whatsapp sender keys")
	})
}

func preKeyPurpose(d *dockStore, id uint32) string {
	return purpose("pre_key", d.dock, strconv.FormatUint(uint64(id), 10))
}

func (d *dockStore) insertPreKey(ctx context.Context, key *keys.PreKey, uploaded bool) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_pre_keys (dock_id, key_id, key, uploaded) VALUES ($1, $2, $3, $4)`,
		d.dock, int64(key.KeyID), d.st.seal.Seal(preKeyPurpose(d, key.KeyID), key.Priv[:]), uploaded)
	return pgerr.Map(err, "insert whatsapp pre-key")
}

func (d *dockStore) nextPreKeyID(ctx context.Context) (uint32, error) {
	var last sql.NullInt64
	if err := d.q(ctx).GetContext(ctx, &last, `SELECT max(key_id) FROM wa_pre_keys WHERE dock_id = $1`, d.dock); err != nil {
		return 0, pgerr.Map(err, "next whatsapp pre-key id")
	}
	return uint32(last.Int64) + 1, nil
}

// GenOnePreKey makes a pre-key that is uploaded right away.
func (d *dockStore) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	d.preKeys.Lock()
	defer d.preKeys.Unlock()
	id, err := d.nextPreKeyID(ctx)
	if err != nil {
		return nil, err
	}
	key := keys.NewPreKey(id)
	return key, d.insertPreKey(ctx, key, true)
}

type preKeyRow struct {
	ID  int64  `db:"key_id"`
	Key []byte `db:"key"`
}

func (d *dockStore) preKey(r preKeyRow) (*keys.PreKey, error) {
	priv, err := d.st.open(preKeyPurpose(d, uint32(r.ID)), r.Key)
	if err != nil {
		return nil, err
	}
	if len(priv) != 32 {
		return nil, errx.Internal("whatsapp pre-key has an invalid length")
	}
	return &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey([32]byte(priv)), KeyID: uint32(r.ID)}, nil
}

// GetOrGenPreKeys returns count not-yet-uploaded pre-keys, generating the
// missing ones.
func (d *dockStore) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	d.preKeys.Lock()
	defer d.preKeys.Unlock()
	var rows []preKeyRow
	if err := d.q(ctx).SelectContext(ctx, &rows, `SELECT key_id, key FROM wa_pre_keys
		WHERE dock_id = $1 AND uploaded = false ORDER BY key_id LIMIT $2`, d.dock, int64(count)); err != nil {
		return nil, pgerr.Map(err, "list whatsapp pre-keys")
	}
	out := make([]*keys.PreKey, 0, count)
	for _, r := range rows {
		k, err := d.preKey(r)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if uint32(len(out)) < count {
		next, err := d.nextPreKeyID(ctx)
		if err != nil {
			return nil, err
		}
		for uint32(len(out)) < count {
			k := keys.NewPreKey(next)
			if err := d.insertPreKey(ctx, k, false); err != nil {
				return nil, err
			}
			out = append(out, k)
			next++
		}
	}
	return out, nil
}

func (d *dockStore) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	var r preKeyRow
	err := d.q(ctx).GetContext(ctx, &r, `SELECT key_id, key FROM wa_pre_keys WHERE dock_id = $1 AND key_id = $2`, d.dock, int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp pre-key")
	}
	return d.preKey(r)
}

func (d *dockStore) RemovePreKey(ctx context.Context, id uint32) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_pre_keys WHERE dock_id = $1 AND key_id = $2`, d.dock, int64(id))
	return pgerr.Map(err, "remove whatsapp pre-key")
}

func (d *dockStore) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	_, err := d.q(ctx).ExecContext(ctx, `UPDATE wa_pre_keys SET uploaded = true WHERE dock_id = $1 AND key_id <= $2`, d.dock, int64(upToID))
	return pgerr.Map(err, "mark whatsapp pre-keys uploaded")
}

func (d *dockStore) UploadedPreKeyCount(ctx context.Context) (int, error) {
	var n int
	err := d.q(ctx).GetContext(ctx, &n, `SELECT count(*) FROM wa_pre_keys WHERE dock_id = $1 AND uploaded = true`, d.dock)
	return n, pgerr.Map(err, "count whatsapp pre-keys")
}

func (d *dockStore) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_sender_keys (dock_id, chat_id, sender_id, sender_key) VALUES ($1, $2, $3, $4)
		ON CONFLICT (dock_id, chat_id, sender_id) DO UPDATE SET sender_key = excluded.sender_key`,
		d.dock, group, user, d.st.seal.Seal(purpose("sender_key", d.dock, group, user), session))
	return pgerr.Map(err, "put whatsapp sender key")
}

func (d *dockStore) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	var sealed []byte
	err := d.q(ctx).GetContext(ctx, &sealed, `SELECT sender_key FROM wa_sender_keys
		WHERE dock_id = $1 AND chat_id = $2 AND sender_id = $3`, d.dock, group, user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp sender key")
	}
	return d.st.open(purpose("sender_key", d.dock, group, user), sealed)
}
