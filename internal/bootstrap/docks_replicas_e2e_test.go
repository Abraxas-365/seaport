package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/dockmodule"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

// world is a channel account shared by the processes of a test: like
// WhatsApp, it notices two live connections of one dock.
type world struct {
	t       *testing.T
	mu      sync.Mutex
	running map[identity.DockID]string // dock → process connected
	sent    []string                   // process that sent each message
	// receivers: each process's inbound receiver (its ship service).
	receivers map[string]dockmodule.Receiver
}

func newWorld(t *testing.T) *world {
	return &world{t: t, running: map[identity.DockID]string{}, receivers: map[string]dockmodule.Receiver{}}
}

// arrive is a message from the account's chat: the process connected to
// the dock receives it.
func (w *world) arrive(d dock.Dock, msgID, text string) ship.Ship {
	w.t.Helper()
	w.mu.Lock()
	process := w.running[d.ID]
	r := w.receivers[process]
	w.mu.Unlock()
	if r == nil {
		w.t.Fatalf("message for dock %s, which no process runs (%q)", d.ID, process)
	}
	s, _, err := r.Receive(context.Background(), config.DefaultOrganization, ship.Inbound{
		Dock: d.ID, Channel: dock.ChannelWhatsApp, ChatID: "chat@test", ChatKind: conversation.KindDirect,
		ExternalID: msgID, SenderID: "chat@test", SenderName: "Ana", Text: text, SentAt: time.Now()})
	if err != nil {
		w.t.Fatalf("receive in %s: %v", process, err)
	}
	return s
}

// exclusiveGateway is one process's gateway of an exclusive channel
// (registered as whatsapp: the real one is off in tests).
type exclusiveGateway struct {
	w    *world
	name string
}

func (g exclusiveGateway) Channel() dock.Channel { return dock.ChannelWhatsApp }

func (g exclusiveGateway) BindReceiver(r dockmodule.Receiver) {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	g.w.receivers[g.name] = r
}
func (g exclusiveGateway) Exclusive() bool { return true }

func (g exclusiveGateway) connect(id identity.DockID) {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	if other, ok := g.w.running[id]; ok && other != g.name {
		g.w.t.Errorf("dock %s connected by %s while %s runs it", id, g.name, other)
	}
	g.w.running[id] = g.name
}

func (g exclusiveGateway) Link(_ context.Context, d dock.Dock) (dock.Status, error) {
	g.connect(d.ID)
	return dock.StatusLinked, nil
}

func (g exclusiveGateway) Restore(_ context.Context, d dock.Dock) error {
	g.connect(d.ID)
	return nil
}

func (g exclusiveGateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (g exclusiveGateway) Running(id identity.DockID) bool {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	return g.w.running[id] == g.name
}

func (g exclusiveGateway) RunningDocks() []identity.DockID {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	var out []identity.DockID
	for id, p := range g.w.running {
		if p == g.name {
			out = append(out, id)
		}
	}
	return out
}

func (g exclusiveGateway) Stop(id identity.DockID) {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	if g.w.running[id] == g.name {
		delete(g.w.running, id)
	}
}

func (g exclusiveGateway) Unlink(_ context.Context, d dock.Dock) error {
	g.Stop(d.ID)
	return nil
}

func (g exclusiveGateway) NewMessageID(identity.DockID) (string, error) {
	return "x-" + identity.NewShipID().String(), nil
}

func (g exclusiveGateway) Send(_ context.Context, msg dock.Outgoing) error {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	if g.w.running[msg.Dock] != g.name {
		g.w.t.Errorf("%s sent through dock %s it doesn't run (%q does)", g.name, msg.Dock, g.w.running[msg.Dock])
	}
	g.w.sent = append(g.w.sent, g.name)
	return nil
}

func (w *world) owner(id identity.DockID) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[id]
}

func (w *world) lastSender() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.sent) == 0 {
		return ""
	}
	return w.sent[len(w.sent)-1]
}

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := bootstrap.OpenDB(context.Background(), testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// allowedConversation stores an allowed chat of the dock (a whatsapp
// dock's chats only arrive through WhatsApp).
func allowedConversation(t *testing.T, db *sqlx.DB, d dock.Dock) identity.ConversationID {
	t.Helper()
	id := identity.NewConversationID()
	if _, err := db.Exec(`INSERT INTO conversations (id, organization_id, dock_id, channel, external_id, kind, access, status)
		VALUES ($1, $2, $3, 'whatsapp', 'chat@test', 'direct', 'allow', 'allowed')`, id, config.DefaultOrganization, d.ID); err != nil {
		t.Fatal(err)
	}
	return id
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A dock of an exclusive channel runs in one process at a time: another
// process's sends and unlinks go to its owner; when the owner stops
// another process takes the dock over and reconnects it; a process that
// lost the lease drops its connection.
func TestReplicasShareTheDocks(t *testing.T) {
	w := newWorld(t)
	with := func(name string) bootstrap.Options {
		return bootstrap.Options{Gateways: []dock.Gateway{exclusiveGateway{w: w, name: name}}}
	}
	none := func(*config.Server) {}
	a := startPortWith(t, none, with("a"))
	b := joinPort(t, none, with("b"))

	// Created through b: b links it and owns it.
	var d dock.Dock
	if code := b.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "whatsapp", "name": "wa"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	if d.Status != dock.StatusLinked || w.owner(d.ID) != "b" {
		t.Fatalf("dock %+v runs in %q", d, w.owner(d.ID))
	}

	// A reply sent through a goes out through b.
	db := testDB(t)
	conv := allowedConversation(t, db, d)
	reply := func(p *port) ship.Ship {
		t.Helper()
		var s ship.Ship
		if code := p.admin(http.MethodPost, "/api/v1/conversations/"+conv.String()+"/ships", map[string]string{"text": "hola"}, &s); code != http.StatusCreated {
			t.Fatalf("send: %d %+v", code, s)
		}
		return s
	}
	if s := reply(a); s.Status != ship.StatusSent || w.lastSender() != "b" {
		t.Fatalf("relayed send: %+v by %q", s, w.lastSender())
	}

	// b stops: a takes the dock over and reconnects it.
	b.stop()
	eventually(t, "a takes the dock over", func() bool { return w.owner(d.ID) == "a" })
	if s := reply(a); s.Status != ship.StatusSent || w.lastSender() != "a" {
		t.Fatalf("send after takeover: %+v by %q", s, w.lastSender())
	}

	// Unlinking through a third process unlinks at the owner.
	c := joinPort(t, none, with("c"))
	var out dock.Dock
	if code := c.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &out); code != http.StatusOK {
		t.Fatalf("unlink: %d", code)
	}
	if out.Status != dock.StatusLoggedOut || w.owner(d.ID) != "" {
		t.Fatalf("after unlink: %+v runs in %q", out, w.owner(d.ID))
	}
	// Relinked through c: c runs it now (a gave the lease up).
	eventually(t, "a releases the unlinked dock", func() bool {
		var owners int
		_ = db.Get(&owners, `SELECT count(*) FROM dock_owners WHERE dock_id = $1`, d.ID)
		return owners == 0
	})
	if code := c.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/relink", nil, &out); code != http.StatusOK {
		t.Fatalf("relink: %d", code)
	}
	if w.owner(d.ID) != "c" {
		t.Fatalf("relinked dock runs in %q", w.owner(d.ID))
	}
	if s := reply(a); s.Status != ship.StatusSent || w.lastSender() != "c" {
		t.Fatalf("send after relink: %+v by %q", s, w.lastSender())
	}

	// Another process (crashed, or cut off) holds the lease: c drops its
	// connection at its next renew, and once the lease runs out a live
	// process takes the dock and reconnects it.
	if _, err := db.Exec(`UPDATE dock_owners SET process = 'ghost', expires_at = now() + interval '2 seconds' WHERE dock_id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c drops the dock it lost", func() bool { return w.owner(d.ID) == "" })
	eventually(t, "a live process takes the dock over", func() bool { return w.owner(d.ID) != "" })
}

// The whole port across three processes: the dock runs in a, the agent is
// connected to c and the owner watches b. A WhatsApp message received by
// a wakes the agent on c; its reply goes out through a; b streams it all.
// When a stops, another process takes the dock and the conversation goes
// on.
func TestPortAcrossProcesses(t *testing.T) {
	w := newWorld(t)
	with := func(name string) bootstrap.Options {
		return bootstrap.Options{Gateways: []dock.Gateway{exclusiveGateway{w: w, name: name}}}
	}
	none := func(*config.Server) {}
	a := startPortWith(t, none, with("a"))
	b := joinPort(t, none, with("b"))
	c := joinPort(t, none, with("c"))
	events := b.openEvents("")
	next(t, events, "ready", nil)

	var d dock.Dock
	if code := a.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "whatsapp", "name": "wa"}, &d); code != http.StatusCreated || w.owner(d.ID) != "a" {
		t.Fatalf("create dock: %d, runs in %q", code, w.owner(d.ID))
	}
	a.assignAs(adminToken, d.ID, a.agent(testAgent).ID, true)
	c.token = a.crewToken()
	agent := c.dialCrewWith(crew.Frame{Type: crew.FrameHello, Agent: "on-c", Concurrency: 1})

	// The first message waits at customs; the owner (on b) allows the chat.
	held := w.arrive(d, "m0", "hola")
	if held.Status != ship.StatusHeld {
		t.Fatalf("first message: %+v", held)
	}
	next(t, events, ship.EventCreated, func(raw json.RawMessage) bool { return hasShip(raw, held.ID, ship.StatusHeld) })
	if code := b.admin(http.MethodPatch, "/api/v1/conversations/"+held.Conversation.String(), map[string]string{"access": "allow"}, nil); code != http.StatusOK {
		t.Fatalf("allow: %d", code)
	}

	// converse: a message reaches the agent on c, which replies; the reply
	// goes out through the process running the dock, and b sees it sent.
	converse := func(msgID, via string) {
		t.Helper()
		in := w.arrive(d, msgID, "¿hay mesa?")
		f := agent.read()
		if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != in.ID {
			t.Fatalf("delivery of %s: %+v", msgID, f)
		}
		conv := f.Delivery.Conversation.ID
		agent.send(crew.Frame{Type: crew.FrameReply, ConversationID: &conv, Text: "sí, a las 9"})
		next(t, events, ship.EventUpdated, func(raw json.RawMessage) bool {
			var s ship.Ship
			return json.Unmarshal(raw, &s) == nil && s.Direction == ship.DirectionOut && s.Status == ship.StatusSent && s.Conversation == conv
		})
		if got := w.lastSender(); got != via {
			t.Fatalf("reply to %s sent by %q, want %q", msgID, got, via)
		}
		agent.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
		next(t, events, ship.EventDelivery, func(raw json.RawMessage) bool {
			var e ship.DeliveryEvent
			return json.Unmarshal(raw, &e) == nil && e.State == ship.DeliveryAcked && e.Delivery != nil && *e.Delivery == f.Delivery.ID
		})
	}
	converse("m1", "a")

	// a stops: b or c takes the dock over, and the conversation goes on.
	a.stop()
	eventually(t, "the dock is taken over", func() bool { o := w.owner(d.ID); return o == "b" || o == "c" })
	converse("m2", w.owner(d.ID))

	var got dock.Dock
	if code := b.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &got); code != http.StatusOK || got.Status != dock.StatusLinked {
		t.Fatalf("dock after takeover: %d %+v", code, got)
	}
}

func hasShip(raw json.RawMessage, id identity.ShipID, status ship.Status) bool {
	var s ship.Ship
	return json.Unmarshal(raw, &s) == nil && s.ID == id && s.Status == status
}
