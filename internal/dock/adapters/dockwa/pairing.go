package dockwa

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// The pairing code of a linking dock lives in wa_pairings (migration
// 00011), sealed and bound to its dock, so any process can show it while
// the dock's owner pairs.

// SetPairing stores the dock's current pairing code ("" deletes it).
func (s *Store) SetPairing(ctx context.Context, org identity.OrganizationID, dock identity.DockID, code string) error {
	if code == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM wa_pairings WHERE organization_id = $1 AND dock_id = $2`, org, dock)
		return pgerr.Map(err, "delete pairing code")
	}
	sealed := s.seal.Seal(purpose("pairing", dock), []byte(code))
	_, err := s.db.ExecContext(ctx, `INSERT INTO wa_pairings (dock_id, organization_id, code) VALUES ($2, $1, $3)
		ON CONFLICT (dock_id) DO UPDATE SET code = EXCLUDED.code, updated_at = now()`, org, dock, sealed)
	return pgerr.Map(err, "store pairing code")
}

// Pairing reads the dock's current pairing code ("" when none).
func (s *Store) Pairing(ctx context.Context, dock identity.DockID) (string, error) {
	var sealed []byte
	err := s.db.GetContext(ctx, &sealed, `SELECT code FROM wa_pairings WHERE dock_id = $1`, dock)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", pgerr.Map(err, "read pairing code")
	}
	plain, err := s.open(purpose("pairing", dock), sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
