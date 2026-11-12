package httpx

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/csrf"
)

// Role is what a principal may do: administer the port (web, CLI) or work
// as crew (agents: /crew/ws, /mcp).
type Role string

const (
	RoleAdmin Role = "admin"
	RoleCrew  Role = "crew"
)

// Principal is who is calling, as established by the Authenticator. Every
// request a handler sees carries one; handlers take the tenant from it and
// never from the URL or the body.
type Principal struct {
	Organization identity.OrganizationID
	Role         Role
	// Subject names the caller for logs ("admin", a crew token name, an
	// IAMKit user id).
	Subject string
	// Name is who the caller is for people reading records (crew token
	// "created by"); empty: use Subject.
	Name string
	// Permissions are what an admin may do (permission.*): every one in
	// the single-owner modes, the IAMKit grants in iam mode. Crew
	// principals hold none; their endpoints check the role.
	Permissions []string
	// Expires, when set, is when the credential stops being valid
	// (long-lived streams end there so the client re-authenticates).
	Expires time.Time
	// Credential identifies a revocable credential (a crew token id) so
	// its live connections can be closed when it is revoked.
	Credential string
	// Agent is the agent a crew principal works as (its token's agent);
	// zero in open mode, where the connector names it in its hello.
	Agent     identity.AgentID
	AgentName string
	// Slot is the hosted slot a slot token runs (its index; 0 for any
	// other credential).
	Slot int
}

// Can reports whether the principal holds the permission.
func (p Principal) Can(perm string) bool { return permission.Has(p.Permissions, perm) }

// Require runs h only for a principal holding perm: 401 without a
// principal, 403 without the permission. Every admin route is wrapped in
// it (architecture test TestAdminRoutesRequirePermission).
func Require(perm string, h fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		p, ok := PrincipalFrom(c.Context())
		switch {
		case !ok:
			return errx.Unauthorized("missing or invalid token")
		case p.Role != RoleAdmin || !p.Can(perm):
			return errx.Forbidden("missing permission " + perm)
		}
		return h(c)
	}
}

// RequireFunc is Require for a handler function.
func RequireFunc(perm string, h func(fiber.Ctx) error) fiber.Handler { return Require(perm, h) }

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the request's principal.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok && !p.Organization.IsZero()
}

// Organization is the caller's tenant. Routes are always mounted behind
// an Authenticator, so a missing principal is a wiring bug: it answers 401
// rather than guessing a tenant.
func Organization(c fiber.Ctx) (identity.OrganizationID, error) {
	p, err := Caller(c)
	return p.Organization, err
}

// Caller is the request's principal (401 when missing, like Organization).
func Caller(c fiber.Ctx) (Principal, error) {
	p, ok := PrincipalFrom(c.Context())
	if !ok {
		return Principal{}, errx.Unauthorized("missing or invalid token")
	}
	return p, nil
}

// SetPrincipal makes p the principal of the request: the context handlers
// (and whatever still runs behind the net/http fallback) read it from.
func SetPrincipal(c fiber.Ctx, p Principal) {
	c.SetContext(WithPrincipal(c.Context(), p))
}

// CrewResolver turns a bearer value into a crew principal (crew tokens
// stored per organization); unknown values are Unauthorized.
type CrewResolver interface {
	ResolveCrew(ctx context.Context, value string) (Principal, error)
}

// Bearer returns the request's bearer token, or "".
func Bearer(c fiber.Ctx) string {
	got, _ := strings.CutPrefix(c.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(got)
}

// CheckBearer is nil when the request's bearer token is token
// (constant-time compare), else 401.
func CheckBearer(c fiber.Ctx, token string) error {
	want := []byte(token)
	if len(want) == 0 || subtle.ConstantTimeCompare([]byte(Bearer(c)), want) != 1 {
		return errx.Unauthorized("missing or invalid token")
	}
	return nil
}

// CrewAuth serves the request with the crew principal of the request's
// bearer token, resolved by tokens (crew tokens: each names its organization
// and its agent).
func CrewAuth(tokens CrewResolver) fiber.Handler {
	return func(c fiber.Ctx) error {
		got := Bearer(c)
		if got == "" || tokens == nil {
			return errx.Unauthorized("missing or invalid token")
		}
		p, err := tokens.ResolveCrew(c.Context(), got)
		if err != nil {
			return err
		}
		SetPrincipal(c, p)
		return c.Next()
	}
}

// Authenticator establishes the principal of a request for one role. It is
// the inbound auth port; the run mode picks the adapter (Open, Tokens...).
// Its handlers are middleware: they answer 401/403 or set the principal
// (SetPrincipal) and call Next.
type Authenticator interface {
	// Admin protects the admin API (/api/v1): it answers 401/403 or sets an
	// admin principal and calls Next.
	Admin() fiber.Handler
	// Crew protects the agents' endpoints (/crew/..., /mcp, the gateway).
	Crew() fiber.Handler
	// Session is /api/v1/session (browser login), mounted unauthenticated.
	Session() SessionRoutes
}

// SessionRoutes are the browser session endpoints of a mode. Login is
// POST /api/v1/session (token mode); SignIn (GET /api/v1/session/login) and
// Callback (GET /auth/callback) are the redirect sign-in of iam mode, nil
// in the others.
type SessionRoutes struct {
	Login, Current, Logout fiber.Handler
	SignIn, Callback       fiber.Handler
}

// Open is SEAPORT_AUTH=none: every caller is the admin (and crew) of one
// organization. The process only listens on loopback (config.Validate).
type Open struct {
	Organization identity.OrganizationID
}

var _ Authenticator = Open{}

// with trusts the caller only when the request is local in fact, not just
// on a loopback socket:
//   - Host must be a loopback name. A page on another site that re-points
//     its own hostname at 127.0.0.1 (DNS rebinding) is same-origin to the
//     browser, so origin checks alone can't stop it, but its requests carry
//     the foreign Host.
//   - No forwarding headers. A reverse proxy or tunnel on this machine
//     (Caddy, nginx, cloudflared, ngrok, tailscale serve) would turn remote
//     callers into loopback connections, and may even rewrite Host to
//     localhost; they all add X-Forwarded-For or similar. Vite's dev proxy
//     adds none, so `pnpm dev` keeps working.
func (o Open) guard(role Role) fiber.Handler {
	csrf := CrossOrigin()
	return func(c fiber.Ctx) error {
		if err := o.allow(c, role); err != nil {
			return err
		}
		return csrf(c)
	}
}

// allow checks the request is local and makes the open principal its own.
func (o Open) allow(c fiber.Ctx, role Role) error {
	p := Principal{Organization: o.Organization, Role: role, Subject: "local"}
	if role == RoleAdmin {
		p.Permissions = permission.All
	}
	if !loopbackHost(string(c.Request().Header.Host())) {
		return errx.Forbidden("an open port only answers to localhost")
	}
	if h := forwardedBy(c); h != "" {
		return errx.Forbidden("an open port refuses proxied requests (" + h + "); use SEAPORT_AUTH=token behind a proxy")
	}
	SetPrincipal(c, p)
	return nil
}

// forwardingHeaders mark a request relayed by a proxy or tunnel.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host",
	"X-Forwarded-Proto", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip", "Tailscale-User-Login"}

// forwardedBy names the first forwarding header present, or "".
func forwardedBy(c fiber.Ctx) string {
	for _, h := range forwardingHeaders {
		if len(c.Request().Header.PeekAll(h)) > 0 {
			return h
		}
	}
	return ""
}

// loopbackHost: localhost or a loopback IP, with or without a port.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Admin: browsers on other sites must not drive an open port either.
func (o Open) Admin() fiber.Handler { return o.guard(RoleAdmin) }

// Crew: connectors send no Origin, browsers on other sites do (the
// WebSocket handshake also checks it).
func (o Open) Crew() fiber.Handler { return o.guard(RoleCrew) }

func (o Open) Session() SessionRoutes {
	// Through allow: a rebinding page must not learn the port is open.
	state := func(c fiber.Ctx) error {
		if err := o.allow(c, RoleAdmin); err != nil {
			return err
		}
		return JSON(c, http.StatusOK, SessionState{Authenticated: true, Subject: "local", Permissions: permission.All})
	}
	return SessionRoutes{Login: state, Current: state,
		Logout: func(c fiber.Ctx) error { c.Status(http.StatusNoContent); return nil }}
}

// Tokens is SEAPORT_AUTH=token: a static admin token (bearer, or traded
// for a session cookie), one organization. Agents use crew tokens created
// in the admin (CrewTokens), each bound to an agent.
type Tokens struct {
	Organization identity.OrganizationID
	Sessions     Sessions // admin token + cookie settings
	CrewTokens   CrewResolver
}

var _ Authenticator = Tokens{}

func (t Tokens) Admin() fiber.Handler {
	p := Principal{Organization: t.Organization, Role: RoleAdmin, Subject: "admin", Permissions: permission.All}
	return t.Sessions.RequireAdmin(p)
}

func (t Tokens) Crew() fiber.Handler { return CrewAuth(t.CrewTokens) }

func (t Tokens) Session() SessionRoutes {
	return SessionRoutes{Login: t.Sessions.Login(), Current: t.Sessions.Current(), Logout: t.Sessions.Logout()}
}

// CrossOrigin rejects unsafe requests a browser sends from another origin
// (Fiber's port of http.CrossOriginProtection: same decisions).
func CrossOrigin() fiber.Handler {
	return csrf.NewCrossOriginProtection(csrf.CrossOriginConfig{
		ErrorHandler: func(fiber.Ctx, error) error { return errx.Forbidden("cross-origin request rejected") },
	})
}

// APIKeyAsBearer lets crew auth read an x-api-key header: the model
// gateway's callers send their slot token that way.
func APIKeyAsBearer() fiber.Handler {
	return func(c fiber.Ctx) error {
		if c.Get("Authorization") == "" {
			if key := c.Get("X-Api-Key"); key != "" {
				c.Request().Header.Set("Authorization", "Bearer "+key)
			}
		}
		return c.Next()
	}
}
