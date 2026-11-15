package authiam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

const (
	testEnv      = "env-1"
	testResource = "res-seaport"
)

var testOrg = identity.MustParseOrganizationID("11111111-1111-4111-8111-111111111111")

// fakeAdmin is the part of IAMKit the Directory talks to: the token
// endpoint (token exchange) and the organization admin API.
type fakeAdmin struct {
	*httptest.Server
	exchanges  atomic.Int32
	mu         sync.Mutex
	exchangeBy map[string]int    // subject token → exchanges
	deny       map[string]string // subject token → OAuth error code
	life       time.Duration     // lifetime of exchanged tokens
	perms      []string
	apiStatus  func(token string) int // forced status of an admin API call, 0 = serve
	calls      []string
	issued     int
	keys       []map[string]any
}

func newFakeAdmin(t *testing.T) *fakeAdmin {
	t.Helper()
	f := &fakeAdmin{exchangeBy: map[string]int{}, deny: map[string]string{}, life: 10 * time.Minute,
		perms: []string{auth.OrgMembersRead, auth.OrgRolesRead}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		subject := r.PostForm.Get("subject_token")
		f.exchanges.Add(1)
		f.mu.Lock()
		f.exchangeBy[subject]++
		code := f.deny[subject]
		f.issued++
		n := f.issued
		perms, life := f.perms, f.life
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if code != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "no"})
			return
		}
		claims := jwt.MapClaims{"permissions": perms, "exp": time.Now().Add(life).Unix(), "n": n}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("k"))
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": signed, "token_type": "Bearer", "expires_in": int(life.Seconds())})
	})
	admin := "/api/v1/environments/" + testEnv + "/organizations/" + testOrg.String() + "/admin"
	serve := func(w http.ResponseWriter, r *http.Request, body any) {
		token := r.Header.Get("Authorization")
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		status := 0
		if f.apiStatus != nil {
			status = f.apiStatus(token)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "denied"}, "message": "denied"})
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	mux.HandleFunc("GET "+admin+"/roles", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, []map[string]any{
			{"id": "r1", "name": "Viewer", "resource_id": testResource},
			{"id": "r2", "name": "Organization owner", "resource_id": "iam"},
		})
	})
	mux.HandleFunc("GET "+admin+"/members", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, []map[string]any{
			{"user_id": "u1", "user_name": "Ana Torres", "user_email": "ana@example.com", "active": true},
			{"user_id": "u2", "user_name": "Bo Ruiz", "user_email": "bo@example.com", "active": true},
		})
	})
	mux.HandleFunc("GET "+admin+"/members/{user}/roles", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, []map[string]any{{"role_id": "r1", "role_name": "Viewer", "user_id": r.PathValue("user")}})
	})
	mux.HandleFunc("GET "+admin+"/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		keys := f.keys
		f.mu.Unlock()
		serve(w, r, map[string]any{"items": keys, "page": map[string]int{"total": len(keys), "limit": 100, "offset": 0}})
	})
	mux.HandleFunc("GET "+admin+"/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, k := range f.keys {
			if k["id"] == r.PathValue("id") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(k)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAdmin) directory(now func() time.Time) *Directory {
	return NewDirectory(DirectoryConfig{URL: f.URL, Environment: testEnv, Resource: testResource,
		ServerClientID: "server", ServerClientSecret: "secret", Now: now})
}

func (f *fakeAdmin) exchangesFor(subject string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchangeBy[subject]
}

func actorOf(token string) auth.Actor { return auth.Actor{Token: token, Subject: "me"} }

func TestDirectoryExchangesOncePerPersonAndReadsPermissions(t *testing.T) {
	f := newFakeAdmin(t)
	d := f.directory(nil)
	ctx := context.Background()
	for range 3 {
		perms, err := d.Permissions(ctx, testOrg, actorOf("tok-a"))
		if err != nil || len(perms) != 2 || perms[0] != auth.OrgMembersRead {
			t.Fatalf("permissions = %v, %v", perms, err)
		}
	}
	if _, err := d.Roles(ctx, testOrg, actorOf("tok-a")); err != nil {
		t.Fatal(err)
	}
	if got := f.exchangesFor("tok-a"); got != 1 {
		t.Fatalf("exchanges for one person = %d, want 1", got)
	}
	// Another person has their own exchange, never sharing the first's token.
	if _, err := d.Permissions(ctx, testOrg, actorOf("tok-b")); err != nil {
		t.Fatal(err)
	}
	if f.exchangesFor("tok-b") != 1 || f.exchanges.Load() != 2 {
		t.Fatalf("exchanges = %d", f.exchanges.Load())
	}
	// The cache is keyed by a hash: the person's token is not a key.
	for k := range d.cache {
		if k == "tok-a" || k == "tok-b" {
			t.Fatalf("cache is keyed by the raw token %q", k)
		}
	}
}

func TestDirectoryExchangesAgainBeforeTheTokenExpires(t *testing.T) {
	f := newFakeAdmin(t)
	f.life = 10 * time.Minute
	clock := time.Now()
	d := f.directory(func() time.Time { return clock })
	ctx := context.Background()
	if _, err := d.Permissions(ctx, testOrg, actorOf("tok")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(9 * time.Minute) // 1 minute left: still cached
	if _, err := d.Permissions(ctx, testOrg, actorOf("tok")); err != nil || f.exchanges.Load() != 1 {
		t.Fatalf("exchanges = %d, %v (cached until 30 s before expiry)", f.exchanges.Load(), err)
	}
	clock = clock.Add(31 * time.Second) // 29 s left: inside the margin
	if _, err := d.Permissions(ctx, testOrg, actorOf("tok")); err != nil || f.exchanges.Load() != 2 {
		t.Fatalf("exchanges = %d, %v (want a new one 30 s before expiry)", f.exchanges.Load(), err)
	}
}

func TestDirectoryRemembersWhoCantAdminister(t *testing.T) {
	f := newFakeAdmin(t)
	f.deny["viewer"] = "invalid_target"
	clock := time.Now()
	d := f.directory(func() time.Time { return clock })
	ctx := context.Background()
	for range 5 {
		perms, err := d.Permissions(ctx, testOrg, actorOf("viewer"))
		if err != nil || len(perms) != 0 {
			t.Fatalf("permissions = %v, %v (a Viewer is not an error)", perms, err)
		}
	}
	if got := f.exchangesFor("viewer"); got != 1 {
		t.Fatalf("exchanges = %d, want 1 (negative cache)", got)
	}
	// Administration calls are refused without asking IAMKit's admin API.
	_, err := d.Members(ctx, testOrg, actorOf("viewer"))
	var typed *errx.Error
	if !errx.As(err, &typed) || typed.HTTPStatus != http.StatusForbidden {
		t.Fatalf("members = %v, want 403", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("admin API called: %v", f.calls)
	}
	// 60 s later they are asked again (they may have been given the role).
	clock = clock.Add(61 * time.Second)
	f.mu.Lock()
	delete(f.deny, "viewer")
	f.mu.Unlock()
	perms, err := d.Permissions(ctx, testOrg, actorOf("viewer"))
	if err != nil || len(perms) != 2 || f.exchangesFor("viewer") != 2 {
		t.Fatalf("after the negative entry expired: %v, %v, exchanges %d", perms, err, f.exchangesFor("viewer"))
	}
}

func TestDirectoryDoesNotCacheFailures(t *testing.T) {
	f := newFakeAdmin(t)
	f.deny["tok"] = "invalid_grant"
	d := f.directory(nil)
	ctx := context.Background()
	_, err := d.Permissions(ctx, testOrg, actorOf("tok"))
	var typed *errx.Error
	if !errx.As(err, &typed) || typed.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("expired subject token = %v, want 401", err)
	}
	f.mu.Lock()
	delete(f.deny, "tok")
	f.mu.Unlock()
	if _, err := d.Permissions(ctx, testOrg, actorOf("tok")); err != nil {
		t.Fatalf("after recovering: %v", err)
	}
	// An API key (no token) can't act as a person.
	if _, err := d.Permissions(ctx, testOrg, auth.Actor{}); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestDirectoryConcurrentCallersShareOneExchange(t *testing.T) {
	f := newFakeAdmin(t)
	d := f.directory(nil)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Permissions(context.Background(), testOrg, actorOf("tok")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := f.exchanges.Load(); got != 1 {
		t.Fatalf("exchanges = %d, want 1", got)
	}
}

func TestDirectoryRetriesOnceWhenTheExchangedTokenWasRevoked(t *testing.T) {
	f := newFakeAdmin(t)
	var first sync.Once
	var stale string
	f.apiStatus = func(token string) int {
		first.Do(func() { stale = token })
		if token == stale {
			return http.StatusUnauthorized
		}
		return 0
	}
	d := f.directory(nil)
	list, err := d.Members(context.Background(), testOrg, actorOf("tok"))
	if err != nil || len(list) != 2 {
		t.Fatalf("members = %v, %v", list, err)
	}
	if f.exchanges.Load() != 2 {
		t.Fatalf("exchanges = %d, want 2 (one retry with a fresh token)", f.exchanges.Load())
	}
}

func TestDirectoryDoesNotLoopOnPersistent401(t *testing.T) {
	f := newFakeAdmin(t)
	f.apiStatus = func(string) int { return http.StatusUnauthorized }
	d := f.directory(nil)
	_, err := d.Members(context.Background(), testOrg, actorOf("tok"))
	var typed *errx.Error
	if !errx.As(err, &typed) || typed.HTTPStatus == http.StatusUnauthorized || typed.HTTPStatus < 500 {
		t.Fatalf("err = %v: IAMKit's 401 must not sign the person out (want 5xx)", err)
	}
	if f.exchanges.Load() != 2 {
		t.Fatalf("exchanges = %d, want 2", f.exchanges.Load())
	}
}

func TestDirectoryPassesIAMKitRefusalsThrough(t *testing.T) {
	f := newFakeAdmin(t)
	f.apiStatus = func(string) int { return http.StatusForbidden }
	d := f.directory(nil)
	_, err := d.Roles(context.Background(), testOrg, actorOf("tok"))
	var typed *errx.Error
	if !errx.As(err, &typed) || typed.HTTPStatus != http.StatusForbidden {
		t.Fatalf("err = %v, want 403", err)
	}
	if f.exchanges.Load() != 1 {
		t.Fatalf("a 403 must not exchange again: %d", f.exchanges.Load())
	}
}

func TestDirectoryMarksSeaportRolesAndReadsMemberRoles(t *testing.T) {
	f := newFakeAdmin(t)
	d := f.directory(nil)
	ctx := context.Background()
	roles, err := d.Roles(ctx, testOrg, actorOf("tok"))
	if err != nil || len(roles) != 2 || !roles[0].Seaport || roles[1].Seaport {
		t.Fatalf("roles = %+v, %v", roles, err)
	}
	members, err := d.Members(ctx, testOrg, actorOf("tok"))
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	for _, m := range members {
		if len(m.Roles) != 1 || m.Roles[0].ID != "r1" || m.Email == "" {
			t.Fatalf("member = %+v", m)
		}
	}
	if _, err := d.Member(ctx, testOrg, actorOf("tok"), "nobody"); err == nil {
		t.Fatal("unknown member found")
	}
}

func TestDirectoryListsOnlyOrganizationKeysOnSeaport(t *testing.T) {
	f := newFakeAdmin(t)
	f.keys = []map[string]any{
		{"id": "k1", "name": "ci", "owner": map[string]string{"type": "organization"}, "resource_id": testResource, "state": "active", "expires": true, "expires_at": "2027-01-02T03:04:05Z", "permissions": []string{"seaport:docks:read"}},
		{"id": "k2", "name": "personal", "owner": map[string]string{"type": "user"}, "resource_id": testResource, "state": "active"},
		{"id": "k3", "name": "elsewhere", "owner": map[string]string{"type": "organization"}, "resource_id": "other", "state": "active"},
	}
	d := f.directory(nil)
	ctx := context.Background()
	keys, err := d.APIKeys(ctx, testOrg, actorOf("tok"))
	if err != nil || len(keys) != 1 || keys[0].ID != "k1" || keys[0].ExpiresAt == nil {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	if _, err := d.APIKey(ctx, testOrg, actorOf("tok"), "k1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"k2", "k3"} {
		_, err := d.APIKey(ctx, testOrg, actorOf("tok"), id)
		var typed *errx.Error
		if !errx.As(err, &typed) || typed.HTTPStatus != http.StatusNotFound {
			t.Fatalf("key %s: %v, want 404", id, err)
		}
	}
}
