// Package authhttp is iam mode's httpx.Authenticator: admins by IAMKit
// bearer (access token or API key) or by the browser session cookie,
// agents by crew tokens, and the redirect sign-in (BFF) endpoints.
package authhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

const (
	// pendingCookie carries the sealed sign-in in progress (state, nonce,
	// PKCE verifier) to the callback only.
	pendingCookie  = "seaport_oauth"
	pendingPurpose = "seaport_oauth"
	// CallbackPath is the OAuth redirect path (SEAPORT_PUBLIC_URL + it is
	// registered on the IAMKit web client).
	CallbackPath = "/auth/callback"
)

type Authenticator struct {
	auth   auth.Commands
	sealer auth.Sealer
	crew   httpx.CrewResolver
	// secure marks cookies HTTPS-only (SEAPORT_PUBLIC_URL is https).
	secure bool
	ttl    time.Duration
}

func New(commands auth.Commands, sealer auth.Sealer, crew httpx.CrewResolver, secure bool, ttl time.Duration) *Authenticator {
	return &Authenticator{auth: commands, sealer: sealer, crew: crew, secure: secure, ttl: ttl}
}

var _ httpx.Authenticator = (*Authenticator)(nil)

func principal(who auth.Identity) httpx.Principal {
	return httpx.Principal{Organization: who.Organization, Role: httpx.RoleAdmin, Subject: who.Subject, Name: who.Name,
		Permissions: who.Permissions, Expires: who.Expires}
}

// Admin: a bearer (CLI, scripts) wins over the cookie; cookie requests go
// through cross-origin protection (CSRF).
func (a *Authenticator) Admin(next http.Handler) http.Handler {
	withCookie := httpx.CrossOrigin(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			who, err := a.auth.Bearer(r.Context(), httpx.Bearer(r))
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(httpx.WithPrincipal(r.Context(), principal(who))))
			return
		}
		who, _, err := a.auth.Authenticate(r.Context(), sessionID(r))
		if err != nil {
			if errx.IsType(err, errx.TypeAuthorization) {
				a.clearSession(w)
			}
			httpx.WriteError(w, r, err)
			return
		}
		withCookie.ServeHTTP(w, r.WithContext(httpx.WithPrincipal(r.Context(), principal(who))))
	})
}

// Crew: agents present a crew token of their organization.
func (a *Authenticator) Crew(next http.Handler) http.Handler {
	return httpx.CrewAuth("", httpx.Principal{}, a.crew, next)
}

func (a *Authenticator) Session() httpx.SessionRoutes {
	return httpx.SessionRoutes{
		Login:    httpx.Handle(a.login),
		Current:  httpx.Handle(a.current),
		Logout:   httpx.Handle(a.logout),
		SignIn:   httpx.Handle(a.signIn),
		Callback: http.HandlerFunc(a.callback),
	}
}

func sessionID(r *http.Request) string {
	c, err := r.Cookie(httpx.SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func (a *Authenticator) cookie(name, value, path string, maxAge int, same http.SameSite) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: a.secure, SameSite: same}
}

func (a *Authenticator) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, a.cookie(httpx.SessionCookie, "", "/", -1, http.SameSiteStrictMode))
}

// login is POST /api/v1/session: token mode's form has no meaning here.
func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) error {
	return errx.Validation("this port signs in with IAMKit: GET /api/v1/session/login")
}

// current is GET /api/v1/session (never 401).
func (a *Authenticator) current(w http.ResponseWriter, r *http.Request) error {
	who, s, err := a.auth.Authenticate(r.Context(), sessionID(r))
	if errx.IsType(err, errx.TypeAuthorization) {
		if sessionID(r) != "" {
			a.clearSession(w)
		}
		return httpx.JSON(w, http.StatusOK, httpx.SignedOut())
	}
	if err != nil {
		return err
	}
	exp := s.ExpiresAt
	perms := who.Permissions
	if perms == nil {
		perms = []string{}
	}
	return httpx.JSON(w, http.StatusOK, httpx.SessionState{Authenticated: true, ExpiresAt: &exp, Subject: who.Subject,
		Name: s.Profile.Name, Email: s.Profile.Email, Organization: who.Organization.String(),
		OrganizationName: s.Profile.OrganizationName, Permissions: perms})
}

type logoutResponse struct {
	// LogoutURL ends the IAMKit session too; the browser goes there.
	LogoutURL string `json:"logout_url"`
}

// logout is DELETE /api/v1/session (admin mux: authenticated).
func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) error {
	end, err := a.auth.Logout(r.Context(), sessionID(r))
	if err != nil {
		return err
	}
	a.clearSession(w)
	return httpx.JSON(w, http.StatusOK, logoutResponse{LogoutURL: end})
}

// signIn is GET /api/v1/session/login?next=/path&organization=<id>&switch=1:
// a top-level navigation to IAMKit's hosted sign-in.
func (a *Authenticator) signIn(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	az, err := a.auth.Begin(r.Context(), auth.Begin{Next: q.Get("next"), Organization: q.Get("organization"),
		SelectAccount: q.Get("switch") == "1"})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(az.Pending)
	// Lax: the callback is a top-level navigation coming back from IAMKit.
	http.SetCookie(w, a.cookie(pendingCookie, a.sealer.Seal(pendingPurpose, raw), CallbackPath,
		int(auth.PendingTTL.Seconds()), http.SameSiteLaxMode))
	http.Redirect(w, r, az.URL, http.StatusSeeOther)
	return nil
}

// callback is GET /auth/callback: IAMKit's redirect. Failures go back to
// the app with ?signin_error= (shown on the sign-in screen), never to an
// error page the user can't leave.
func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.cookie(pendingCookie, "", CallbackPath, -1, http.SameSiteLaxMode))
	q := r.URL.Query()
	pending, err := a.pending(r)
	if err == nil {
		var id string
		var s auth.Session
		id, s, err = a.auth.Complete(r.Context(), pending, auth.Callback{Code: q.Get("code"), State: q.Get("state"), Error: q.Get("error")})
		if err == nil {
			http.SetCookie(w, a.cookie(httpx.SessionCookie, id, "/", int(time.Until(s.ExpiresAt).Seconds()), http.SameSiteStrictMode))
			http.Redirect(w, r, pending.Next, http.StatusSeeOther)
			return
		}
	}
	var e *errx.Error
	msg := errx.GenericMessage
	if errx.As(err, &e) && e.HTTPStatus < 500 {
		msg = e.Message
	} else {
		slog.Error("IAMKit sign-in callback", "request_id", httpx.RequestID(r.Context()), "err", err)
	}
	http.Redirect(w, r, "/?signin_error="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *Authenticator) pending(r *http.Request) (auth.Pending, error) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return auth.Pending{}, errx.Unauthorized("the sign-in expired or was started in another browser; start again")
	}
	raw, err := a.sealer.Open(pendingPurpose, c.Value)
	if err != nil {
		return auth.Pending{}, errx.Unauthorized("the sign-in expired or was started in another browser; start again")
	}
	var p auth.Pending
	if err := json.Unmarshal(raw, &p); err != nil || !auth.LocalPath(p.Next) {
		return auth.Pending{}, errx.Unauthorized("invalid sign-in state; start again")
	}
	return p, nil
}
