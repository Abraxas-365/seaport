package dockwa

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// Message-level secrets, privacy tokens, the decrypted-event buffer, the
// retry buffer of our sent messages, and the dock's phone↔LID map.

// altJID is the other identity of a user (phone number ↔ LID), "" when
// unknown or not a user.
func (d *dockStore) altJID(ctx context.Context, j types.JID) (string, error) {
	var alt types.JID
	var err error
	switch j.Server {
	case types.HiddenUserServer:
		alt, err = d.GetPNForLID(ctx, j)
	case types.DefaultUserServer:
		alt, err = d.GetLIDForPN(ctx, j)
	default:
		return "", nil
	}
	if err != nil || alt.IsEmpty() {
		return "", err
	}
	return alt.ToNonAD().String(), nil
}

func (d *dockStore) msgSecretPurpose(chat, sender, id string) string {
	return purpose("message_secret", d.dock, chat, sender, id)
}

func (d *dockStore) putMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	c, s := chat.ToNonAD().String(), sender.ToNonAD().String()
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_message_secrets (dock_id, chat_jid, sender_jid, message_id, key)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
		d.dock, c, s, string(id), d.st.seal.Seal(d.msgSecretPurpose(c, s, string(id)), secret))
	return pgerr.Map(err, "put whatsapp message secret")
}

// PutMessageSecrets keeps the first secret of a message; incomplete
// entries are skipped.
func (d *dockStore) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	if len(inserts) == 0 {
		return nil
	}
	return d.st.inTx(ctx, func(ctx context.Context) error {
		for _, in := range inserts {
			if in.Chat.IsEmpty() || in.Sender.IsEmpty() || in.ID == "" || len(in.Secret) == 0 {
				continue
			}
			if err := d.putMessageSecret(ctx, in.Chat, in.Sender, in.ID, in.Secret); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *dockStore) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	return d.putMessageSecret(ctx, chat, sender, id, secret)
}

// GetMessageSecret finds the secret by chat and sender, each under either
// identity (phone number or LID), and returns the sender it was stored for.
func (d *dockStore) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error) {
	chat, sender = chat.ToNonAD(), sender.ToNonAD()
	altChat, err := d.altJID(ctx, chat)
	if err != nil {
		return nil, types.EmptyJID, err
	}
	altSender, err := d.altJID(ctx, sender)
	if err != nil {
		return nil, types.EmptyJID, err
	}
	var row struct {
		Chat   string `db:"chat_jid"`
		Sender string `db:"sender_jid"`
		Key    []byte `db:"key"`
	}
	err = d.q(ctx).GetContext(ctx, &row, `SELECT chat_jid, sender_jid, key FROM wa_message_secrets
		WHERE dock_id = $1 AND message_id = $2 AND chat_jid IN ($3, $4) AND sender_jid IN ($5, $6) LIMIT 1`,
		d.dock, string(id), chat.String(), altChat, sender.String(), altSender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.EmptyJID, nil
	}
	if err != nil {
		return nil, types.EmptyJID, pgerr.Map(err, "get whatsapp message secret")
	}
	secret, err := d.st.open(d.msgSecretPurpose(row.Chat, row.Sender, string(id)), row.Key)
	if err != nil {
		return nil, types.EmptyJID, err
	}
	realSender, err := types.ParseJID(row.Sender)
	if err != nil {
		return nil, types.EmptyJID, errx.Wrap(err, "parse whatsapp message sender", errx.TypeInternal)
	}
	return secret, realSender, nil
}

// PutPrivacyTokens keeps the newest token per user (a missing sender
// timestamp keeps the stored one).
func (d *dockStore) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	if len(tokens) == 0 {
		return nil
	}
	return d.st.inTx(ctx, func(ctx context.Context) error {
		for _, t := range tokens {
			user := t.User.ToNonAD().String()
			var senderTS sql.NullInt64
			if !t.SenderTimestamp.IsZero() {
				senderTS = sql.NullInt64{Int64: t.SenderTimestamp.Unix(), Valid: true}
			}
			if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_privacy_tokens (dock_id, their_jid, token, timestamp, sender_timestamp)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (dock_id, their_jid) DO UPDATE SET token = excluded.token, timestamp = excluded.timestamp,
					sender_timestamp = COALESCE(excluded.sender_timestamp, wa_privacy_tokens.sender_timestamp)
				WHERE excluded.timestamp >= wa_privacy_tokens.timestamp`,
				d.dock, user, d.st.seal.Seal(purpose("privacy_token", d.dock, user), t.Token), t.Timestamp.Unix(), senderTS); err != nil {
				return pgerr.Map(err, "put whatsapp privacy token")
			}
		}
		return nil
	})
}

// GetPrivacyToken finds the newest token of a user under either identity.
func (d *dockStore) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	user = user.ToNonAD()
	alt, err := d.altJID(ctx, user)
	if err != nil {
		return nil, err
	}
	var row struct {
		User      string        `db:"their_jid"`
		Token     []byte        `db:"token"`
		Timestamp int64         `db:"timestamp"`
		SenderTS  sql.NullInt64 `db:"sender_timestamp"`
	}
	err = d.q(ctx).GetContext(ctx, &row, `SELECT their_jid, token, timestamp, sender_timestamp FROM wa_privacy_tokens
		WHERE dock_id = $1 AND their_jid IN ($2, $3) ORDER BY timestamp DESC LIMIT 1`, d.dock, user.String(), alt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp privacy token")
	}
	token, err := d.st.open(purpose("privacy_token", d.dock, row.User), row.Token)
	if err != nil {
		return nil, err
	}
	return &store.PrivacyToken{User: user, Token: token, Timestamp: time.Unix(row.Timestamp, 0), SenderTimestamp: unixOrZero(row.SenderTS)}, nil
}

func (d *dockStore) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_privacy_tokens WHERE dock_id = $1 AND timestamp < $2`, d.dock, cutoff.Unix())
	if err != nil {
		return 0, pgerr.Map(err, "delete expired whatsapp privacy tokens")
	}
	n, err := res.RowsAffected()
	return n, pgerr.Map(err, "count deleted whatsapp privacy tokens")
}

// Event buffer: the plaintext of each decrypted incoming message, kept
// until it was handled (a crash replays it instead of decrypting twice —
// signal ratchets can't decrypt twice). NULL plaintext = handled.

func (d *dockStore) eventPurpose(hash []byte) string {
	return purpose("event_buffer", d.dock, hexKey(hash))
}

func (d *dockStore) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	var row struct {
		Plaintext []byte `db:"plaintext"`
		ServerTS  int64  `db:"server_timestamp"`
		InsertTS  int64  `db:"insert_timestamp"`
	}
	err := d.q(ctx).GetContext(ctx, &row, `SELECT plaintext, server_timestamp, insert_timestamp FROM wa_event_buffer
		WHERE dock_id = $1 AND ciphertext_hash = $2`, d.dock, ciphertextHash[:])
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "get whatsapp buffered event")
	}
	out := &store.BufferedEvent{ServerTime: time.Unix(row.ServerTS, 0), InsertTime: time.UnixMilli(row.InsertTS)}
	if row.Plaintext != nil {
		plain, err := d.st.open(d.eventPurpose(ciphertextHash[:]), row.Plaintext)
		if err != nil {
			return nil, err
		}
		if plain == nil {
			plain = []byte{}
		}
		out.Plaintext = plain
	}
	return out, nil
}

func (d *dockStore) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	var sealed []byte
	if plaintext != nil {
		sealed = d.st.seal.Seal(d.eventPurpose(ciphertextHash[:]), plaintext)
	}
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_event_buffer (dock_id, ciphertext_hash, plaintext, server_timestamp, insert_timestamp)
		VALUES ($1, $2, $3, $4, $5)`, d.dock, ciphertextHash[:], sealed, serverTimestamp.Unix(), time.Now().UnixMilli())
	return pgerr.Map(err, "put whatsapp buffered event")
}

// DoDecryptionTxn runs a decryption and its buffer write atomically.
func (d *dockStore) DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error {
	return d.st.inTx(ctx, fn)
}

func (d *dockStore) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	_, err := d.q(ctx).ExecContext(ctx, `UPDATE wa_event_buffer SET plaintext = NULL WHERE dock_id = $1 AND ciphertext_hash = $2`,
		d.dock, ciphertextHash[:])
	return pgerr.Map(err, "clear whatsapp buffered event")
}

// eventBufferTTL: WhatsApp's servers buffer events for 14 days.
const eventBufferTTL = 14 * 24 * time.Hour

func (d *dockStore) DeleteOldBufferedHashes(ctx context.Context) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_event_buffer WHERE dock_id = $1 AND insert_timestamp < $2`,
		d.dock, time.Now().Add(-eventBufferTTL).UnixMilli())
	return pgerr.Map(err, "delete old whatsapp buffered events")
}

// Retry buffer: our sent messages, to re-encrypt for a peer that asks.

const retryBufferTTL = 7 * 24 * time.Hour

func (d *dockStore) retryPurpose(chat, id string) string {
	return purpose("retry_buffer", d.dock, chat, id)
}

func (d *dockStore) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error) {
	var row struct {
		Chat      string `db:"chat_jid"`
		Format    string `db:"format"`
		Plaintext []byte `db:"plaintext"`
	}
	err := d.q(ctx).GetContext(ctx, &row, `SELECT chat_jid, format, plaintext FROM wa_retry_buffer
		WHERE dock_id = $1 AND chat_jid IN ($2, $3) AND message_id = $4 LIMIT 1`,
		d.dock, jidText(chatJID), jidText(altChatJID), string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, errx.NotFound("message " + string(id) + " is not in the retry buffer")
	}
	if err != nil {
		return "", nil, pgerr.Map(err, "get whatsapp outgoing event")
	}
	plain, err := d.st.open(d.retryPurpose(row.Chat, string(id)), row.Plaintext)
	if err != nil {
		return "", nil, err
	}
	return row.Format, plain, nil
}

func (d *dockStore) AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error {
	chat := jidText(chatJID)
	_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_retry_buffer (dock_id, chat_jid, message_id, format, plaintext, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (dock_id, chat_jid, message_id) DO UPDATE SET format = excluded.format, plaintext = excluded.plaintext,
			timestamp = excluded.timestamp`,
		d.dock, chat, string(id), format, d.st.seal.Seal(d.retryPurpose(chat, string(id)), plaintext), time.Now().UnixMilli())
	return pgerr.Map(err, "add whatsapp outgoing event")
}

func (d *dockStore) DeleteOldOutgoingEvents(ctx context.Context) error {
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_retry_buffer WHERE dock_id = $1 AND timestamp < $2`,
		d.dock, time.Now().Add(-retryBufferTTL).UnixMilli())
	return pgerr.Map(err, "delete old whatsapp outgoing events")
}

// Phone number ↔ LID map, cached (negative results too, like whatsmeow).
// Keys are the bare user parts.

func (d *dockStore) lookupLID(ctx context.Context, source types.JID, query, targetServer string, forward, reverse map[string]string) (types.JID, error) {
	d.lidMu.Lock()
	defer d.lidMu.Unlock()
	target, ok := forward[source.User]
	if !ok {
		err := d.q(ctx).GetContext(ctx, &target, query, d.dock, source.User)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return types.JID{}, pgerr.Map(err, "get whatsapp lid mapping")
		}
		forward[source.User] = target
		if target != "" {
			reverse[target] = source.User
		}
	}
	if target == "" {
		return types.JID{}, nil
	}
	return types.JID{User: target, Device: source.Device, Server: targetServer}, nil
}

func (d *dockStore) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if pn.Server != types.DefaultUserServer {
		return types.JID{}, errx.Internal("GetLIDForPN called with a non-phone JID " + pn.String())
	}
	return d.lookupLID(ctx, pn, `SELECT lid FROM wa_lid_map WHERE dock_id = $1 AND pn = $2`, types.HiddenUserServer, d.pnToLID, d.lidToPN)
}

func (d *dockStore) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if lid.Server != types.HiddenUserServer {
		return types.JID{}, errx.Internal("GetPNForLID called with a non-LID JID " + lid.String())
	}
	return d.lookupLID(ctx, lid, `SELECT pn FROM wa_lid_map WHERE dock_id = $1 AND lid = $2`, types.DefaultUserServer, d.lidToPN, d.pnToLID)
}

func (d *dockStore) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	if len(pns) == 0 {
		return nil, nil
	}
	out := make(map[types.JID]types.JID, len(pns))
	d.lidMu.Lock()
	defer d.lidMu.Unlock()
	var missing []string
	byUser := map[string][]types.JID{}
	for _, pn := range pns {
		if pn.Server != types.DefaultUserServer {
			continue
		}
		if lid := d.pnToLID[pn.User]; lid != "" {
			out[pn] = types.JID{User: lid, Device: pn.Device, Server: types.HiddenUserServer}
			continue
		}
		if _, seen := byUser[pn.User]; !seen {
			missing = append(missing, pn.User)
		}
		byUser[pn.User] = append(byUser[pn.User], pn)
	}
	if len(missing) == 0 {
		return out, nil
	}
	var rows []struct {
		LID string `db:"lid"`
		PN  string `db:"pn"`
	}
	if err := d.q(ctx).SelectContext(ctx, &rows, `SELECT lid, pn FROM wa_lid_map WHERE dock_id = $1 AND pn = ANY($2::text[])`,
		d.dock, missing); err != nil {
		return nil, pgerr.Map(err, "get whatsapp lid mappings")
	}
	for _, r := range rows {
		d.pnToLID[r.PN], d.lidToPN[r.LID] = r.LID, r.PN
		for _, pn := range byUser[r.PN] {
			out[pn] = types.JID{User: r.LID, Device: pn.Device, Server: types.HiddenUserServer}
		}
	}
	return out, nil
}

func (d *dockStore) PutLIDMapping(ctx context.Context, lid, pn types.JID) error {
	return d.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: lid, PN: pn}})
}

// PutManyLIDMappings stores mappings (the last one wins for a user); a
// phone number maps to one LID, so its older mapping goes.
func (d *dockStore) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	d.lidMu.Lock()
	defer d.lidMu.Unlock()
	var todo []store.LIDMapping
	seen := map[store.LIDMapping]bool{}
	for _, m := range mappings {
		if m.LID.Server != types.HiddenUserServer || m.PN.Server != types.DefaultUserServer {
			if len(mappings) == 1 {
				return errx.Internal("invalid lid mapping " + m.LID.String() + " / " + m.PN.String())
			}
			continue
		}
		if seen[m] || d.pnToLID[m.PN.User] == m.LID.User {
			continue
		}
		seen[m] = true
		todo = append(todo, m)
	}
	if len(todo) == 0 {
		return nil
	}
	err := d.st.inTx(ctx, func(ctx context.Context) error {
		for _, m := range todo {
			if _, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_lid_map WHERE dock_id = $1 AND pn = $3 AND lid <> $2`,
				d.dock, m.LID.User, m.PN.User); err != nil {
				return pgerr.Map(err, "replace whatsapp lid mapping")
			}
			if _, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_lid_map (dock_id, lid, pn) VALUES ($1, $2, $3)
				ON CONFLICT (dock_id, lid) DO UPDATE SET pn = excluded.pn`, d.dock, m.LID.User, m.PN.User); err != nil {
				return pgerr.Map(err, "put whatsapp lid mapping")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, m := range todo {
		lid, pn := m.LID.User, m.PN.User
		if oldLID := d.pnToLID[pn]; oldLID != "" && oldLID != lid && d.lidToPN[oldLID] == pn {
			delete(d.lidToPN, oldLID)
		}
		if oldPN := d.lidToPN[lid]; oldPN != "" && oldPN != pn && d.pnToLID[oldPN] == lid {
			delete(d.pnToLID, oldPN)
		}
		d.pnToLID[pn], d.lidToPN[lid] = lid, pn
	}
	return nil
}
