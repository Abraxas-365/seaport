package bootstrap_test

// Catch-up: a delivery carries the thread's newest other messages
// (Delivery.Earlier) and says when its ships were handed over before in a
// turn that didn't finish (Ship.Attempts, Delivery.Retried), so a session
// that missed part of the thread (another slot, a dropped connection, a
// person replying from the web) can pick it up.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// A connector whose sessions never saw the thread (another slot worked it)
// is shown the earlier messages before the new one; the same session later
// is shown only what came since its last turn and isn't its own reply (a
// person's from the web), and nothing when nothing came.
func TestConnectorCatchesUpOnTheThread(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("catchup")
	p.letIn(d)

	other := p.startConnector(connectorOpts{caps: "load"})
	p.ready(anyConn)
	first := p.inject(d, "654", "c1", "hola")
	p.shipsOf(first.Conversation, "the first answer", answered(1))
	if got := other.prompts(1)[0].Text; strings.Contains(got, "Earlier in this conversation") {
		t.Fatalf("a first message has nothing earlier:\n%s", got)
	}
	other.stop()
	p.noConnections()

	h := p.startConnector(connectorOpts{caps: "load"})
	p.ready(anyConn)
	p.inject(d, "654", "c2", "qué dijiste?")
	p.shipsOf(first.Conversation, "the second answer", answered(2))
	got := h.prompts(1)[0].Text
	// Both connectors run the same library agent: its reply reads as its own.
	for _, want := range []string{"Earlier in this conversation", "- [", "Ana: hola", "you: echo: hola", "New message(s):\n[", "Ana: qué dijiste?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the fresh session's prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Ana: hola") > strings.Index(got, "Ana: qué dijiste?") {
		t.Fatalf("earlier messages go first:\n%s", got)
	}

	p.inject(d, "654", "c3", "ok")
	p.shipsOf(first.Conversation, "the third answer", answered(3))
	if got := h.prompts(2)[1].Text; strings.Contains(got, "Earlier in this conversation") || strings.Contains(got, "Since your last turn") {
		t.Fatalf("a session that saw everything is told nothing again:\n%s", got)
	}

	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+first.Conversation.String()+"/ships",
		map[string]string{"text": "te lo confirmo yo"}, nil); code != http.StatusCreated {
		t.Fatalf("person's reply: %d", code)
	}
	p.inject(d, "654", "c4", "gracias")
	p.shipsOf(first.Conversation, "the fourth answer", answered(5))
	got = h.prompts(3)[2].Text
	if !strings.Contains(got, "Since your last turn here") || !strings.Contains(got, "a person, from Seaport: te lo confirmo yo") ||
		strings.Contains(got, "echo: ok") {
		t.Fatalf("the resumed session's catch-up:\n%s", got)
	}
}

// A claim's Earlier lists the handled messages, the replies (the agent's
// and a person's from the web) and the group chatter held for not calling
// the agent, oldest first, never the delivery's own ships; a steer carries
// none. A dock whose history is off gives no Earlier.
func TestDeliveryCarriesTheEarlierMessages(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("earlier")
	p.letIn(d)
	c := p.dialCrewWith(crew.Frame{Type: crew.FrameHello, Agent: "rness", Concurrency: 1, Steer: true})

	group := func(id, text string, addressed bool) ship.Ship {
		return p.injectWith(d, map[string]any{"chat_id": "777@g.us", "chat_kind": "group", "chat_title": "Equipo",
			"message_id": id, "sender_id": "+34600000000", "sender_name": "Ana", "text": text, "addressed": addressed})
	}
	first := group("e1", "@seaport hola", true)
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Earlier) != 0 {
		t.Fatalf("first delivery: %+v", f)
	}
	c.send(crew.Frame{Type: crew.FrameReply, ConversationID: &first.Conversation, Text: "hola Ana"})
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{first.ID}})
	p.shipsOf(first.Conversation, "the reply stored", answered(1))

	if chatter := group("e2", "alguien vio el reporte?", false); chatter.Status != ship.StatusHeld {
		t.Fatalf("chatter: %s", chatter.Status)
	}
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+first.Conversation.String()+"/ships",
		map[string]string{"text": "lo reviso yo"}, nil); code != http.StatusCreated {
		t.Fatalf("person's reply: %d", code)
	}
	ask := group("e3", "@seaport resúmelo", true)
	f = c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != ask.ID {
		t.Fatalf("second delivery: %+v", f)
	}
	got := f.Delivery.Earlier
	want := []string{"@seaport hola", "hola Ana", "alguien vio el reporte?", "lo reviso yo"}
	if len(got) != len(want) {
		t.Fatalf("earlier: %q", texts(got))
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Fatalf("earlier: %q, want %q", texts(got), want)
		}
	}
	if got[0].Status != ship.StatusHandled || got[1].Agent == nil || got[1].Delivery == nil ||
		got[2].Status != ship.StatusHeld || got[2].HoldReason == "" || got[3].Agent != nil {
		t.Fatalf("earlier ships: %+v", got)
	}
	if f.Delivery.Ships[0].Attempts != 1 || f.Delivery.Retried() {
		t.Fatalf("a first hand-over: attempts %d", f.Delivery.Ships[0].Attempts)
	}
	more := group("e4", "@seaport y en pdf", true)
	steer := c.read()
	if steer.Type != crew.FrameSteer || steer.Delivery.Ships[0].ID != more.ID || len(steer.Delivery.Earlier) != 0 {
		t.Fatalf("steer: %+v", steer)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{ask.ID, more.ID}})

	// The dock keeps its history from agents: nothing earlier.
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(),
		map[string]any{"tools": map[string]bool{"history": false}}, nil); code != http.StatusOK {
		t.Fatalf("history off: %d", code)
	}
	group("e5", "@seaport gracias", true)
	f = c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Earlier) != 0 || len(f.Delivery.NoHistory) != 1 {
		t.Fatalf("delivery without history: %+v", f)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{f.Delivery.Ships[0].ID}})
	c.quiet(300 * time.Millisecond)
}

// A delivery whose connection dropped comes back marked: its ship was
// handed over before (Attempts 2, Retried). A ship that joined a delivery
// after the agent's turn ended (the ack doesn't name it) wasn't seen, and
// doesn't count.
func TestRedeliveryCountsAttempts(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("attempts")
	p.letIn(d)
	in := p.inject(d, "321", "a1", "sigues ahí?")

	first := p.dialCrew(1)
	f := first.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].Attempts != 1 || f.Delivery.Retried() {
		t.Fatalf("first: %+v", f)
	}
	first.ws.CloseNow()

	c := p.dialCrewWith(crew.Frame{Type: crew.FrameHello, Agent: "rness", Concurrency: 1, Steer: true})
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != in.ID || f.Delivery.Ships[0].Attempts != 2 || !f.Delivery.Retried() {
		t.Fatalf("redelivery: %+v", f.Delivery)
	}
	late := p.inject(d, "321", "a2", "hola?")
	if s := c.read(); s.Type != crew.FrameSteer || s.Delivery.Ships[0].ID != late.ID {
		t.Fatalf("steer: %+v", s)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{in.ID}})
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != late.ID || f.Delivery.Ships[0].Attempts != 1 || f.Delivery.Retried() {
		t.Fatalf("the unseen ship's delivery: %+v", f.Delivery)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{late.ID}})
	c.quiet(300 * time.Millisecond)
}
