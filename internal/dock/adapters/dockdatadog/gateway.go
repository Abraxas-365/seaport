// Package dockdatadog is the gateway of Datadog docks: a dock is one
// endpoint Datadog's webhook integration calls, POST
// /datadog/webhook/{dock}, with the dock's secret in a header. Each alert
// is a message of the conversation of its monitor and scope, so a
// recovery lands where the alert did. The dock only triggers: nothing is
// posted to Datadog. With a mirror (dock.Mirror: a destination of a dock
// that replies) the ship service opens a conversation there for every new
// alert thread, linked to the alert's, and the agent and people talk there.
// Stateless: any process serves any Datadog dock.
package dockdatadog

import (
	"context"
	"sync"

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

// Config says where this port is (the URL Datadog calls).
type Config struct {
	PublicURL string
}

type Gateway struct {
	store *Store
	cfg   Config

	mu       sync.Mutex
	receiver Receiver
}

// Open opens the sealed secret store (secretKey: SEAPORT_SECRET_KEY).
func Open(ctx context.Context, db *sqlx.DB, secretKey string, cfg Config) (*Gateway, error) {
	sealer, err := seal.New(secretKey, SealInfo)
	if err != nil {
		return nil, err
	}
	store := NewStore(db, sealer)
	if err := store.CheckKey(ctx); err != nil {
		return nil, err
	}
	return &Gateway{store: store, cfg: cfg}, nil
}

// Bind hands inbound alerts to the ship domain (built later); nil leaves
// it as it was.
func (g *Gateway) Bind(r Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r != nil {
		g.receiver = r
	}
}

func (g *Gateway) bound() Receiver {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.receiver
}

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelDatadog }

// Exclusive: no connection; any process serves a Datadog dock.
func (*Gateway) Exclusive() bool { return false }

// Link makes the dock's endpoint (with a secret nobody has seen yet: the
// owner rotates it to read one), so a new dock is linked at once.
func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	if _, err := g.store.Create(ctx, d.Organization, d.ID); err != nil {
		return dock.StatusLinking, err
	}
	return dock.StatusLinked, nil
}

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Running(identity.DockID) bool { return false }

func (*Gateway) RunningDocks() []identity.DockID { return nil }

func (*Gateway) Stop(identity.DockID) {}

// Unlink forgets the endpoint, secret and mirror: the URL stops answering.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	return g.store.Delete(ctx, d.Organization, d.ID)
}

func (*Gateway) NewMessageID(identity.DockID) (string, error) {
	return "datadog-" + uuid.NewString(), nil
}

// Send: unreachable in practice (the channel doesn't reply, so the
// service refuses before).
func (*Gateway) Send(context.Context, dock.Outgoing) error {
	return errx.Business("datadog docks only trigger: nothing can be posted to Datadog")
}

// Open: likewise, a Datadog dock has nowhere to post a first message.
func (*Gateway) Open(context.Context, dock.Opening) (dock.Opened, error) {
	return dock.Opened{}, errx.Business("datadog docks only trigger: nothing can be posted to Datadog")
}
