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
	"github.com/gofiber/fiber/v3"
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
func (a *Authenticator) Admin() fiber.Handler {
	csrf := httpx.CrossOrigin()
	return func(c fiber.Ctx) error {
		if c.Get("Authorization") != "" {
			who, err := a.auth.Bearer(c.Context(), httpx.Bearer(c))
			if err != nil {
				return err
			}
			httpx.SetPrincipal(c, principal(who))
			return c.Next()
		}
		who, _, err := a.auth.Authenticate(c.Context(), sessionID(c))
		if err != nil {
			if errx.IsType(err, errx.TypeAuthorization) {
				a.clearSession(c)
			}
			return err
		}
		httpx.SetPrincipal(c, principal(who))
		return csrf(c)
	}
}

// Crew: agents present a crew token of their organization.
func (a *Authenticator) Crew() fiber.Handler { return httpx.CrewAuth(a.crew) }

func (a *Authenticator) Session() httpx.SessionRoutes {
	return httpx.SessionRoutes{
		Login:    a.login,
		Current:  a.current,
		Logout:   a.logout,
		SignIn:   a.signIn,
		Callback: a.callback,
	}
}

func sessionID(c fiber.Ctx) string { return c.Cookies(httpx.SessionCookie) }

func (a *Authenticator) cookie(name, value, path string, maxAge int, same string) *fiber.Cookie {
	return &fiber.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HTTPOnly: true, Secure: a.secure, SameSite: same}
}

func (a *Authenticator) clearSession(c fiber.Ctx) {
	c.Cookie(a.cookie(httpx.SessionCookie, "", "/", -1, fiber.CookieSameSiteStrictMode))
}

// login is POST /api/v1/session: token mode's form has no meaning here.
func (a *Authenticator) login(c fiber.Ctx) error {
	return errx.Validation("this port signs in with IAMKit: GET /api/v1/session/login")
}

// current is GET /api/v1/session (never 401).
func (a *Authenticator) current(c fiber.Ctx) error {
	who, s, err := a.auth.Authenticate(c.Context(), sessionID(c))
	if errx.IsType(err, errx.TypeAuthorization) {
		if sessionID(c) != "" {
			a.clearSession(c)
		}
		return httpx.JSON(c, http.StatusOK, httpx.SignedOut())
	}
	if err != nil {
		return err
	}
	exp := s.ExpiresAt
	perms := who.Permissions
	if perms == nil {
		perms = []string{}
	}
	return httpx.JSON(c, http.StatusOK, httpx.SessionState{Authenticated: true, ExpiresAt: &exp, Subject: who.Subject,
		Name: s.Profile.Name, Email: s.Profile.Email, Organization: who.Organization.String(),
		OrganizationName: s.Profile.OrganizationName, Permissions: perms})
}

type logoutResponse struct {
	// LogoutURL ends the IAMKit session too; the browser goes there.
	LogoutURL string `json:"logout_url"`
}

// logout is DELETE /api/v1/session (admin routes: authenticated).
func (a *Authenticator) logout(c fiber.Ctx) error {
	end, err := a.auth.Logout(c.Context(), sessionID(c))
	if err != nil {
		return err
	}
	a.clearSession(c)
	return httpx.JSON(c, http.StatusOK, logoutResponse{LogoutURL: end})
}

// signIn is GET /api/v1/session/login?next=/path&organization=<id>&switch=1:
// a top-level navigation to IAMKit's hosted sign-in.
func (a *Authenticator) signIn(c fiber.Ctx) error {
	az, err := a.auth.Begin(c.Context(), auth.Begin{Next: c.Query("next"), Organization: c.Query("organization"),
		SelectAccount: c.Query("switch") == "1"})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(az.Pending)
	// Lax: the callback is a top-level navigation coming back from IAMKit.
	c.Cookie(a.cookie(pendingCookie, a.sealer.Seal(pendingPurpose, raw), CallbackPath,
		int(auth.PendingTTL.Seconds()), fiber.CookieSameSiteLaxMode))
	return c.Redirect().Status(http.StatusSeeOther).To(az.URL)
}

// callback is GET /auth/callback: IAMKit's redirect. Failures go back to
// the app with ?signin_error= (shown on the sign-in screen), never to an
// error page the user can't leave.
func (a *Authenticator) callback(c fiber.Ctx) error {
	c.Cookie(a.cookie(pendingCookie, "", CallbackPath, -1, fiber.CookieSameSiteLaxMode))
	pending, err := a.pending(c)
	if err == nil {
		var id string
		var s auth.Session
		id, s, err = a.auth.Complete(c.Context(), pending, auth.Callback{Code: c.Query("code"), State: c.Query("state"), Error: c.Query("error")})
		if err == nil {
			c.Cookie(a.cookie(httpx.SessionCookie, id, "/", int(time.Until(s.ExpiresAt).Seconds()), fiber.CookieSameSiteStrictMode))
			return c.Redirect().Status(http.StatusSeeOther).To(pending.Next)
		}
	}
	var e *errx.Error
	msg := errx.GenericMessage
	if errx.As(err, &e) && e.HTTPStatus < 500 {
		msg = e.Message
	} else {
		slog.Error("IAMKit sign-in callback", "request_id", httpx.RequestID(c.Context()), "err", err)
	}
	return c.Redirect().Status(http.StatusSeeOther).To("/?signin_error=" + url.QueryEscape(msg))
}

func (a *Authenticator) pending(c fiber.Ctx) (auth.Pending, error) {
	value := c.Cookies(pendingCookie)
	if value == "" {
		return auth.Pending{}, errx.Unauthorized("the sign-in expired or was started in another browser; start again")
	}
	raw, err := a.sealer.Open(pendingPurpose, value)
	if err != nil {
		return auth.Pending{}, errx.Unauthorized("the sign-in expired or was started in another browser; start again")
	}
	var p auth.Pending
	if err := json.Unmarshal(raw, &p); err != nil || !auth.LocalPath(p.Next) {
		return auth.Pending{}, errx.Unauthorized("invalid sign-in state; start again")
	}
	return p, nil
}
