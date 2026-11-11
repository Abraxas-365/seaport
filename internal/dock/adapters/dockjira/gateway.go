// Package dockjira is the gateway of Jira docks: a dock is one Jira Cloud
// site reached as a service account (email + API token, sealed). Issue
// comments (and new issues) that mention the account or an agent of the
// dock's team arrive by webhook, and replies are comments posted as ADF
// converted from the agent's Markdown. Stateless: any process serves any
// Jira dock.
package dockjira

import (
	"context"
	"log/slog"
	"strings"
	"sync"

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

// Config says where this port is and which Jira sites a dock may name.
type Config struct {
	// PublicURL is this port as Jira reaches it (the webhook URL).
	PublicURL string
	// Hosts is the allow-list of site hosts (SEAPORT_JIRA_HOSTS): exact
	// names, or "*.domain" for the sites under one. Empty: *.atlassian.net.
	Hosts []string
}

// DefaultHosts are the sites a dock may name unless configured.
var DefaultHosts = []string{"*.atlassian.net"}

type Gateway struct {
	store  *Store
	client *client
	cfg    Config

	mu       sync.Mutex
	receiver Receiver
	teams    Teams
	links    LinkSink
}

// Open opens the sealed site store (secretKey: SEAPORT_SECRET_KEY).
func Open(ctx context.Context, db *sqlx.DB, secretKey string, cfg Config) (*Gateway, error) {
	sealer, err := seal.New(secretKey, SealInfo)
	if err != nil {
		return nil, err
	}
	store := NewStore(db, sealer)
	if err := store.CheckKey(ctx); err != nil {
		return nil, err
	}
	if len(cfg.Hosts) == 0 {
		cfg.Hosts = DefaultHosts
	}
	return &Gateway{store: store, client: newClient(), cfg: cfg}, nil
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

func (*Gateway) Channel() dock.Channel { return dock.ChannelJira }

// Exclusive: webhooks and REST calls need no connection; any process
// serves a Jira dock.
func (*Gateway) Exclusive() bool { return false }

// Link: a dock without a site is linking (the web asks for the site, the
// account and its token); with one, the stored credentials are checked
// again, so linking a logged-out dock tells whether they work now.
func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	s, err := g.store.Site(ctx, d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		return dock.StatusLinking, nil
	}
	if err != nil {
		return dock.StatusLinking, err
	}
	if _, _, err := g.client.myself(ctx, s.URL, s.Email, s.token); err != nil {
		if refusedCredentials(err) {
			return dock.StatusLoggedOut, nil
		}
		return dock.StatusLinking, wrapped(err, "check the jira account")
	}
	return dock.StatusLinked, nil
}

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Running(identity.DockID) bool { return false }

func (*Gateway) RunningDocks() []identity.DockID { return nil }

func (*Gateway) Stop(identity.DockID) {}

// Unlink forgets the site and its credentials. Nothing is revoked in
// Jira: its API token belongs to the service account, which revokes it.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	return g.store.Delete(ctx, d.ID)
}

// NewMessageID: Jira picks comment ids; ours names the stored ship and is
// stamped on the comment (a property) so its webhook is recognized.
func (*Gateway) NewMessageID(identity.DockID) (string, error) { return "jira-" + uuid.NewString(), nil }

// Send posts the text as comments on the issue, in ADF converted from
// Markdown (a long reply is several comments, all stamped with the
// message id).
func (g *Gateway) Send(ctx context.Context, msg dock.Outgoing) error {
	if len(msg.Files) > 0 {
		return errx.Business("Jira docks don't send files yet: reply with text (link to files with full URLs)")
	}
	if !dock.ValidIssueKey(msg.ChatID) {
		return errx.Validation("not a Jira conversation: " + msg.ChatID)
	}
	chunks := split(msg.Text, MaxComment)
	if len(chunks) == 0 {
		return errx.Validation("a Jira comment needs text")
	}
	s, err := g.store.Site(ctx, msg.Dock)
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		if _, err := g.client.comment(ctx, s, msg.ChatID, toADF(chunk), msg.MessageID); err != nil {
			return g.failed(ctx, s, err, "post the jira comment")
		}
	}
	return nil
}

// failed reports a Jira failure; credentials Jira refuses (an API token
// that expired or was revoked) log the dock out so the web says so.
func (g *Gateway) failed(ctx context.Context, s Site, err error, what string) error {
	if refusedCredentials(err) {
		if links, _, _ := g.bound(); links != nil {
			if lerr := links.SetStatus(ctx, s.Dock, dock.StatusLoggedOut); lerr != nil {
				slog.Warn("log out jira dock", "dock", s.Dock, "err", lerr)
			}
		}
		return errx.External("Jira refused the dock's credentials (the API token expired or was revoked, or the account lost access): link the dock again with a new token")
	}
	return wrapped(err, what)
}

// hostOf is the address of a dock: its site's host.
func hostOf(site string) string {
	return strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
}
