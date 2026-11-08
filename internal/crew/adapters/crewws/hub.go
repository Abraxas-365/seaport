// Package crewws is the WebSocket hub agents (connectors) dial into. It
// claims deliveries for each connection up to its concurrency, pushes them,
// and turns acks/releases/disconnects into ship commands. It is the ship
// domain's Notifier.
package crewws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
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

type Hub struct {
	lease  time.Duration
	mcpURL string
	events event.Publisher
	// maxConns bounds each organization's live connections (≤ 0: unlimited).
	maxConns int

	mu    sync.Mutex
	ships ship.Commands
	conns map[*conn]struct{}
	// opening are connections still in their hello handshake (not yet in
	// conns): a revoke must close them too.
	opening map[*pending]struct{}
}

// pending is a connection before its hello: enough for Disconnect.
type pending struct {
	organization identity.OrganizationID
	credential   string
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
}

// tracked is an open delivery on a connection. ships are the ships pushed
// for it (the delivery plus every steer), the ack default for clients
// that don't name ships.
type tracked struct {
	conversation identity.ConversationID
	ships        []identity.ShipID
	since        time.Time
}

func New(lease time.Duration, mcpURL string, events event.Publisher, maxConns int) *Hub {
	if events == nil {
		events = event.Nop{}
	}
	return &Hub{lease: lease, mcpURL: mcpURL, events: events, maxConns: maxConns,
		conns: map[*conn]struct{}{}, opening: map[*pending]struct{}{}}
}

// Status lists an organization's connected agents and their deliveries in
// flight.
func (h *Hub) Status(org identity.OrganizationID) crew.Status {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		if c.organization == org {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	out := crew.Status{Agents: make([]crew.Agent, 0, len(conns))}
	for _, c := range conns {
		out.Agents = append(out.Agents, c.status())
	}
	slices.SortFunc(out.Agents, func(a, b crew.Agent) int {
		if c := a.ConnectedAt.Compare(b.ConnectedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Session, b.Session)
	})
	return out
}

func (c *conn) status() crew.Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := crew.Agent{Session: c.session, Name: c.agent, Concurrency: c.concurrency, Steer: c.steer,
		Remote: c.remote, ConnectedAt: c.connectedAt, Deliveries: make([]crew.AgentDelivery, 0, len(c.inFlight))}
	for id, t := range c.inFlight {
		a.Deliveries = append(a.Deliveries, crew.AgentDelivery{ID: id, Conversation: t.conversation, Ships: len(t.ships), Since: t.since})
	}
	slices.SortFunc(a.Deliveries, func(x, y crew.AgentDelivery) int { return x.Since.Compare(y.Since) })
	return a
}

// StatusHandler answers GET /api/v1/crew (admin).
func (h *Hub) StatusHandler() http.Handler {
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		org, err := httpx.Organization(r)
		if err != nil {
			return err
		}
		return httpx.JSON(w, http.StatusOK, h.Status(org))
	})
}

var _ crew.Disconnector = (*Hub)(nil)

// Disconnect closes the organization's connections opened with the token.
func (h *Hub) Disconnect(org identity.OrganizationID, token identity.CrewTokenID) {
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
	h.events.Publish(org, crew.EventChanged, h.Status(org))
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

// Notify wakes an organization's connections so they try to claim.
func (h *Hub) Notify(org identity.OrganizationID) {
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

// countLocked counts an organization's open and opening connections.
func (h *Hub) countLocked(org identity.OrganizationID) int {
	n := 0
	for c := range h.conns {
		if c.organization == org {
			n++
		}
	}
	for o := range h.opening {
		if o.organization == org {
			n++
		}
	}
	return n
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
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	// Tracked from here, so a revoke during the handshake closes it too;
	// the quota counts open and opening connections under the same lock.
	open := &pending{organization: org, credential: p.Credential, cancel: cancel}
	h.mu.Lock()
	if h.maxConns > 0 && h.countLocked(org) >= h.maxConns {
		h.mu.Unlock()
		httpx.WriteError(w, r, errx.QuotaExceeded("crew_connections", h.maxConns, "this organization already has "+
			strconv.Itoa(h.maxConns)+" agents connected; disconnect one first"))
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
	c := &conn{organization: org, credential: p.Credential, cancel: cancel, ws: ws, session: newSession(), remote: r.RemoteAddr, connectedAt: time.Now().UTC(),
		agent: hello.Agent, concurrency: hello.EffectiveConcurrency(), steer: hello.Steer,
		wake: make(chan struct{}, 1), send: make(chan crew.Frame, 8), inFlight: map[identity.DeliveryID]*tracked{}}

	// From opening to conns under one lock: a revoke sees it in one of them.
	h.mu.Lock()
	delete(h.opening, open)
	opened = true
	if ctx.Err() != nil { // revoked or expired during the handshake
		h.mu.Unlock()
		_ = ws.Close(websocket.StatusPolicyViolation, "the crew token is no longer valid")
		return
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	slog.Info("crew connected", "organization", org, "agent", c.agent, "concurrency", c.concurrency, "steer", c.steer, "remote", r.RemoteAddr)
	h.changed(org)
	defer h.drop(c)

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
		h.changed(c.organization)
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
			h.changed(c.organization)
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
		h.changed(c.organization)
		if !c.push(ctx, crew.Frame{Type: crew.FrameSteer, Delivery: &d}) {
			return false
		}
	}
	return true
}

func (h *Hub) claim(ctx context.Context, c *conn) (ship.Delivery, bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return h.commands().Claim(opCtx, c.organization, ship.Claim{Owner: c.agent, Lease: h.lease})
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
