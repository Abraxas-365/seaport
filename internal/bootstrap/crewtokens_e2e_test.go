package bootstrap_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Crew tokens: created in the admin, shown once, usable on /crew/ws and
// /mcp, revoked → refused and live connections closed.
func TestCrewTokens(t *testing.T) {
	// No static SEAPORT_CREW_TOKEN: agents only use crew tokens.
	p := startPortWith(t, func(c *config.Server) { c.CrewToken = "" }, bootstrap.Options{})

	var issued crew.Issued
	if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": "laptop"}, &issued); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if !strings.HasPrefix(issued.Value, crew.TokenPrefix) || !strings.HasPrefix(issued.Value, issued.Hint) || issued.CreatedBy != "admin" {
		t.Fatalf("issued: %+v", issued)
	}
	for _, bad := range []map[string]any{{"name": ""}, {"name": "x", "expires_at": time.Now().Add(-time.Hour)}, {"name": "x", "nope": 1}} {
		if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", bad, nil); code != http.StatusBadRequest {
			t.Fatalf("create %v: %d", bad, code)
		}
	}

	var list query.Paginated[map[string]any]
	if code := p.admin(http.MethodGet, "/api/v1/crew/tokens", nil, &list); code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list: %d %+v", code, list)
	}
	if _, leaked := list.Items[0]["value"]; leaked {
		t.Fatal("list returns the token value")
	}

	// The token works as crew, never as admin; no token or a wrong one is 401.
	for _, tok := range []string{"", crew.TokenPrefix + strings.Repeat("A", 43), crewToken} {
		if code := p.call(http.MethodPost, "/mcp", tok, map[string]any{}, nil); code != http.StatusUnauthorized {
			t.Fatalf("mcp with %q: %d", tok, code)
		}
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", issued.Value, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("crew token on admin api: %d", code)
	}
	c := p.dialCrewAs(issued.Value, crew.Frame{Type: crew.FrameHello, Agent: "laptop", Concurrency: 1})
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: p.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{issued.Value}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// Revoking closes the live connection and refuses the token.
	if code := p.admin(http.MethodDelete, "/api/v1/crew/tokens/"+issued.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	select {
	case _, ok := <-c.frames:
		for ok {
			_, ok = <-c.frames
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked token's connection still open")
	}
	if code := p.call(http.MethodPost, "/mcp", issued.Value, map[string]any{}, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token on mcp: %d", code)
	}
	if code := p.admin(http.MethodDelete, "/api/v1/crew/tokens/"+issued.ID.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/crew/tokens", nil, &list); code != http.StatusOK || len(list.Items) != 0 {
		t.Fatalf("list after revoke: %d %+v", code, list)
	}

	// An expiring token works until it expires; its connection ends then.
	var short crew.Issued
	if code := p.admin(http.MethodPost, "/api/v1/crew/tokens",
		map[string]any{"name": "short", "expires_at": time.Now().Add(1500 * time.Millisecond)}, &short); code != http.StatusCreated {
		t.Fatalf("create expiring: %d", code)
	}
	sc := p.dialCrewAs(short.Value, crew.Frame{Type: crew.FrameHello, Agent: "short", Concurrency: 1})
	select {
	case _, ok := <-sc.frames:
		for ok {
			_, ok = <-sc.frames
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expired token's connection still open")
	}
	if code := p.call(http.MethodPost, "/mcp", short.Value, map[string]any{}, nil); code != http.StatusUnauthorized {
		t.Fatalf("expired token on mcp: %d", code)
	}
}

// Another organization's crew tokens are invisible: listing shows only
// one's own, revoking another's is 404.
func TestCrewTokensAreIsolated(t *testing.T) {
	p := startPortWith(t, func(*config.Server) {}, bootstrap.Options{Authenticator: twoOrgs{}})
	var a crew.Issued
	if code := p.call(http.MethodPost, "/api/v1/crew/tokens", adminA, map[string]any{"name": "a"}, &a); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	var list query.Paginated[crew.Token]
	if code := p.call(http.MethodGet, "/api/v1/crew/tokens", adminB, nil, &list); code != http.StatusOK || len(list.Items) != 0 {
		t.Fatalf("B lists A's tokens: %d %+v", code, list)
	}
	if code := p.call(http.MethodDelete, "/api/v1/crew/tokens/"+a.ID.String(), adminB, nil, nil); code != http.StatusNotFound {
		t.Fatalf("B revokes A's token: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/crew/tokens", adminA, nil, &list); code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("A lists: %d %+v", code, list)
	}
}

// The per-organization cap holds under concurrent creations (count and
// insert are atomic), and revoked tokens free their slot.
func TestCrewTokenCap(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.CrewToken = "" }, bootstrap.Options{})
	var last crew.Issued
	for i := range crew.MaxTokens - 2 {
		if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": fmt.Sprint("t", i)}, &last); code != http.StatusCreated {
			t.Fatalf("create %d: %d", i, code)
		}
	}
	var created, full atomic.Int32
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": fmt.Sprint("race", i)}, nil); code {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusUnprocessableEntity:
				full.Add(1)
			default:
				t.Errorf("concurrent create: %d", code)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 2 || full.Load() != 8 {
		t.Fatalf("concurrent creations: %d created, %d refused; want 2 and 8", created.Load(), full.Load())
	}
	if code := p.admin(http.MethodDelete, "/api/v1/crew/tokens/"+last.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/crew/tokens", map[string]any{"name": "after revoke"}, nil); code != http.StatusCreated {
		t.Fatalf("create after revoke: %d", code)
	}
}
