package dockjira

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

// SealInfo derives the key of Jira credentials (migration 00025).
const SealInfo = "seaport jira v1"

// Site is a dock's Jira site and service account, its secrets opened.
type Site struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	// URL is https://<name>.atlassian.net, without a trailing slash.
	URL         string
	AccountID   string
	DisplayName string
	Email       string
	CreatedAt   time.Time

	token         string
	webhookSecret string
}

// Store keeps the sites (token and webhook secret sealed, bound to their
// dock and field).
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID, field string) string {
	return "jira\x00" + dock.String() + "\x00" + field
}

type siteRow struct {
	Dock          identity.DockID         `db:"dock_id"`
	Organization  identity.OrganizationID `db:"organization_id"`
	Site          string                  `db:"site"`
	AccountID     string                  `db:"account_id"`
	DisplayName   string                  `db:"display_name"`
	Email         string                  `db:"email"`
	APIToken      []byte                  `db:"api_token"`
	WebhookSecret []byte                  `db:"webhook_secret"`
	CreatedAt     time.Time               `db:"created_at"`
}

const siteColumns = `dock_id, organization_id, site, account_id, display_name, email, api_token, webhook_secret, created_at`

var siteMessages = pgerr.Messages{NotFound: "this dock has no Jira site yet"}

// newSecret is a webhook secret: 256 random bits.
func newSecret() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Save stores the site a link checked (replacing one the dock had). A
// first link makes the webhook secret and returns it (shown once); linking
// again keeps the stored one, so a new token doesn't break the webhook
// configured in Jira ("" is returned then).
func (s *Store) Save(ctx context.Context, org identity.OrganizationID, dock identity.DockID, site, accountID, displayName, email, token string) (string, error) {
	secret := newSecret()
	var inserted bool
	err := s.db.GetContext(ctx, &inserted, `INSERT INTO jira_sites (dock_id, organization_id, site, account_id, display_name, email, api_token, webhook_secret)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (dock_id) DO UPDATE SET site = EXCLUDED.site, account_id = EXCLUDED.account_id,
		    display_name = EXCLUDED.display_name, email = EXCLUDED.email, api_token = EXCLUDED.api_token, updated_at = now()
		WHERE jira_sites.organization_id = EXCLUDED.organization_id
		RETURNING (xmax = 0)`,
		dock, org, site, accountID, displayName, email,
		s.seal.Seal(purpose(dock, "api_token"), []byte(token)),
		s.seal.Seal(purpose(dock, "webhook_secret"), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "store jira site")
	}
	if !inserted {
		return "", nil
	}
	return secret, nil
}

// RotateSecret replaces the dock's webhook secret and returns it.
func (s *Store) RotateSecret(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (string, error) {
	secret := newSecret()
	res, err := s.db.ExecContext(ctx, `UPDATE jira_sites SET webhook_secret = $3, updated_at = now() WHERE dock_id = $1 AND organization_id = $2`,
		dock, org, s.seal.Seal(purpose(dock, "webhook_secret"), []byte(secret)))
	if err != nil {
		return "", pgerr.Map(err, "rotate jira webhook secret")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errx.NotFound(siteMessages.NotFound)
	}
	return secret, nil
}

// Site reads and opens the dock's site (system: webhooks name the dock).
func (s *Store) Site(ctx context.Context, dock identity.DockID) (Site, error) {
	var r siteRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+siteColumns+` FROM jira_sites WHERE dock_id = $1`, dock); err != nil {
		return Site{}, pgerr.MapWith(err, "find jira site", siteMessages)
	}
	token, err := s.seal.Open(purpose(dock, "api_token"), r.APIToken)
	if err != nil {
		return Site{}, errx.Internal("jira credentials can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	secret, err := s.seal.Open(purpose(dock, "webhook_secret"), r.WebhookSecret)
	if err != nil {
		return Site{}, errx.Internal("jira credentials can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	return Site{Dock: r.Dock, Organization: r.Organization, URL: r.Site, AccountID: r.AccountID, DisplayName: r.DisplayName,
		Email: r.Email, CreatedAt: r.CreatedAt, token: string(token), webhookSecret: string(secret)}, nil
}

// Delete forgets the dock's site.
func (s *Store) Delete(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jira_sites WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "delete jira site")
}

// CheckKey opens one site, if any: a changed SEAPORT_SECRET_KEY stops the
// start instead of failing every Jira dock later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM jira_sites LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check jira site key")
	}
	if _, err := s.Site(ctx, dock); err != nil {
		return errx.Validation("the Jira credentials were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}
