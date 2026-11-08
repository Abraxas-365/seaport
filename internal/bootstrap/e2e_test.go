package bootstrap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/puerto/internal/bootstrap"
	"github.com/Abraxas-365/puerto/internal/config"
	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/crew"
	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	adminToken = "admin-token-0123456789"
	crewToken  = "crew-token-0123456789ab"
)

// testDatabaseURL is the throwaway database of the integration tests
// (docker compose up -d; createdb puerto_test). Tests skip without it.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("PUERTO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PUERTO_TEST_DATABASE_URL not set")
	}
	return url
}

type port struct {
	t   *testing.T
	srv *httptest.Server
}

func startPort(t *testing.T) *port {
	t.Helper()
	return startPortWith(t, func(*config.Server) {}, bootstrap.Options{})
}

// startPortWith starts a port on a wiped test database; edit adjusts the
// config and opts the build (WhatsApp is always off).
func startPortWith(t *testing.T, edit func(*config.Server), opts bootstrap.Options) *port {
	t.Helper()
	url := testDatabaseURL(t)
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	srv := httptest.NewUnstartedServer(nil)
	cfg := config.Server{Addr: srv.Listener.Addr().String(), DatabaseURL: url,
		PublicURL: "http://" + srv.Listener.Addr().String(), AdminToken: adminToken, CrewToken: crewToken}
	edit(&cfg)
	opts.DisableWhatsApp, opts.Lease = true, 2*time.Second
	app, err := bootstrap.Build(ctx, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	app.Start(runCtx)
	srv.Config.Handler = app.Handler
	srv.Start()
	t.Cleanup(func() {
		cancel()
		srv.CloseClientConnections()
		srv.Close()
		app.Close()
	})
	return &port{t: t, srv: srv}
}

func (p *port) call(method, path, token string, body, out any) int {
	p.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, p.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (p *port) admin(method, path string, body, out any) int {
	return p.call(method, path, adminToken, body, out)
}

func (p *port) inject(d dock.Dock, chat, msgID, text string) ship.Ship {
	p.t.Helper()
	var s ship.Ship
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships",
		map[string]any{"chat_id": chat, "chat_title": "Chat " + chat, "message_id": msgID, "sender_id": chat, "sender_name": "Ana", "text": text}, &s); code != http.StatusCreated {
		p.t.Fatalf("inject: status %d", code)
	}
	return s
}

// crewConn reads frames in the background: a websocket Read whose context
// expires closes the connection, so timeouts are applied on the channel.
type crewConn struct {
	t      *testing.T
	ws     *websocket.Conn
	frames chan crew.Frame
}

func (p *port) dialCrew(concurrency int) *crewConn {
	p.t.Helper()
	return p.dialCrewWith(crew.Frame{Type: crew.FrameHello, Agent: "test", Concurrency: concurrency})
}

func (p *port) dialCrewWith(hello crew.Frame) *crewConn {
	p.t.Helper()
	return p.dialCrewAs(crewToken, hello)
}

// dialCrewAs connects a connector with the given crew token.
func (p *port) dialCrewAs(token string, hello crew.Frame) *crewConn {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.srv.URL, "http")+"/crew/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { ws.CloseNow() })
	c := &crewConn{t: p.t, ws: ws, frames: make(chan crew.Frame, 16)}
	go func() {
		defer close(c.frames)
		for {
			_, raw, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			if f, err := crew.Decode(raw); err == nil {
				c.frames <- f
			}
		}
	}()
	c.send(hello)
	if f := c.read(); f.Type != crew.FrameWelcome || !strings.HasSuffix(f.MCPURL, "/mcp") {
		p.t.Fatalf("welcome: %+v", f)
	}
	return c
}

func (c *crewConn) send(f crew.Frame) {
	c.t.Helper()
	raw, _ := json.Marshal(f)
	if err := c.ws.Write(context.Background(), websocket.MessageText, raw); err != nil {
		c.t.Fatal(err)
	}
}

func (c *crewConn) read() crew.Frame {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatal("crew connection closed")
		}
		return f
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for a frame")
	}
	return crew.Frame{}
}

// quiet asserts no frame arrives within d.
func (c *crewConn) quiet(d time.Duration) {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if ok {
			c.t.Fatalf("unexpected frame: %+v", f)
		}
	case <-time.After(d):
	}
}

func TestPortEndToEnd(t *testing.T) {
	p := startPort(t)

	// auth
	if code := p.call(http.MethodGet, "/api/v1/docks", crewToken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("crew token on admin api: %d", code)
	}
	if code := p.call(http.MethodGet, "/mcp", adminToken, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin token on mcp: %d", code)
	}

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "test"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	if d.Status != dock.StatusLinked {
		t.Fatalf("api dock status %s", d.Status)
	}

	// A new direct chat is held at customs: stored, not delivered.
	held := p.inject(d, "111", "m1", "hola, estás?")
	if held.Status != ship.StatusHeld {
		t.Fatalf("first ship of unknown chat: %s", held.Status)
	}
	var dup struct {
		Duplicate bool `json:"duplicate"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships",
		map[string]any{"chat_id": "111", "message_id": "m1", "sender_id": "111", "text": "hola, estás?"}, &dup); code != http.StatusOK || !dup.Duplicate {
		t.Fatalf("duplicate inject: %d %+v", code, dup)
	}

	c := p.dialCrew(2)
	c.quiet(300 * time.Millisecond)

	// Allow it: new ships are delivered (the held one stays in history).
	var conv conversation.Conversation
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+held.Conversation.String(),
		map[string]string{"access": "allow"}, &conv); code != http.StatusOK || !conv.Allowed() {
		t.Fatalf("allow: %d %+v", code, conv)
	}
	p.inject(d, "111", "m2", "me ayudas con algo?")
	p.inject(d, "111", "m3", "es urgente")

	f := c.read()
	if f.Type != crew.FrameDelivery || f.Delivery == nil {
		t.Fatalf("expected delivery, got %+v", f)
	}
	del := f.Delivery
	if del.Conversation.ID != conv.ID {
		t.Fatalf("delivery of conversation %s", del.Conversation.ID)
	}
	// m2 and m3 may arrive in one delivery or two; collect both.
	texts := []string{}
	for _, s := range del.Ships {
		texts = append(texts, s.Text)
	}
	if len(texts) == 1 {
		// one in flight per conversation: m3 waits for the ack
		c.quiet(300 * time.Millisecond)
	}

	// The agent replies through MCP.
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: p.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{crewToken}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": conv.ID.String(), "text": "Claro, dime."}})
	if err != nil || res.IsError {
		t.Fatalf("reply tool: %v %+v", err, res)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "history",
		Arguments: map[string]any{"conversation_id": conv.ID.String(), "limit": 10}})
	if err != nil || res.IsError {
		t.Fatalf("history tool: %v %+v", err, res)
	}
	if raw, _ := json.Marshal(res.StructuredContent); !strings.Contains(string(raw), "Claro, dime.") {
		t.Fatalf("history misses the reply: %s", raw)
	}

	// Replying to a held conversation is refused.
	var other conversation.Conversation
	otherShip := p.inject(d, "222", "x1", "spam")
	p.admin(http.MethodGet, "/api/v1/conversations/"+otherShip.Conversation.String(), nil, &other)
	res, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": other.ID.String(), "text": "hi"}})
	if res == nil || !res.IsError {
		t.Fatalf("reply to held conversation should fail: %+v", res)
	}

	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &del.ID})
	if len(texts) == 1 {
		f = c.read()
		if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 {
			t.Fatalf("expected second delivery: %+v", f)
		}
		c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}

	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+conv.ID.String()+"/ships", nil, &hist)
	if hist.Page.Total != 4 {
		t.Fatalf("history total %d, want 4 (held, 2 handled, reply)", hist.Page.Total)
	}
	statuses := map[ship.Status]int{}
	for _, s := range hist.Items {
		statuses[s.Status]++
	}
	deadline := time.Now().Add(3 * time.Second)
	for statuses[ship.StatusHandled] != 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		p.admin(http.MethodGet, "/api/v1/conversations/"+conv.ID.String()+"/ships", nil, &hist)
		statuses = map[ship.Status]int{}
		for _, s := range hist.Items {
			statuses[s.Status]++
		}
	}
	if statuses[ship.StatusHeld] != 1 || statuses[ship.StatusHandled] != 2 || statuses[ship.StatusSent] != 1 {
		t.Fatalf("statuses %v", statuses)
	}
}

// A connector that disconnects (or stops renewing) gives its delivery back.
func TestDeliveryReleasedOnDisconnect(t *testing.T) {
	p := startPort(t)
	var d dock.Dock
	p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "t"}, &d)
	s := p.inject(d, "333", "a1", "hola")
	p.admin(http.MethodPatch, "/api/v1/conversations/"+s.Conversation.String(), map[string]string{"access": "allow"}, nil)
	p.inject(d, "333", "a2", "sigues ahí?")

	first := p.dialCrew(1)
	f := first.read()
	if f.Type != crew.FrameDelivery {
		t.Fatalf("first: %+v", f)
	}
	second := p.dialCrew(1)
	second.quiet(300 * time.Millisecond) // one in flight per conversation
	first.ws.CloseNow()

	f2 := second.read()
	if f2.Type != crew.FrameDelivery || f2.Delivery.Ships[0].Text != "sigues ahí?" {
		t.Fatalf("redelivery: %+v", f2)
	}
	if f2.Delivery.ID == f.Delivery.ID {
		t.Fatal("redelivery must use a new delivery id")
	}
	// The stale delivery id cannot be acked by anyone.
	second.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	if e := second.read(); e.Type != crew.FrameError {
		t.Fatalf("stale ack: %+v", e)
	}
	second.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f2.Delivery.ID})
	second.quiet(300 * time.Millisecond)
}

// A steering connector gets ships that arrive mid-turn as steer frames of
// the delivery in flight; the ack names what the agent saw and the rest is
// queued again.
func TestSteerJoinsShipsToTheDeliveryInFlight(t *testing.T) {
	p := startPort(t)
	var d dock.Dock
	p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "t"}, &d)
	s := p.inject(d, "444", "s0", "hola")
	p.admin(http.MethodPatch, "/api/v1/conversations/"+s.Conversation.String(), map[string]string{"access": "allow"}, nil)

	c := p.dialCrewWith(crew.Frame{Type: crew.FrameHello, Agent: "rness", Concurrency: 1, Steer: true})
	first := p.inject(d, "444", "s1", "necesito un reporte")
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != first.ID {
		t.Fatalf("delivery: %+v", f)
	}
	del := f.Delivery.ID

	second := p.inject(d, "444", "s2", "en pdf porfa")
	f = c.read()
	if f.Type != crew.FrameSteer || f.Delivery.ID != del || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != second.ID {
		t.Fatalf("steer: %+v", f)
	}
	if f.Delivery.Conversation.ID != s.Conversation {
		t.Fatalf("steer conversation %s", f.Delivery.Conversation.ID)
	}
	// Pushed after the agent's turn ended: the connector won't name it.
	late := p.inject(d, "444", "s3", "gracias!")
	if f = c.read(); f.Type != crew.FrameSteer || f.Delivery.Ships[0].ID != late.ID {
		t.Fatalf("late steer: %+v", f)
	}

	// Steering connectors must say what was handled.
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &del})
	if e := c.read(); e.Type != crew.FrameError {
		t.Fatalf("ack without ship_ids: %+v", e)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &del, ShipIDs: []identity.ShipID{first.ID, second.ID}})
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.ID == del || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != late.ID {
		t.Fatalf("unhandled ship must come back as a new delivery: %+v", f)
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID, ShipIDs: []identity.ShipID{late.ID}})

	var hist query.Paginated[ship.Ship]
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.admin(http.MethodGet, "/api/v1/conversations/"+s.Conversation.String()+"/ships", nil, &hist)
		handled := 0
		for _, sh := range hist.Items {
			if sh.Status == ship.StatusHandled {
				handled++
			}
		}
		if handled == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handled %d of 3: %+v", handled, hist.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.quiet(300 * time.Millisecond)
}

// A connector that doesn't steer keeps one delivery per turn.
func TestNoSteerKeepsLaterShipsQueued(t *testing.T) {
	p := startPort(t)
	var d dock.Dock
	p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "t"}, &d)
	s := p.inject(d, "555", "q0", "hola")
	p.admin(http.MethodPatch, "/api/v1/conversations/"+s.Conversation.String(), map[string]string{"access": "allow"}, nil)
	c := p.dialCrew(1)
	p.inject(d, "555", "q1", "uno")
	f := c.read()
	p.inject(d, "555", "q2", "dos")
	c.quiet(400 * time.Millisecond)
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	if f2 := c.read(); f2.Type != crew.FrameDelivery || f2.Delivery.Ships[0].Text != "dos" {
		t.Fatalf("second delivery: %+v", f2)
	}
}

func (p *port) injectWith(d dock.Dock, body map[string]any) ship.Ship {
	p.t.Helper()
	var s ship.Ship
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", body, &s); code != http.StatusCreated {
		p.t.Fatalf("inject: status %d", code)
	}
	return s
}

func (p *port) conversation(id identity.ConversationID) conversation.Conversation {
	p.t.Helper()
	var c conversation.Conversation
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+id.String(), nil, &c); code != http.StatusOK {
		p.t.Fatalf("conversation: %d", code)
	}
	return c
}

// Customs rules decide per dock which chats and messages wake the crew;
// held messages are stored, and rule changes re-decide existing chats.
func TestCustomsRules(t *testing.T) {
	p := startPort(t)
	var d dock.Dock
	p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "t"}, &d)
	base := "/api/v1/docks/" + d.ID.String() + "/customs"

	var c conversation.Customs
	p.admin(http.MethodGet, base, nil, &c)
	if c.Policy.Default != conversation.DefaultAsk || c.Policy.GroupMode != conversation.GroupMentions {
		t.Fatalf("default policy: %+v", c.Policy)
	}
	var allowPeru, denySpam conversation.Rule
	if code := p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "51*"}, &allowPeru); code != http.StatusCreated || allowPeru.Pattern != "+51*" {
		t.Fatalf("allow rule: %d %+v", code, allowPeru)
	}
	p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "deny", "pattern": "+51 999 888 777"}, &denySpam)
	if code := p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "51*"}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate rule: %d", code)
	}
	if code := p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "*"}, nil); code != http.StatusBadRequest {
		t.Fatalf("catch-all rule: %d", code)
	}

	c1 := p.dialCrew(4)
	// Chat handles come from the adapter: a LID chat known to be +51….
	friend := p.injectWith(d, map[string]any{"chat_id": "2314@lid", "chat_handles": []string{"+51999123436"},
		"message_id": "f1", "sender_id": "2314@lid", "text": "hola"})
	spam := p.inject(d, "+51999888777", "s1", "oferta!!")
	spain := p.inject(d, "+34600000000", "e1", "hola desde España")
	if friend.Status != ship.StatusQueued || spam.Status != ship.StatusHeld || spain.Status != ship.StatusHeld {
		t.Fatalf("statuses: friend=%s spam=%s spain=%s", friend.Status, spam.Status, spain.Status)
	}
	if f := c1.read(); f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != friend.ID {
		t.Fatalf("friend delivery: %+v", f)
	} else {
		c1.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}
	if st := p.conversation(spam.Conversation).Status; st != conversation.StatusDenied {
		t.Fatalf("spam chat: %s", st)
	}
	if st := p.conversation(spain.Conversation).Status; st != conversation.StatusPending {
		t.Fatalf("spain chat: %s", st)
	}
	// Replies follow customs.
	if code := p.call(http.MethodPost, "/api/v1/conversations/"+spam.Conversation.String()+"/ships", adminToken,
		map[string]string{"text": "no"}, nil); code != http.StatusForbidden {
		t.Fatalf("reply to denied chat: %d", code)
	}

	// Groups: allowed by id, only mentions wake the crew.
	p.admin(http.MethodPost, base+"/rules", map[string]string{"effect": "allow", "pattern": "999@g.us"}, nil)
	g1 := p.injectWith(d, map[string]any{"chat_id": "999@g.us", "chat_kind": "group", "chat_title": "Familia",
		"message_id": "g1", "sender_id": "+34600000000", "text": "qué tal todos"})
	g2 := p.injectWith(d, map[string]any{"chat_id": "999@g.us", "chat_kind": "group",
		"message_id": "g2", "sender_id": "+34600000000", "text": "@puerto ayuda", "addressed": true})
	g3 := p.injectWith(d, map[string]any{"chat_id": "999@g.us", "chat_kind": "group",
		"message_id": "g3", "sender_id": "+51999888777", "text": "@puerto compra ya", "addressed": true})
	if g1.Status != ship.StatusHeld || g2.Status != ship.StatusQueued || g3.Status != ship.StatusHeld {
		t.Fatalf("group: unmentioned=%s mentioned=%s denied member=%s", g1.Status, g2.Status, g3.Status)
	}
	f := c1.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ID != g2.ID {
		t.Fatalf("group delivery: %+v", f)
	}
	c1.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	// This group: everything.
	var gc conversation.Conversation
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+g1.Conversation.String(),
		map[string]string{"group_mode": "all"}, &gc); code != http.StatusOK || gc.GroupMode != conversation.GroupAll {
		t.Fatalf("group mode: %d %+v", code, gc)
	}
	if g4 := p.injectWith(d, map[string]any{"chat_id": "999@g.us", "chat_kind": "group",
		"message_id": "g4", "sender_id": "+34600000000", "text": "y ahora?"}); g4.Status != ship.StatusQueued {
		t.Fatalf("group in all mode: %s", g4.Status)
	}
	if f := c1.read(); f.Type == crew.FrameDelivery {
		c1.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+spain.Conversation.String(),
		map[string]string{"group_mode": "all"}, nil); code != http.StatusBadRequest {
		t.Fatalf("group mode on a direct chat: %d", code)
	}

	// Rule changes re-decide existing chats (held history stays held).
	if code := p.admin(http.MethodDelete, "/api/v1/customs/rules/"+denySpam.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove rule: %d", code)
	}
	if st := p.conversation(spam.Conversation).Status; st != conversation.StatusAllowed {
		t.Fatalf("spam chat after removing the deny rule: %s", st)
	}
	p.admin(http.MethodPatch, base+"/policy", map[string]string{"default": "deny"}, &c)
	if st := p.conversation(spain.Conversation).Status; st != conversation.StatusDenied {
		t.Fatalf("spain chat with default deny: %s", st)
	}
	// The owner's override beats the default.
	p.admin(http.MethodPatch, "/api/v1/conversations/"+spain.Conversation.String(), map[string]string{"access": "allow"}, nil)
	if st := p.conversation(spain.Conversation).Status; st != conversation.StatusAllowed {
		t.Fatalf("spain chat allowed by owner: %s", st)
	}
	var hist query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+spain.Conversation.String()+"/ships", nil, &hist)
	if len(hist.Items) != 1 || hist.Items[0].Status != ship.StatusHeld {
		t.Fatalf("held history must stay held: %+v", hist.Items)
	}
	c1.quiet(300 * time.Millisecond)
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
