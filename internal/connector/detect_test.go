package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// fakeChromium answers /json/version and speaks enough CDP for the
// detector: it attaches one page on Target.setAutoAttach, answers
// Page.enable, emits Page.loadEventFired and answers Runtime.evaluate with
// the value the test sets. Calls are recorded.
type fakeChromium struct {
	srv   *httptest.Server
	value map[string]any

	mu    sync.Mutex
	calls []string
	evals int
}

func newFakeChromium(t *testing.T, value map[string]any) *fakeChromium {
	t.Helper()
	f := &fakeChromium{value: value}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/browser/x"})
	})
	mux.HandleFunc("/devtools/browser/x", f.serve)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChromium) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	send := func(v any) {
		raw, _ := json.Marshal(v)
		_ = c.Write(ctx, websocket.MessageText, raw)
	}
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID        int64  `json:"id"`
			SessionID string `json:"sessionId"`
			Method    string `json:"method"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		f.mu.Lock()
		f.calls = append(f.calls, m.Method)
		if m.Method == "Runtime.evaluate" {
			f.evals++
		}
		f.mu.Unlock()
		switch m.Method {
		case "Target.setAutoAttach":
			send(map[string]any{"id": m.ID, "result": map[string]any{}})
			send(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{
				"sessionId": "S1", "targetInfo": map[string]any{"type": "page", "url": "https://app.example.com/login"}}})
			send(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{
				"sessionId": "S2", "targetInfo": map[string]any{"type": "service_worker", "url": "https://app.example.com/sw.js"}}})
		case "Page.enable":
			send(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": map[string]any{}})
			send(map[string]any{"method": "Page.loadEventFired", "sessionId": m.SessionID, "params": map[string]any{"timestamp": 1}})
		case "Runtime.evaluate":
			send(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": map[string]any{"result": map[string]any{"type": "object", "value": f.value}}})
		default:
			send(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": map[string]any{}})
		}
	}
}

func (f *fakeChromium) browser() *Browser {
	return &Browser{Engine: "chromium", URL: f.srv.URL, done: make(chan struct{})}
}

func fastDetector(t *testing.T) {
	t.Helper()
	settle, recheck, retry := detectSettle, detectRecheck, detectRetry
	detectSettle, detectRecheck, detectRetry = 20*time.Millisecond, 60*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { detectSettle, detectRecheck, detectRetry = settle, recheck, retry })
}

func TestWatchPagesReportsAHit(t *testing.T) {
	fastDetector(t)
	chromium := newFakeChromium(t, map[string]any{"host": "App.Example.com", "path": "/login", "kind": "login"})
	hits := make(chan pageHit, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go watchPages(ctx, chromium.browser(), func(h pageHit) { hits <- h })
	select {
	case h := <-hits:
		if h.Host != "app.example.com" || h.Path != "/login" || h.Kind != agent.KindLogin {
			t.Fatalf("hit %+v", h)
		}
	case <-ctx.Done():
		t.Fatal("no hit")
	}
	// The recheck reads the page once more; nothing else runs the script.
	select {
	case <-hits:
	case <-ctx.Done():
		t.Fatal("no recheck")
	}
	chromium.mu.Lock()
	defer chromium.mu.Unlock()
	enables := 0
	for _, c := range chromium.calls {
		if c == "Page.enable" {
			enables++
		}
	}
	if enables != 1 {
		t.Fatalf("Page.enable must run once, for the page target only: %v", chromium.calls)
	}
	if chromium.evals != 2 {
		t.Fatalf("evaluations: %d", chromium.evals)
	}
}

func TestWatchPagesIgnoresAPlainPage(t *testing.T) {
	fastDetector(t)
	chromium := newFakeChromium(t, map[string]any{"host": "app.example.com", "path": "/", "kind": ""})
	hits := make(chan pageHit, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	go watchPages(ctx, chromium.browser(), func(h pageHit) { hits <- h })
	<-ctx.Done()
	if len(hits) != 0 {
		t.Fatalf("a plain page is no hit: %+v", <-hits)
	}
	chromium.mu.Lock()
	defer chromium.mu.Unlock()
	if chromium.evals < 2 {
		t.Fatalf("the page must still be read: %d evaluations", chromium.evals)
	}
}

func TestWatchPagesEndsWithTheBrowser(t *testing.T) {
	fastDetector(t)
	chromium := newFakeChromium(t, map[string]any{"host": "app.example.com", "path": "/", "kind": ""})
	b := chromium.browser()
	done := make(chan struct{})
	go func() {
		watchPages(context.Background(), b, func(pageHit) {})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(b.done)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher must end when the browser does")
	}
}

func TestDetectedDecides(t *testing.T) {
	kit := &agent.Kit{Browser: true, BrowserDetect: true, BrowserIgnoreHosts: []string{"intranet.example.com"}}
	now := time.Now()
	hit := pageHit{Host: "app.example.com", Path: "/login", Kind: agent.KindLogin}

	t.Run("quiet while a person is in the viewer", func(t *testing.T) {
		jobs := newJobTable()
		jobs.viewer(true)
		if ask, _, _ := jobs.detected(hit, kit, now); ask {
			t.Fatal("asked")
		}
		jobs.viewer(false)
		if ask, _, _ := jobs.detected(hit, kit, now); !ask {
			t.Fatal("not asked once the viewer closed")
		}
	})
	t.Run("ignored hosts and their subdomains", func(t *testing.T) {
		jobs := newJobTable()
		for _, h := range []string{"intranet.example.com", "sso.intranet.example.com"} {
			if ask, _, _ := jobs.detected(pageHit{Host: h, Path: "/", Kind: agent.KindLogin}, kit, now); ask {
				t.Fatalf("%s must be ignored", h)
			}
		}
		if ask, _, _ := jobs.detected(pageHit{Host: "notintranet.example.com", Path: "/", Kind: agent.KindLogin}, kit, now); !ask {
			t.Fatal("a host that only ends with the name is not a subdomain")
		}
	})
	t.Run("once per host per DetectEvery", func(t *testing.T) {
		jobs := newJobTable()
		if ask, _, _ := jobs.detected(hit, kit, now); !ask {
			t.Fatal("first")
		}
		if ask, _, _ := jobs.detected(hit, kit, now.Add(agent.DetectEvery/2)); ask {
			t.Fatal("asked again too soon")
		}
		other := pageHit{Host: "other.example.com", Path: "/", Kind: agent.KindCaptcha}
		if ask, _, _ := jobs.detected(other, kit, now.Add(time.Second)); !ask {
			t.Fatal("another host is asked")
		}
		if ask, _, _ := jobs.detected(hit, kit, now.Add(agent.DetectEvery)); !ask {
			t.Fatal("asked again after DetectEvery")
		}
	})
	t.Run("the newest running job's conversation", func(t *testing.T) {
		jobs := newJobTable()
		if ask, conv, running := jobs.detected(hit, kit, now); !ask || conv != nil || len(running) != 0 {
			t.Fatalf("idle: %v %v %d", ask, conv, len(running))
		}
		old := newJob(delivery(identity.NewConversationID()))
		jobs.add(old, now.Add(-time.Minute))
		newest := newJob(delivery(identity.NewConversationID()))
		jobs.add(newest, now)
		ask, conv, running := jobs.detected(hit, kit, now.Add(agent.DetectEvery))
		if !ask || conv == nil || *conv != newest.d.Conversation.ID || len(running) != 2 {
			t.Fatalf("busy: %v %v %d", ask, conv, len(running))
		}
	})
	t.Run("no kit, no ask", func(t *testing.T) {
		jobs := newJobTable()
		if ask, _, _ := jobs.detected(hit, nil, now); ask {
			t.Fatal("asked")
		}
	})
}

func delivery(conv identity.ConversationID) ship.Delivery {
	return ship.Delivery{ID: identity.NewDeliveryID(), Conversation: conversation.Conversation{ID: conv}}
}

func TestDetectorHitAsksAndTellsTheTurns(t *testing.T) {
	jobs := newJobTable()
	j := newJob(delivery(identity.NewConversationID()))
	jobs.add(j, time.Now())
	kit := &agent.Kit{Browser: true, BrowserDetect: true}
	var got crew.TakeoverRequest
	d := newDetector(Config{}, kit, jobs)
	d.post = func(_ context.Context, req crew.TakeoverRequest) (crew.TakeoverAnswer, error) {
		got = req
		return crew.TakeoverAnswer{Host: req.Host, Kind: agent.KindCaptcha, Notified: true}, nil
	}
	d.hit(context.Background(), pageHit{Host: "shop.example.com", Path: "/checkout", Kind: agent.KindCaptcha})
	if !got.Detected || got.Host != "shop.example.com" || got.Kind != "captcha" || got.Path != "/checkout" {
		t.Fatalf("request %+v", got)
	}
	if got.ConversationID == nil || *got.ConversationID != j.d.Conversation.ID {
		t.Fatalf("the running job's conversation must be named: %+v", got.ConversationID)
	}
	select {
	case n := <-j.notes:
		for _, want := range []string{"shop.example.com/checkout", "captcha", "told a person", "Never guess"} {
			if !strings.Contains(n, want) {
				t.Fatalf("note %q lacks %q", n, want)
			}
		}
	default:
		t.Fatal("the running turn was not told")
	}

	// Nobody told: the note says so; the ask is remembered either way.
	jobs2 := newJobTable()
	j2 := newJob(delivery(identity.NewConversationID()))
	jobs2.add(j2, time.Now())
	d2 := newDetector(Config{}, kit, jobs2)
	d2.post = func(_ context.Context, req crew.TakeoverRequest) (crew.TakeoverAnswer, error) {
		return crew.TakeoverAnswer{Host: req.Host, Kind: agent.KindLogin}, nil
	}
	d2.hit(context.Background(), pageHit{Host: "app.example.com", Path: "/", Kind: agent.KindLogin})
	if n := <-j2.notes; !strings.Contains(n, "nobody is configured") || !strings.Contains(n, "sign-in") {
		t.Fatalf("note %q", n)
	}

	// A failed POST tells nobody (logged), and the host is not asked again
	// before DetectEvery.
	calls := 0
	d3 := newDetector(Config{}, kit, newJobTable())
	d3.post = func(context.Context, crew.TakeoverRequest) (crew.TakeoverAnswer, error) {
		calls++
		return crew.TakeoverAnswer{}, errx.External("down")
	}
	d3.hit(context.Background(), pageHit{Host: "app.example.com", Path: "/", Kind: agent.KindLogin})
	d3.hit(context.Background(), pageHit{Host: "app.example.com", Path: "/", Kind: agent.KindLogin})
	if calls != 1 {
		t.Fatalf("posts: %d", calls)
	}
}

func TestCallTakeoversPostsAsTheAgent(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != crew.TakeoversPath {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var b strings.Builder
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		b.Write(buf[:n])
		gotBody = b.String()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"host":"app.example.com","kind":"login","notified":true,"done":false,"pending_takeovers":1}`)
	}))
	defer srv.Close()
	d := newDetector(Config{Server: srv.URL + "/", Token: "sp_crew_x"}, nil, newJobTable())
	ans, err := d.callTakeovers(context.Background(), crew.TakeoverRequest{Host: "app.example.com", Detected: true, Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sp_crew_x" || !strings.Contains(gotBody, `"detected":true`) || !strings.Contains(gotBody, `"path":"/login"`) {
		t.Fatalf("auth %q body %s", gotAuth, gotBody)
	}
	if !ans.Notified || ans.Pending != 1 {
		t.Fatalf("answer %+v", ans)
	}
	// A refusal is an error with the port's message.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"agent tokens only"}}`)
	}))
	defer refuse.Close()
	d = newDetector(Config{Server: refuse.URL, Token: "sp_crew_x"}, nil, newJobTable())
	if _, err := d.callTakeovers(context.Background(), crew.TakeoverRequest{Host: "app.example.com"}); err == nil || !strings.Contains(err.Error(), "agent tokens only") {
		t.Fatalf("err %v", err)
	}
}
