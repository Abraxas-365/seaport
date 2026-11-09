package docksvc_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/docksvc"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// flakyOwners: one process that owns every dock it is asked for, while
// the database answers (down: every call fails).
type flakyOwners struct {
	down  atomic.Bool
	docks []dock.Dock
	taken atomic.Bool
}

func (o *flakyOwners) err() error {
	if o.down.Load() {
		return errors.New("database down")
	}
	return nil
}

func (o *flakyOwners) Acquire(_ context.Context, _ identity.OrganizationID, _ identity.DockID, process string, _ time.Duration) (string, error) {
	return process, o.err()
}
func (o *flakyOwners) Release(context.Context, identity.OrganizationID, identity.DockID, string) error {
	return o.err()
}
func (o *flakyOwners) Renew(_ context.Context, _ string, docks []identity.DockID, _ time.Duration) ([]identity.DockID, error) {
	return docks, o.err()
}
func (o *flakyOwners) Claim(context.Context, string, time.Duration, []dock.Channel, []dock.Status, int) ([]dock.Dock, error) {
	if err := o.err(); err != nil {
		return nil, err
	}
	if o.taken.Swap(true) {
		return nil, nil
	}
	return o.docks, nil
}
func (o *flakyOwners) ReleaseAll(context.Context, string) error { return o.err() }

type idleRelay struct{}

func (idleRelay) Call(context.Context, identity.OrganizationID, string, dock.Request) (dock.Reply, error) {
	return dock.Reply{}, errors.New("no other process")
}
func (idleRelay) Serve(ctx context.Context, _ string, _ func(context.Context, identity.OrganizationID, dock.Request) dock.Reply) {
	<-ctx.Done()
}

// connGateway is an exclusive gateway that tracks its connections.
type connGateway struct {
	qrGateway
	mu      sync.Mutex
	running map[identity.DockID]bool
}

func (g *connGateway) Restore(_ context.Context, d dock.Dock) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running[d.ID] = true
	return nil
}
func (g *connGateway) Running(id identity.DockID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running[id]
}
func (g *connGateway) RunningDocks() []identity.DockID {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []identity.DockID
	for id := range g.running {
		out = append(out, id)
	}
	return out
}
func (g *connGateway) Stop(id identity.DockID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, id)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A process that can't renew its leases for half their length stops its
// connections (another process may take the docks when they run out), and
// reconnects once it renews again.
func TestUnrenewedLeasesStopConnections(t *testing.T) {
	d := dock.Dock{ID: identity.NewDockID(), Organization: org, Channel: dock.ChannelWhatsApp, Name: "wa", Status: dock.StatusLinked}
	repo := &memRepo{docks: map[identity.DockID]dock.Dock{d.ID: d}}
	owners := &flakyOwners{docks: []dock.Dock{d}}
	g := &connGateway{running: map[identity.DockID]bool{}}
	s := docksvc.New(repo, nil, 0, docksvc.Cluster{Owners: owners, Relay: idleRelay{}, Process: "p", TTL: 300 * time.Millisecond}, g)
	s.Start(context.Background())
	defer s.Stop()

	waitFor(t, "the dock is taken and connected", func() bool { return g.Running(d.ID) })
	owners.down.Store(true)
	waitFor(t, "fencing stops the connection", func() bool { return !g.Running(d.ID) })
	owners.down.Store(false)
	waitFor(t, "reconnected after renewing", func() bool { return g.Running(d.ID) })
}
