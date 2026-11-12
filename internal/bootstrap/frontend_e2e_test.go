package bootstrap_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// sseEvent is one parsed Server-Sent Event.
type sseEvent struct {
	ID, Type string
	Data     json.RawMessage
}

// openEvents subscribes to /api/v1/events with the admin bearer token.
func (p *port) openEvents(lastID string) <-chan sseEvent {
	p.t.Helper()
	return p.openEventsAs(adminToken, lastID)
}

// openEventsAs subscribes to /api/v1/events with token.
func (p *port) openEventsAs(token, lastID string) <-chan sseEvent {
	p.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		p.t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 256)
	p.t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		var e sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if e.Type != "" {
					out <- e
				}
				e = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				e.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				e.Type = line[7:]
			case strings.HasPrefix(line, "data: "):
				e.Data = json.RawMessage(line[6:])
			}
		}
	}()
	return out
}

// next returns the next event of type typ matching ok (others are skipped).
func next(t *testing.T, events <-chan sseEvent, typ string, ok func(json.RawMessage) bool) sseEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, open := <-events:
			if !open {
				t.Fatalf("event stream closed waiting for %s", typ)
			}
			if e.Type == typ && (ok == nil || ok(e.Data)) {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s event", typ)
		}
	}
}

// The UI needs: live events, crew status, an inbox with previews, dock
// management and a browser session.
func TestFrontendAPI(t *testing.T) {
	p := startPort(t)
	events := p.openEvents("")
	ready := next(t, events, "ready", nil)
	if !strings.Contains(string(ready.Data), "boot") {
		t.Fatalf("ready: %s", ready.Data)
	}

	d := p.apiDock("web")
	next(t, events, dock.EventUpdated, nil)

	// A held message: ship + conversation events, inbox preview.
	held := p.inject(d, "+34600000000", "h1", "hola?")
	next(t, events, conversation.EventUpdated, nil) // the conversation arrives first
	var created ship.Ship
	e := next(t, events, ship.EventCreated, nil)
	_ = json.Unmarshal(e.Data, &created)
	if created.ID != held.ID || created.Status != ship.StatusHeld {
		t.Fatalf("ship.created: %s", e.Data)
	}

	var inbox query.Paginated[ship.InboxItem]
	if code := p.admin(http.MethodGet, "/api/v1/inbox?status=pending", nil, &inbox); code != http.StatusOK || len(inbox.Items) != 1 {
		t.Fatalf("inbox: %d %+v", code, inbox)
	}
	item := inbox.Items[0]
	if item.Held != 1 || item.Last == nil || item.Last.Text != "hola?" || item.Status != conversation.StatusPending || item.InFlight {
		t.Fatalf("inbox item: %+v", item)
	}

	// Allow it, connect an agent: crew + delivery events, crew status.
	p.admin(http.MethodPatch, "/api/v1/conversations/"+held.Conversation.String(), map[string]string{"access": "allow"}, nil)
	next(t, events, conversation.EventUpdated, func(raw json.RawMessage) bool {
		return strings.Contains(string(raw), `"status":"allowed"`)
	})
	c := p.dialCrew(2)
	next(t, events, crew.EventChanged, func(raw json.RawMessage) bool { return strings.Contains(string(raw), `"name":"test"`) })
	p.inject(d, "+34600000000", "m1", "ahora sí")
	f := c.read()
	if f.Type != crew.FrameDelivery {
		t.Fatalf("delivery: %+v", f)
	}
	next(t, events, ship.EventDelivery, func(raw json.RawMessage) bool { return strings.Contains(string(raw), `"state":"claimed"`) })
	var status crew.Status
	p.admin(http.MethodGet, "/api/v1/crew", nil, &status)
	if len(status.Agents) != 1 || len(status.Agents[0].Deliveries) != 1 ||
		status.Agents[0].Deliveries[0].Conversation != held.Conversation || status.Agents[0].Session == "" {
		t.Fatalf("crew status: %+v", status)
	}
	p.admin(http.MethodGet, "/api/v1/inbox", nil, &inbox)
	if !inbox.Items[0].InFlight {
		t.Fatalf("inbox in flight: %+v", inbox.Items[0])
	}
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	next(t, events, ship.EventDelivery, func(raw json.RawMessage) bool { return strings.Contains(string(raw), `"state":"acked"`) })

	// A reply: ship.created (pending) then ship.updated (sent); activity moves.
	var reply ship.Ship
	p.admin(http.MethodPost, "/api/v1/conversations/"+held.Conversation.String()+"/ships", map[string]string{"text": "listo"}, &reply)
	next(t, events, ship.EventUpdated, func(raw json.RawMessage) bool { return strings.Contains(string(raw), reply.ID.String()) })
	p.admin(http.MethodGet, "/api/v1/inbox", nil, &inbox)
	if inbox.Items[0].Last == nil || inbox.Items[0].Last.ID != reply.ID {
		t.Fatalf("inbox last ship after reply: %+v", inbox.Items[0].Last)
	}

	// Replay: reconnecting with Last-Event-ID gets what came after it; an
	// unknown id (another process run) gets "reset".
	next(t, p.openEvents("otherboot-7"), "reset", nil)
	again := p.openEvents(e.ID)
	next(t, again, ship.EventUpdated, func(raw json.RawMessage) bool { return strings.Contains(string(raw), reply.ID.String()) })

	// Docks: rename, unlink (api docks can't), delete cascades.
	var renamed dock.Dock
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), map[string]string{"name": "Ventas"}, &renamed); code != http.StatusOK || renamed.Name != "Ventas" {
		t.Fatalf("rename: %d %+v", code, renamed)
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unlink api dock: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+d.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete dock: %d", code)
	}
	next(t, events, dock.EventDeleted, nil)
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+held.Conversation.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("conversation of a deleted dock: %d", code)
	}
}

// Browsers trade the admin token for an HttpOnly cookie; cookie requests
// from another origin can't change anything.
func TestBrowserSession(t *testing.T) {
	p := startPort(t)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	do := func(method, path, body string, header map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, p.srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if r := do(http.MethodGet, "/api/v1/docks", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.StatusCode)
	}
	if r := do(http.MethodPost, "/api/v1/session", `{"token":"wrong-token-wrong-token"}`, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", r.StatusCode)
	}
	r := do(http.MethodPost, "/api/v1/session", `{"token":"`+adminToken+`"}`, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", r.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == "seaport_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", cookie)
	}
	if r := do(http.MethodGet, "/api/v1/docks", "", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("with session: %d", r.StatusCode)
	}
	if r := do(http.MethodPost, "/api/v1/docks", `{"channel":"api","name":"same-origin"}`, map[string]string{"Sec-Fetch-Site": "same-origin"}); r.StatusCode != http.StatusCreated {
		t.Fatalf("same-origin write: %d", r.StatusCode)
	}
	if r := do(http.MethodPost, "/api/v1/docks", `{"channel":"api","name":"evil"}`, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site write: %d", r.StatusCode)
	}
	// A forged cookie is not a session.
	forged := &http.Cookie{Name: "seaport_session", Value: "99999999999.deadbeef"}
	req, _ := http.NewRequest(http.MethodGet, p.srv.URL+"/api/v1/docks", nil)
	req.AddCookie(forged)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d", resp.StatusCode)
	}
	if r := do(http.MethodDelete, "/api/v1/session", "", map[string]string{"Sec-Fetch-Site": "same-origin"}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r := do(http.MethodGet, "/api/v1/docks", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
}

// TestWebUIRoutes: the admin UI owns every GET outside the API; API, crew
// and unknown non-GET paths keep their JSON errors.
func TestWebUIRoutes(t *testing.T) {
	p := startPort(t)
	get := func(method, path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, p.srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(b)
	}
	// The UI may not be built in CI: it is either the app or the
	// "not built" error, never an API 404.
	for _, path := range []string{"/", "/inbox/123", "/docks"} {
		resp, body := get(http.MethodGet, path)
		built := resp.StatusCode == http.StatusOK && strings.Contains(body, `<div id="root">`)
		unbuilt := resp.StatusCode == http.StatusNotFound && strings.Contains(body, "not built")
		if !built && !unbuilt {
			t.Errorf("GET %s: %d %.80s", path, resp.StatusCode, body)
		}
	}
	if resp, body := get(http.MethodGet, "/api/v1/nope"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated API: %d %s", resp.StatusCode, body)
	}
	if resp, _ := get(http.MethodPost, "/inbox"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST outside the API: %d", resp.StatusCode)
	}
	if resp, _ := get(http.MethodGet, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	// A known path with the wrong method is not a 405 (it never was: main's
	// "/" catch-all answered it): other methods get the JSON 404, a GET goes
	// to the web app, which is what main answered too. /healthz alone says 405.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/crew/profile"}, {http.MethodPut, "/crew/reply"}, {http.MethodPost, "/crew/workspace"},
		{http.MethodGet, "/github/apps/created"}, {http.MethodDelete, "/jira/webhook/x"},
	} {
		resp, body := get(c.method, c.path)
		if c.method == http.MethodGet {
			built := resp.StatusCode == http.StatusOK && strings.Contains(body, `<div id="root">`)
			unbuilt := resp.StatusCode == http.StatusNotFound && strings.Contains(body, "not built")
			if !built && !unbuilt {
				t.Errorf("%s %s: %d %.80s", c.method, c.path, resp.StatusCode, body)
			}
		} else if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"NOT_FOUND"`) {
			t.Errorf("%s %s: %d %.80s", c.method, c.path, resp.StatusCode, body)
		}
	}
	// A wrong-method GET stays what main answered: the web app, never a 405.
	if resp, body := get(http.MethodGet, "/crew/reply"); resp.StatusCode == http.StatusMethodNotAllowed {
		t.Errorf("GET /crew/reply: %d %.80s", resp.StatusCode, body)
	}
	if resp, body := get(http.MethodPost, "/healthz"); resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(body, `"METHOD_NOT_ALLOWED"`) {
		t.Errorf("POST /healthz: %d %.80s", resp.StatusCode, body)
	}
}

// What an agent does on its delivery reaches the web live (a flash: no
// id, not replayed); activity for a delivery the connection doesn't hold
// is dropped, a bad frame is refused.
func TestAgentActivityIsLive(t *testing.T) {
	p := startPort(t)
	events := p.openEvents("")
	d := p.apiDock("t")
	p.letIn(d)
	s := p.inject(d, "555", "act1", "revisa esto")
	c := p.dialCrew(1)
	f := c.read()
	if f.Type != crew.FrameDelivery {
		t.Fatalf("delivery: %+v", f)
	}
	del := f.Delivery.ID

	stranger := identity.NewDeliveryID()
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &stranger,
		Items: []crew.ActivityItem{{Kind: crew.ActivityThought, Text: "not mine"}}})
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &del,
		Items: []crew.ActivityItem{{Kind: "secret-dump", Text: "x"}}})
	if e := c.read(); e.Type != crew.FrameError {
		t.Fatalf("bad item kind: %+v", e)
	}
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &del, Items: []crew.ActivityItem{
		{Kind: crew.ActivityThought, Text: "Leo el <diff>"},
		{Kind: crew.ActivityTool, Tool: "t1", Title: "Bash: git diff", ToolKind: "execute", Status: crew.ToolInProgress},
	}})
	e := next(t, events, crew.EventActivity, nil)
	var a crew.Activity
	if err := json.Unmarshal(e.Data, &a); err != nil {
		t.Fatal(err)
	}
	if e.ID != "" || a.Delivery != del || a.Conversation != s.Conversation || a.AgentName != "test" ||
		len(a.Items) != 2 || a.Items[0].Text != "Leo el <diff>" || a.Items[1].Status != crew.ToolInProgress {
		t.Fatalf("activity: id=%q %+v", e.ID, a)
	}
	// Flashes are not replayed to a client resuming from before them.
	for _, ev := range replayAll(t, p) {
		if ev.Type == crew.EventActivity {
			t.Fatalf("activity was stored: %+v", ev)
		}
	}
	// A later update of t1 (completed) and a title with a token in it.
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &del, Items: []crew.ActivityItem{
		{Kind: crew.ActivityTool, Tool: "t1", Status: crew.ToolCompleted},
		{Kind: crew.ActivityTool, Tool: "t2", Title: "Bash: curl -H 'Authorization: token ghp_" + strings.Repeat("a", 36) + "'",
			ToolKind: "execute", Status: crew.ToolFailed},
	}})
	next(t, events, crew.EventActivity, nil)
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &del})
	// After the ack the delivery is gone: late activity is dropped quietly.
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &del,
		Items: []crew.ActivityItem{{Kind: crew.ActivityMessage, Text: "late"}}})
	c.quiet(300 * time.Millisecond)

	// The turn's summary is stored: tools only (no thoughts), titles redacted.
	e = next(t, events, ship.EventTurn, nil)
	var turn ship.Turn
	if err := json.Unmarshal(e.Data, &turn); err != nil {
		t.Fatal(err)
	}
	var got query.Paginated[ship.Turn]
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+s.Conversation.String()+"/turns", nil, &got); code != http.StatusOK {
		t.Fatalf("turns: %d", code)
	}
	if len(got.Items) != 1 || got.Items[0].Delivery != del || got.Items[0].Outcome != ship.TurnAcked ||
		got.Items[0].AgentName != "test" || len(got.Items[0].Tools) != 2 {
		t.Fatalf("turns: %+v", got)
	}
	t1, t2 := got.Items[0].Tools[0], got.Items[0].Tools[1]
	if t1.Title != "Bash: git diff" || t1.Kind != "execute" || t1.Status != crew.ToolCompleted || t1.EndedAt == nil {
		t.Fatalf("t1: %+v", t1)
	}
	if t2.Status != crew.ToolFailed || strings.Contains(t2.Title, "ghp_") || !strings.Contains(t2.Title, text.Redacted) {
		t.Fatalf("t2 not redacted: %+v", t2)
	}
	if turn.Delivery != del || strings.Contains(string(e.Data), "ghp_") || strings.Contains(string(e.Data), "diff>") {
		t.Fatalf("turn event: %s", e.Data)
	}

	// A turn without tool calls stores nothing.
	p.inject(d, "555", "act2", "otra")
	f = c.read()
	del2 := f.Delivery.ID
	c.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &del2, Items: []crew.ActivityItem{{Kind: crew.ActivityThought, Text: "hmm"}}})
	c.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &del2})
	c.quiet(300 * time.Millisecond)
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+s.Conversation.String()+"/turns", nil, &got); code != http.StatusOK || got.Page.Total != 1 {
		t.Fatalf("turns after a tool-less turn: %d %+v", code, got.Page)
	}
}

// replayAll reads every stored event a client resuming from 0 gets.
func replayAll(t *testing.T, p *port) []sseEvent {
	t.Helper()
	events := p.openEvents("0")
	var out []sseEvent
	for {
		select {
		case e := <-events:
			out = append(out, e)
		case <-time.After(300 * time.Millisecond):
			return out
		}
	}
}
