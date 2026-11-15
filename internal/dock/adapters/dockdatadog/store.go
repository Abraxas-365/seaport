package dockdatadog

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// SealInfo derives the key of Datadog webhook secrets (migration 00035).
const SealInfo = "seaport datadog v1"

// Mirror names the destination (phase 2) where each new alert thread is
// opened: a dock that replies and one of its destinations.
type Mirror struct {
	Dock        identity.DockID `json:"dock_id"`
	Destination string          `json:"destination"`
}

func (m Mirror) IsZero() bool { return m.Dock.IsZero() && m.Destination == "" }

// Endpoint is a dock's Datadog webhook: its mirror and, opened, its secret.
type Endpoint struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	Mirror       *Mirror
	CreatedAt    time.Time

	secret string
}

// Store keeps the endpoints (secret sealed, bound to its dock and field).
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID) string { return "datadog\x00" + dock.String() + "\x00secret" }

type row struct {
	Dock         identity.DockID         `db:"dock_id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Secret       []byte                  `db:"secret"`
	MirrorDock   *identity.DockID        `db:"mirror_dock"`
	MirrorDest   *string                 `db:"mirror_destination"`
	CreatedAt    time.Time               `db:"created_at"`
}

const columns = `dock_id, organization_id, secret, mirror_dock, mirror_destination, created_at`

var messages = pgerr.Messages{NotFound: "this dock has no Datadog webhook (link it first)"}

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
	err := s.db.GetContext(ctx, &inserted, `INSERT INTO datadog_docks (dock_id, organization_id, secret) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id) DO UPDATE SET updated_at = datadog_docks.updated_at
		WHERE datadog_docks.organization_id = EXCLUDED.organization_id
		RETURNING (xmax = 0)`,
		dock, org, s.seal.Seal(purpose(dock), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "create datadog webhook")
	}
	if !inserted {
		return "", nil
	}
	return secret, nil
}

// RotateSecret replaces the dock's secret and returns it.
func (s *Store) RotateSecret(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (string, error) {
	secret := newSecret()
	res, err := s.db.ExecContext(ctx, `UPDATE datadog_docks SET secret = $3, updated_at = now() WHERE dock_id = $1 AND organization_id = $2`,
		dock, org, s.seal.Seal(purpose(dock), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "rotate datadog secret")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errx.NotFound(messages.NotFound)
	}
	return secret, nil
}

// SaveMirror stores the dock's mirror (nil: none). The destination must
// exist (foreign key): a missing one is NotFound.
func (s *Store) SaveMirror(ctx context.Context, org identity.OrganizationID, dock identity.DockID, m *Mirror) error {
	var mdock *identity.DockID
	var mdest *string
	if m != nil {
		mdock, mdest = &m.Dock, &m.Destination
	}
	res, err := s.db.ExecContext(ctx, `UPDATE datadog_docks SET mirror_dock = $3, mirror_destination = $4, updated_at = now()
		WHERE dock_id = $1 AND organization_id = $2`, dock, org, mdock, mdest)
	if err != nil {
		return pgerr.MapWith(err, "save datadog mirror", pgerr.Messages{ForeignKey: "that destination does not exist"})
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound(messages.NotFound)
	}
	return nil
}

// Endpoint reads and opens the dock's webhook (system: the public route
// names only the dock).
func (s *Store) Endpoint(ctx context.Context, dock identity.DockID) (Endpoint, error) {
	var r row
	if err := s.db.GetContext(ctx, &r, `SELECT `+columns+` FROM datadog_docks WHERE dock_id = $1`, dock); err != nil {
		return Endpoint{}, pgerr.MapWith(err, "find datadog webhook", messages)
	}
	secret, err := s.seal.Open(purpose(dock), r.Secret)
	if err != nil {
		return Endpoint{}, errx.Internal("datadog secret can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	out := Endpoint{Dock: r.Dock, Organization: r.Organization, CreatedAt: r.CreatedAt, secret: string(secret)}
	if r.MirrorDock != nil && r.MirrorDest != nil {
		out.Mirror = &Mirror{Dock: *r.MirrorDock, Destination: *r.MirrorDest}
	}
	return out, nil
}

// Delete forgets the dock's endpoint.
func (s *Store) Delete(ctx context.Context, org identity.OrganizationID, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM datadog_docks WHERE dock_id = $1 AND organization_id = $2`, dock, org)
	return pgerr.Map(err, "delete datadog webhook")
}

// CheckKey opens one secret, if any: a changed SEAPORT_SECRET_KEY stops
// the start instead of failing every webhook later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM datadog_docks LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check datadog key")
	}
	if _, err := s.Endpoint(ctx, dock); err != nil {
		return errx.Validation("the Datadog secrets were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}
