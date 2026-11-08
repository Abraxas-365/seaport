// Package crewws is the WebSocket hub agents (connectors) dial into. It
// claims deliveries for each connection up to its concurrency, pushes them,
// and turns acks/releases/disconnects into ship commands. It is the ship
// domain's Notifier.
//
// Several processes may serve one database: every connection is recorded
// in crew.Presence (status and quota span processes), wakes and revokes
// are signalled to every process, and each process keeps its connections
// alive (touch) and reaps those of dead processes, releasing their
// deliveries.
package crewws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/coder/websocket"
)

// opTimeout bounds one ship command run for a connection.
const opTimeout = 15 * time.Second

// maxTouch is the longest pause between two touches of a process's
// connections; a connection untouched for staleTouches periods is a dead
// process's and gets reaped.
const (
	maxTouch     = 10 * time.Second
	staleTouches = 3
)

// closeWait bounds Close waiting for connections to release their work.
const closeWait = 5 * time.Second

type Hub struct {
	lease  time.Duration
	mcpURL string
	events event.Publisher
	// maxConns bounds each organization's live connections, across
	// processes (≤ 0: unlimited).
	maxConns int
	presence crew.Presence
	// process names this process in crew.Presence and its signals.
	process string
	touch   time.Duration

	mu    sync.Mutex
	ships ship.Commands
	conns map[*conn]struct{}
	// opening are connections still in their hello handshake (not yet in
	// conns): a revoke must close them too.
	opening map[*pending]struct{}
	closing bool

	serving sync.WaitGroup // ServeHTTP calls in progress
	stop    context.CancelFunc
	done    sync.WaitGroup // Start's goroutines

	// outbox coalesces signals for the other processes; one goroutine
	// sends them, so Notify and Disconnect never wait on the database.
	outboxMu sync.Mutex
	outbox   map[crew.Signal]struct{}
	kick     chan struct{}
	// unreleased are reaped sessions whose claims could not be released
	// yet (retried on the next reap).
	unreleased map[crew.Reaped]struct{}
}

// pending is a connection before its hello: enough for Disconnect.
type pending struct {
	organization identity.OrganizationID
	credential   string
	session      string
	cancel       context.CancelFunc
}

type conn struct {
	// organization owns the connection (its crew token): it only claims,
	// acks and replies within it.
	organization identity.OrganizationID
	// credential is the crew token it connected with ("" for the static
	// token and open mode); revoking the token closes the connection.
	credential  string
	cancel      context.CancelFunc
	ws          *websocket.Conn
	session     string
	remote      string
	connectedAt time.Time
	agent       string
	concurrency int
	steer       bool
	wake        chan struct{}
	send        chan crew.Frame

	mu sync.Mutex
	// inFlight maps each open delivery to what was pushed for it.
	inFlight map[identity.DeliveryID]*tracked
	// recording serializes reading inFlight and writing it to crew.Presence,
	// so an older snapshot never lands after a newer one.
	recording sync.Mutex
}

// tracked is an open delivery on a connection. ships are the ships pushed
// for it (the delivery plus every steer), the ack default for clients
// that don't name ships.
type tracked struct {
	conversation identity.ConversationID
	ships        []identity.ShipID
	since        time.Time
}

// New builds the hub; a delivery lease is lease, and connections are
// touched every lease/4 (at most maxTouch).
func New(lease time.Duration, mcpURL string, events event.Publisher, maxConns int, presence crew.Presence) *Hub {
	if events == nil {
		events = event.Nop{}
	}
	return &Hub{lease: lease, mcpURL: mcpURL, events: events, maxConns: maxConns, presence: presence,
		process: newSession(), touch: min(lease/4, maxTouch),
		conns: map[*conn]struct{}{}, opening: map[*pending]struct{}{},
		outbox: map[crew.Signal]struct{}{}, kick: make(chan struct{}, 1), unreleased: map[crew.Reaped]struct{}{}}
}

// Status lists an organization's connected agents (of every process) and
// their deliveries in flight.
func (h *Hub) Status(ctx context.Context, org identity.OrganizationID) (crew.Status, error) {
	return h.presence.Status(ctx, org)
}

func (c *conn) agentInfo() crew.Agent {
	return crew.Agent{Session: c.session, Name: c.agent, Concurrency: c.concurrency, Steer: c.steer}
}

// deliveryList is what the connection works on (for crew.Presence).
func (c *conn) deliveryList() []crew.AgentDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]crew.AgentDelivery, 0, len(c.inFlight))
	for id, t := range c.inFlight {
		out = append(out, crew.AgentDelivery{ID: id, Conversation: t.conversation, Ships: len(t.ships), Since: t.since})
	}
	slices.SortFunc(out, func(x, y crew.AgentDelivery) int {
		if c := x.Since.Compare(y.Since); c != 0 {
			return c
		}
		return strings.Compare(x.ID.String(), y.ID.String())
	})
	return out
}

// StatusHandler answers GET /api/v1/crew (admin).
func (h *Hub) StatusHandler() http.Handler {
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		org, err := httpx.Organization(r)
		if err != nil {
			return err
		}
		st, err := h.Status(r.Context(), org)
		if err != nil {
			return err
		}
		return httpx.JSON(w, http.StatusOK, st)
	})
}

// Start listens to the other processes' signals and keeps this process's
// connections alive (reaping dead processes') until ctx ends or Close.
func (h *Hub) Start(ctx context.Context) {
	ctx, h.stop = context.WithCancel(ctx)
	h.done.Add(4)
	go func() {
		defer h.done.Done()
		// Signals sent while not listening are lost: every (re)connect
		// wakes every local connection (they find what was queued).
		h.presence.Listen(ctx, h.wakeAll, h.signal)
	}()
	go func() {
		defer h.done.Done()
		h.sender(ctx)
	}()
	// Touching never waits behind reaping, so a slow reap can't age this
	// process's own rows toward the stale limit.
	go func() {
		defer h.done.Done()
		every(ctx, h.touch, h.keepAlive)
	}()
	go func() {
		defer h.done.Done()
		every(ctx, h.touch, h.reap)
	}()
}

func every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// Close closes this process's connections (they release their
// deliveries, so other processes take them at once) and stops the hub.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closing = true
	for c := range h.conns {
		c.cancel()
	}
	for o := range h.opening {
		o.cancel()
	}
	h.mu.Unlock()
	served := make(chan struct{})
	go func() { h.serving.Wait(); close(served) }()
	select {
	case <-served:
	case <-time.After(closeWait):
		slog.Warn("crew connections still closing")
	}
	if h.stop != nil {
		h.stop()
		h.done.Wait()
	}
}

// keepAlive touches this process's connections and closes those it must
// (reaped, or revoked elsewhere).
func (h *Hub) keepAlive(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, h.touch)
	defer cancel()
	h.mu.Lock()
	byID := map[string]context.CancelFunc{}
	for c := range h.conns {
		byID[c.session] = c.cancel
	}
	for o := range h.opening {
		byID[o.session] = o.cancel
	}
	h.mu.Unlock()
	sessions := make([]string, 0, len(byID))
	for s := range byID {
		sessions = append(sessions, s)
	}
	closing, err := h.presence.Touch(opCtx, h.process, sessions)
	if err != nil {
		slog.Error("touch crew connections", "err", err)
		return
	}
	for _, s := range closing {
		if cancel, ok := byID[s]; ok {
			slog.Info("closing crew connection (revoked or reaped)", "session", s)
			cancel()
		}
	}
}

// reap forgets dead processes' connections and requeues what they held.
func (h *Hub) reap(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	reaped, err := h.presence.Reap(opCtx, staleTouches*h.touch)
	if err != nil {
		slog.Error("reap crew connections", "err", err)
		return
	}
	for _, r := range reaped {
		slog.Info("reaped crew connection of a stopped process", "organization", r.Organization, "session", r.Session)
		h.unreleased[r] = struct{}{}
	}
	changed := map[identity.OrganizationID]bool{}
	for r := range h.unreleased {
		// Requeues and wakes every process; a failure is retried on the
		// next round (and the lease ends it at worst).
		if _, err := h.commands().ReleaseSession(opCtx, r.Organization, r.Session); err != nil {
			slog.Error("release deliveries of reaped connection", "session", r.Session, "err", err)
			continue
		}
		delete(h.unreleased, r)
		changed[r.Organization] = true
	}
	for org := range changed {
		h.changed(org)
	}
}

// wakeAll wakes every local connection.
func (h *Hub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.poke()
	}
}

// signal acts on another process's signal.
func (h *Hub) signal(s crew.Signal) {
	if s.From == h.process {
		return // acted locally already
	}
	switch s.Kind {
	case crew.SignalWake:
		h.wakeLocal(s.Organization)
	case crew.SignalRevoke:
		h.disconnectLocal(s.Organization, s.Token)
	}
}

// send queues a signal for the other processes (coalesced: one wake per
// organization is enough).
func (h *Hub) send(s crew.Signal) {
	s.From = h.process
	h.outboxMu.Lock()
	h.outbox[s] = struct{}{}
	h.outboxMu.Unlock()
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// sender sends queued signals until ctx ends. A lost revoke is caught by
// the next touch; a lost wake by the claim tick.
func (h *Hub) sender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
		}
		h.outboxMu.Lock()
		batch := h.outbox
		h.outbox = map[crew.Signal]struct{}{}
		h.outboxMu.Unlock()
		for s := range batch {
			sctx, cancel := context.WithTimeout(ctx, opTimeout)
			if err := h.presence.Signal(sctx, s); err != nil {
				slog.Error("signal crew processes", "kind", s.Kind, "err", err)
			}
			cancel()
		}
	}
}

var _ crew.Disconnector = (*Hub)(nil)

// Disconnect closes the organization's connections opened with the
// token, in every process (the touch backstops a lost signal).
func (h *Hub) Disconnect(org identity.OrganizationID, token identity.CrewTokenID) {
	h.disconnectLocal(org, token)
	h.send(crew.Signal{Kind: crew.SignalRevoke, Organization: org, Token: token})
}

func (h *Hub) disconnectLocal(org identity.OrganizationID, token identity.CrewTokenID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.organization == org && c.credential != "" && c.credential == token.String() {
			slog.Info("crew token revoked: closing connection", "organization", org, "agent", c.agent)
			c.cancel()
		}
	}
	for o := range h.opening {
		if o.organization == org && o.credential != "" && o.credential == token.String() {
			o.cancel()
		}
	}
}

// changed announces an organization's new crew state.
func (h *Hub) changed(org identity.OrganizationID) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	st, err := h.Status(ctx, org)
	if err != nil {
		slog.Error("crew status", "err", err)
		return
	}
	h.events.Publish(org, crew.EventChanged, st)
}

// working records a connection's deliveries (shown in the status; what
// to release is on the ships themselves) and announces the change.
func (h *Hub) working(c *conn) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	c.recording.Lock()
	err := h.presence.SetDeliveries(ctx, c.organization, c.session, c.deliveryList())
	c.recording.Unlock()
	if err != nil {
		slog.Error("record crew deliveries", "session", c.session, "err", err)
	}
	h.changed(c.organization)
}

func newSession() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var _ ship.Notifier = (*Hub)(nil)

// Bind sets the ship commands (the hub is built before the ship service).
func (h *Hub) Bind(ships ship.Commands) {
	h.mu.Lock()
	h.ships = ships
	for c := range h.conns {
		c.poke()
	}
	h.mu.Unlock()
}

// Notify wakes an organization's connections, in every process, so they
// try to claim.
func (h *Hub) Notify(org identity.OrganizationID) {
	h.wakeLocal(org)
	h.send(crew.Signal{Kind: crew.SignalWake, Organization: org})
}

func (h *Hub) wakeLocal(org identity.OrganizationID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.organization != org {
			continue
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) commands() ship.Commands {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ships
}

// ServeHTTP upgrades /crew/ws. Auth is done by the router (crew
// principal): the connection belongs to the principal's organization.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.commands() == nil {
		httpx.WriteError(w, r, errx.Internal("hub not ready"))
		return
	}
	p, err := httpx.Caller(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	org := p.Organization
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		httpx.WriteError(w, r, errx.External("the port is shutting down; reconnect"))
		return
	}
	h.serving.Add(1)
	h.mu.Unlock()
	defer h.serving.Done()
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	// Recorded before the upgrade: the quota counts open and opening
	// connections of every process.
	session := newSession()
	connectedAt := time.Now().UTC()
	if err := h.presence.Open(r.Context(), org, crew.Connection{Session: session, Process: h.process,
		Credential: p.Credential, Remote: r.RemoteAddr, ConnectedAt: connectedAt}, h.maxConns); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dropped := false // from then on drop owns the row (it may keep it for the reaper)
	defer func() {
		if dropped {
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), opTimeout)
		defer ccancel()
		if err := h.presence.Close(cctx, org, session); err != nil {
			slog.Error("forget crew connection", "session", session, "err", err)
		}
	}()
	// Tracked from here, so a revoke during the handshake closes it too.
	open := &pending{organization: org, credential: p.Credential, session: session, cancel: cancel}
	h.mu.Lock()
	if h.closing { // Close swept while Open ran
		h.mu.Unlock()
		httpx.WriteError(w, r, errx.External("the port is shutting down; reconnect"))
		return
	}
	h.opening[open] = struct{}{}
	h.mu.Unlock()
	opened := false
	defer func() {
		if !opened {
			h.mu.Lock()
			delete(h.opening, open)
			h.mu.Unlock()
		}
	}()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already answered
	}
	ws.SetReadLimit(crew.MaxFrame)
	if !p.Expires.IsZero() {
		// An expiring token's connection ends with it; the connector
		// reconnects and is refused.
		stop := time.AfterFunc(time.Until(p.Expires), cancel)
		defer stop.Stop()
	}

	hello, err := readHello(ctx, ws)
	if err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	c := &conn{organization: org, credential: p.Credential, cancel: cancel, ws: ws, session: session, remote: r.RemoteAddr, connectedAt: connectedAt,
		agent: hello.Agent, concurrency: hello.EffectiveConcurrency(), steer: hello.Steer,
		wake: make(chan struct{}, 1), send: make(chan crew.Frame, 8), inFlight: map[identity.DeliveryID]*tracked{}}

	// From opening to conns under one lock: a revoke sees it in one of them.
	h.mu.Lock()
	delete(h.opening, open)
	opened = true
	if ctx.Err() != nil || h.closing { // revoked, expired or shutting down during the handshake
		h.mu.Unlock()
		_ = ws.Close(websocket.StatusPolicyViolation, "the crew token is no longer valid")
		return
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	dropped = true
	defer h.drop(c)
	if err := h.presence.Ready(ctx, org, session, c.agentInfo()); err != nil {
		slog.Error("record crew connection", "session", session, "err", err)
		_ = ws.Close(websocket.StatusInternalError, "try again")
		return
	}
	slog.Info("crew connected", "organization", org, "agent", c.agent, "concurrency", c.concurrency, "steer", c.steer, "remote", r.RemoteAddr)
	h.changed(org)

	go c.writer(ctx, cancel)
	c.send <- crew.Frame{Type: crew.FrameWelcome, MCPURL: h.mcpURL}
	go h.claimer(ctx, c)
	go h.leaseKeeper(ctx, c)
	h.reader(ctx, c)
}

func readHello(ctx context.Context, ws *websocket.Conn) (crew.Hello, error) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, raw, err := ws.Read(rctx)
	if err != nil {
		return crew.Hello{}, errx.Validation("expected hello")
	}
	f, err := crew.Decode(raw)
	if err != nil || f.Type != crew.FrameHello {
		return crew.Hello{}, errx.Validation("first frame must be hello")
	}
	hello := crew.Hello{Agent: f.Agent, Concurrency: f.Concurrency, Steer: f.Steer}
	if err := hello.Validate(); err != nil {
		return crew.Hello{}, err
	}
	return hello, nil
}

func (c *conn) writer(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	ping := time.NewTicker(crew.PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-c.send:
			raw, _ := json.Marshal(f)
			wctx, done := context.WithTimeout(ctx, opTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, raw)
			done()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, opTimeout)
			err := c.ws.Ping(pctx)
			done()
			if err != nil {
				return
			}
		}
	}
}

func (h *Hub) reader(ctx context.Context, c *conn) {
	for {
		_, raw, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		f, err := crew.Decode(raw)
		if err != nil {
			c.push(ctx, crew.Frame{Type: crew.FrameError, Message: err.Error()})
			continue
		}
		if err := h.apply(ctx, c, f); err != nil {
			slog.Warn("crew frame failed", "agent", c.agent, "type", f.Type, "err", err)
			c.push(ctx, crew.Frame{Type: crew.FrameError, Message: publicMessage(err)})
		}
	}
}

func (h *Hub) apply(ctx context.Context, c *conn, f crew.Frame) error {
	ships := h.commands()
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	switch f.Type {
	case crew.FrameAck, crew.FrameRelease:
		if f.DeliveryID == nil || !c.owns(*f.DeliveryID) {
			return errx.NotFound("delivery not found on this connection")
		}
		var err error
		if f.Type == crew.FrameAck {
			handled := f.ShipIDs
			if len(handled) == 0 {
				if c.steer {
					// Joined ships may be pushed after the client's turn
					// ended; only it knows which ones the agent saw.
					return errx.InvalidField("ship_ids", "steering connectors must name the handled ship_ids")
				}
				handled = c.shipsOf(*f.DeliveryID)
			}
			err = ships.Ack(opCtx, c.organization, ship.Ack{Delivery: *f.DeliveryID, Ships: handled})
		} else {
			err = ships.Release(opCtx, c.organization, *f.DeliveryID)
		}
		c.forget(*f.DeliveryID)
		h.working(c)
		c.poke()
		return err
	case crew.FrameReply:
		if f.ConversationID == nil {
			return errx.InvalidField("conversation_id", "conversation_id is required")
		}
		_, err := ships.Send(opCtx, c.organization, ship.Outbound{Conversation: *f.ConversationID, Text: f.Text})
		return err
	default:
		return errx.Validation("unexpected frame type " + string(f.Type))
	}
}

// claimer claims deliveries while the connection has room, each time it is
// woken (new ships, an ack, a release) and every lease period as a fallback.
// For a steering connection it first joins new ships of its conversations
// in flight to their deliveries.
func (h *Hub) claimer(ctx context.Context, c *conn) {
	tick := time.NewTicker(h.lease / 4)
	defer tick.Stop()
	c.poke()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-tick.C:
		}
		if c.steer && !h.joinAll(ctx, c) {
			return
		}
		for c.room() {
			d, ok, err := h.claim(ctx, c)
			if err != nil {
				slog.Error("claim delivery", "agent", c.agent, "err", err)
				break
			}
			if !ok {
				break
			}
			c.track(d)
			h.working(c)
			if !c.push(ctx, crew.Frame{Type: crew.FrameDelivery, Delivery: &d}) {
				return
			}
		}
	}
}

// joinAll pushes a steer frame for every in-flight delivery that got new
// ships. false means the connection is gone.
func (h *Hub) joinAll(ctx context.Context, c *conn) bool {
	for _, id := range c.deliveries() {
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		d, ok, err := h.commands().Join(opCtx, c.organization, ship.Join{Delivery: id, Lease: h.lease})
		cancel()
		if err != nil {
			slog.Error("join delivery", "agent", c.agent, "delivery", id, "err", err)
			continue
		}
		if !ok {
			continue
		}
		if !c.extendTracked(id, d.Ships) {
			// Acked meanwhile. Nothing is orphaned: a join that committed
			// before the ack had its ships requeued by it (the ack only
			// names ships the client was sent), and one after it found no
			// claimed ships. The steer is just dropped.
			continue
		}
		h.working(c)
		if !c.push(ctx, crew.Frame{Type: crew.FrameSteer, Delivery: &d}) {
			return false
		}
	}
	return true
}

func (h *Hub) claim(ctx context.Context, c *conn) (ship.Delivery, bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return h.commands().Claim(opCtx, c.organization, ship.Claim{Owner: c.agent, Session: c.session, Lease: h.lease})
}

// leaseKeeper extends the leases of a live connection's deliveries, so a
// long agent turn is not handed to someone else.
func (h *Hub) leaseKeeper(ctx context.Context, c *conn) {
	tick := time.NewTicker(h.lease / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, id := range c.deliveries() {
				opCtx, cancel := context.WithTimeout(ctx, opTimeout)
				if err := h.commands().Extend(opCtx, c.organization, id, h.lease); err != nil {
					slog.Warn("extend delivery", "delivery", id, "err", err)
				}
				cancel()
			}
		}
	}
}

// drop releases everything a closed connection still holds.
func (h *Hub) drop(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, id := range c.deliveries() {
		if err := h.commands().Release(ctx, c.organization, id); err != nil && !errx.IsType(err, errx.TypeNotFound) {
			slog.Error("release delivery of closed connection", "delivery", id, "err", err)
		}
	}
	// And any claim it never tracked (committed as the connection closed).
	if _, err := h.commands().ReleaseSession(ctx, c.organization, c.session); err != nil {
		// Its row stays: the reaper retries once the touches stop.
		slog.Error("release deliveries of closed connection", "session", c.session, "err", err)
		h.changed(c.organization)
		return
	}
	if err := h.presence.Close(ctx, c.organization, c.session); err != nil {
		slog.Error("forget crew connection", "session", c.session, "err", err)
	}
	slog.Info("crew disconnected", "organization", c.organization, "agent", c.agent)
	h.changed(c.organization)
}

func (c *conn) push(ctx context.Context, f crew.Frame) bool {
	select {
	case c.send <- f:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *conn) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) room() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inFlight) < c.concurrency
}

func (c *conn) track(d ship.Delivery) {
	c.mu.Lock()
	c.inFlight[d.ID] = &tracked{conversation: d.Conversation.ID, ships: shipIDs(nil, d.Ships), since: time.Now().UTC()}
	c.mu.Unlock()
}

// extendTracked records joined ships; false when the delivery is no longer
// open on this connection.
func (c *conn) extendTracked(id identity.DeliveryID, ships []ship.Ship) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.inFlight[id]
	if ok {
		t.ships = shipIDs(t.ships, ships)
	}
	return ok
}

func (c *conn) shipsOf(id identity.DeliveryID) []identity.ShipID {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.inFlight[id]
	if !ok {
		return nil
	}
	return append([]identity.ShipID(nil), t.ships...)
}

func shipIDs(into []identity.ShipID, ships []ship.Ship) []identity.ShipID {
	for _, s := range ships {
		into = append(into, s.ID)
	}
	return into
}

func (c *conn) forget(id identity.DeliveryID) {
	c.mu.Lock()
	delete(c.inFlight, id)
	c.mu.Unlock()
}

func (c *conn) owns(id identity.DeliveryID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.inFlight[id]
	return ok
}

func (c *conn) deliveries() []identity.DeliveryID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]identity.DeliveryID, 0, len(c.inFlight))
	for id := range c.inFlight {
		out = append(out, id)
	}
	return out
}

func publicMessage(err error) string {
	var e *errx.Error
	if errx.As(err, &e) && e.HTTPStatus < 500 {
		return e.Message
	}
	return errx.GenericMessage
}
