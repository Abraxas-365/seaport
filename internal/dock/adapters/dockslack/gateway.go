// Package dockslack is the gateway of Slack docks: a dock is a Slack App
// (made from a manifest, or by hand from the same manifest; credentials
// sealed). Mentions of the app or of an agent of its team, DMs and
// follow-ups in threads Seaport answers in arrive by signed Events API
// requests; replies are chat.postMessage calls with the bot token.
// Stateless apart from its tables: any process serves any Slack dock.
package dockslack

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Receiver takes inbound messages (the ship domain, bound late).
type Receiver interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}

// Teams tells which agents work on a dock (the agent domain, bound late):
// a message mentioning one of them is for the crew.
type Teams interface {
	Team(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]agent.Member, error)
}

// LinkSink records link state (the dock domain).
type LinkSink interface {
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
	SetStatus(ctx context.Context, dock identity.DockID, status dock.Status) error
}

// Config says where Slack and this port are.
type Config struct {
	// PublicURL is this port as Slack reaches it (events, OAuth redirect).
	PublicURL string
	// WebURL (https://slack.com: the OAuth authorize host) and APIURL
	// (https://slack.com/api): a fake in tests.
	WebURL, APIURL string
}

type Gateway struct {
	store  *Store
	client *client
	seal   seal.Sealer
	cfg    Config

	mu       sync.Mutex
	receiver Receiver
	teams    Teams
	links    LinkSink
}

// Open opens the sealed App store (secretKey: SEAPORT_SECRET_KEY).
func Open(ctx context.Context, db *sqlx.DB, secretKey string, cfg Config) (*Gateway, error) {
	sealer, err := seal.New(secretKey, SealInfo)
	if err != nil {
		return nil, err
	}
	store := NewStore(db, sealer)
	if err := store.CheckKey(ctx); err != nil {
		return nil, err
	}
	if cfg.WebURL == "" {
		cfg.WebURL = "https://slack.com"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://slack.com/api"
	}
	return &Gateway{store: store, client: newClient(cfg.APIURL), seal: sealer, cfg: cfg}, nil
}

// Bind sets the domain callbacks (built after the gateway).
func (g *Gateway) Bind(links LinkSink, receiver Receiver, teams Teams) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if links != nil {
		g.links = links
	}
	if receiver != nil {
		g.receiver = receiver
	}
	if teams != nil {
		g.teams = teams
	}
}

func (g *Gateway) bound() (LinkSink, Receiver, Teams) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.links, g.receiver, g.teams
}

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelSlack }

// Exclusive: events and REST calls need no connection; any process serves
// a Slack dock.
func (*Gateway) Exclusive() bool { return false }

// Link: a dock is linked once its App has a bot token; until then it is
// linking (the web walks the owner through creating and installing it).
func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	ok, err := g.store.HasToken(ctx, d.ID)
	if err != nil || !ok {
		return dock.StatusLinking, err
	}
	return dock.StatusLinked, nil
}

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Running(identity.DockID) bool { return false }

func (*Gateway) RunningDocks() []identity.DockID { return nil }

func (*Gateway) Stop(identity.DockID) {}

// Unlink uninstalls the app from its workspace (best effort: only an app
// Seaport made knows its client secret) and forgets its credentials. The
// app itself stays registered in Slack: its owner deletes it at
// api.slack.com/apps.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	a, err := g.store.App(ctx, d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil {
		slog.Warn("open slack app being unlinked", "dock", d.ID, "err", err)
	} else if a.Installed() && a.ClientID != "" && a.clientSecret != "" {
		if err := g.client.uninstall(ctx, a.botToken, a.ClientID, a.clientSecret); err != nil {
			slog.Warn("uninstall slack app", "dock", d.ID, "err", err)
		}
	}
	g.client.forget(d.ID)
	return g.store.DeleteApp(ctx, d.ID)
}

// NewMessageID: Slack picks message timestamps; ours only names the stored
// ship (our own messages come back as bot events, which are dropped).
func (*Gateway) NewMessageID(identity.DockID) (string, error) { return "sl-" + uuid.NewString(), nil }

// Send posts the text (as Markdown blocks, split when long) in the
// conversation's channel and thread, then uploads the files.
func (g *Gateway) Send(ctx context.Context, msg dock.Outgoing) error {
	channel, thread, ok := splitChat(msg.ChatID)
	if !ok {
		return errx.Validation("not a Slack conversation: " + msg.ChatID)
	}
	a, err := g.store.App(ctx, msg.Dock)
	if err != nil {
		return err
	}
	if !a.Installed() {
		return errx.Business("the Slack App of this dock is not installed")
	}
	if err := g.deliver(ctx, a, channel, thread, msg); err != nil {
		if revoked(err) {
			g.lost(ctx, a)
			return errx.Business("Slack no longer accepts this app's token (uninstalled?): install it again from the dock")
		}
		return err
	}
	return nil
}

func (g *Gateway) deliver(ctx context.Context, a App, channel, thread string, msg dock.Outgoing) error {
	if strings.TrimSpace(msg.Text) != "" {
		for _, part := range Split(msg.Text, MaxMarkdown) {
			if _, err := g.client.post(ctx, a.botToken, channel, thread, part); err != nil {
				return err
			}
		}
	}
	if len(msg.Files) == 0 {
		return nil
	}
	files := make([]fileUpload, 0, len(msg.Files))
	for _, f := range msg.Files {
		files = append(files, fileUpload{Name: f.Name, Data: f.Data})
	}
	err := g.client.upload(ctx, a.botToken, channel, thread, files)
	switch code(err) {
	case "missing_scope", "not_allowed_token_type", "file_uploads_disabled", "access_denied":
		return errx.Business("Slack does not let this app upload files (" + code(err) + "): add the files:write scope and install the app again")
	}
	return err
}

// lost: Slack refuses the token: forget it and mark the dock logged out.
func (g *Gateway) lost(ctx context.Context, a App) {
	if err := g.store.ClearInstall(ctx, a.Dock); err != nil {
		slog.Warn("clear slack install", "dock", a.Dock, "err", err)
	}
	if links, _, _ := g.bound(); links != nil {
		if err := links.SetStatus(ctx, a.Dock, dock.StatusLoggedOut); err != nil {
			slog.Warn("mark slack dock logged out", "dock", a.Dock, "err", err)
		}
	}
}

// seen marks a message the crew got (👀), in the background.
func (g *Gateway) seen(a App, at arrival) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := g.client.react(ctx, a.botToken, at.channel, at.ts, "eyes"); err != nil {
			slog.Warn("react to slack message", "dock", a.Dock, "channel", at.channel, "err", err)
		}
	}()
}

// directoryOf names people and channels with this app's token.
type directoryOf struct {
	g     *Gateway
	dock  identity.DockID
	token string
	ctx   context.Context
}

func (d directoryOf) User(id string) userInfo {
	return d.g.client.user(d.ctx, d.dock, d.token, id)
}

func (d directoryOf) Channel(id string) string {
	return d.g.client.channel(d.ctx, d.dock, d.token, id)
}
