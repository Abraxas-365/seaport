package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Allowing a pending chat delivers what it sent while it waited at
// customs (oldest first, within ship.ReleaseWindow), through customs'
// message-level checks again; a denied chat's messages stay held.
func TestAllowingDeliversWhatWaited(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("t")
	base := "/api/v1/docks/" + d.ID.String() + "/customs"
	c := p.dialCrew(4)

	stale := p.inject(d, "111", "a0", "hace dos días")
	first := p.inject(d, "111", "a1", "hola")
	second := p.inject(d, "111", "a2", "sigues ahí?")
	for _, s := range []ship.Ship{stale, first, second} {
		if s.Status != ship.StatusHeld || s.HoldReason != conversation.HoldPending {
			t.Fatalf("pending chat: %+v", s)
		}
	}
	db, err := bootstrap.OpenDB(context.Background(), testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE ships SET created_at = now() - interval '25 hours' WHERE id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	group := p.injectWith(d, map[string]any{"chat_id": "777@g.us", "chat_kind": "group",
		"message_id": "g1", "sender_id": "+34600000000", "text": "@seaport hola", "addressed": true})
	chatter := p.injectWith(d, map[string]any{"chat_id": "777@g.us", "chat_kind": "group",
		"message_id": "g2", "sender_id": "+34600000001", "text": "nos vemos luego"})
	c.quiet(200 * time.Millisecond)

	// The owner allows the chat: the recent two go out, in order, at once.
	p.admin(http.MethodPatch, "/api/v1/conversations/"+first.Conversation.String(), map[string]string{"access": "allow"}, nil)
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 2 || f.Delivery.Ships[0].ID != first.ID || f.Delivery.Ships[1].ID != second.ID {
		t.Fatalf("released delivery: %+v", f)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	if s := find(p.history(first.Conversation), stale.ID); s.Status != ship.StatusHeld {
		t.Fatalf("a message older than the window must stay held: %+v", s)
	}

	// A group allowed by a rule (a GitHub pull request, a Slack thread): the
	// message that addressed us goes out; the one that didn't stays held,
	// now for not mentioning us (the group is in mentions mode).
	p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "777@g.us"}, nil)
	if st := p.conversation(group.Conversation).Status; st != conversation.StatusAllowed {
		t.Fatalf("group: %s", st)
	}
	if f := c.read(); f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != group.ID {
		t.Fatalf("released group delivery: %+v", f)
	} else {
		c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}
	if s := find(p.history(group.Conversation), chatter.ID); s.Status != ship.StatusHeld || s.HoldReason != conversation.HoldNotMentioned {
		t.Fatalf("a group message without a mention must stay held: %+v", s)
	}
	c.quiet(300 * time.Millisecond)

	// A denied chat's message stays held after an allow rule lets it in.
	var deny conversation.Rule
	p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "deny", "pattern": "+222"}, &deny)
	denied := p.inject(d, "+222", "b1", "oferta")
	if denied.HoldReason != conversation.HoldDenied {
		t.Fatalf("denied chat: %+v", denied)
	}
	p.admin(http.MethodDelete, "/api/v1/customs/rules/"+deny.ID.String(), nil, nil)
	p.admin(http.MethodPatch, "/api/v1/conversations/"+denied.Conversation.String(), map[string]string{"access": "allow"}, nil)
	c.quiet(300 * time.Millisecond)

	// A pending chat let in by a rule is released too.
	waiting := p.inject(d, "+333", "c1", "buenas")
	p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "+333"}, nil)
	if f := c.read(); f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != waiting.ID {
		t.Fatalf("released by a rule: %+v", f)
	} else {
		c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}
	c.quiet(300 * time.Millisecond)
}

func find(ships []ship.Ship, id identity.ShipID) ship.Ship {
	for _, s := range ships {
		if s.ID == id {
			return s
		}
	}
	return ship.Ship{}
}
