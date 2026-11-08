// Package authsvc implements iam mode's sign-in (auth.Commands).
package authsvc

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
)

type Service struct {
	provider auth.Provider
	sessions auth.SessionRepository
	// ttl is how long a session lasts at most (config.SessionTTL).
	ttl time.Duration
	now func() time.Time
}

func New(p auth.Provider, sessions auth.SessionRepository, ttl time.Duration) *Service {
	return &Service{provider: p, sessions: sessions, ttl: ttl, now: time.Now}
}

var _ auth.Commands = (*Service)(nil)

var signedOut = errx.Unauthorized("not signed in")

// refreshTimeout bounds a session refresh (lock + IAMKit round-trip).
const refreshTimeout = 15 * time.Second

func (s *Service) Begin(ctx context.Context, in auth.Begin) (auth.Authorization, error) {
	if err := in.Validate(); err != nil {
		return auth.Authorization{}, err
	}
	next := in.Next
	if next == "" {
		next = "/"
	}
	verifier, challenge := auth.NewPKCE()
	p := auth.Pending{State: auth.Random(24), Nonce: auth.Random(24), Verifier: verifier, Next: next,
		Expires: s.now().Add(auth.PendingTTL)}
	return auth.Authorization{URL: s.provider.AuthorizeURL(p.State, p.Nonce, challenge, in.Organization, in.SelectAccount),
		Pending: p}, nil
}

func (s *Service) Complete(ctx context.Context, p auth.Pending, cb auth.Callback) (string, auth.Session, error) {
	if err := cb.Validate(); err != nil {
		return "", auth.Session{}, err
	}
	if p.State == "" || !s.now().Before(p.Expires) {
		return "", auth.Session{}, errx.Unauthorized("the sign-in expired; start again")
	}
	if subtle.ConstantTimeCompare([]byte(p.State), []byte(cb.State)) != 1 {
		return "", auth.Session{}, errx.Unauthorized("the sign-in does not belong to this browser; start again")
	}
	tokens, err := s.provider.Exchange(ctx, cb.Code, p.Verifier, p.Nonce)
	if err != nil {
		return "", auth.Session{}, err
	}
	who, err := s.provider.VerifyAccess(ctx, tokens.Access)
	if err != nil {
		return "", auth.Session{}, err
	}
	profile, err := s.provider.Profile(ctx, tokens.Access, who.Organization.String())
	if err != nil {
		// Display only: sign-in works without it.
		slog.Warn("read IAMKit profile", "err", err)
	}
	now := s.now()
	id := auth.NewSessionID()
	session := auth.Session{IDHash: auth.HashSessionID(id), Organization: who.Organization, Subject: who.Subject,
		Profile: profile, Tokens: tokens, ExpiresAt: now.Add(s.ttl), CreatedAt: now}
	if err := s.sessions.Create(ctx, session); err != nil {
		return "", auth.Session{}, errx.Wrap(err, "create session", errx.TypeInternal)
	}
	return id, session, nil
}

func (s *Service) Authenticate(ctx context.Context, id string) (auth.Identity, auth.Session, error) {
	if id == "" {
		return auth.Identity{}, auth.Session{}, signedOut
	}
	hash := auth.HashSessionID(id)
	session, err := s.sessions.Find(ctx, hash)
	if errx.IsType(err, errx.TypeNotFound) {
		return auth.Identity{}, auth.Session{}, signedOut
	}
	if err != nil {
		return auth.Identity{}, auth.Session{}, errx.Wrap(err, "find session", errx.TypeInternal)
	}
	now := s.now()
	if session.Expired(now) {
		_ = s.sessions.Delete(ctx, hash)
		return auth.Identity{}, auth.Session{}, signedOut
	}
	if session.NeedsRefresh(now) {
		if session, err = s.refresh(ctx, hash); err != nil {
			return auth.Identity{}, auth.Session{}, err
		}
	}
	who, err := s.provider.VerifyAccess(ctx, session.Tokens.Access)
	if errx.IsType(err, errx.TypeAuthorization) {
		// IAMKit refused the token (user removed, session ended there):
		// the session is over, drop its row with the cookie.
		_ = s.sessions.Delete(ctx, hash)
		return auth.Identity{}, auth.Session{}, signedOut
	}
	if err != nil {
		return auth.Identity{}, auth.Session{}, err
	}
	if who.Organization != session.Organization || who.Subject != session.Subject {
		_ = s.sessions.Delete(ctx, hash)
		return auth.Identity{}, auth.Session{}, signedOut
	}
	if who.Expires.IsZero() || session.ExpiresAt.Before(who.Expires) {
		who.Expires = session.ExpiresAt
	}
	who.Name = session.Profile.Name
	if who.Name == "" {
		who.Name = session.Profile.Email
	}
	return who, session, nil
}

// refresh renews the session's tokens under the row lock: of concurrent
// requests (or processes) one refreshes, the others see its result —
// IAMKit rotates refresh tokens, so a second use would end the session.
func (s *Service) refresh(ctx context.Context, hash string) (auth.Session, error) {
	// Detached from the request: once IAMKit rotates the refresh token the
	// new one must be stored even if the browser has gone away, or the
	// session is lost (the old refresh token is dead).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	session, err := s.sessions.Update(ctx, hash, func(cur auth.Session) (auth.Session, error) {
		if !cur.NeedsRefresh(s.now()) {
			return cur, nil
		}
		if cur.Tokens.Refresh == "" {
			return cur, signedOut
		}
		t, err := s.provider.Refresh(ctx, cur.Tokens.Refresh)
		if err != nil {
			return cur, err
		}
		if t.Refresh == "" {
			t.Refresh = cur.Tokens.Refresh
		}
		if t.IDToken == "" {
			t.IDToken = cur.Tokens.IDToken
		}
		cur.Tokens = t
		return cur, nil
	})
	switch {
	case err == nil:
		return session, nil
	case errx.IsType(err, errx.TypeNotFound):
		return auth.Session{}, signedOut
	case errx.IsType(err, errx.TypeAuthorization):
		// IAMKit refused the refresh token (signed out, revoked, expired).
		_ = s.sessions.Delete(ctx, hash)
		return auth.Session{}, signedOut
	}
	return auth.Session{}, errx.Wrap(err, "refresh session", errx.TypeExternal)
}

func (s *Service) Bearer(ctx context.Context, token string) (auth.Identity, error) {
	if token == "" {
		return auth.Identity{}, errx.Unauthorized("missing or invalid token")
	}
	if s.provider.IsAPIKey(token) {
		return s.provider.VerifyAPIKey(ctx, token)
	}
	return s.provider.VerifyAccess(ctx, token)
}

func (s *Service) Logout(ctx context.Context, id string) (string, error) {
	if id == "" {
		return s.provider.EndSessionURL(""), nil
	}
	hash := auth.HashSessionID(id)
	session, err := s.sessions.Find(ctx, hash)
	if errx.IsType(err, errx.TypeNotFound) {
		return s.provider.EndSessionURL(""), nil
	}
	if err != nil {
		return "", errx.Wrap(err, "find session", errx.TypeInternal)
	}
	if err := s.sessions.Delete(ctx, hash); err != nil {
		return "", errx.Wrap(err, "delete session", errx.TypeInternal)
	}
	if session.Tokens.Refresh != "" {
		if err := s.provider.Revoke(ctx, session.Tokens.Refresh); err != nil {
			slog.Warn("revoke IAMKit refresh token at sign-out", "err", err)
		}
	}
	return s.provider.EndSessionURL(session.Tokens.IDToken), nil
}

func (s *Service) Sweep(ctx context.Context) (int, error) {
	return s.sessions.DeleteExpired(ctx, s.now())
}
