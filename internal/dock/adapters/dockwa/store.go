package dockwa

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"

	mathrand "math/rand/v2"
)

// SealInfo names the WhatsApp store key (HKDF info of SEAPORT_SECRET_KEY).
const SealInfo = "seaport whatsapp v1"

// Store is Seaport's implementation of whatsmeow's store (instead of its
// sqlstore): one device per dock in the wa_* tables (migration 00008), every
// secret sealed and bound to its dock and row, so a database dump or backup
// holds no usable WhatsApp key and a value can't be moved to another row.
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

// errWrongKey: a sealed value doesn't open (SEAPORT_SECRET_KEY changed, or
// the row was tampered with).
var errWrongKey = errx.Internal("whatsapp store value does not open with this SEAPORT_SECRET_KEY")

// queryer is a *sqlx.DB or the *sqlx.Tx of the current store transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

type txKey struct{}

func (s *Store) q(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return tx
	}
	return s.db
}

// inTx runs fn in a transaction carried by ctx (nested calls join it), as
// whatsmeow expects for DoDecryptionTxn and its batch writes.
func (s *Store) inTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return fn(ctx)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin whatsapp store transaction")
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return pgerr.Map(tx.Commit(), "commit whatsapp store transaction")
}

// purpose binds a sealed value to its table, dock and row key.
func purpose(table string, dock identity.DockID, key ...string) string {
	return "whatsapp\x00" + table + "\x00" + dock.String() + "\x00" + strings.Join(key, "\x00")
}

func (s *Store) open(p string, sealed []byte) ([]byte, error) {
	plain, err := s.seal.Open(p, sealed)
	if err != nil {
		return nil, errWrongKey
	}
	return plain, nil
}

// dockStore is one dock's device: whatsmeow's DeviceContainer and every
// session-specific store, plus the dock's phone↔LID map.
type dockStore struct {
	st   *Store
	org  identity.OrganizationID
	dock identity.DockID

	mu sync.Mutex
	// jid of the stored device; "" until the first PutDevice (pairing).
	jid string

	preKeys sync.Mutex

	contactsMu sync.Mutex
	contacts   map[types.JID]*types.ContactInfo

	migratedMu sync.Mutex
	migrated   map[string]bool

	lidMu   sync.Mutex
	pnToLID map[string]string
	lidToPN map[string]string
}

var (
	_ store.DeviceContainer          = (*dockStore)(nil)
	_ store.AllSessionSpecificStores = (*dockStore)(nil)
	_ store.LIDStore                 = (*dockStore)(nil)
)

func (s *Store) dockStore(org identity.OrganizationID, dock identity.DockID) *dockStore {
	return &dockStore{st: s, org: org, dock: dock, contacts: map[types.JID]*types.ContactInfo{},
		migrated: map[string]bool{}, pnToLID: map[string]string{}, lidToPN: map[string]string{}}
}

func (d *dockStore) q(ctx context.Context) queryer { return d.st.q(ctx) }

// NewDevice is a fresh, unpaired device for a dock; pairing stores it,
// replacing any previous device of the dock.
func (s *Store) NewDevice(org identity.OrganizationID, dock identity.DockID) *store.Device {
	dev := &store.Device{
		Log:            newLogger("store"),
		Container:      s.dockStore(org, dock),
		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: mathrand.Uint32(),
		AdvSecretKey:   random.Bytes(32),
	}
	dev.SignedPreKey = dev.IdentityKey.CreateSignedPreKey(1)
	return dev
}

// deviceSecrets is the sealed part of a device row.
type deviceSecrets struct {
	Noise            []byte `json:"noise"`
	Identity         []byte `json:"identity"`
	SignedPreKey     []byte `json:"signed_pre_key"`
	SignedPreKeySig  []byte `json:"signed_pre_key_sig"`
	AdvKey           []byte `json:"adv_key"`
	AdvDetails       []byte `json:"adv_details"`
	AdvAccountSig    []byte `json:"adv_account_sig"`
	AdvAccountSigKey []byte `json:"adv_account_sig_key"`
	AdvDeviceSig     []byte `json:"adv_device_sig"`
}

type deviceRow struct {
	JID                string        `db:"jid"`
	LID                string        `db:"lid"`
	RegistrationID     int64         `db:"registration_id"`
	SignedPreKeyID     int64         `db:"signed_pre_key_id"`
	Platform           string        `db:"platform"`
	BusinessName       string        `db:"business_name"`
	PushName           string        `db:"push_name"`
	FacebookUUID       uuid.NullUUID `db:"facebook_uuid"`
	LIDMigrationTS     int64         `db:"lid_migration_ts"`
	CompanionMetaNonce string        `db:"companion_meta_nonce"`
	Secrets            []byte        `db:"secrets"`
}

func jidText(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.String()
}

func (s *Store) sealDevice(dock identity.DockID, dev *store.Device) ([]byte, error) {
	sec := deviceSecrets{
		Noise: dev.NoiseKey.Priv[:], Identity: dev.IdentityKey.Priv[:],
		SignedPreKey: dev.SignedPreKey.Priv[:], SignedPreKeySig: dev.SignedPreKey.Signature[:],
		AdvKey: dev.AdvSecretKey,
	}
	if dev.Account != nil {
		sec.AdvDetails, sec.AdvAccountSig = dev.Account.Details, dev.Account.AccountSignature
		sec.AdvAccountSigKey, sec.AdvDeviceSig = dev.Account.AccountSignatureKey, dev.Account.DeviceSignature
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		return nil, errx.Wrap(err, "encode whatsapp device", errx.TypeInternal)
	}
	return s.seal.Seal(purpose("device", dock, dev.ID.String()), raw), nil
}

// PutDevice stores the device (whatsmeow calls it through Device.Save).
// The first save of a new device (pairing) replaces whatever device the
// dock had; later saves only update that same device, so a stale client
// can't overwrite a newer pairing.
func (d *dockStore) PutDevice(ctx context.Context, dev *store.Device) error {
	if dev.ID == nil {
		return errx.Internal("whatsapp device has no JID yet")
	}
	jid := dev.ID.String()
	d.mu.Lock()
	defer d.mu.Unlock()
	switch d.jid {
	case "":
		secrets, err := d.st.sealDevice(d.dock, dev)
		if err != nil {
			return err
		}
		err = d.st.inTx(ctx, func(ctx context.Context) error {
			if _, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_devices WHERE dock_id = $1`, d.dock); err != nil {
				return pgerr.Map(err, "replace whatsapp device")
			}
			_, err := d.q(ctx).ExecContext(ctx, `INSERT INTO wa_devices (dock_id, organization_id, jid, lid,
				registration_id, signed_pre_key_id, platform, business_name, push_name, facebook_uuid,
				lid_migration_ts, companion_meta_nonce, secrets)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				d.dock, d.org, jid, jidText(dev.LID), int64(dev.RegistrationID), int64(dev.SignedPreKey.KeyID),
				dev.Platform, dev.BusinessName, dev.PushName,
				uuid.NullUUID{UUID: dev.FacebookUUID, Valid: dev.FacebookUUID != uuid.Nil},
				dev.LIDMigrationTimestamp, dev.CompanionMetaNonce, secrets)
			return pgerr.Map(err, "insert whatsapp device")
		})
		if err != nil {
			return err
		}
		d.jid = jid
	case jid:
		res, err := d.q(ctx).ExecContext(ctx, `UPDATE wa_devices SET lid = $3, platform = $4, business_name = $5,
			push_name = $6, lid_migration_ts = $7, companion_meta_nonce = $8, updated_at = now()
			WHERE dock_id = $1 AND jid = $2`,
			d.dock, jid, jidText(dev.LID), dev.Platform, dev.BusinessName, dev.PushName,
			dev.LIDMigrationTimestamp, dev.CompanionMetaNonce)
		if err != nil {
			return pgerr.Map(err, "update whatsapp device")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errx.NotFound("whatsapp device of dock " + d.dock.String() + " was replaced or deleted")
		}
	default:
		return errx.Internal("whatsapp device JID changed")
	}
	if !dev.Initialized {
		d.attach(dev)
	}
	return nil
}

// DeleteDevice removes the device and, by cascade, all its keys (logout).
func (d *dockStore) DeleteDevice(ctx context.Context, dev *store.Device) error {
	if dev.ID == nil {
		return errx.Internal("whatsapp device has no JID yet")
	}
	_, err := d.q(ctx).ExecContext(ctx, `DELETE FROM wa_devices WHERE dock_id = $1 AND jid = $2`, d.dock, dev.ID.String())
	return pgerr.Map(err, "delete whatsapp device")
}

func (d *dockStore) attach(dev *store.Device) {
	dev.SetAllStores(d)
	dev.LIDs = d
	dev.Container = d
	dev.Initialized = true
}

// Device loads a dock's device, or nil when it has none (never linked,
// logged out, unlinked). errWrongKey: it was sealed with another key.
func (s *Store) Device(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (*store.Device, error) {
	var row deviceRow
	err := s.q(ctx).GetContext(ctx, &row, `SELECT jid, lid, registration_id, signed_pre_key_id, platform,
		business_name, push_name, facebook_uuid, lid_migration_ts, companion_meta_nonce, secrets
		FROM wa_devices WHERE organization_id = $1 AND dock_id = $2`, org, dock)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "load whatsapp device")
	}
	jid, err := types.ParseJID(row.JID)
	if err != nil {
		return nil, errx.Wrap(err, "parse whatsapp device jid", errx.TypeInternal)
	}
	raw, err := s.open(purpose("device", dock, row.JID), row.Secrets)
	if err != nil {
		return nil, err
	}
	var sec deviceSecrets
	if err := json.Unmarshal(raw, &sec); err != nil {
		return nil, errx.Wrap(err, "decode whatsapp device", errx.TypeInternal)
	}
	if len(sec.Noise) != 32 || len(sec.Identity) != 32 || len(sec.SignedPreKey) != 32 || len(sec.SignedPreKeySig) != 64 {
		return nil, errx.Internal("whatsapp device keys have an invalid length")
	}
	dev := &store.Device{
		Log:                   newLogger("store"),
		ID:                    &jid,
		NoiseKey:              keys.NewKeyPairFromPrivateKey([32]byte(sec.Noise)),
		IdentityKey:           keys.NewKeyPairFromPrivateKey([32]byte(sec.Identity)),
		SignedPreKey:          &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey([32]byte(sec.SignedPreKey)), KeyID: uint32(row.SignedPreKeyID), Signature: (*[64]byte)(sec.SignedPreKeySig)},
		RegistrationID:        uint32(row.RegistrationID),
		AdvSecretKey:          sec.AdvKey,
		Account:               &waAdv.ADVSignedDeviceIdentity{Details: sec.AdvDetails, AccountSignature: sec.AdvAccountSig, AccountSignatureKey: sec.AdvAccountSigKey, DeviceSignature: sec.AdvDeviceSig},
		Platform:              row.Platform,
		BusinessName:          row.BusinessName,
		PushName:              row.PushName,
		FacebookUUID:          row.FacebookUUID.UUID,
		LIDMigrationTimestamp: row.LIDMigrationTS,
		CompanionMetaNonce:    row.CompanionMetaNonce,
	}
	if row.LID != "" {
		if dev.LID, err = types.ParseJID(row.LID); err != nil {
			return nil, errx.Wrap(err, "parse whatsapp device lid", errx.TypeInternal)
		}
	}
	ds := s.dockStore(org, dock)
	ds.jid = row.JID
	ds.attach(dev)
	return dev, nil
}

// DeleteDock forgets a dock's device and keys.
func (s *Store) DeleteDock(ctx context.Context, org identity.OrganizationID, dock identity.DockID) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM wa_devices WHERE organization_id = $1 AND dock_id = $2`, org, dock)
	return pgerr.Map(err, "delete whatsapp device")
}

// CheckKey opens one sealed device: a changed SEAPORT_SECRET_KEY is found
// at start, not as every dock failing to restore.
func (s *Store) CheckKey(ctx context.Context) error {
	var row struct {
		Dock    identity.DockID `db:"dock_id"`
		JID     string          `db:"jid"`
		Secrets []byte          `db:"secrets"`
	}
	err := s.db.GetContext(ctx, &row, `SELECT dock_id, jid, secrets FROM wa_devices LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check whatsapp store key")
	}
	if _, err := s.open(purpose("device", row.Dock, row.JID), row.Secrets); err != nil {
		return errx.Validation("the WhatsApp store was sealed with another SEAPORT_SECRET_KEY: restore that key " +
			"(if it is lost, delete the rows of wa_devices and link the WhatsApp docks again)")
	}
	return nil
}

func hexKey(b []byte) string { return hex.EncodeToString(b) }
