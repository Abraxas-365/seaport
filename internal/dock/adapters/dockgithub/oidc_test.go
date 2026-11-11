package dockgithub

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type issuer struct {
	srv     *httptest.Server
	keys    map[string]*rsa.PrivateKey
	fetches atomic.Int32
	jwks    string // override of the discovery's jwks_uri
}

func newIssuer(t *testing.T) *issuer {
	is := &issuer{keys: map[string]*rsa.PrivateKey{}}
	is.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			u := is.srv.URL + "/jwks"
			if is.jwks != "" {
				u = is.jwks
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": is.srv.URL, "jwks_uri": u})
		case "/jwks":
			is.fetches.Add(1)
			var keys []map[string]string
			for kid, k := range is.keys {
				keys = append(keys, map[string]string{"kty": "RSA", "kid": kid,
					"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) addKey(t *testing.T, kid string) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is.keys[kid] = k
	return k
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCVerify(t *testing.T) {
	is := newIssuer(t)
	k1 := is.addKey(t, "k1")
	v := newOIDCVerifier(is.srv.URL + "/")
	now := time.Now()
	claims := func(extra map[string]any) map[string]any {
		c := map[string]any{"iss": is.srv.URL, "aud": []string{"other", "https://port.test"}, "exp": now.Add(time.Minute).Unix(),
			"repository": "acme/web", "actor": "carol", "run_id": "1"}
		for k, val := range extra {
			c[k] = val
		}
		return c
	}
	ctx := context.Background()
	c, err := v.verify(ctx, signJWT(t, k1, "k1", "RS256", claims(nil)), "https://port.test", now)
	if err != nil || c.Repository != "acme/web" || c.RunAttempt != "1" {
		t.Fatalf("good token: %+v %v", c, err)
	}
	bad := map[string]string{
		"alg none":     signJWT(t, k1, "k1", "none", claims(nil)),
		"audience":     signJWT(t, k1, "k1", "RS256", claims(map[string]any{"aud": "https://elsewhere.test"})),
		"issuer":       signJWT(t, k1, "k1", "RS256", claims(map[string]any{"iss": "https://evil.test"})),
		"expired":      signJWT(t, k1, "k1", "RS256", claims(map[string]any{"exp": now.Add(-2 * time.Minute).Unix()})),
		"future":       signJWT(t, k1, "k1", "RS256", claims(map[string]any{"nbf": now.Add(5 * time.Minute).Unix()})),
		"no repo":      signJWT(t, k1, "k1", "RS256", claims(map[string]any{"repository": ""})),
		"tampered":     strings.Replace(signJWT(t, k1, "k1", "RS256", claims(nil)), ".", ".e30", 1),
		"garbage":      "a.b",
		"unknown kid":  signJWT(t, k1, "k9", "RS256", claims(nil)),
		"wrong signer": signJWT(t, is.addKeyless(t), "k1", "RS256", claims(nil)),
	}
	for name, tok := range bad {
		if _, err := v.verify(ctx, tok, "https://port.test", now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A rotated key is fetched (unknown kid), at most once a minute.
	before := is.fetches.Load()
	k2 := is.addKey(t, "k2")
	if _, err := v.verify(ctx, signJWT(t, k2, "k2", "RS256", claims(map[string]any{"exp": now.Add(5 * time.Minute).Unix()})), "https://port.test", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if got := is.fetches.Load() - before; got != 1 {
		t.Fatalf("fetches for a rotated key = %d", got)
	}
}

// addKeyless makes a key the issuer doesn't publish.
func (is *issuer) addKeyless(t *testing.T) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A discovery document pointing the key set at another host is refused.
func TestOIDCKeysStayOnTheIssuer(t *testing.T) {
	is := newIssuer(t)
	k := is.addKey(t, "k1")
	is.jwks = "https://evil.test/jwks"
	v := newOIDCVerifier(is.srv.URL)
	tok := signJWT(t, k, "k1", "RS256", map[string]any{"iss": is.srv.URL, "aud": "a", "exp": time.Now().Add(time.Minute).Unix(), "repository": "a/b", "actor": "x", "run_id": "1"})
	if _, err := v.verify(context.Background(), tok, "a", time.Now()); err == nil {
		t.Fatal("key set from another host accepted")
	}
}

func TestActionMessageValidate(t *testing.T) {
	for _, m := range []ActionMessage{{Text: ""}, {Text: "x", Number: -1}, {Text: "x", Agent: "bad name!"}, {Text: "x", ID: "a/b"}} {
		if m.Validate() == nil {
			t.Errorf("%+v valid", m)
		}
	}
	if err := (ActionMessage{Text: "x", Number: 3, Agent: "@qa", ID: "step-1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if m := pullRef.FindStringSubmatch("refs/pull/12/merge"); m == nil || m[1] != "12" {
		t.Fatal("pull ref")
	}
}
