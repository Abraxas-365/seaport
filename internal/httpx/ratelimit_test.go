package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/gofiber/fiber/v3"
)

func TestRateLimit(t *testing.T) {
	if NewRateLimit(0, 10) != nil {
		t.Fatal("rate 0 must disable the limit")
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := NewRateLimit(2, 3)
	l.now = func() time.Time { return now }
	orgA := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	orgB := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	// The principal under test is set by a middleware in front, as the
	// authenticator does.
	var current *Principal
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Use(func(c fiber.Ctx) error {
		if current != nil {
			SetPrincipal(c, *current)
		}
		return c.Next()
	}, l.Middleware())
	app.Get("/api/v1/docks", func(c fiber.Ctx) error { return c.SendStatus(http.StatusTeapot) })
	call := func(p *Principal) *http.Response {
		current = p
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/docks", nil))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	alice := &Principal{Organization: orgA, Role: RoleAdmin, Subject: "alice"}
	for i := range 3 { // the burst
		if w := call(alice); w.StatusCode != http.StatusTeapot {
			t.Fatalf("burst call %d: %d", i, w.StatusCode)
		}
	}
	w := call(alice)
	if w.StatusCode != http.StatusTooManyRequests || w.Header.Get("Retry-After") != "1" {
		t.Fatalf("over the burst: %d Retry-After %q", w.StatusCode, w.Header.Get("Retry-After"))
	}
	// Other callers have their own bucket: another subject, another
	// organization with the same subject, the same subject as crew.
	for _, p := range []*Principal{{Organization: orgA, Role: RoleAdmin, Subject: "bob"},
		{Organization: orgB, Role: RoleAdmin, Subject: "alice"}, {Organization: orgA, Role: RoleCrew, Subject: "alice"},
		{Organization: orgA, Role: RoleAdmin, Subject: "alice", Credential: "token-2"}} {
		if w := call(p); w.StatusCode != http.StatusTeapot {
			t.Errorf("%+v shares alice's bucket: %d", p, w.StatusCode)
		}
	}
	// A refused call spends nothing: half a second later one token is back.
	now = now.Add(500 * time.Millisecond)
	if w := call(alice); w.StatusCode != http.StatusTeapot {
		t.Fatalf("after refill: %d", w.StatusCode)
	}
	if w := call(alice); w.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("refill gave more than one token: %d", w.StatusCode)
	}
	// No principal: the authenticator already refused or it is public.
	if w := call(nil); w.StatusCode != http.StatusTeapot {
		t.Errorf("no principal: %d", w.StatusCode)
	}
	// Idle buckets are swept.
	now = now.Add(2 * time.Minute)
	call(alice)
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("after sweep: %d buckets, want 1", n)
	}
	// nil passes through.
	var off *RateLimit
	app2 := fiber.New()
	app2.Use(off.Middleware())
	app2.Get("/", func(c fiber.Ctx) error { return c.SendStatus(http.StatusTeapot) })
	resp, err := app2.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || resp.StatusCode != http.StatusTeapot {
		t.Errorf("nil RateLimit: %v %v", err, resp)
	}
}
