package docksvc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Cluster lets several processes serve one database. Docks of exclusive
// channels (Gateway.Exclusive) are run by the process holding their lease
// in Owners; the others hand their work to it through Relay. A zero
// Cluster (no Owners) runs everything in this process and Start does
// nothing (unit tests: no linked dock is reconnected).
type Cluster struct {
	Owners  dock.Owners
	Relay   dock.Relay
	Process string
	// TTL is a lease's length; leases are renewed every TTL/5 and the
	// docks are reconciled every TTL/3.
	TTL time.Duration
}

// The tuning of ownership, as fractions of the lease.
var (
	// routeTries bounds how often work follows a dock that moved; the
	// pause grows by routePause each time (a stopping owner refuses work
	// until it has handed its docks over, about a second).
	routeTries = 5
	routePause = 250 * time.Millisecond
	// claimBatch is how many ownerless docks one reconcile round takes.
	claimBatch = 10
	// orphanGrace: a linking dock that no process pairs for that long had
	// its pairing process die (marked failed).
	orphanGrace = time.Minute
	// maxBackoff bounds the pause between two restores of a dock.
	maxBackoff = 5 * time.Minute
	// stopWait bounds how long Stop waits for routed work in flight.
	stopWait = 15 * time.Second
)

// ownership is this process's view of the docks it runs.
type ownership struct {
	mu sync.Mutex
	// owned: docks whose lease this process holds (as of the last renew).
	owned map[identity.DockID]struct{}
	// retry: when a failed restore of a dock may be tried again.
	retry map[identity.DockID]backoff
	// renewed: the last successful renew (fencing).
	renewed time.Time
	// stopped: Stop was called; routed work goes elsewhere.
	stopped bool

	locksMu sync.Mutex
	locks   map[identity.DockID]*dockLock

	cancel context.CancelFunc
	done   sync.WaitGroup
}

type backoff struct {
	at    time.Time
	delay time.Duration
}

type dockLock struct {
	mu   sync.Mutex
	refs int
}

// lock serializes this process's work on one dock (link, unlink, restore,
// reconcile); other processes never run it (they are not its owner).
func (s *Service) lock(id identity.DockID) func() {
	o := &s.own
	o.locksMu.Lock()
	l := o.locks[id]
	if l == nil {
		l = &dockLock{}
		o.locks[id] = l
	}
	l.refs++
	o.locksMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		o.locksMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(o.locks, id)
		}
		o.locksMu.Unlock()
	}
}

func (s *Service) clustered(g dock.Gateway) bool { return s.cluster.Owners != nil && g.Exclusive() }

// owns: this process holds the dock's lease, as far as it knows; never
// while fenced (leases not renewed for half their length).
func (s *Service) owns(id identity.DockID) bool {
	s.own.mu.Lock()
	defer s.own.mu.Unlock()
	_, ok := s.own.owned[id]
	return ok && !s.fencedLocked()
}

func (s *Service) fencedLocked() bool {
	return !s.own.renewed.IsZero() && time.Since(s.own.renewed) > s.cluster.TTL/2
}

func (s *Service) setOwned(id identity.DockID, owned bool) {
	s.own.mu.Lock()
	defer s.own.mu.Unlock()
	if owned {
		s.own.owned[id] = struct{}{}
	} else {
		delete(s.own.owned, id)
		delete(s.own.retry, id)
	}
}

// route runs request where the dock lives: here for channels that are not
// exclusive or docks this process owns (taken if ownerless), else at the
// owner, following the dock when it moves.
func (s *Service) route(ctx context.Context, d dock.Dock, g dock.Gateway, req dock.Request) (dock.Reply, error) {
	if !s.clustered(g) {
		return s.local(ctx, d.Organization, req)
	}
	if req.Kind == dock.RequestSend && s.owns(d.ID) && g.Running(d.ID) {
		return s.local(ctx, d.Organization, req)
	}
	if s.stopping() {
		// Acquire could hand this process a dock it would then keep,
		// unused, until the lease ran out.
		return dock.Reply{}, errx.External("this Seaport process is stopping; try again")
	}
	for try := range routeTries {
		if try > 0 {
			select {
			case <-ctx.Done():
				return dock.Reply{}, errx.Wrap(ctx.Err(), "find the dock's process", errx.TypeExternal)
			case <-time.After(time.Duration(try) * routePause):
			}
		}
		owner, err := s.cluster.Owners.Acquire(ctx, d.Organization, d.ID, s.cluster.Process, s.cluster.TTL)
		if err != nil {
			return dock.Reply{}, errx.Wrap(err, "find the dock's process", errx.TypeInternal)
		}
		if owner == s.cluster.Process {
			s.setOwned(d.ID, true)
			return s.local(ctx, d.Organization, req)
		}
		reply, err := s.cluster.Relay.Call(ctx, d.Organization, owner, req)
		if err == nil {
			err = reply.Err()
		}
		if dock.IsMoved(err) {
			continue
		}
		return reply, err
	}
	return dock.Reply{}, dock.Moved()
}

// serve answers a request another process routed here.
func (s *Service) serve(ctx context.Context, org identity.OrganizationID, req dock.Request) dock.Reply {
	if s.stopping() {
		return dock.Reply{Error: dock.Moved()}
	}
	owner, err := s.cluster.Owners.Acquire(ctx, org, req.Dock, s.cluster.Process, s.cluster.TTL)
	if err != nil {
		return dock.Reply{Error: replyError(err)}
	}
	if owner != s.cluster.Process {
		return dock.Reply{Error: dock.Moved()}
	}
	reply, err := s.local(ctx, org, req)
	if err != nil {
		reply.Error = replyError(err)
	}
	return reply
}

// stopping: this process is shutting down (Stop called).
func (s *Service) stopping() bool {
	s.own.mu.Lock()
	defer s.own.mu.Unlock()
	return s.own.stopped
}

// replyError is err as it crosses to another process (type, code,
// message and details; the cause stays in this process's logs).
func replyError(err error) *errx.Error {
	var e *errx.Error
	if errx.As(err, &e) {
		c := *e
		c.Err, c.Op = nil, ""
		return &c
	}
	slog.Error("dock work", "err", err)
	return errx.Internal("dock work failed")
}

// local runs request on this process's gateway.
func (s *Service) local(ctx context.Context, org identity.OrganizationID, req dock.Request) (dock.Reply, error) {
	if req.Kind == dock.RequestSend {
		return dock.Reply{}, s.sendHere(ctx, org, *req.Outgoing)
	}
	unlock := s.lock(req.Dock)
	defer unlock()
	d, err := s.repo.Find(ctx, org, req.Dock)
	if err != nil {
		return dock.Reply{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return dock.Reply{}, err
	}
	// Under the dock's lock, which reconciling takes too: the lease can't
	// be given up between here and the status stored below. A stopping
	// process takes no dock (it would keep it until its lease ran out).
	if s.clustered(g) {
		if s.stopping() {
			return dock.Reply{}, dock.Moved()
		}
		owner, err := s.cluster.Owners.Acquire(ctx, org, d.ID, s.cluster.Process, s.cluster.TTL)
		if err != nil {
			return dock.Reply{}, errx.Wrap(err, "keep the dock", errx.TypeInternal)
		}
		s.setOwned(d.ID, owner == s.cluster.Process)
		if !s.owns(d.ID) {
			return dock.Reply{}, dock.Moved()
		}
	}
	switch req.Kind {
	case dock.RequestLink:
		status, err := g.Link(ctx, d)
		if err != nil {
			_ = s.repo.SetStatus(ctx, d.ID, dock.StatusFailed)
			return dock.Reply{}, errx.Wrap(err, "start linking", errx.TypeExternal)
		}
		// Stored even when unchanged: updated_at dates the pairing
		// (reconcile gives an orphaned one up after orphanGrace).
		if err := s.repo.SetStatus(ctx, d.ID, status); err != nil {
			return dock.Reply{}, errx.Wrap(err, "set dock status", errx.TypeInternal)
		}
		s.forgetRetry(d.ID)
		return dock.Reply{Status: status}, nil
	default:
		if err := g.Unlink(ctx, d); err != nil {
			return dock.Reply{}, errx.Wrap(err, "unlink dock", errx.TypeExternal)
		}
		s.forgetRetry(d.ID)
		if d.Status != dock.StatusLoggedOut {
			if err := s.repo.SetStatus(ctx, d.ID, dock.StatusLoggedOut); err != nil {
				return dock.Reply{}, errx.Wrap(err, "set dock status", errx.TypeInternal)
			}
		}
		return dock.Reply{Status: dock.StatusLoggedOut}, nil
	}
}

// sendHere sends through this process's connection of the dock, opening
// it first when this process just took the dock.
func (s *Service) sendHere(ctx context.Context, org identity.OrganizationID, msg dock.Outgoing) error {
	d, err := s.repo.Find(ctx, org, msg.Dock)
	if err != nil {
		return errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if d.Status != dock.StatusLinked {
		return errx.Business("dock is not linked (status " + string(d.Status) + ")")
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return err
	}
	if s.clustered(g) && !g.Running(d.ID) {
		if err := s.ensureRunning(ctx, d, g); err != nil {
			return err
		}
	}
	return errx.Wrap(g.Send(ctx, msg), "send through dock", errx.TypeExternal)
}

func (s *Service) ensureRunning(ctx context.Context, d dock.Dock, g dock.Gateway) error {
	unlock := s.lock(d.ID)
	defer unlock()
	if g.Running(d.ID) {
		return nil
	}
	if s.stopping() || !s.owns(d.ID) {
		return dock.Moved() // stopping, or lost while waiting for the lock
	}
	if err := g.Restore(ctx, d); err != nil {
		return errx.Wrap(err, "connect dock", errx.TypeExternal)
	}
	return nil
}

func (s *Service) forgetRetry(id identity.DockID) {
	s.own.mu.Lock()
	delete(s.own.retry, id)
	s.own.mu.Unlock()
}

// Start runs ownership: serving routed requests, renewing leases (and
// dropping every connection when renewing fails for half a lease, since
// another process may take the docks then) and reconciling (taking
// ownerless docks, connecting the linked ones this process owns, dropping
// those it lost). It returns at once; Stop ends it.
func (s *Service) Start(ctx context.Context) {
	if s.cluster.Owners == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.own.mu.Lock()
	s.own.cancel, s.own.renewed = cancel, time.Now()
	s.own.mu.Unlock()
	run := func(fn func(context.Context)) {
		s.own.done.Add(1)
		go func() {
			defer s.own.done.Done()
			fn(ctx)
		}()
	}
	run(func(ctx context.Context) { s.cluster.Relay.Serve(ctx, s.cluster.Process, s.serve) })
	run(func(ctx context.Context) { every(ctx, s.cluster.TTL/5, s.renew) })
	run(func(ctx context.Context) { every(ctx, s.cluster.TTL/3, s.reconcile) })
	run(func(ctx context.Context) { every(ctx, s.cluster.TTL/10, s.fence) })
}

// fence stops every connection while leases go unrenewed for half their
// length: another process may take the docks once they run out, and two
// connections of one account must never run. It takes no dock lock (a
// holder may be blocked on the same database) and repeats every round,
// so a connection opened meanwhile is stopped too.
func (s *Service) fence(context.Context) {
	s.own.mu.Lock()
	fenced := s.fencedLocked()
	s.own.mu.Unlock()
	if !fenced {
		return
	}
	for _, g := range s.gateways {
		if !g.Exclusive() {
			continue
		}
		for _, id := range g.RunningDocks() {
			slog.Error("dock leases not renewed for half their length: connection stopped", "dock", id)
			g.Stop(id)
		}
	}
}

// Stop ends ownership: the loops, the routed work in flight, every
// connection of exclusive docks, then the leases (other processes take
// the docks at once).
func (s *Service) Stop() {
	s.own.mu.Lock()
	cancel := s.own.cancel
	s.own.cancel, s.own.stopped = nil, true
	s.own.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	waited := make(chan struct{})
	go func() {
		s.own.done.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(stopWait):
		slog.Warn("dock work still running at shutdown")
	}
	// Under each dock's lock: work in flight finishes first, and any
	// that starts now refuses (stopping).
	ids := map[identity.DockID]struct{}{}
	for _, id := range s.ownedIDs() {
		ids[id] = struct{}{}
	}
	for _, g := range s.gateways {
		if g.Exclusive() {
			for _, id := range g.RunningDocks() {
				ids[id] = struct{}{}
			}
		}
	}
	for id := range ids {
		s.drop(id)
	}
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := s.cluster.Owners.ReleaseAll(ctx, s.cluster.Process); err != nil {
		slog.Warn("release docks", "err", err)
	}
}

func every(ctx context.Context, period time.Duration, fn func(context.Context)) {
	t := time.NewTicker(period)
	defer t.Stop()
	fn(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

func (s *Service) ownedIDs() []identity.DockID {
	s.own.mu.Lock()
	defer s.own.mu.Unlock()
	out := make([]identity.DockID, 0, len(s.own.owned))
	for id := range s.own.owned {
		out = append(out, id)
	}
	return out
}

func (s *Service) renew(ctx context.Context) {
	mine := s.ownedIDs()
	started := time.Now() // the leases run from no earlier than this
	// Bounded: a hung database must not hold off the next renew.
	rctx, cancel := context.WithTimeout(ctx, s.cluster.TTL/5)
	ids, err := s.cluster.Owners.Renew(rctx, s.cluster.Process, mine, s.cluster.TTL)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("renew dock leases", "err", err) // fence acts if this lasts
		}
		return
	}
	held := make(map[identity.DockID]struct{}, len(ids))
	for _, id := range ids {
		held[id] = struct{}{}
	}
	s.own.mu.Lock()
	s.own.renewed = started
	s.own.mu.Unlock()
	var lost []identity.DockID
	for _, id := range mine { // docks taken meanwhile are renewed next time
		if _, ok := held[id]; !ok {
			lost = append(lost, id)
		}
	}
	for _, id := range lost {
		// Its own goroutine: a dock busy under its lock (linking) must
		// not hold off renewing the others.
		s.own.done.Add(1)
		go func() {
			defer s.own.done.Done()
			s.recheck(ctx, id)
		}()
	}
}

// recheck: a renew missed the dock's lease (it ran out, or was given up
// and taken again meanwhile). Under the dock's lock, keep it when it is
// still, or again, this process's; drop it when another process owns it.
func (s *Service) recheck(ctx context.Context, id identity.DockID) {
	unlock := s.lock(id)
	defer unlock()
	if !s.owns(id) {
		return
	}
	d, err := s.repo.Get(ctx, id)
	if err == nil {
		var owner string
		if owner, err = s.cluster.Owners.Acquire(ctx, d.Organization, id, s.cluster.Process, s.cluster.TTL); err == nil && owner == s.cluster.Process {
			return
		}
	}
	slog.Warn("dock lease lost", "dock", id, "err", err)
	s.stopLocked(id)
	if d.Organization != (identity.OrganizationID{}) {
		s.release(ctx, d) // ours or not (a no-op then): nobody keeps it idle
	}
}

// drop stops this process's connection of a dock it no longer owns.
func (s *Service) drop(id identity.DockID) {
	unlock := s.lock(id)
	defer unlock()
	s.stopLocked(id)
}

func (s *Service) stopLocked(id identity.DockID) {
	s.setOwned(id, false)
	for _, g := range s.gateways {
		if g.Exclusive() && g.Running(id) {
			g.Stop(id)
		}
	}
}

func (s *Service) exclusive() (channels []dock.Channel) {
	for c, g := range s.gateways {
		if g.Exclusive() {
			channels = append(channels, c)
		}
	}
	return channels
}

func (s *Service) reconcile(ctx context.Context) {
	claimed, err := s.cluster.Owners.Claim(ctx, s.cluster.Process, s.cluster.TTL, s.exclusive(),
		[]dock.Status{dock.StatusLinked, dock.StatusLinking}, claimBatch)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("claim docks", "err", err)
		}
	}
	for _, d := range claimed {
		slog.Info("dock taken over", "dock", d.ID, "organization", d.Organization, "status", d.Status)
		s.setOwned(d.ID, true)
	}
	for _, id := range s.ownedIDs() {
		if ctx.Err() != nil {
			return
		}
		s.reconcileDock(ctx, id)
	}
	// A connection of a dock this process doesn't own (lost meanwhile).
	for _, g := range s.gateways {
		if !g.Exclusive() {
			continue
		}
		for _, id := range g.RunningDocks() {
			if !s.owns(id) {
				s.drop(id)
			}
		}
	}
}

func (s *Service) reconcileDock(ctx context.Context, id identity.DockID) {
	unlock := s.lock(id)
	defer unlock()
	if !s.owns(id) {
		return
	}
	d, err := s.repo.Get(ctx, id)
	if errx.IsType(err, errx.TypeNotFound) {
		s.setOwned(id, false) // deleted (its lease went with it)
		for _, g := range s.gateways {
			if g.Exclusive() && g.Running(id) {
				g.Stop(id)
			}
		}
		return
	}
	if err != nil {
		slog.Warn("reconcile dock", "dock", id, "err", err)
		return
	}
	g, err := s.gateway(d.Channel)
	if err != nil || !g.Exclusive() {
		s.release(ctx, d)
		return
	}
	switch {
	case d.Status == dock.StatusLinked && !g.Running(id):
		s.restore(ctx, d, g)
	case d.Status == dock.StatusLinking && !g.Running(id):
		if time.Since(d.UpdatedAt) < orphanGrace {
			return // a link may be on its way here
		}
		slog.Warn("dock pairing abandoned (its process stopped)", "dock", id)
		if err := s.SetStatus(ctx, id, dock.StatusFailed); err != nil {
			slog.Warn("mark abandoned pairing failed", "dock", id, "err", err)
		}
		s.release(ctx, d)
	case d.Status == dock.StatusLoggedOut || d.Status == dock.StatusFailed:
		if g.Running(id) {
			g.Stop(id)
		}
		s.release(ctx, d)
	}
}

func (s *Service) release(ctx context.Context, d dock.Dock) {
	s.setOwned(d.ID, false)
	if err := s.cluster.Owners.Release(ctx, d.Organization, d.ID, s.cluster.Process); err != nil {
		slog.Warn("release dock", "dock", d.ID, "err", err)
	}
}

func (s *Service) restore(ctx context.Context, d dock.Dock, g dock.Gateway) {
	s.own.mu.Lock()
	b := s.own.retry[d.ID]
	s.own.mu.Unlock()
	if time.Now().Before(b.at) {
		return
	}
	if err := g.Restore(ctx, d); err != nil {
		b.delay = min(max(2*b.delay, s.cluster.TTL/3), maxBackoff)
		b.at = time.Now().Add(b.delay)
		slog.Error("restore dock", "dock", d.ID, "err", err, "retry_in", b.delay)
		s.own.mu.Lock()
		if _, ok := s.own.owned[d.ID]; ok {
			s.own.retry[d.ID] = b
		}
		s.own.mu.Unlock()
		return
	}
	s.forgetRetry(d.ID)
	slog.Info("dock connected", "dock", d.ID, "process", s.cluster.Process)
}
