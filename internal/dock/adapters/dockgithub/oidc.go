package dockgithub

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// DefaultOIDCURL is the issuer of GitHub Actions' OIDC tokens on
// github.com (GitHub Enterprise Server: https://HOST/_services/token).
const DefaultOIDCURL = "https://token.actions.githubusercontent.com"

// oidcLeeway tolerates clock skew between GitHub and the port.
const oidcLeeway = time.Minute

// jwksTTL is how long the issuer's keys are trusted before a refetch;
// an unknown key id refetches sooner, at most once per jwksRetry.
const (
	jwksTTL   = time.Hour
	jwksRetry = time.Minute
)

// ActionClaims are the claims of a GitHub Actions OIDC token Seaport
// reads: who ran which workflow of which repository.
type ActionClaims struct {
	Issuer     string   `json:"iss"`
	Audience   audience `json:"aud"`
	Expiry     int64    `json:"exp"`
	NotBefore  int64    `json:"nbf"`
	IssuedAt   int64    `json:"iat"`
	Repository string   `json:"repository"`
	Owner      string   `json:"repository_owner"`
	Actor      string   `json:"actor"`
	Ref        string   `json:"ref"`
	SHA        string   `json:"sha"`
	Event      string   `json:"event_name"`
	Workflow   string   `json:"workflow"`
	RunID      string   `json:"run_id"`
	RunAttempt string   `json:"run_attempt"`
}

// audience is "aud": one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) has(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// oidcVerifier checks GitHub Actions OIDC tokens (RS256) against the
// issuer's published keys.
type oidcVerifier struct {
	issuer string
	http   *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
	tried   time.Time
}

func newOIDCVerifier(issuer string) *oidcVerifier {
	return &oidcVerifier{issuer: strings.TrimRight(issuer, "/"), http: &http.Client{Timeout: 15 * time.Second}}
}

// verify checks the token's signature, issuer, audience and validity
// window, and that it names a repository and a run.
func (v *oidcVerifier) verify(ctx context.Context, token, aud string, now time.Time) (ActionClaims, error) {
	bad := func(why string) (ActionClaims, error) {
		return ActionClaims{}, errx.Unauthorized("invalid GitHub Actions token: " + why)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return bad("not a JWT")
	}
	rawHeader, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	rawClaims, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return bad("not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(rawHeader, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return bad("unsupported algorithm")
	}
	key, err := v.key(ctx, header.Kid, now)
	if err != nil {
		return ActionClaims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return bad("bad signature")
	}
	var c ActionClaims
	if json.Unmarshal(rawClaims, &c) != nil {
		return bad("claims don't decode")
	}
	switch {
	case c.Issuer != v.issuer:
		return bad("wrong issuer")
	case !c.Audience.has(aud):
		return bad("wrong audience (the action must ask for " + aud + ")")
	case c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(oidcLeeway)):
		return bad("expired")
	case c.NotBefore != 0 && now.Add(oidcLeeway).Before(time.Unix(c.NotBefore, 0)):
		return bad("not valid yet")
	case c.Repository == "" || c.RunID == "" || c.Actor == "":
		return bad("no repository, run or actor")
	}
	if c.RunAttempt == "" {
		c.RunAttempt = "1"
	}
	return c, nil
}

// key returns the issuer's key kid, fetching the key set when it is
// stale or doesn't have kid (at most once per jwksRetry).
func (v *oidcVerifier) key(ctx context.Context, kid string, now time.Time) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && now.Sub(v.fetched) < jwksTTL {
		return k, nil
	}
	if now.Sub(v.tried) >= jwksRetry || v.keys == nil {
		v.tried = now
		keys, err := v.fetch(ctx)
		if err != nil {
			if k, ok := v.keys[kid]; ok {
				return k, nil // GitHub unreachable: the key we had still holds
			}
			return nil, err
		}
		v.keys, v.fetched = keys, now
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, errx.Unauthorized("invalid GitHub Actions token: unknown signing key")
}

// fetch reads the issuer's discovery document and its key set (served by
// the issuer's own host).
func (v *oidcVerifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	var disc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.get(ctx, v.issuer+"/.well-known/openid-configuration", &disc); err != nil {
		return nil, err
	}
	iss, _ := url.Parse(v.issuer)
	ju, err := url.Parse(disc.JWKSURI)
	if err != nil || iss == nil || ju.Host != iss.Host || (ju.Scheme != "https" && ju.Scheme != iss.Scheme) {
		return nil, errx.External("GitHub's OIDC discovery names a key set elsewhere")
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := v.get(ctx, disc.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exp := int(new(big.Int).SetBytes(e).Int64())
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
		if pub.N.BitLen() < 2048 || exp < 3 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errx.External("GitHub's OIDC key set has no usable key")
	}
	return keys, nil
}

func (v *oidcVerifier) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errx.Wrap(err, "oidc request", errx.TypeInternal)
	}
	req.Header.Set("Accept", "application/json")
	res, err := v.http.Do(req)
	if err != nil {
		return errx.Wrap(err, "reach GitHub's OIDC issuer", errx.TypeExternal)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return errx.Wrap(err, "read GitHub's OIDC issuer", errx.TypeExternal)
	}
	if res.StatusCode != http.StatusOK {
		return errx.External("GitHub's OIDC issuer answered " + strconv.Itoa(res.StatusCode))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errx.Wrap(err, "decode GitHub's OIDC issuer", errx.TypeExternal)
	}
	return nil
}
