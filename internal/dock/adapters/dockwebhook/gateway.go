// Package dockwebhook is the gateway of webhook docks: a dock is one
// inbound endpoint, POST /hooks/{dock}, that any system able to send JSON
// (alerts, CI, payments) can call. It only triggers: nothing is posted
// back, so the crew answers on another door of the thread or stays
// silent. A per-dock template maps the body to the message. Stateless:
// any process serves any webhook dock.
package dockwebhook

import (
	"context"
	"log/slog"
	"sync"
	"time"

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

// Config says where this port is (the URL a provider calls).
type Config struct {
	PublicURL string
}

// warnEvery bounds how often one dock's failing template is logged.
const warnEvery = time.Minute

type Gateway struct {
	store *Store
	cfg   Config

	mu       sync.Mutex
	receiver Receiver
	warned   map[identity.DockID]time.Time
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
	return &Gateway{store: store, cfg: cfg, warned: map[identity.DockID]time.Time{}}, nil
}

// BindReceiver hands inbound messages to the ship domain (built later).
func (g *Gateway) BindReceiver(r Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.receiver = r
}

func (g *Gateway) bound() Receiver {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.receiver
}

// templateFailed logs a template that failed to run, at most once a
// minute per dock (a noisy provider must not flood the log).
func (g *Gateway) templateFailed(d identity.DockID, fields map[string]string) {
	g.mu.Lock()
	last := g.warned[d]
	now := time.Now()
	ok := now.Sub(last) >= warnEvery
	if ok {
		g.warned[d] = now
	}
	g.mu.Unlock()
	if ok {
		slog.Warn("webhook template failed, defaults used", "dock", d, "fields", fields)
	}
}

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelWebhook }

// Exclusive: no connection; any process serves a webhook dock.
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

// Unlink forgets the endpoint, secret and template: the URL stops
// answering.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error { return g.store.Delete(ctx, d.ID) }

func (*Gateway) NewMessageID(identity.DockID) (string, error) {
	return "webhook-" + uuid.NewString(), nil
}

// Send: unreachable in practice (the channel doesn't reply, so the
// service refuses before).
func (*Gateway) Send(context.Context, dock.Outgoing) error {
	return errx.Business("webhook docks only trigger: nothing can be posted there")
}
