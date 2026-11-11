// Package dockgithub is the gateway of GitHub docks: a dock is a GitHub
// App (registered from a manifest, credentials sealed); issues and pull
// requests that mention it or an agent of its team arrive by signed
// webhook, and replies are issue comments posted with an installation
// token. Stateless: any process serves any GitHub dock.
package dockgithub

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

// Teams tells which agents work on a dock and where an agent works (the
// agent domain, bound late): a message mentioning one of them is for the
// crew, and an agent gets App tokens for the repos of its docks' Apps.
type Teams interface {
	Team(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]agent.Member, error)
	Find(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (agent.Agent, error)
}

// LinkSink records link state (the dock domain).
type LinkSink interface {
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
	SetStatus(ctx context.Context, dock identity.DockID, status dock.Status) error
}

// Config says where GitHub and this port are.
type Config struct {
	// PublicURL is this port as GitHub reaches it (webhooks, redirects).
	PublicURL string
	// WebURL (https://github.com) and APIURL (https://api.github.com):
	// GitHub Enterprise Server or a fake in tests.
	WebURL, APIURL string
	// OIDCURL is the issuer of GitHub Actions' OIDC tokens
	// (DefaultOIDCURL; GHES: https://HOST/_services/token).
	OIDCURL string
}

type Gateway struct {
	store  *Store
	client *client
	oidc   *oidcVerifier
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
		cfg.WebURL = "https://github.com"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.github.com"
	}
	if cfg.OIDCURL == "" {
		cfg.OIDCURL = DefaultOIDCURL
	}
	return &Gateway{store: store, client: newClient(cfg.APIURL), oidc: newOIDCVerifier(cfg.OIDCURL), seal: sealer, cfg: cfg}, nil
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

func (*Gateway) Channel() dock.Channel { return dock.ChannelGitHub }

// Exclusive: webhooks and REST calls need no connection; any process
// serves a GitHub dock.
func (*Gateway) Exclusive() bool { return false }

// Link: a dock is linked once its App is installed somewhere; until then
// it is linking (the web walks the owner through creating and installing
// the App).
func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	ok, err := g.store.HasApp(ctx, d.ID)
	if err != nil || !ok {
		return dock.StatusLinking, err
	}
	installs, err := g.store.Installations(ctx, d.ID)
	if err != nil {
		return dock.StatusLinking, err
	}
	if active(installs) > 0 {
		return dock.StatusLinked, nil
	}
	return dock.StatusLinking, nil
}

func active(installs []Installation) int {
	n := 0
	for _, in := range installs {
		if !in.Suspended {
			n++
		}
	}
	return n
}

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Running(identity.DockID) bool { return false }

func (*Gateway) RunningDocks() []identity.DockID { return nil }

func (*Gateway) Stop(identity.DockID) {}

// Unlink uninstalls the App everywhere (best effort) and forgets its
// credentials. The App itself stays registered on GitHub (no API deletes
// one): its owner deletes it in GitHub's settings.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	a, err := g.store.App(ctx, d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err == nil {
		installs, _ := g.store.Installations(ctx, d.ID)
		for _, in := range installs {
			if err := g.client.uninstall(ctx, a, in.ID); err != nil && !errx.IsType(err, errx.TypeNotFound) {
				slog.Warn("uninstall github app", "dock", d.ID, "installation", in.ID, "err", err)
			}
		}
	} else {
		slog.Warn("open github app being unlinked", "dock", d.ID, "err", err)
	}
	g.client.forget(d.ID)
	return g.store.DeleteApp(ctx, d.ID)
}

// NewMessageID: GitHub picks comment ids; ours only names the stored ship
// (our comments come back as bot events, which are ignored).
func (*Gateway) NewMessageID(identity.DockID) (string, error) { return "gh-" + uuid.NewString(), nil }

// Send posts the text as a comment on the issue or pull request.
func (g *Gateway) Send(ctx context.Context, msg dock.Outgoing) error {
	repo, number, ok := splitChat(msg.ChatID)
	if !ok {
		return errx.Validation("not a GitHub conversation: " + msg.ChatID)
	}
	a, err := g.store.App(ctx, msg.Dock)
	if err != nil {
		return err
	}
	body := msg.Text
	if len(msg.Files) > 0 {
		links, err := g.client.commitFiles(ctx, a, g.cfg.WebURL, repo, number, msg.MessageID, msg.Files)
		if err != nil {
			return err
		}
		body = filesBody(msg.Text, msg.Files, links)
	}
	_, err = g.client.comment(ctx, a, repo, number, body+marker(msg.MessageID))
	return err
}

// markerPrefix starts the hidden line Send appends to its comments with
// the outbound ship's message id: the webhook of that comment comes back
// as the App's own message and is matched to the stored ship by it.
const markerPrefix = "<!-- seaport-message "

func marker(id string) string { return "\n\n" + markerPrefix + id + " -->" }

// unmark splits a comment's hidden message id off its body ("" when it
// has none).
func unmark(body string) (string, string) {
	i := strings.LastIndex(body, markerPrefix)
	if i < 0 {
		return body, ""
	}
	id, ok := strings.CutSuffix(strings.TrimSpace(body[i+len(markerPrefix):]), "-->")
	id = strings.TrimSpace(id)
	if !ok || !strings.HasPrefix(id, "gh-") || len(id) > 64 || strings.ContainsAny(id, " \n") {
		return body, ""
	}
	return strings.TrimRight(body[:i], "\n "), id
}

// seen marks a message the crew got (👀), in the background.
func (g *Gateway) seen(a App, at arrival) {
	if at.reactPath == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := g.client.react(ctx, a, at.repo, at.reactPath, "eyes"); err != nil {
			slog.Warn("react to github message", "dock", a.Dock, "repo", at.repo, "err", err)
		}
	}()
}
