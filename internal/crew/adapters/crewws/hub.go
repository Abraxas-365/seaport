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
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/puerto/internal/crew"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/httpx"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/coder/websocket"
)

// opTimeout bounds one ship command run for a connection.
const opTimeout = 15 * time.Second

type Hub struct {
	lease  time.Duration
	mcpURL string
	events event.Publisher

	mu    sync.Mutex
	ships ship.Commands
	conns map[*conn]struct{}
}

type conn struct {
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

func New(lease time.Duration, mcpURL string, events event.Publisher) *Hub {
	if events == nil {
		events = event.Nop{}
	}
	return &Hub{lease: lease, mcpURL: mcpURL, events: events, conns: map[*conn]struct{}{}}
}

// Status lists the connected agents and their deliveries in flight.
func (h *Hub) Status() crew.Status {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
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
		return httpx.JSON(w, http.StatusOK, h.Status())
	})
}

// changed announces the crew's new state.
func (h *Hub) changed() { h.events.Publish(crew.EventChanged, h.Status()) }

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
	h.mu.Unlock()
	h.Notify()
}

// Notify wakes every connection so it tries to claim.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// Connected reports how many connectors are attached.
func (h *Hub) Connected() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

func (h *Hub) commands() ship.Commands {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ships
}

// ServeHTTP upgrades /crew/ws. Auth is done by the router (crew token).
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.commands() == nil {
		httpx.WriteError(w, r, errx.Internal("hub not ready"))
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already answered
	}
	ws.SetReadLimit(crew.MaxFrame)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()

	hello, err := readHello(ctx, ws)
	if err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	c := &conn{ws: ws, session: newSession(), remote: r.RemoteAddr, connectedAt: time.Now().UTC(),
		agent: hello.Agent, concurrency: hello.EffectiveConcurrency(), steer: hello.Steer,
		wake: make(chan struct{}, 1), send: make(chan crew.Frame, 8), inFlight: map[identity.DeliveryID]*tracked{}}
	slog.Info("crew connected", "agent", c.agent, "concurrency", c.concurrency, "steer", c.steer, "remote", r.RemoteAddr)

	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	h.changed()
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
			err = ships.Ack(opCtx, ship.Ack{Delivery: *f.DeliveryID, Ships: handled})
		} else {
			err = ships.Release(opCtx, *f.DeliveryID)
		}
		c.forget(*f.DeliveryID)
		h.changed()
		c.poke()
		return err
	case crew.FrameReply:
		if f.ConversationID == nil {
			return errx.InvalidField("conversation_id", "conversation_id is required")
		}
		_, err := ships.Send(opCtx, ship.Outbound{Conversation: *f.ConversationID, Text: f.Text})
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
			h.changed()
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
		d, ok, err := h.commands().Join(opCtx, ship.Join{Delivery: id, Lease: h.lease})
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
		h.changed()
		if !c.push(ctx, crew.Frame{Type: crew.FrameSteer, Delivery: &d}) {
			return false
		}
	}
	return true
}

func (h *Hub) claim(ctx context.Context, c *conn) (ship.Delivery, bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return h.commands().Claim(opCtx, ship.Claim{Owner: c.agent, Lease: h.lease})
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
				if err := h.commands().Extend(opCtx, id, h.lease); err != nil {
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
		if err := h.commands().Release(ctx, id); err != nil && !errx.IsType(err, errx.TypeNotFound) {
			slog.Error("release delivery of closed connection", "delivery", id, "err", err)
		}
	}
	slog.Info("crew disconnected", "agent", c.agent)
	h.changed()
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
