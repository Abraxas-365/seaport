package dockgithub

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// client speaks GitHub's REST API (no SDK: a JWT, a few endpoints).
type client struct {
	api  string // https://api.github.com (GHES: https://host/api/v3)
	http *http.Client

	mu sync.Mutex
	// tokens caches installation tokens (an hour each) by dock and
	// installation; repos caches which installation serves a repository.
	tokens map[tokenKey]installationToken
	repos  map[repoKey]int64
}

type tokenKey struct {
	dock         identity.DockID
	installation int64
	// repo, when set, scopes the token to that repository (owner/name,
	// lowercase): what agents get.
	repo string
}

type repoKey struct {
	dock identity.DockID
	repo string // owner/repo, lowercase
}

type installationToken struct {
	value   string
	expires time.Time
}

// tokenMargin: a cached token closer than this to its expiry is renewed.
const tokenMargin = 5 * time.Minute

func newClient(api string) *client {
	return &client{api: strings.TrimRight(api, "/"), http: &http.Client{Timeout: 30 * time.Second},
		tokens: map[tokenKey]installationToken{}, repos: map[repoKey]int64{}}
}

// appJWT signs the App's 9-minute JWT (RS256, issued a minute in the past
// for clock drift).
func appJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10)})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errx.Wrap(err, "sign github app jwt", errx.TypeInternal)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// parseKey reads the App's private key (GitHub hands out PKCS#1 PEM).
func parseKey(pemText []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemText)
	if block == nil {
		return nil, errx.Validation("github app private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errx.Validation("github app private key can't be read")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errx.Validation("github app private key is not RSA")
	}
	return rk, nil
}

// apiError is GitHub's error body.
type apiError struct {
	Message string `json:"message"`
}

// do calls the API with a bearer (an App JWT, an installation token or
// none) and decodes a JSON answer into out (nil: ignored).
func (c *client) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errx.Wrap(err, "encode github request", errx.TypeInternal)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rd)
	if err != nil {
		return errx.Wrap(err, "github request", errx.TypeInternal)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "seaport")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errx.Wrap(err, "call github", errx.TypeExternal)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return errx.Wrap(err, "read github answer", errx.TypeExternal)
	}
	if res.StatusCode >= 300 {
		var e apiError
		_ = json.Unmarshal(raw, &e)
		msg := "github answered " + strconv.Itoa(res.StatusCode)
		if e.Message != "" {
			msg += ": " + e.Message
		}
		if res.StatusCode == http.StatusNotFound {
			return errx.NotFound(msg)
		}
		return errx.External(msg)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errx.Wrap(err, "decode github answer", errx.TypeExternal)
		}
	}
	return nil
}

// conversion is what GitHub answers when a manifest code is converted.
type conversion struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
}

// convert trades a manifest flow's code (single use, an hour) for the
// App's credentials.
func (c *client) convert(ctx context.Context, code string) (conversion, error) {
	var out conversion
	err := c.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &out)
	return out, err
}

// installation is GitHub's installation object (the fields we keep).
type installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

func (c *client) installation(ctx context.Context, a App, id int64) (installation, error) {
	jwt, err := appJWT(a.AppID, a.key, time.Now())
	if err != nil {
		return installation{}, err
	}
	var out installation
	err = c.do(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(id, 10), jwt, nil, &out)
	return out, err
}

func (c *client) uninstall(ctx context.Context, a App, id int64) error {
	jwt, err := appJWT(a.AppID, a.key, time.Now())
	if err != nil {
		return err
	}
	c.forget(a.Dock)
	return c.do(ctx, http.MethodDelete, "/app/installations/"+strconv.FormatInt(id, 10), jwt, nil, nil)
}

// forget drops the dock's cached tokens and repositories.
func (c *client) forget(dock identity.DockID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.tokens {
		if k.dock == dock {
			delete(c.tokens, k)
		}
	}
	for k := range c.repos {
		if k.dock == dock {
			delete(c.repos, k)
		}
	}
}

// token returns an installation token of the App, cached until close to
// its expiry.
func (c *client) token(ctx context.Context, a App, inst int64) (string, error) {
	t, err := c.scopedToken(ctx, a, inst, "")
	return t.value, err
}

// scopedToken returns an installation token, limited to one repository
// (owner/name) when repo is set (the App's permissions are the ceiling).
func (c *client) scopedToken(ctx context.Context, a App, inst int64, repo string) (installationToken, error) {
	key := tokenKey{a.Dock, inst, strings.ToLower(repo)}
	c.mu.Lock()
	t, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && time.Until(t.expires) > tokenMargin {
		return t, nil
	}
	jwt, err := appJWT(a.AppID, a.key, time.Now())
	if err != nil {
		return installationToken{}, err
	}
	var body any
	if repo != "" {
		_, name, _ := strings.Cut(repo, "/")
		body = map[string][]string{"repositories": {name}}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", jwt, body, &out); err != nil {
		return installationToken{}, err
	}
	if out.Token == "" {
		return installationToken{}, errx.External("github gave no installation token")
	}
	t = installationToken{value: out.Token, expires: out.ExpiresAt}
	c.mu.Lock()
	c.tokens[key] = t
	c.mu.Unlock()
	return t, nil
}

// installationOf says which installation of the App serves repo
// (owner/name), asking GitHub the first time (NotFound: none).
func (c *client) installationOf(ctx context.Context, a App, repo string) (int64, error) {
	key := repoKey{a.Dock, strings.ToLower(repo)}
	c.mu.Lock()
	inst, ok := c.repos[key]
	c.mu.Unlock()
	if ok {
		return inst, nil
	}
	jwt, err := appJWT(a.AppID, a.key, time.Now())
	if err != nil {
		return 0, err
	}
	var out installation
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/installation", jwt, nil, &out); err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.repos[key] = out.ID
	c.mu.Unlock()
	return out.ID, nil
}

// repoToken returns a token of the installation that serves repo
// (owner/name), asking GitHub which one the first time.
func (c *client) repoToken(ctx context.Context, a App, repo string) (string, error) {
	inst, err := c.installationOf(ctx, a, repo)
	if err != nil {
		return "", errx.Wrap(err, "find the installation of "+repo, errx.TypeExternal)
	}
	return c.token(ctx, a, inst)
}

// comment posts an issue (or pull request) comment and returns its id.
func (c *client) comment(ctx context.Context, a App, repo string, number int, body string) (int64, error) {
	tok, err := c.repoToken(ctx, a, repo)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err = c.do(ctx, http.MethodPost, "/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments", tok,
		map[string]string{"body": body}, &out)
	return out.ID, err
}

// react adds a reaction ("eyes") to what path names (an issue, a comment).
func (c *client) react(ctx context.Context, a App, repo, path, content string) error {
	tok, err := c.repoToken(ctx, a, repo)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/repos/"+repo+path+"/reactions", tok, map[string]string{"content": content}, nil)
}
