// Package dockemail is the gateway of email docks: a dock is one mailbox,
// read over IMAP (IDLE, else polling) and answered over SMTP, both with a
// password sealed at rest. New mail wakes the crew; a reply goes out as a
// reply in the same email thread (In-Reply-To/References). Exclusive: one
// process holds the IMAP connection of a dock (the owner of its lease).
package dockemail

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

// LinkSink records link state (the dock domain).
type LinkSink interface {
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
	SetStatus(ctx context.Context, dock identity.DockID, status dock.Status) error
	PairingChanged(ctx context.Context, dock identity.DockID)
}

// Gateway runs the mailboxes of this process's docks.
type Gateway struct {
	store *Store
	cfg   Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	receiver Receiver
	links    LinkSink
	sessions map[identity.DockID]*session
}

// Open opens the sealed account store (secretKey: SEAPORT_SECRET_KEY).
func Open(ctx context.Context, db *sqlx.DB, secretKey string, cfg Config) (*Gateway, error) {
	sealer, err := seal.New(secretKey, SealInfo)
	if err != nil {
		return nil, err
	}
	store := NewStore(db, sealer)
	if err := store.CheckKey(ctx); err != nil {
		return nil, err
	}
	base, cancel := context.WithCancel(context.Background())
	return &Gateway{store: store, cfg: cfg, ctx: base, cancel: cancel, sessions: map[identity.DockID]*session{}}, nil
}

// Bind sets the domain callbacks (built after the gateway).
func (g *Gateway) Bind(links LinkSink, receiver Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if links != nil {
		g.links = links
	}
	if receiver != nil {
		g.receiver = receiver
	}
}

// BindReceiver hands the gateway the ship domain.
func (g *Gateway) BindReceiver(r Receiver) { g.Bind(nil, r) }

func (g *Gateway) bound() (LinkSink, Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.links, g.receiver
}

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelEmail }

// Exclusive: a dock keeps one IMAP connection, so one process runs it.
func (*Gateway) Exclusive() bool { return true }

// Close ends every session and waits for them.
func (g *Gateway) Close() {
	g.cancel()
	g.wg.Wait()
}

// Link starts the dock's session when it has a mailbox (the web sends the
// settings first, checked and stored by the handler); without one the dock
// is linking and waits for them.
func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	a, err := g.store.Find(ctx, d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		g.Stop(d.ID)
		return dock.StatusLinking, nil
	}
	if err != nil {
		return "", err
	}
	g.start(a)
	if links, _ := g.bound(); links != nil {
		if err := links.SetLinked(ctx, d.ID, a.Address); err != nil {
			return "", err
		}
	}
	return dock.StatusLinked, nil
}

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

// Restore reconnects a linked dock; the connection itself is made (and
// retried) by its session, so a mailbox that is down doesn't fail it.
func (g *Gateway) Restore(ctx context.Context, d dock.Dock) error {
	a, err := g.store.Find(ctx, d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		if links, _ := g.bound(); links != nil {
			_ = links.SetStatus(ctx, d.ID, dock.StatusLoggedOut)
		}
		return errx.NotFound("the mailbox of dock " + d.ID.String() + " is gone; link it again")
	}
	if err != nil {
		return err
	}
	g.start(a)
	return nil
}

func (g *Gateway) Running(id identity.DockID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.sessions[id]
	return ok
}

func (g *Gateway) RunningDocks() []identity.DockID {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]identity.DockID, 0, len(g.sessions))
	for id := range g.sessions {
		out = append(out, id)
	}
	return out
}

// Stop drops this process's connection and keeps the mailbox (another
// process runs the dock next).
func (g *Gateway) Stop(id identity.DockID) {
	g.mu.Lock()
	s := g.sessions[id]
	delete(g.sessions, id)
	g.mu.Unlock()
	if s != nil {
		s.stop()
		slog.Info("email dock stopped in this process", "dock", id)
	}
}

// Unlink stops the session and forgets the mailbox and its password.
// Nothing is revoked at the provider: its app password is the owner's to
// delete.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	g.Stop(d.ID)
	return g.store.Delete(ctx, d.ID)
}

// NewMessageID is a Message-ID in the mailbox's own domain (providers
// judge mail by it), made without a connection.
func (g *Gateway) NewMessageID(id identity.DockID) (string, error) {
	ctx, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	a, err := g.store.Find(ctx, id)
	if err != nil {
		return "", err
	}
	return newMessageID(a.Address), nil
}

func newMessageID(address string) string {
	domain := "seaport.invalid"
	if i := strings.LastIndex(address, "@"); i >= 0 && i < len(address)-1 {
		domain = address[i+1:]
	}
	return "seaport-" + uuid.NewString() + "@" + domain
}

// start (re)starts the dock's session.
func (g *Gateway) start(a Account) {
	g.mu.Lock()
	if g.ctx.Err() != nil {
		g.mu.Unlock()
		return
	}
	old := g.sessions[a.Dock]
	s := newSession(g, a)
	g.sessions[a.Dock] = s
	g.wg.Add(1)
	g.mu.Unlock()
	if old != nil {
		old.stop()
	}
	go func() {
		defer g.wg.Done()
		s.run()
		g.mu.Lock()
		if g.sessions[a.Dock] == s {
			delete(g.sessions, a.Dock)
		}
		g.mu.Unlock()
	}()
}

// errReconfigured ends a connection so the next one uses the saved settings.
var errReconfigured = errors.New("the mailbox settings changed")
