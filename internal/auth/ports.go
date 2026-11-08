package auth

import (
	"context"
	"time"
)

// Commands are iam mode's sign-in. Sessions are keyed by a random id, not
// by organization: auth is not a tenant domain (TestTenantFirstPorts).
type Commands interface {
	// Begin prepares a sign-in: the IAMKit authorize URL and the pending
	// state the browser keeps until the callback.
	Begin(ctx context.Context, input Begin) (Authorization, error)
	// Complete finishes a sign-in at the callback and opens a session; it
	// returns the session id (cookie value) and the session.
	Complete(ctx context.Context, pending Pending, callback Callback) (string, Session, error)
	// Authenticate resolves a session id, refreshing its access token when
	// needed, and checks it with IAMKit.
	Authenticate(ctx context.Context, sessionID string) (Identity, Session, error)
	// Bearer checks an IAMKit access token or API key (CLI, scripts).
	Bearer(ctx context.Context, token string) (Identity, error)
	// Logout ends the session (IAMKit tokens revoked) and returns where
	// to send the browser to end its IAMKit session too.
	Logout(ctx context.Context, sessionID string) (string, error)
	// Sweep deletes expired sessions.
	Sweep(ctx context.Context) (int, error)
}

// Provider is IAMKit.
type Provider interface {
	// AuthorizeURL is the authorization request (code + PKCE S256).
	AuthorizeURL(state, nonce, challenge, organization string, selectAccount bool) string
	// Exchange trades a code for tokens and verifies the id_token
	// (signature, issuer, audience, nonce).
	Exchange(ctx context.Context, code, verifier, nonce string) (Tokens, error)
	Refresh(ctx context.Context, refresh string) (Tokens, error)
	// Revoke invalidates a token (best effort at sign-out).
	Revoke(ctx context.Context, token string) error
	// VerifyAccess introspects an access token of a user acting in an
	// organization (Seaport's application and resource).
	VerifyAccess(ctx context.Context, token string) (Identity, error)
	// VerifyAPIKey validates an ik_key_ API key of an organization.
	VerifyAPIKey(ctx context.Context, key string) (Identity, error)
	// IsAPIKey: the value looks like an API key.
	IsAPIKey(value string) bool
	// Profile reads the person's name, email and organization name.
	Profile(ctx context.Context, access, organization string) (Profile, error)
	// EndSessionURL is IAMKit's sign-out page for this session.
	EndSessionURL(idToken string) string
}

// SessionRepository stores browser sessions (system-wide: keyed by the
// hash of a random id).
type SessionRepository interface {
	Create(ctx context.Context, session Session) error
	Find(ctx context.Context, idHash string) (Session, error)
	// Update loads the session locked (SELECT … FOR UPDATE), lets fn
	// change it and stores the result, so concurrent requests refresh a
	// session's tokens once (IAMKit rotates refresh tokens). fn returning
	// an error leaves the row as it was.
	Update(ctx context.Context, idHash string, fn func(Session) (Session, error)) (Session, error)
	Delete(ctx context.Context, idHash string) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}
