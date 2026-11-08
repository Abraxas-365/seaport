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

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/crew"
	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/ship"
)

// sseEvent is one parsed Server-Sent Event.
type sseEvent struct {
	ID, Type string
	Data     json.RawMessage
}

// openEvents subscribes to /api/v1/events with the admin bearer token.
func (p *port) openEvents(lastID string) <-chan sseEvent {
	p.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
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

	var d dock.Dock
	p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "web"}, &d)
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
		if c.Name == "puerto_session" {
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
	forged := &http.Cookie{Name: "puerto_session", Value: "99999999999.deadbeef"}
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
}
