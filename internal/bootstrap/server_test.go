package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/coder/websocket"
	fws "github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

// These tests run the Fiber shell (newServer) over a real listener with a few
// routes of their own: no database needed.

func startShell(t *testing.T, mount func(app *fiber.App)) (url string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, newServer(mount), ln, func() {}) }()
	var stopped bool
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return "http://" + ln.Addr().String(), stop
}

// bodyLimit covers every body a handler accepts: it is the one limit Fiber
// applies before any handler runs.
func TestBodyLimitCoversTheBiggestBodies(t *testing.T) {
	biggest := max(int64(crew.MaxReplyBody), int64(hosting.MaxHomeUpload), int64(httpx.MaxJSONBody))
	if bodyLimit < biggest {
		t.Fatalf("bodyLimit %d is below the biggest body a handler accepts (%d)", bodyLimit, biggest)
	}
	if bodyLimit > 2*biggest {
		t.Fatalf("bodyLimit %d is more than twice the biggest body (%d): tighten it", bodyLimit, biggest)
	}
}

func TestShellHealthzAndEdges(t *testing.T) {
	url, _ := startShell(t, func(app *fiber.App) {
		app.Get("/own-cache", func(c fiber.Ctx) error {
			c.Set("Cache-Control", "public, max-age=60")
			return c.SendString("static")
		})
		app.Get("/id", func(c fiber.Ctx) error { return c.SendString(httpx.RequestID(c.Context())) })
		app.Get("/panic", func(fiber.Ctx) error { panic("boom") })
	})
	get := func(method, path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, url+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	once := func(resp *http.Response, key, want string) {
		t.Helper()
		if got := resp.Header.Values(key); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want exactly %q", key, got, want)
		}
	}

	resp, body := get("GET", "/healthz", map[string]string{"X-Request-ID": "abc-123"})
	if resp.StatusCode != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}
	once(resp, "Content-Type", "application/json")
	once(resp, "X-Request-ID", "abc-123")
	once(resp, "X-Content-Type-Options", "nosniff")
	once(resp, "Referrer-Policy", "no-referrer")
	once(resp, "Cache-Control", "no-store")
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on plain http")
	}

	// Routes get the same headers, once; a handler's own win.
	resp, _ = get("GET", "/id", map[string]string{"X-Forwarded-Proto": "https"})
	once(resp, "X-Content-Type-Options", "nosniff")
	once(resp, "Cache-Control", "no-store")
	once(resp, "Strict-Transport-Security", "max-age=31536000")
	resp, _ = get("GET", "/own-cache", nil)
	once(resp, "Cache-Control", "public, max-age=60")
	once(resp, "X-Content-Type-Options", "nosniff")

	// A handler sees the request id the edges made (valid ones kept, others replaced).
	_, body = get("GET", "/id", map[string]string{"X-Request-ID": "mine_1"})
	if body != "mine_1" {
		t.Errorf("handler request id = %q", body)
	}
	resp, body = get("GET", "/id", map[string]string{"X-Request-ID": "no spaces allowed"})
	if id := resp.Header.Get("X-Request-ID"); len(id) != 32 || body != id {
		t.Errorf("replaced request id = %q (handler saw %q)", id, body)
	}

	// Error bodies: the shell's and a handler's are the same shape.
	var e struct {
		Error struct{ Code, Message string }
	}
	resp, body = get("POST", "/healthz", nil)
	if resp.StatusCode != 405 || json.Unmarshal([]byte(body), &e) != nil || e.Error.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("POST /healthz: %d %s", resp.StatusCode, body)
	}
	resp, body = get("GET", "/nope", nil)
	if resp.StatusCode != 404 || json.Unmarshal([]byte(body), &e) != nil || e.Error.Code != "NOT_FOUND" {
		t.Errorf("GET /nope: %d %s", resp.StatusCode, body)
	}
	resp, body = get("GET", "/healthz", map[string]string{"Content-Encoding": "gzip"})
	if resp.StatusCode != 415 || json.Unmarshal([]byte(body), &e) != nil || e.Error.Code != "UNSUPPORTED_MEDIA_TYPE" {
		t.Errorf("Content-Encoding: %d %s", resp.StatusCode, body)
	}
	once(resp, "X-Content-Type-Options", "nosniff")
	resp, body = get("GET", "/panic", nil)
	if resp.StatusCode != 500 || strings.Contains(body, "boom") || json.Unmarshal([]byte(body), &e) != nil || e.Error.Code != "INTERNAL" {
		t.Errorf("panic: %d %s", resp.StatusCode, body)
	}
}

func TestShellBodyLimit(t *testing.T) {
	var reached bool
	url, _ := startShell(t, func(app *fiber.App) {
		app.Post("/x", func(c fiber.Ctx) error {
			reached = true
			return c.SendString(string(rune('0' + len(c.Body())%10)))
		})
	})
	post := func(n int) (int, string) {
		resp, err := http.Post(url+"/x", "application/octet-stream", bytes.NewReader(make([]byte, n)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := post(bodyLimit - 1<<10); code != 200 {
		t.Fatalf("a body under the limit: %d", code)
	}
	reached = false
	// Over the limit: the server answers from the headers alone (Content-Length)
	// and closes, without reading the body, so a client still writing it may
	// see a broken pipe instead of the answer. Send the headers only.
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: "+strconv.Itoa(bodyLimit+1<<10)+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(b), `"PAYLOAD_TOO_LARGE"`) {
		t.Fatalf("a body over the limit: %d %s", resp.StatusCode, b)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Request-ID") == "" {
		t.Errorf("the refusal lacks the edge headers: %v", resp.Header)
	}
	if reached {
		t.Fatal("a handler ran a request the server refused")
	}
}

// A streamed response's first event reaches the client while the handler's
// writer is still running.
func TestShellStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	url, _ := startShell(t, func(app *fiber.App) {
		app.Get("/stream", func(c fiber.Ctx) error {
			c.Set("Content-Type", "text/event-stream")
			return c.SendStreamWriter(func(w *bufio.Writer) {
				_, _ = io.WriteString(w, "event: ready\ndata: 1\n\n")
				_ = w.Flush()
				<-release
				_, _ = io.WriteString(w, "event: end\ndata: 2\n\n")
				_ = w.Flush()
			})
		})
	})
	resp, err := http.Get(url + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	first := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				first <- sc.Text()
				return
			}
		}
		first <- ""
	}()
	select {
	case got := <-first:
		if got != "event: ready" {
			t.Fatalf("first event = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event did not arrive before the stream ended")
	}
	close(release)
	var rest strings.Builder
	for sc.Scan() {
		rest.WriteString(sc.Text() + "\n")
	}
	if !strings.Contains(rest.String(), "event: end") {
		t.Fatalf("rest of the stream: %q", rest.String())
	}
}

// Shutdown: once the streams are told to end (what App.Close's first step
// does for SSE), the drain returns, within the timeout.
func TestShutdownEndsStreams(t *testing.T) {
	end := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, newServer(func(app *fiber.App) {
			app.Get("/api/v1/events", func(c fiber.Ctx) error {
				c.Set("Content-Type", "text/event-stream")
				return c.SendStreamWriter(func(w *bufio.Writer) {
					_, _ = io.WriteString(w, "event: ready\n\n")
					_ = w.Flush()
					<-end
				})
			})
		}), ln, func() { close(end) })
	}()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("shutdown took %v", d)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown did not return")
	}
	if _, err := io.ReadAll(resp.Body); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Logf("stream ended with %v", err)
	}
}

// A hijacked connection (the crew WebSocket) doesn't hold the shutdown up
// once the stream-ending step closed it: fasthttp waits for hijacked
// connections, net/http didn't.
func TestShutdownWaitsForNoWebSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conn *fws.Conn
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, newServer(func(app *fiber.App) {
			app.Get("/crew/ws", fws.New(func(c *fws.Conn) {
				conn = c
				close(ready)
				for { // until closed
					if _, _, err := c.ReadMessage(); err != nil {
						return
					}
				}
			}))
		}), ln, func() { _ = conn.Close() }) // what Hub.Close does
	}()
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	c, _, err := websocket.Dial(dctx, "http://"+ln.Addr().String()+"/crew/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	<-ready
	start := time.Now()
	cancel()
	select {
	case err := <-served:
		if err != nil || time.Since(start) > 3*time.Second {
			t.Fatalf("shutdown with a websocket open: %v after %v", err, time.Since(start))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown waited for the websocket")
	}
}
