package dockslack

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// SealInfo derives the key of Slack App credentials (migration 00026).
const SealInfo = "seaport slack v1"

// App is a dock's Slack App, its secrets opened.
type App struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	AppID        string
	Name         string
	TeamID       string
	TeamName     string
	TeamURL      string
	BotUserID    string
	BotID        string
	ClientID     string
	CreatedAt    time.Time

	clientSecret  string // "": an app made by hand (no OAuth client known)
	signingSecret []byte
	botToken      string // "": not installed yet
}

// Installed: the app has a bot token (the dock can send and is linked).
func (a App) Installed() bool { return a.botToken != "" }

// NewApp is what a flow hands the store: an app made by the manifest flow
// (client credentials, no token yet) or pasted by hand (token, no client).
type NewApp struct {
	AppID, Name                                 string
	ClientID, ClientSecret, SigningSecret       string
	BotToken                                    string
	TeamID, TeamName, TeamURL, BotUserID, BotID string
}

// Install is what the OAuth exchange (or auth.test) tells about an
// installed app.
type Install struct {
	AppID                                       string
	BotToken                                    string
	TeamID, TeamName, TeamURL, BotUserID, BotID string
}

// Store keeps the Apps (secrets sealed, bound to their dock and field)
// and the threads Seaport answers in.
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID, field string) string {
	return "slack\x00" + dock.String() + "\x00" + field
}

type appRow struct {
	Dock          identity.DockID         `db:"dock_id"`
	Organization  identity.OrganizationID `db:"organization_id"`
	AppID         string                  `db:"app_id"`
	Name          string                  `db:"name"`
	TeamID        string                  `db:"team_id"`
	TeamName      string                  `db:"team_name"`
	TeamURL       string                  `db:"team_url"`
	BotUserID     string                  `db:"bot_user_id"`
	BotID         string                  `db:"bot_id"`
	ClientID      string                  `db:"client_id"`
	ClientSecret  []byte                  `db:"client_secret"`
	SigningSecret []byte                  `db:"signing_secret"`
	BotToken      []byte                  `db:"bot_token"`
	CreatedAt     time.Time               `db:"created_at"`
}

const appColumns = `dock_id, organization_id, app_id, name, team_id, team_name, team_url, bot_user_id, bot_id,
	client_id, client_secret, signing_secret, bot_token, created_at`

var appMessages = pgerr.Messages{NotFound: "this dock has no Slack App yet"}

func (s *Store) sealOpt(dock identity.DockID, field, value string) []byte {
	if value == "" {
		return nil
	}
	return s.seal.Seal(purpose(dock, field), []byte(value))
}

// SaveApp stores the App a flow made for the dock (replacing one it had,
// with its threads).
func (s *Store) SaveApp(ctx context.Context, org identity.OrganizationID, dock identity.DockID, n NewApp) error {
	if n.SigningSecret == "" {
		return errx.External("slack created the app without a signing secret")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO slack_apps (dock_id, organization_id, app_id, name, team_id, team_name, team_url,
		bot_user_id, bot_id, client_id, client_secret, signing_secret, bot_token)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (dock_id) DO UPDATE SET app_id = EXCLUDED.app_id, name = EXCLUDED.name, team_id = EXCLUDED.team_id,
		    team_name = EXCLUDED.team_name, team_url = EXCLUDED.team_url, bot_user_id = EXCLUDED.bot_user_id,
		    bot_id = EXCLUDED.bot_id, client_id = EXCLUDED.client_id, client_secret = EXCLUDED.client_secret,
		    signing_secret = EXCLUDED.signing_secret, bot_token = EXCLUDED.bot_token,
		    created_at = now(), updated_at = now()`,
		dock, org, n.AppID, n.Name, n.TeamID, n.TeamName, n.TeamURL, n.BotUserID, n.BotID, n.ClientID,
		s.sealOpt(dock, "client_secret", n.ClientSecret),
		s.seal.Seal(purpose(dock, "signing_secret"), []byte(n.SigningSecret)),
		s.sealOpt(dock, "bot_token", n.BotToken))
	if err != nil {
		return pgerr.Map(err, "store slack app")
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM slack_threads WHERE dock_id = $1 AND organization_id = $2`, dock, org)
	return pgerr.Map(err, "reset slack threads")
}

// SaveInstall records the install of the dock's App (its bot token).
func (s *Store) SaveInstall(ctx context.Context, dock identity.DockID, in Install) error {
	res, err := s.db.ExecContext(ctx, `UPDATE slack_apps SET app_id = CASE WHEN $2 <> '' THEN $2 ELSE app_id END,
		team_id = $3, team_name = $4, team_url = $5, bot_user_id = $6, bot_id = $7, bot_token = $8, updated_at = now()
		WHERE dock_id = $1`, dock, in.AppID, in.TeamID, in.TeamName, in.TeamURL, in.BotUserID, in.BotID,
		s.seal.Seal(purpose(dock, "bot_token"), []byte(in.BotToken)))
	if err != nil {
		return pgerr.Map(err, "store slack install")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound(appMessages.NotFound)
	}
	return nil
}

// ClearInstall forgets the bot token (the app was uninstalled in Slack).
func (s *Store) ClearInstall(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE slack_apps SET bot_token = NULL, updated_at = now() WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "clear slack install")
}

// App reads and opens the dock's App (system: events name the dock).
func (s *Store) App(ctx context.Context, dock identity.DockID) (App, error) {
	var r appRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+appColumns+` FROM slack_apps WHERE dock_id = $1`, dock); err != nil {
		return App{}, pgerr.MapWith(err, "find slack app", appMessages)
	}
	a := App{Dock: r.Dock, Organization: r.Organization, AppID: r.AppID, Name: r.Name, TeamID: r.TeamID, TeamName: r.TeamName,
		TeamURL: r.TeamURL, BotUserID: r.BotUserID, BotID: r.BotID, ClientID: r.ClientID, CreatedAt: r.CreatedAt}
	cannot := errx.Internal("slack app credentials can't be opened (SEAPORT_SECRET_KEY changed?)")
	signing, err := s.seal.Open(purpose(dock, "signing_secret"), r.SigningSecret)
	if err != nil {
		return App{}, cannot
	}
	a.signingSecret = signing
	if len(r.ClientSecret) > 0 {
		v, err := s.seal.Open(purpose(dock, "client_secret"), r.ClientSecret)
		if err != nil {
			return App{}, cannot
		}
		a.clientSecret = string(v)
	}
	if len(r.BotToken) > 0 {
		v, err := s.seal.Open(purpose(dock, "bot_token"), r.BotToken)
		if err != nil {
			return App{}, cannot
		}
		a.botToken = string(v)
	}
	return a, nil
}

// HasToken says whether the dock has an installed App, without opening it.
func (s *Store) HasToken(ctx context.Context, dock identity.DockID) (bool, error) {
	var ok bool
	err := s.db.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM slack_apps WHERE dock_id = $1 AND bot_token IS NOT NULL)`, dock)
	return ok, pgerr.Map(err, "find slack app")
}

// DeleteApp forgets the dock's App and threads.
func (s *Store) DeleteApp(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM slack_apps WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "delete slack app")
}

// RememberThread records a thread Seaport answers in.
func (s *Store) RememberThread(ctx context.Context, org identity.OrganizationID, dock identity.DockID, channel, ts string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO slack_threads (dock_id, organization_id, channel, thread_ts)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, dock, org, channel, ts)
	return pgerr.Map(err, "remember slack thread")
}

// KnownThread: Seaport answers in this thread.
func (s *Store) KnownThread(ctx context.Context, dock identity.DockID, channel, ts string) (bool, error) {
	var ok bool
	err := s.db.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM slack_threads WHERE dock_id = $1 AND channel = $2 AND thread_ts = $3)`,
		dock, channel, ts)
	return ok, pgerr.Map(err, "find slack thread")
}

// CheckKey opens one App, if any: a changed SEAPORT_SECRET_KEY stops the
// start instead of failing every Slack dock later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM slack_apps LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check slack app key")
	}
	if _, err := s.App(ctx, dock); err != nil {
		return errx.Validation("the Slack App credentials were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}
