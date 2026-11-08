package authiam

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// fakeIAMKit serves JWKS, /oauth/token and /identity/v1/introspect.
type fakeIAMKit struct {
	*httptest.Server
	key     *rsa.PrivateKey
	idToken func() string
	claims  map[string]any // introspection claims
}

func newFake(t *testing.T) *fakeIAMKit {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIAMKit{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") == "used" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"used"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt",
			"token_type": "Bearer", "expires_in": 900, "id_token": f.idToken()})
	})
	mux.HandleFunc("POST /identity/v1/introspect", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "claims": f.claims})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIAMKit) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	env  = "e0000000-0000-4000-8000-000000000000"
	app  = "a0000000-0000-4000-8000-000000000000"
	res  = "r0000000-0000-4000-8000-000000000000"
	org  = "11111111-1111-4111-8111-111111111111"
	aud  = "https://seaport.test"
	cid  = "web-client"
	back = "https://seaport.test/auth/callback"
)

func provider(f *fakeIAMKit) *Provider {
	return New(Config{URL: f.URL, PublicURL: "https://iam.test", Issuer: f.URL, Environment: env, Application: app,
		Resource: res, Audience: aud, ClientID: cid, RedirectURI: back, AfterLogout: "https://seaport.test/"})
}

func TestExchangeVerifiesTheIDToken(t *testing.T) {
	f := newFake(t)
	p := provider(f)
	good := jwt.MapClaims{"iss": f.URL, "aud": cid, "sub": "u1", "nonce": "n1", "exp": time.Now().Add(time.Hour).Unix()}
	with := func(k string, v any) jwt.MapClaims {
		c := jwt.MapClaims{}
		for a, b := range good {
			c[a] = b
		}
		c[k] = v
		return c
	}
	for name, tc := range map[string]struct {
		token string
		ok    bool
	}{
		"valid":        {f.sign(t, good), true},
		"other nonce":  {f.sign(t, with("nonce", "n2")), false},
		"other client": {f.sign(t, with("aud", "someone-else")), false},
		"other issuer": {f.sign(t, with("iss", "https://evil.test")), false},
		"expired":      {f.sign(t, with("exp", time.Now().Add(-time.Hour).Unix())), false},
		"unsigned": {func() string {
			s, _ := jwt.NewWithClaims(jwt.SigningMethodNone, good).SignedString(jwt.UnsafeAllowNoneSignatureType)
			return s
		}(), false},
		"no id_token": {"", false},
	} {
		f.idToken = func() string { return tc.token }
		tokens, err := p.Exchange(context.Background(), "code", "verifier", "n1")
		if tc.ok != (err == nil) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if tc.ok && (tokens.Access != "at" || tokens.Refresh != "rt" || time.Until(tokens.ExpiresAt) < 14*time.Minute) {
			t.Errorf("%s: tokens = %+v", name, tokens)
		}
		if !tc.ok && !errx.IsType(err, errx.TypeAuthorization) {
			t.Errorf("%s: err type = %v, want unauthorized", name, err)
		}
	}
	f.idToken = func() string { return f.sign(t, good) }
	if _, err := p.Exchange(context.Background(), "used", "v", "n1"); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("invalid_grant err = %v, want unauthorized", err)
	}
}

func TestVerifyAccessNeedsAnOrganizationUserToken(t *testing.T) {
	f := newFake(t)
	p := provider(f)
	base := func(purpose, organization string) map[string]any {
		return map[string]any{"iss": f.URL, "aud": []string{aud}, "sub": "u1", "environment_id": env,
			"application_id": app, "resource_id": res, "purpose": purpose, "organization_id": organization,
			"permissions": []string{"x"}, "exp": time.Now().Add(time.Hour).Unix()}
	}
	f.claims = base("application", org)
	who, err := p.VerifyAccess(context.Background(), "t")
	if err != nil || who.Organization.String() != org || who.Subject != "u1" || who.Expires.IsZero() {
		t.Fatalf("who = %+v, %v", who, err)
	}
	for name, c := range map[string]map[string]any{
		"machine token":   base("machine", org),
		"no organization": base("application", ""),
	} {
		f.claims = c
		if _, err := p.VerifyAccess(context.Background(), "t"); err == nil || !strings.Contains(err.Error(), "organization") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	c := base("application", org)
	c["resource_id"] = "other"
	f.claims = c
	if _, err := p.VerifyAccess(context.Background(), "t"); !errx.IsType(err, errx.TypeAuthorization) {
		t.Errorf("other resource: err = %v", err)
	}
}

func TestAuthorizeURL(t *testing.T) {
	p := provider(newFake(t))
	u, err := url.Parse(p.AuthorizeURL("st", "no", "ch", org, true))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "iam.test" || q.Get("redirect_uri") != back || q.Get("code_challenge_method") != "S256" ||
		q.Get("organization_id") != org || q.Get("prompt") != "select_account" || !strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("authorize URL = %s", u)
	}
	if end := p.EndSessionURL("idt"); !strings.HasPrefix(end, "https://iam.test/oauth/end_session?") ||
		!strings.Contains(end, "post_logout_redirect_uri=") {
		t.Fatalf("end session = %s", end)
	}
}

func TestOutageIsNotASignOut(t *testing.T) {
	p := New(Config{URL: "http://127.0.0.1:1", Issuer: "x", Environment: env, Application: app, Resource: res,
		Audience: aud, ClientID: cid, HTTP: &http.Client{Timeout: time.Second}})
	if _, err := p.VerifyAccess(context.Background(), "t"); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("introspect outage err = %v, want external", err)
	}
	if _, err := p.Refresh(context.Background(), "rt"); !errx.IsType(err, errx.TypeExternal) {
		t.Fatalf("refresh outage err = %v, want external", err)
	}
}
