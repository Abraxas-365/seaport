// Package auth is sign-in for iam mode (SEAPORT_AUTH=iam): IAMKit
// authenticates people, Seaport keeps their browser sessions on the server
// (backend-for-frontend: tokens never reach page JavaScript) and checks
// every request's token with IAMKit, so permission changes and sign-outs
// apply at once.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

const (
	// RefreshAhead: an access token closer than this to its expiry is
	// refreshed before use.
	RefreshAhead = 60 * time.Second
	// PendingTTL bounds a sign-in between leaving for IAMKit and coming back.
	PendingTTL = 10 * time.Minute
	// MaxNext bounds the path a sign-in returns to.
	MaxNext = 2048
)

// Identity is who a request is, as IAMKit confirmed it.
type Identity struct {
	Organization identity.OrganizationID
	// Subject is the IAMKit user (or API key) id.
	Subject string
	// Name is who that is, for people reading records ("Ana",
	// "API key ci"); empty when unknown (bare access tokens).
	Name        string
	Permissions []string
	// Expires is when the credential stops being valid (zero: unknown).
	Expires time.Time
}

// Tokens are what IAMKit issues at sign-in and refresh.
type Tokens struct {
	Access    string
	Refresh   string
	IDToken   string
	ExpiresAt time.Time
	// IDSubject is the verified id_token's subject (sign-in only): it must
	// name the same person as the access token.
	IDSubject string
}

// Profile is the signed-in person, for display.
type Profile struct {
	Name             string `json:"name"`
	Email            string `json:"email"`
	OrganizationName string `json:"organization_name"`
}

// Session is a browser session (cookie = its id; only the id's hash is
// stored). Tokens are sealed at rest by the repository.
type Session struct {
	// IDHash is HashSessionID of the cookie value.
	IDHash       string
	Organization identity.OrganizationID
	Subject      string
	Profile      Profile
	Tokens       Tokens
	// ExpiresAt ends the session whatever IAMKit says (config.SessionTTL).
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Expired reports whether the session is over at now.
func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// NeedsRefresh: the access token expires within RefreshAhead of now.
func (s Session) NeedsRefresh(now time.Time) bool {
	return s.Tokens.ExpiresAt.Before(now.Add(RefreshAhead))
}

// Pending is a sign-in in progress, kept by the browser (sealed) while it
// is at IAMKit: state ties the callback to this browser, nonce the
// id_token to this sign-in, verifier is the PKCE secret.
type Pending struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Next     string    `json:"x"`
	Expires  time.Time `json:"e"`
}

// Begin starts a sign-in.
type Begin struct {
	// Next is the local path to return to (default "/").
	Next string
	// Organization, when set, asks IAMKit for that organization.
	Organization string
	// SelectAccount shows IAMKit's organization chooser (switching).
	SelectAccount bool
}

func (b Begin) Validate() error {
	if b.Next != "" && !LocalPath(b.Next) {
		return errx.InvalidField("next", "next must be a path on this site")
	}
	if b.Organization != "" {
		if _, err := identity.ParseOrganizationID(b.Organization); err != nil {
			return err
		}
	}
	return nil
}

// Authorization is what Begin produces: where to send the browser and
// what it must bring back.
type Authorization struct {
	URL     string
	Pending Pending
}

// Callback is IAMKit's answer at the redirect URI.
type Callback struct {
	Code, State string
	// Error is IAMKit's error code (access_denied...) when sign-in failed.
	Error string
}

func (c Callback) Validate() error {
	if c.Error != "" {
		return errx.Unauthorized(callbackErrorMessage(c.Error))
	}
	if c.Code == "" || c.State == "" {
		return errx.Validation("code and state are required")
	}
	return nil
}

// callbackErrorMessage words IAMKit's error code. The code arrives in the
// URL (anyone can craft one), so only known codes are shown.
func callbackErrorMessage(code string) string {
	switch code {
	case "access_denied":
		return "sign-in was cancelled or refused"
	case "login_required", "interaction_required", "consent_required", "account_selection_required":
		return "sign-in needs you at IAMKit; start again"
	case "temporarily_unavailable", "server_error":
		return "IAMKit is unavailable; try again"
	}
	return "sign-in failed"
}

// LocalPath: a path on this site ("/docks?x=1"), never another origin
// ("//evil", "/\evil", "https://…").
func LocalPath(p string) bool {
	return len(p) <= MaxNext && strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") &&
		!strings.ContainsAny(p, "\\\r\n\t")
}

// Random returns n random bytes, base64url.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewSessionID returns a cookie value for a new session.
func NewSessionID() string { return Random(32) }

// NewPKCE returns a PKCE verifier and its S256 challenge (RFC 7636).
func NewPKCE() (verifier, challenge string) {
	verifier = Random(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// HashSessionID is how a session id is stored and looked up.
func HashSessionID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
