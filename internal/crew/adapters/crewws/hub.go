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
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
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
	events event.Stream
	// maxConns bounds each organization's live connections, across
	// processes (≤ 0: unlimited).
	maxConns int
	presence crew.Presence
	// process names this process in crew.Presence and its signals.
	process string
	touch   time.Duration

	mu    sync.Mutex
	ships ship.Commands
	// agents resolves the agent an open-mode connection names (hello.as).
	agents agent.Queries
	conns  map[*conn]struct{}
	// opening are connections still in their hello handshake (not yet in
	// conns): a revoke must close them too.
	opening map[*pending]struct{}
	closing bool

	serving sync.WaitGroup // upgraded connections (and their upgrade) in progress
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

// pending is a connection before its hello: enough for Disconnect and
// DisconnectAgent (agent is zero until the hello in open mode).
type pending struct {
	organization identity.OrganizationID
	credential   string
	agent        identity.AgentID
	session      string
	cancel       context.CancelFunc
}

type conn struct {
	// organization owns the connection (its crew token): it only claims,
	// acks and replies within it.
	organization identity.OrganizationID
	// credential is the crew token it connected with ("" in open mode);
	// revoking the token closes the connection.
	credential string
	// agentID/agentName: the library agent it works as. It claims only
	// ships routed to it; deleting the agent closes the connection.
	agentID     identity.AgentID
	agentName   string
	cancel      context.CancelFunc
	ws          *wsConn
	session     string
	remote      string
	connectedAt time.Time
	agent       string
	concurrency int
	steer       bool
	profile     int
	wake        chan struct{}
	send        chan crew.Frame

	mu sync.Mutex
	// draining: the connector asked for no new deliveries (it restarts
	// its harness once idle).
	draining bool
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
	// turn gathers the agent's tool calls (activity frames) for the turn
	// summary stored when the delivery ends.
	turn turnLog
}

// New builds the hub of process (its name among the processes serving the
// database); a delivery lease is lease, and connections are touched every
// lease/4 (at most maxTouch).
func New(process string, lease time.Duration, mcpURL string, events event.Stream, maxConns int, presence crew.Presence) *Hub {
	if events == nil {
		events = event.Nop{}
	}
	return &Hub{lease: lease, mcpURL: mcpURL, events: events, maxConns: maxConns, presence: presence,
		process: process, touch: min(lease/4, maxTouch),
		conns: map[*conn]struct{}{}, opening: map[*pending]struct{}{},
		outbox: map[crew.Signal]struct{}{}, kick: make(chan struct{}, 1), unreleased: map[crew.Reaped]struct{}{}}
}

// Status lists an organization's connected agents (of every process) and
// their deliveries in flight.
func (h *Hub) Status(ctx context.Context, org identity.OrganizationID) (crew.Status, error) {
	return h.presence.Status(ctx, org)
}

func (c *conn) agentInfo() crew.Agent {
	return crew.Agent{Session: c.session, AgentID: c.agentID, AgentName: c.agentName, Name: c.agent,
		Concurrency: c.concurrency, Steer: c.steer, ProfileVersion: c.profile}
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
		h.wakeLocal(s.Organization, s.Agent)
	case crew.SignalRevoke:
		h.disconnectLocal(s.Organization, s.Token)
	case crew.SignalAgentGone:
		h.disconnectAgentLocal(s.Organization, s.Agent)
	case crew.SignalProfile:
		h.profileLocal(s.Organization, s.Agent)
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

var _ agent.Revoker = (*Hub)(nil)

// DisconnectAgent closes the organization's connections working as the
// agent, in every process (the touch backstops a lost signal).
func (h *Hub) DisconnectAgent(org identity.OrganizationID, id identity.AgentID) {
	h.disconnectAgentLocal(org, id)
	h.send(crew.Signal{Kind: crew.SignalAgentGone, Organization: org, Agent: id})
}

func (h *Hub) disconnectAgentLocal(org identity.OrganizationID, id identity.AgentID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.organization == org && c.agentID == id {
			slog.Info("agent deleted: closing connection", "organization", org, "agent", c.agentName)
			c.cancel()
		}
	}
	for o := range h.opening {
		if o.organization == org && o.agent == id {
			o.cancel()
		}
	}
}

// ProfileChanged tells the organization's connections working as the
// agent, in every process, that its kit changed (a profile frame: they
// fetch it again), and announces the crew (their version is outdated).
func (h *Hub) ProfileChanged(org identity.OrganizationID, id identity.AgentID) {
	h.profileLocal(org, id)
	h.send(crew.Signal{Kind: crew.SignalProfile, Organization: org, Agent: id})
	go h.changed(org)
}

func (h *Hub) profileLocal(org identity.OrganizationID, id identity.AgentID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.organization == org && c.agentID == id {
			select {
			case c.send <- crew.Frame{Type: crew.FrameProfile}:
			default: // a full queue: the connector also re-checks its kit on reconnect
				slog.Warn("crew connection busy: profile frame dropped", "session", c.session)
			}
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

// Bind sets the ship commands and the agent queries (the hub is built
// before the services that use it).
func (h *Hub) Bind(ships ship.Commands, agents agent.Queries) {
	h.mu.Lock()
	h.ships, h.agents = ships, agents
	for c := range h.conns {
		c.poke()
	}
	h.mu.Unlock()
}

// Notify wakes an organization's connections of the agent (zero: of
// every agent), in every process, so they try to claim.
func (h *Hub) Notify(org identity.OrganizationID, id identity.AgentID) {
	h.wakeLocal(org, id)
	h.send(crew.Signal{Kind: crew.SignalWake, Organization: org, Agent: id})
}

func (h *Hub) wakeLocal(org identity.OrganizationID, id identity.AgentID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.organization != org || (!id.IsZero() && c.agentID != id) {
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

// resolveAgent is the agent a connection works as: its token's (hello.as,
// when given, must name it), or in open mode the one hello.as names.
func (h *Hub) resolveAgent(ctx context.Context, p httpx.Principal, hello crew.Hello) (identity.AgentID, string, error) {
	h.mu.Lock()
	agents := h.agents
	h.mu.Unlock()
	return crew.ResolveAgent(ctx, agents, crew.Caller{Organization: p.Organization, Agent: p.Agent, AgentName: p.AgentName}, hello.As)
}

// admitted is a connection from its quota row to its end: everything the
// upgrade needs, and what finish gives back.
type admitted struct {
	p           httpx.Principal
	session     string
	connectedAt time.Time
	remote      string
	ctx         context.Context
	cancel      context.CancelFunc
	open        *pending
	// opened: the connection left h.opening (it is in h.conns, or gone).
	opened bool
	// dropped: drop owns the presence row from here (it may keep it for
	// the reaper).
	dropped bool
	// started: run began (it owns the cleanup from then on); finished: finish
	// ran (it runs once). Both are guarded by lifecycle.
	lifecycle sync.Mutex
	started   bool
	finished  bool
}

// start marks run as begun. It reports false when finish already ran (the
// watchdog gave up on the hijack), so run must not touch anything.
func (a *admitted) start() bool {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if a.finished {
		return false
	}
	a.started = true
	return true
}

// finishOnce marks the cleanup done and reports whether the caller must run
// it (the first caller only).
func (a *admitted) finishOnce() bool {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if a.finished {
		return false
	}
	a.finished = true
	return true
}

// abandon is finishOnce for the watchdog: only while run hasn't started
// (decided under the same lock as start, so run and the watchdog never both
// own the admission).
func (a *admitted) abandon() bool {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if a.started || a.finished {
		return false
	}
	a.finished = true
	return true
}

// hijackGrace is how long after Handle returns the hijacked connection's
// handler may take to start. fasthttp calls it right after the response is
// written; if it never does (the write failed), the watchdog cleans up.
var hijackGrace = 10 * time.Second

// watch ends an admission whose run never started.
func (h *Hub) watch(a *admitted) *time.Timer {
	return time.AfterFunc(hijackGrace, func() {
		if a.abandon() {
			slog.Warn("crew connection never started after its upgrade", "session", a.session)
			h.release(a)
		}
	})
}

// finish releases what admit took, whatever the connection's end.
// It is idempotent: run, the error paths of Handle and the watchdog of an
// upgrade whose hijack never ran may all call it.
func (h *Hub) finish(a *admitted) {
	if a.finishOnce() {
		h.release(a)
	}
}

// release is finish's work, for whoever won finishOnce or abandon.
func (h *Hub) release(a *admitted) {
	if !a.opened {
		h.mu.Lock()
		delete(h.opening, a.open)
		h.mu.Unlock()
	}
	if !a.dropped {
		cctx, ccancel := context.WithTimeout(context.Background(), opTimeout)
		defer ccancel()
		if err := h.presence.Close(cctx, a.p.Organization, a.session); err != nil {
			slog.Error("forget crew connection", "session", a.session, "err", err)
		}
	}
	a.cancel()
	h.serving.Done()
}

// sameOrigin is coder/websocket's default check (authenticateOrigin with no
// patterns), which main used: no Origin is fine (not a browser); otherwise
// its host must equal the request's Host, case-insensitively.
func sameOrigin(origin, host string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, u.Host)
}

// Handle upgrades /crew/ws. Auth is done by the router (crew principal):
// the connection belongs to the principal's organization. Everything that
// can refuse (hub not ready, shutting down, the organization's connection
// quota) happens before the upgrade, as an ordinary error response; after
// it the hijacked connection is served by run, which owns the cleanup.
func (h *Hub) Handle(c fiber.Ctx) error {
	if h.commands() == nil {
		return errx.Internal("hub not ready")
	}
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	// A browser always sends its Origin: one from another site must not
	// drive an agent connection (the connector sends none).
	if !sameOrigin(c.Get(fiber.HeaderOrigin), string(c.RequestCtx().Host())) {
		return errx.Forbidden("the WebSocket origin is not allowed")
	}
	org := p.Organization
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return errx.External("the port is shutting down; reconnect")
	}
	h.serving.Add(1)
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Context()))
	// Recorded before the upgrade: the quota counts open and opening
	// connections of every process.
	a := &admitted{p: p, session: newSession(), connectedAt: time.Now().UTC(), remote: c.RequestCtx().RemoteAddr().String(),
		ctx: ctx, cancel: cancel, dropped: true}
	if err := h.presence.Open(c.Context(), org, crew.Connection{Session: a.session, Process: h.process,
		Credential: p.Credential, Remote: a.remote, ConnectedAt: a.connectedAt}, h.maxConns); err != nil {
		h.finish(a)
		return err
	}
	a.dropped = false
	// Tracked from here, so a revoke during the handshake closes it too.
	a.open = &pending{organization: org, credential: p.Credential, agent: p.Agent, session: a.session, cancel: cancel}
	h.mu.Lock()
	if h.closing { // Close swept while Open ran
		h.mu.Unlock()
		a.opened = true
		h.finish(a)
		return errx.External("the port is shutting down; reconnect")
	}
	h.opening[a.open] = struct{}{}
	h.mu.Unlock()
	// The Origin was checked above (sameOrigin); the connector
	// (coder/websocket) sends none.
	upgrade := websocket.New(func(conn *websocket.Conn) { h.run(a, conn) }, websocket.Config{AllowEmptyOrigin: true})
	if err := upgrade(c); err != nil {
		h.finish(a) // refused (not an upgrade request, bad handshake)
		return err
	}
	// The hijacked connection is served by run, which fasthttp starts once
	// this handler returns; if it never does, nobody else would release
	// the row, the registration and the serving count.
	h.watch(a)
	return nil
}

// run serves one upgraded connection until it ends. It runs on the
// hijacked connection's goroutine: it must not touch the Fiber context.
func (h *Hub) run(a *admitted, raw *websocket.Conn) {
	defer raw.Close() // the library leaves the socket open when the handler returns
	if !a.start() {
		return // the watchdog already gave this admission up
	}
	defer h.finish(a)
	p, org, ctx, cancel, open := a.p, a.p.Organization, a.ctx, a.cancel, a.open
	ws := newWSConn(raw, crew.MaxFrame, ctx)
	if !p.Expires.IsZero() {
		// An expiring token's connection ends with it; the connector
		// reconnects and is refused.
		stop := time.AfterFunc(time.Until(p.Expires), cancel)
		defer stop.Stop()
	}

	hello, err := readHello(ctx, ws)
	if err != nil {
		ws.closeWith(websocket.ClosePolicyViolation, err.Error())
		return
	}
	actx, acancel := context.WithTimeout(ctx, opTimeout)
	agentID, agentName, err := h.resolveAgent(actx, p, hello)
	acancel()
	if err != nil {
		ws.closeWith(websocket.ClosePolicyViolation, publicMessage(err))
		return
	}
	if p.Agent.IsZero() {
		// Open mode: the agent came from hello.as. Record it on the pending
		// connection, then check it again: a delete that committed before
		// this check fails it, one after finds the connection to close.
		h.mu.Lock()
		open.agent = agentID
		agents := h.agents
		h.mu.Unlock()
		actx, acancel := context.WithTimeout(ctx, opTimeout)
		_, err := agents.Find(actx, org, agentID)
		acancel()
		if err != nil {
			ws.closeWith(websocket.ClosePolicyViolation, publicMessage(err))
			return
		}
	}
	c := &conn{organization: org, credential: p.Credential, cancel: cancel, ws: ws, session: a.session, remote: a.remote, connectedAt: a.connectedAt,
		agentID: agentID, agentName: agentName, agent: hello.Agent, concurrency: hello.EffectiveConcurrency(), steer: hello.Steer, profile: hello.ProfileVersion,
		wake: make(chan struct{}, 1), send: make(chan crew.Frame, 8), inFlight: map[identity.DeliveryID]*tracked{}}

	// From opening to conns under one lock: a revoke sees it in one of them.
	h.mu.Lock()
	delete(h.opening, open)
	a.opened = true
	if ctx.Err() != nil || h.closing { // revoked, expired or shutting down during the handshake
		h.mu.Unlock()
		ws.closeWith(websocket.ClosePolicyViolation, "the crew token is no longer valid")
		return
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	a.dropped = true
	defer h.drop(c)
	if err := h.presence.Ready(ctx, org, a.session, c.agentInfo()); err != nil {
		slog.Error("record crew connection", "session", a.session, "err", err)
		ws.closeWith(websocket.CloseInternalServerErr, "try again")
		return
	}
	slog.Info("crew connected", "organization", org, "agent", c.agentName, "label", c.agent, "concurrency", c.concurrency, "steer", c.steer, "remote", a.remote)
	h.changed(org)

	go c.writer(ctx, cancel)
	c.send <- crew.Frame{Type: crew.FrameWelcome, MCPURL: h.mcpURL}
	go h.claimer(ctx, c)
	go h.leaseKeeper(ctx, c)
	h.reader(ctx, c)
}

func readHello(ctx context.Context, ws *wsConn) (crew.Hello, error) {
	raw, err := ws.read(ctx, 10*time.Second)
	if err != nil {
		return crew.Hello{}, errx.Validation("expected hello")
	}
	f, err := crew.Decode(raw)
	if err != nil || f.Type != crew.FrameHello {
		return crew.Hello{}, errx.Validation("first frame must be hello")
	}
	hello := crew.Hello{Agent: f.Agent, As: f.As, Concurrency: f.Concurrency, Steer: f.Steer, ProfileVersion: f.ProfileVersion}
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
			if err := c.ws.write(raw, opTimeout); err != nil {
				return
			}
		case <-ping.C:
			if !c.ws.ping(opTimeout) {
				return
			}
		}
	}
}

func (h *Hub) reader(ctx context.Context, c *conn) {
	for {
		raw, err := c.ws.read(ctx, 0)
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
		t := c.take(*f.DeliveryID)
		if err == nil {
			outcome := ship.TurnAcked
			if f.Type == crew.FrameRelease {
				outcome = ship.TurnReleased
			}
			h.recordTurn(opCtx, c, *f.DeliveryID, t, outcome)
		}
		h.working(c)
		c.poke()
		return err
	case crew.FrameDrain:
		c.mu.Lock()
		c.draining = true
		c.mu.Unlock()
		return nil
	case crew.FrameReply:
		if f.ConversationID == nil {
			return errx.InvalidField("conversation_id", "conversation_id is required")
		}
		_, err := ships.Send(opCtx, c.organization, ship.Outbound{Conversation: *f.ConversationID, Text: f.Text, Agent: &c.agentID})
		return err
	case crew.FrameActivity:
		if f.DeliveryID == nil {
			return errx.InvalidField("delivery_id", "delivery_id is required")
		}
		now := time.Now().UTC()
		a := crew.Activity{Delivery: *f.DeliveryID, AgentID: c.agentID, AgentName: c.agentName, At: now, Items: f.Items}
		if err := a.Validate(); err != nil {
			return err
		}
		conv, ok := c.noteActivity(*f.DeliveryID, f.Items, now)
		if !ok {
			// The delivery just ended (acked): late activity is dropped.
			return nil
		}
		a.Conversation = conv
		h.events.Flash(c.organization, crew.EventActivity, a)
		return nil
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
	return h.commands().Claim(opCtx, c.organization, ship.Claim{Agent: c.agentID, Owner: c.agentName, Session: c.session, Lease: h.lease})
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
	c.ws.closeWith(websocket.CloseNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, id := range c.deliveries() {
		err := h.commands().Release(ctx, c.organization, id)
		if err != nil && !errx.IsType(err, errx.TypeNotFound) {
			slog.Error("release delivery of closed connection", "delivery", id, "err", err)
		}
		if err == nil {
			h.recordTurn(ctx, c, id, c.take(id), ship.TurnDropped)
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
	return !c.draining && len(c.inFlight) < c.concurrency
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

// take forgets an open delivery and hands back what was tracked for it
// (nil if it wasn't open).
func (c *conn) take(id identity.DeliveryID) *tracked {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.inFlight[id]
	delete(c.inFlight, id)
	return t
}

func (c *conn) owns(id identity.DeliveryID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.inFlight[id]
	return ok
}

// noteActivity adds a frame's tool calls to an open delivery's turn and
// says its conversation; false when the delivery isn't open here.
func (c *conn) noteActivity(id identity.DeliveryID, items []crew.ActivityItem, now time.Time) (identity.ConversationID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.inFlight[id]
	if !ok {
		return identity.ConversationID{}, false
	}
	t.turn.note(items, now)
	return t.conversation, true
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
