package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

func TestRateLimit(t *testing.T) {
	if NewRateLimit(0, 10) != nil {
		t.Fatal("rate 0 must disable the limit")
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := NewRateLimit(2, 3)
	l.now = func() time.Time { return now }
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	orgA := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	orgB := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	call := func(p *Principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/docks", nil)
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), *p))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	alice := &Principal{Organization: orgA, Role: RoleAdmin, Subject: "alice"}
	for i := range 3 { // the burst
		if w := call(alice); w.Code != http.StatusTeapot {
			t.Fatalf("burst call %d: %d", i, w.Code)
		}
	}
	w := call(alice)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("over the burst: %d Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// Other callers have their own bucket: another subject, another
	// organization with the same subject, the same subject as crew.
	for _, p := range []*Principal{{Organization: orgA, Role: RoleAdmin, Subject: "bob"},
		{Organization: orgB, Role: RoleAdmin, Subject: "alice"}, {Organization: orgA, Role: RoleCrew, Subject: "alice"},
		{Organization: orgA, Role: RoleAdmin, Subject: "alice", Credential: "token-2"}} {
		if w := call(p); w.Code != http.StatusTeapot {
			t.Errorf("%+v shares alice's bucket: %d", p, w.Code)
		}
	}
	// A refused call spends nothing: half a second later one token is back.
	now = now.Add(500 * time.Millisecond)
	if w := call(alice); w.Code != http.StatusTeapot {
		t.Fatalf("after refill: %d", w.Code)
	}
	if w := call(alice); w.Code != http.StatusTooManyRequests {
		t.Fatalf("refill gave more than one token: %d", w.Code)
	}
	// No principal: the authenticator already refused or it is public.
	if w := call(nil); w.Code != http.StatusTeapot {
		t.Errorf("no principal: %d", w.Code)
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
	if off.Wrap(h) == nil {
		t.Error("nil RateLimit must return the handler")
	}
}
