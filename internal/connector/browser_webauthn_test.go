package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// guardChromium speaks the CDP the WebAuthn guard needs: on the browser's
// Target.setAutoAttach it attaches a page waiting for the debugger, a page
// already running and a waiting service worker, and it records every call
// as "<session> <method> <params>".
type guardChromium struct {
	srv *httptest.Server

	mu    sync.Mutex
	calls []string
	conns int
}

func newGuardChromium(t *testing.T) *guardChromium {
	t.Helper()
	f := &guardChromium{}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/browser/g"})
	})
	mux.HandleFunc("/devtools/browser/g", f.serve)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *guardChromium) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	f.mu.Lock()
	f.conns++
	first := f.conns == 1
	f.mu.Unlock()
	ctx := r.Context()
	send := func(v any) {
		raw, _ := json.Marshal(v)
		_ = c.Write(ctx, websocket.MessageText, raw)
	}
	attach := func(session, kind string, waiting bool) {
		send(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{
			"sessionId": session, "waitingForDebugger": waiting,
			"targetInfo": map[string]any{"type": kind, "url": "https://app.example.com/"}}})
	}
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID        int64           `json:"id"`
			SessionID string          `json:"sessionId"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		params := string(m.Params)
		if params == "" {
			params = "null"
		}
		f.mu.Lock()
		f.calls = append(f.calls, strings.TrimSpace(m.SessionID+" "+m.Method+" "+params))
		f.mu.Unlock()
		send(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": map[string]any{}})
		if m.Method == "Target.setAutoAttach" && m.SessionID == "" {
			attach("NEW", "page", true)
			attach("OLD", "page", false)
			attach("SW", "service_worker", true)
			if first {
				return // the guard loses the browser once and dials again
			}
		}
	}
}

func (f *guardChromium) snapshot() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls), f.conns
}

func TestGuardWebAuthnEnablesEveryPageAndResumesWaitingTargets(t *testing.T) {
	retry := webauthnRetry
	webauthnRetry = 20 * time.Millisecond
	t.Cleanup(func() { webauthnRetry = retry })
	chromium := newGuardChromium(t)
	b := &Browser{Engine: "chromium", URL: chromium.srv.URL, done: make(chan struct{})}
	ended := make(chan struct{})
	go func() {
		guardWebAuthn(context.Background(), b)
		close(ended)
	}()

	const (
		attach  = `Target.setAutoAttach {"autoAttach":true,"flatten":true,"waitForDebuggerOnStart":true}`
		enable  = `WebAuthn.enable {"enableUI":false}`
		resume  = `Runtime.runIfWaitingForDebugger null`
		pageNew = "NEW "
	)
	// The first connection is lost right after it attaches (nothing more is
	// read on it); the second one guards the three targets.
	want := func(calls []string) bool {
		return count(calls, attach) == 2 &&
			count(calls, "NEW "+enable) == 1 && count(calls, "NEW "+attach) == 1 && count(calls, "NEW "+resume) == 1 &&
			count(calls, "OLD "+enable) == 1 && count(calls, "OLD "+attach) == 1 &&
			count(calls, "SW "+resume) == 1
	}
	deadline := time.Now().Add(5 * time.Second)
	var calls []string
	var conns int
	for {
		calls, conns = chromium.snapshot()
		if want(calls) && conns == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connections %d, calls:\n%s", conns, strings.Join(calls, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A running page is never "resumed"; a worker gets no WebAuthn.
	for _, c := range calls {
		if c == "OLD "+resume || c == "SW "+enable || c == "SW "+attach {
			t.Fatalf("unexpected call %q in:\n%s", c, strings.Join(calls, "\n"))
		}
	}
	// A page that waits is guarded before it runs.
	if i, j := slices.Index(calls, pageNew+enable), slices.Index(calls, pageNew+resume); i < 0 || j < i {
		t.Fatalf("the page must be guarded before it is resumed:\n%s", strings.Join(calls, "\n"))
	}

	close(b.done)
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the guard must end with the browser")
	}
}

func count(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}
