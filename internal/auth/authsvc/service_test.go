package authsvc

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

var orgA = identity.MustParseOrganizationID("11111111-1111-4111-8111-111111111111")
var orgB = identity.MustParseOrganizationID("22222222-2222-4222-8222-222222222222")

type fakeProvider struct {
	mu        sync.Mutex
	refreshes atomic.Int32
	// access token → identity
	tokens      map[string]auth.Identity
	refreshErr  error
	revoked     []string
	gotVerifier string
	gotNonce    string
	issue       int
}

func newProvider() *fakeProvider { return &fakeProvider{tokens: map[string]auth.Identity{}} }

func (f *fakeProvider) mint(who auth.Identity, ttl time.Duration) auth.Tokens {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issue++
	n := string(rune('a' + f.issue))
	f.tokens["access-"+n] = who
	return auth.Tokens{Access: "access-" + n, Refresh: "refresh-" + n, IDToken: "id-" + n, ExpiresAt: time.Now().Add(ttl)}
}

func (f *fakeProvider) AuthorizeURL(state, nonce, challenge, org string, sel bool) string {
	return "https://iam.test/oauth/authorize?state=" + state + "&org=" + org
}

func (f *fakeProvider) Exchange(ctx context.Context, code, verifier, nonce string) (auth.Tokens, error) {
	f.gotVerifier, f.gotNonce = verifier, nonce
	if code != "good" {
		return auth.Tokens{}, errx.Unauthorized("bad code")
	}
	return f.mint(auth.Identity{Organization: orgA, Subject: "user-1", Permissions: []string{"p"}}, time.Hour), nil
}

func (f *fakeProvider) Refresh(ctx context.Context, refresh string) (auth.Tokens, error) {
	f.refreshes.Add(1)
	time.Sleep(10 * time.Millisecond) // widen the race window
	if f.refreshErr != nil {
		return auth.Tokens{}, f.refreshErr
	}
	return f.mint(auth.Identity{Organization: orgA, Subject: "user-1", Permissions: []string{"p"}}, time.Hour), nil
}

func (f *fakeProvider) Revoke(ctx context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return nil
}

func (f *fakeProvider) VerifyAccess(ctx context.Context, token string) (auth.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	who, ok := f.tokens[token]
	if !ok {
		return auth.Identity{}, errx.Unauthorized("inactive")
	}
	return who, nil
}

func (f *fakeProvider) VerifyAPIKey(ctx context.Context, key string) (auth.Identity, error) {
	return auth.Identity{Organization: orgB, Subject: "api-key:1"}, nil
}
func (f *fakeProvider) IsAPIKey(v string) bool { return strings.HasPrefix(v, "ik_key_") }
func (f *fakeProvider) Profile(ctx context.Context, access, org string) (auth.Profile, error) {
	return auth.Profile{Name: "Ana", OrganizationName: "Acme"}, nil
}
func (f *fakeProvider) EndSessionURL(id string) string { return "https://iam.test/end?hint=" + id }

// memRepo serializes Update like SELECT … FOR UPDATE does.
type memRepo struct {
	mu   sync.Mutex
	rows map[string]auth.Session
}

func (m *memRepo) Create(ctx context.Context, s auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[s.IDHash] = s
	return nil
}

func (m *memRepo) Find(ctx context.Context, h string) (auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[h]
	if !ok {
		return auth.Session{}, errx.NotFound("session not found")
	}
	return s, nil
}

func (m *memRepo) Update(ctx context.Context, h string, fn func(auth.Session) (auth.Session, error)) (auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[h]
	if !ok {
		return auth.Session{}, errx.NotFound("session not found")
	}
	next, err := fn(s)
	if err != nil {
		return auth.Session{}, err
	}
	m.rows[h] = next
	return next, nil
}

func (m *memRepo) Delete(ctx context.Context, h string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, h)
	return nil
}

func (m *memRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) { return 0, nil }

func setup(t *testing.T) (*Service, *fakeProvider, *memRepo) {
	t.Helper()
	p, r := newProvider(), &memRepo{rows: map[string]auth.Session{}}
	return New(p, r, 24*time.Hour), p, r
}

// signIn runs Begin + Complete and returns the session id.
func signIn(t *testing.T, s *Service) string {
	t.Helper()
	az, err := s.Begin(context.Background(), auth.Begin{Next: "/docks"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Complete(context.Background(), az.Pending, auth.Callback{Code: "good", State: az.Pending.State})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSignInOpensASession(t *testing.T) {
	s, p, r := setup(t)
	az, err := s.Begin(context.Background(), auth.Begin{Next: "/docks", Organization: orgA.String()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(az.URL, "org="+orgA.String()) || az.Pending.Next != "/docks" {
		t.Fatalf("authorization = %+v", az)
	}
	id, sess, err := s.Complete(context.Background(), az.Pending, auth.Callback{Code: "good", State: az.Pending.State})
	if err != nil {
		t.Fatal(err)
	}
	if p.gotVerifier != az.Pending.Verifier || p.gotNonce != az.Pending.Nonce {
		t.Fatal("exchange must get the pending verifier and nonce")
	}
	if _, ok := r.rows[auth.HashSessionID(id)]; !ok || sess.Organization != orgA || sess.Profile.OrganizationName != "Acme" {
		t.Fatalf("session = %+v (stored under the id hash: %v)", sess, ok)
	}
	if _, ok := r.rows[id]; ok {
		t.Fatal("the raw session id must never be stored")
	}
	who, _, err := s.Authenticate(context.Background(), id)
	if err != nil || who.Organization != orgA || who.Subject != "user-1" {
		t.Fatalf("authenticate = %+v, %v", who, err)
	}
}

func TestCompleteRefusesForeignOrExpiredState(t *testing.T) {
	s, _, _ := setup(t)
	az, _ := s.Begin(context.Background(), auth.Begin{})
	for name, tc := range map[string]struct {
		p  auth.Pending
		cb auth.Callback
	}{
		"other state":  {az.Pending, auth.Callback{Code: "good", State: "forged"}},
		"expired":      {func() auth.Pending { p := az.Pending; p.Expires = time.Now().Add(-time.Second); return p }(), auth.Callback{Code: "good", State: az.Pending.State}},
		"iamkit error": {az.Pending, auth.Callback{Error: "access_denied", State: az.Pending.State}},
		"no code":      {az.Pending, auth.Callback{State: az.Pending.State}},
		"refused code": {az.Pending, auth.Callback{Code: "bad", State: az.Pending.State}},
		"no pending":   {auth.Pending{}, auth.Callback{Code: "good", State: az.Pending.State}},
	} {
		if _, _, err := s.Complete(context.Background(), tc.p, tc.cb); err == nil {
			t.Errorf("%s: sign-in accepted", name)
		}
	}
	if _, err := s.Begin(context.Background(), auth.Begin{Next: "//evil.example"}); err == nil {
		t.Error("an off-site next must be refused")
	}
}

func TestConcurrentRequestsRefreshOnce(t *testing.T) {
	s, p, r := setup(t)
	id := signIn(t, s)
	h := auth.HashSessionID(id)
	sess := r.rows[h]
	sess.Tokens.ExpiresAt = time.Now().Add(10 * time.Second) // inside RefreshAhead
	r.rows[h] = sess

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Authenticate(context.Background(), id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := p.refreshes.Load(); n != 1 {
		t.Fatalf("refreshed %d times; a rotated refresh token must be used once", n)
	}
	if got := r.rows[h].Tokens; got.Access == sess.Tokens.Access || got.IDToken == "" {
		t.Fatalf("tokens not replaced: %+v", got)
	}
}

func TestRefusedRefreshEndsTheSession(t *testing.T) {
	s, p, r := setup(t)
	id := signIn(t, s)
	h := auth.HashSessionID(id)
	sess := r.rows[h]
	sess.Tokens.ExpiresAt = time.Now()
	r.rows[h] = sess
	p.refreshErr = errx.Unauthorized("revoked")
	if _, _, err := s.Authenticate(context.Background(), id); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if _, ok := r.rows[h]; ok {
		t.Fatal("a session IAMKit refused must be deleted")
	}
}

func TestIAMKitOutageKeepsTheSession(t *testing.T) {
	s, p, r := setup(t)
	id := signIn(t, s)
	h := auth.HashSessionID(id)
	sess := r.rows[h]
	sess.Tokens.ExpiresAt = time.Now()
	r.rows[h] = sess
	p.refreshErr = errx.External("IAMKit down")
	if _, _, err := s.Authenticate(context.Background(), id); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("err = %v, want external", err)
	}
	if _, ok := r.rows[h]; !ok {
		t.Fatal("an outage must not sign people out")
	}
}

func TestTokenOfAnotherOrganizationEndsTheSession(t *testing.T) {
	s, p, r := setup(t)
	id := signIn(t, s)
	h := auth.HashSessionID(id)
	p.tokens[r.rows[h].Tokens.Access] = auth.Identity{Organization: orgB, Subject: "user-1"}
	if _, _, err := s.Authenticate(context.Background(), id); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := r.rows[h]; ok {
		t.Fatal("session kept after its organization changed")
	}
}

func TestExpiredSessionIsGone(t *testing.T) {
	s, _, r := setup(t)
	id := signIn(t, s)
	h := auth.HashSessionID(id)
	sess := r.rows[h]
	sess.ExpiresAt = time.Now().Add(-time.Minute)
	r.rows[h] = sess
	if _, _, err := s.Authenticate(context.Background(), id); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.Authenticate(context.Background(), "unknown"); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("unknown id err = %v", err)
	}
}

func TestLogoutRevokesAndDeletes(t *testing.T) {
	s, p, r := setup(t)
	id := signIn(t, s)
	sess := r.rows[auth.HashSessionID(id)]
	end, err := s.Logout(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(end, sess.Tokens.IDToken) || len(p.revoked) != 1 || p.revoked[0] != sess.Tokens.Refresh {
		t.Fatalf("end = %q revoked = %v", end, p.revoked)
	}
	if len(r.rows) != 0 {
		t.Fatal("session kept after logout")
	}
	if _, _, err := s.Authenticate(context.Background(), id); err == nil {
		t.Fatal("signed-out session still works")
	}
}

func TestBearerPicksAPIKeyOrAccessToken(t *testing.T) {
	s, p, _ := setup(t)
	who, err := s.Bearer(context.Background(), "ik_key_x")
	if err != nil || who.Organization != orgB {
		t.Fatalf("api key: %+v %v", who, err)
	}
	tok := p.mint(auth.Identity{Organization: orgA, Subject: "u"}, time.Hour)
	if who, err = s.Bearer(context.Background(), tok.Access); err != nil || who.Organization != orgA {
		t.Fatalf("access: %+v %v", who, err)
	}
	if _, err = s.Bearer(context.Background(), ""); err == nil {
		t.Fatal("empty bearer accepted")
	}
}
