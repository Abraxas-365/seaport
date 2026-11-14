package dockwebhook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// SealInfo derives the key of webhook secrets (migration 00030).
const SealInfo = "seaport webhook v1"

// Hook is a dock's webhook endpoint: its template and, opened, its secret.
type Hook struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	Template     Template
	CreatedAt    time.Time

	secret string
}

// Store keeps the hooks (secret sealed, bound to its dock and field).
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID) string { return "webhook\x00" + dock.String() + "\x00secret" }

type hookRow struct {
	Dock         identity.DockID         `db:"dock_id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Secret       []byte                  `db:"secret"`
	Template     []byte                  `db:"template"`
	CreatedAt    time.Time               `db:"created_at"`
}

const hookColumns = `dock_id, organization_id, secret, template, created_at`

var hookMessages = pgerr.Messages{NotFound: "this dock has no webhook (link it first)"}

// newSecret is a webhook secret: 256 random bits.
func newSecret() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Create makes the dock's endpoint with a fresh secret, returned (shown
// once). A dock that has one keeps it (secret "": nothing new to show).
func (s *Store) Create(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (string, error) {
	secret := newSecret()
	var inserted bool
	err := s.db.GetContext(ctx, &inserted, `INSERT INTO webhook_docks (dock_id, organization_id, secret) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id) DO UPDATE SET updated_at = webhook_docks.updated_at
		WHERE webhook_docks.organization_id = EXCLUDED.organization_id
		RETURNING (xmax = 0)`,
		dock, org, s.seal.Seal(purpose(dock), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "create webhook")
	}
	if !inserted {
		return "", nil
	}
	return secret, nil
}

// RotateSecret replaces the dock's secret and returns it.
func (s *Store) RotateSecret(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (string, error) {
	secret := newSecret()
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_docks SET secret = $3, updated_at = now() WHERE dock_id = $1 AND organization_id = $2`,
		dock, org, s.seal.Seal(purpose(dock), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "rotate webhook secret")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errx.NotFound(hookMessages.NotFound)
	}
	return secret, nil
}

// SaveTemplate stores the dock's template.
func (s *Store) SaveTemplate(ctx context.Context, org identity.OrganizationID, dock identity.DockID, t Template) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return errx.Wrap(err, "encode template", errx.TypeInternal)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_docks SET template = $3, updated_at = now() WHERE dock_id = $1 AND organization_id = $2`, dock, org, raw)
	if err != nil {
		return pgerr.Map(err, "save webhook template")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound(hookMessages.NotFound)
	}
	return nil
}

// Hook reads and opens the dock's endpoint (system: the public route
// names only the dock).
func (s *Store) Hook(ctx context.Context, dock identity.DockID) (Hook, error) {
	var r hookRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+hookColumns+` FROM webhook_docks WHERE dock_id = $1`, dock); err != nil {
		return Hook{}, pgerr.MapWith(err, "find webhook", hookMessages)
	}
	secret, err := s.seal.Open(purpose(dock), r.Secret)
	if err != nil {
		return Hook{}, errx.Internal("webhook secret can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	var t Template
	if len(r.Template) > 0 {
		if err := json.Unmarshal(r.Template, &t); err != nil {
			return Hook{}, errx.Internal("stored webhook template is unreadable")
		}
	}
	return Hook{Dock: r.Dock, Organization: r.Organization, Template: t, CreatedAt: r.CreatedAt, secret: string(secret)}, nil
}

// Delete forgets the dock's endpoint.
func (s *Store) Delete(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhook_docks WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "delete webhook")
}

// CheckKey opens one secret, if any: a changed SEAPORT_SECRET_KEY stops
// the start instead of failing every webhook later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM webhook_docks LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check webhook key")
	}
	if _, err := s.Hook(ctx, dock); err != nil {
		return errx.Validation("the webhook secrets were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}
