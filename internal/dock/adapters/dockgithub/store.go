package dockgithub

import (
	"context"
	"crypto/rsa"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// SealInfo derives the key of GitHub App credentials (migration 00022).
const SealInfo = "seaport github v1"

// App is a dock's GitHub App, its secrets opened.
type App struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	AppID        int64
	Slug         string
	Name         string
	Owner        string
	OwnerType    string
	HTMLURL      string
	ClientID     string
	CreatedAt    time.Time

	key           *rsa.PrivateKey
	webhookSecret []byte
}

// Installation is an account (user or organization) the App is installed on.
type Installation struct {
	ID          int64     `db:"installation_id" json:"id"`
	Account     string    `db:"account" json:"account"`
	AccountType string    `db:"account_type" json:"account_type"`
	Suspended   bool      `db:"suspended" json:"suspended"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// Store keeps the Apps (secrets sealed, bound to their dock and field)
// and their installations.
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID, field string) string {
	return "github-app\x00" + dock.String() + "\x00" + field
}

type appRow struct {
	Dock          identity.DockID         `db:"dock_id"`
	Organization  identity.OrganizationID `db:"organization_id"`
	AppID         int64                   `db:"app_id"`
	Slug          string                  `db:"slug"`
	Name          string                  `db:"name"`
	Owner         string                  `db:"owner"`
	OwnerType     string                  `db:"owner_type"`
	HTMLURL       string                  `db:"html_url"`
	ClientID      string                  `db:"client_id"`
	PrivateKey    []byte                  `db:"private_key"`
	WebhookSecret []byte                  `db:"webhook_secret"`
	CreatedAt     time.Time               `db:"created_at"`
}

const appColumns = `dock_id, organization_id, app_id, slug, name, owner, owner_type, html_url, client_id, private_key, webhook_secret, created_at`

var appMessages = pgerr.Messages{NotFound: "this dock has no GitHub App yet"}

// SaveApp stores the App a manifest flow created for the dock (replacing
// one it had).
func (s *Store) SaveApp(ctx context.Context, org identity.OrganizationID, dock identity.DockID, c conversion) error {
	if _, err := parseKey([]byte(c.PEM)); err != nil {
		return err
	}
	if c.WebhookSecret == "" {
		return errx.External("github created the app without a webhook secret")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_apps (dock_id, organization_id, app_id, slug, name, owner, owner_type,
		html_url, client_id, private_key, webhook_secret, client_secret)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (dock_id) DO UPDATE SET app_id = EXCLUDED.app_id, slug = EXCLUDED.slug, name = EXCLUDED.name,
		    owner = EXCLUDED.owner, owner_type = EXCLUDED.owner_type, html_url = EXCLUDED.html_url,
		    client_id = EXCLUDED.client_id, private_key = EXCLUDED.private_key,
		    webhook_secret = EXCLUDED.webhook_secret, client_secret = EXCLUDED.client_secret, created_at = now()`,
		dock, org, c.ID, c.Slug, c.Name, c.Owner.Login, c.Owner.Type, c.HTMLURL, c.ClientID,
		s.seal.Seal(purpose(dock, "private_key"), []byte(c.PEM)),
		s.seal.Seal(purpose(dock, "webhook_secret"), []byte(c.WebhookSecret)),
		s.seal.Seal(purpose(dock, "client_secret"), []byte(c.ClientSecret)))
	if err != nil {
		return pgerr.Map(err, "store github app")
	}
	// A new App starts with no installations (the old App's are gone).
	_, err = s.db.ExecContext(ctx, `DELETE FROM github_installations WHERE dock_id = $1 AND organization_id = $2`, dock, org)
	return pgerr.Map(err, "reset github installations")
}

// App reads and opens the dock's App (system: webhooks name the dock).
func (s *Store) App(ctx context.Context, dock identity.DockID) (App, error) {
	var r appRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+appColumns+` FROM github_apps WHERE dock_id = $1`, dock); err != nil {
		return App{}, pgerr.MapWith(err, "find github app", appMessages)
	}
	pemText, err := s.seal.Open(purpose(dock, "private_key"), r.PrivateKey)
	if err != nil {
		return App{}, errx.Internal("github app credentials can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	secret, err := s.seal.Open(purpose(dock, "webhook_secret"), r.WebhookSecret)
	if err != nil {
		return App{}, errx.Internal("github app credentials can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	key, err := parseKey(pemText)
	if err != nil {
		return App{}, err
	}
	return App{Dock: r.Dock, Organization: r.Organization, AppID: r.AppID, Slug: r.Slug, Name: r.Name, Owner: r.Owner,
		OwnerType: r.OwnerType, HTMLURL: r.HTMLURL, ClientID: r.ClientID, CreatedAt: r.CreatedAt, key: key, webhookSecret: secret}, nil
}

// HasApp says whether the dock has an App, without opening it.
func (s *Store) HasApp(ctx context.Context, dock identity.DockID) (bool, error) {
	var ok bool
	err := s.db.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM github_apps WHERE dock_id = $1)`, dock)
	return ok, pgerr.Map(err, "find github app")
}

// DeleteApp forgets the dock's App and installations.
func (s *Store) DeleteApp(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_apps WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "delete github app")
}

// SaveInstallation records (or refreshes) an installation of the dock's App.
func (s *Store) SaveInstallation(ctx context.Context, org identity.OrganizationID, dock identity.DockID, in Installation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_installations (dock_id, organization_id, installation_id, account, account_type, suspended)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (dock_id, installation_id) DO UPDATE SET account = EXCLUDED.account,
		    account_type = EXCLUDED.account_type, suspended = EXCLUDED.suspended, updated_at = now()`,
		dock, org, in.ID, in.Account, in.AccountType, in.Suspended)
	return pgerr.Map(err, "store github installation")
}

// DeleteInstallation forgets an installation (uninstalled on GitHub).
func (s *Store) DeleteInstallation(ctx context.Context, dock identity.DockID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_installations WHERE dock_id = $1 AND installation_id = $2`, dock, id)
	return pgerr.Map(err, "delete github installation")
}

// Installations lists the dock's installations, oldest first.
func (s *Store) Installations(ctx context.Context, dock identity.DockID) ([]Installation, error) {
	out := []Installation{}
	err := s.db.SelectContext(ctx, &out, `SELECT installation_id, account, account_type, suspended, created_at
		FROM github_installations WHERE dock_id = $1 ORDER BY created_at, installation_id`, dock)
	return out, pgerr.Map(err, "list github installations")
}

// CheckKey opens one App, if any: a changed SEAPORT_SECRET_KEY stops the
// start instead of failing every GitHub dock later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM github_apps LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check github app key")
	}
	if _, err := s.App(ctx, dock); err != nil {
		return errx.Validation("the GitHub App credentials were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}
