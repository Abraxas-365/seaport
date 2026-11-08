package bootstrap_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/coder/websocket"
)

type errorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// Dock quota: concurrent creations can't overshoot, it is per
// organization, and deleting a dock frees a slot.
func TestDockQuota(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.MaxDocks = 3 }, bootstrap.Options{Authenticator: twoOrgs{}})
	create := func(token, name string) (dock.Dock, int, errorBody) {
		var out struct {
			dock.Dock
			errorBody
		}
		code := p.call(http.MethodPost, "/api/v1/docks", token, map[string]string{"channel": "api", "name": name}, &out)
		return out.Dock, code, out.errorBody
	}
	first, code, _ := create(adminA, "a0")
	if code != http.StatusCreated {
		t.Fatalf("first dock: %d", code)
	}
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			var d dock.Dock
			code := p.call(http.MethodPost, "/api/v1/docks", adminA, map[string]string{"channel": "api", "name": fmt.Sprint("a", i+1)}, &d)
			mu.Lock()
			codes[code]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if codes[http.StatusCreated] != 2 || codes[http.StatusUnprocessableEntity] != 6 {
		t.Fatalf("dock quota under concurrency: %v", codes)
	}
	if _, code, e := create(adminA, "over"); code != http.StatusUnprocessableEntity ||
		e.Error.Code != "QUOTA_EXCEEDED" || e.Error.Details["quota"] != "docks" || e.Error.Details["limit"] != float64(3) {
		t.Fatalf("over quota: %d %+v", code, e)
	}
	// Another organization has its own quota.
	if _, code, _ := create(adminB, "b0"); code != http.StatusCreated {
		t.Fatalf("org B: %d", code)
	}
	// Deleting frees a slot.
	if code := p.call(http.MethodDelete, "/api/v1/docks/"+first.ID.String(), adminA, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, code, _ := create(adminA, "again"); code != http.StatusCreated {
		t.Fatalf("after delete: %d", code)
	}
}

// Crew connection quota: refused before the upgrade (422), per
// organization, and a disconnect frees the slot.
func TestCrewConnectionQuota(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.MaxCrewConnections = 2 }, bootstrap.Options{Authenticator: twoOrgs{}})
	hello := crew.Frame{Type: crew.FrameHello, Agent: "a", Concurrency: 1}
	first := p.dialCrewAs(crewA, hello)
	p.dialCrewAs(crewA, hello)

	dial := func(token string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.srv.URL, "http")+"/crew/ws",
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	}
	ws, resp, err := dial(crewA)
	if err == nil {
		ws.CloseNow()
		t.Fatal("third connection accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("third connection: %v %v", err, resp)
	}
	p.dialCrewAs(crewB, crew.Frame{Type: crew.FrameHello, Agent: "b", Concurrency: 1})

	first.ws.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, _, err := dial(crewA)
		if err == nil {
			ws.CloseNow()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not freed after disconnect: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The organization's hourly outbound quota spans its docks.
func TestOrganizationSendQuota(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) {
		c.SendPerMinute, c.SendPerHour, c.SendPerChatMinute, c.SendOrgPerHour = 0, 0, 0, 3
	}, bootstrap.Options{})
	conv := func(name string) string {
		var d dock.Dock
		if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": name}, &d); code != http.StatusCreated {
			t.Fatalf("create dock: %d", code)
		}
		s := p.inject(d, "111", "m-"+name, "hola")
		if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+s.Conversation.String(),
			map[string]string{"access": "allow"}, nil); code != http.StatusOK {
			t.Fatalf("allow: %d", code)
		}
		return s.Conversation.String()
	}
	a, b := conv("one"), conv("two")
	for i, c := range []string{a, a, b} {
		if code := p.admin(http.MethodPost, "/api/v1/conversations/"+c+"/ships", map[string]string{"text": fmt.Sprint("r", i)}, nil); code != http.StatusCreated {
			t.Fatalf("reply %d: %d", i, code)
		}
	}
	var e errorBody
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+b+"/ships", map[string]string{"text": "over"}, &e); code != http.StatusTooManyRequests ||
		e.Error.Details["retry_after_seconds"] == nil {
		t.Fatalf("org quota: %d %+v", code, e)
	}
}

// The API rate limit is wired after auth, per caller, and answers 429 with
// Retry-After (exact bucket arithmetic: httpx's unit test, fake clock).
func TestAPIRateLimit(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.APIRate, c.APIBurst = 1, 5 }, bootstrap.Options{Authenticator: twoOrgs{}})
	var resp *http.Response
	ok := 0
	for range 20 {
		req, _ := http.NewRequest(http.MethodGet, p.srv.URL+"/api/v1/docks", nil)
		req.Header.Set("Authorization", "Bearer "+adminA)
		var err error
		if resp, err = http.DefaultClient.Do(req); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			break
		}
		ok++
	}
	if ok < 5 || resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d OKs: %d Retry-After=%q", ok, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", adminB, nil, nil); code != http.StatusOK {
		t.Fatalf("another caller is limited too: %d", code)
	}
	// Public routes are not limited.
	if code := p.call(http.MethodGet, "/healthz", "", nil, nil); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}
}
