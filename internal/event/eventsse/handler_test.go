package eventsse

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

var perms = map[string]string{"dock.updated": permission.DocksRead, "ship.created": permission.ShipsRead}

type rig struct {
	url  string
	bus  *event.Bus
	app  *fiber.App
	done chan struct{}
}

func start(t *testing.T, p *httpx.Principal) *rig {
	t.Helper()
	bus := event.NewBus(event.NewMemoryLog())
	ctx, cancel := context.WithCancel(context.Background())
	if err := bus.Start(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(func(c fiber.Ctx) error {
		if p != nil {
			httpx.SetPrincipal(c, *p)
		}
		return c.Next()
	})
	app.Get("/api/v1/events", Handler(bus, done, perms))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	t.Cleanup(func() {
		cancel()
		bus.Close()
		_ = app.ShutdownWithTimeout(time.Second)
	})
	return &rig{url: "http://" + ln.Addr().String() + "/api/v1/events", bus: bus, app: app, done: done}
}

func admin(perms ...string) *httpx.Principal {
	return &httpx.Principal{Organization: config.DefaultOrganization, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms}
}

func open(t *testing.T, url, lastID string) (*http.Response, chan string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return resp, lines
}

func expect(t *testing.T, lines chan string, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case got, ok := <-lines:
			if !ok || got != w {
				t.Fatalf("got %q (open %v), want %q", got, ok, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", w)
		}
	}
}

func TestWireFormatReplayAndFilter(t *testing.T) {
	r := start(t, admin(permission.DocksRead))
	r.bus.Publish(config.DefaultOrganization, "dock.updated", map[string]int{"n": 1})
	r.bus.Publish(config.DefaultOrganization, "ship.created", map[string]int{"n": 2}) // not readable
	time.Sleep(300 * time.Millisecond)
	resp, lines := open(t, r.url, "0")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	expect(t, lines, "retry: 3000", "event: ready", `data: {"boot":"`+r.bus.Boot()+`"}`, "")
	expect(t, lines, "id: 1", "event: dock.updated", `data: {"n":1}`, "")
	// Live events and flashes (no id line).
	r.bus.Publish(config.DefaultOrganization, "dock.updated", map[string]int{"n": 3})
	expect(t, lines, "id: 3", "event: dock.updated", `data: {"n":3}`, "")
	r.bus.Flash(config.DefaultOrganization, "dock.updated", map[string]int{"n": 4})
	expect(t, lines, "event: dock.updated", `data: {"n":4}`, "")
}

func TestResetForAnUnknownID(t *testing.T) {
	r := start(t, admin(permission.DocksRead))
	_, lines := open(t, r.url, "999")
	expect(t, lines, "retry: 3000", "event: ready")
	if l := <-lines; !strings.HasPrefix(l, "data:") {
		t.Fatal(l)
	}
	expect(t, lines, "", "event: reset", "data: {}", "")
}

func TestUnauthenticatedIsAJSONError(t *testing.T) {
	r := start(t, nil)
	resp, err := http.Get(r.url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}

func TestHeartbeatAndClientDisconnect(t *testing.T) {
	old := heartbeat
	heartbeat = 50 * time.Millisecond
	defer func() { heartbeat = old }()
	r := start(t, admin(permission.DocksRead))
	resp, lines := open(t, r.url, "")
	expect(t, lines, "retry: 3000", "event: ready")
	<-lines
	expect(t, lines, "", ": ping", "")
	if r.bus.Subscribers() != 1 {
		t.Fatalf("subscribers %d", r.bus.Subscribers())
	}
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for r.bus.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the subscription outlived the client")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEndsOnDoneAndOnShutdown(t *testing.T) {
	r := start(t, admin(permission.DocksRead))
	_, lines := open(t, r.url, "")
	expect(t, lines, "retry: 3000", "event: ready")
	close(r.done)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				goto shut
			}
		case <-deadline:
			t.Fatal("done didn't end the stream")
		}
	}
shut:
	r2 := start(t, admin(permission.DocksRead))
	_, lines = open(t, r2.url, "")
	expect(t, lines, "retry: 3000", "event: ready")
	go r2.app.ShutdownWithTimeout(3 * time.Second)
	deadline = time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("shutdown didn't end the stream")
		}
	}
}

func TestEndsAtTheCredentialsExpiry(t *testing.T) {
	p := admin(permission.DocksRead)
	p.Expires = time.Now().Add(300 * time.Millisecond)
	r := start(t, p)
	_, lines := open(t, r.url, "")
	expect(t, lines, "retry: 3000", "event: ready")
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("expiry didn't end the stream")
		}
	}
}
