package httpx

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
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
}

// Can reports whether the principal holds the permission.
func (p Principal) Can(perm string) bool { return permission.Has(p.Permissions, perm) }

// Require serves next only to a principal holding perm: 401 without a
// principal, 403 without the permission. Every admin route is wrapped in
// it (architecture test TestAdminRoutesRequirePermission).
func Require(perm string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		switch {
		case !ok:
			WriteError(w, r, errx.Unauthorized("missing or invalid token"))
		case p.Role != RoleAdmin || !p.Can(perm):
			WriteError(w, r, errx.Forbidden("missing permission "+perm))
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// RequireFunc is Require for an httpx.HandlerFunc.
func RequireFunc(perm string, h HandlerFunc) http.Handler { return Require(perm, Handle(h)) }

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
func Organization(r *http.Request) (identity.OrganizationID, error) {
	p, err := Caller(r)
	return p.Organization, err
}

// Caller is the request's principal (401 when missing, like Organization).
func Caller(r *http.Request) (Principal, error) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return Principal{}, errx.Unauthorized("missing or invalid token")
	}
	return p, nil
}

// CrewResolver turns a bearer value into a crew principal (crew tokens
// stored per organization); unknown values are Unauthorized.
type CrewResolver interface {
	ResolveCrew(ctx context.Context, value string) (Principal, error)
}

// Bearer returns the request's bearer token, or "".
func Bearer(r *http.Request) string {
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(got)
}

// CrewAuth serves next with the crew principal of the request's bearer
// token, resolved by tokens (crew tokens: each names its organization and
// its agent).
func CrewAuth(tokens CrewResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := Bearer(r)
		if got == "" || tokens == nil {
			WriteError(w, r, errx.Unauthorized("missing or invalid token"))
			return
		}
		p, err := tokens.ResolveCrew(r.Context(), got)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// Authenticator establishes the principal of a request for one role. It is
// the inbound auth port; the run mode picks the adapter (Open, Tokens...).
type Authenticator interface {
	// Admin protects the admin API (/api/v1): it serves next with an admin
	// principal or answers 401/403.
	Admin(next http.Handler) http.Handler
	// Crew protects the agents' endpoints (/crew/ws, /mcp).
	Crew(next http.Handler) http.Handler
	// Session is /api/v1/session (browser login), mounted unauthenticated.
	Session() SessionRoutes
}

// SessionRoutes are the browser session endpoints of a mode. Login is
// POST /api/v1/session (token mode); SignIn (GET /api/v1/session/login) and
// Callback (GET /auth/callback) are the redirect sign-in of iam mode, nil
// in the others.
type SessionRoutes struct {
	Login, Current, Logout http.Handler
	SignIn, Callback       http.Handler
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
func (o Open) with(role Role, next http.Handler) http.Handler {
	p := Principal{Organization: o.Organization, Role: role, Subject: "local"}
	if role == RoleAdmin {
		p.Permissions = permission.All
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			WriteError(w, r, errx.Forbidden("an open port only answers to localhost"))
			return
		}
		if h := forwardedBy(r); h != "" {
			WriteError(w, r, errx.Forbidden("an open port refuses proxied requests ("+h+"); use SEAPORT_AUTH=token behind a proxy"))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// forwardingHeaders mark a request relayed by a proxy or tunnel.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host",
	"X-Forwarded-Proto", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip", "Tailscale-User-Login"}

// forwardedBy names the first forwarding header present, or "".
func forwardedBy(r *http.Request) string {
	for _, h := range forwardingHeaders {
		if _, ok := r.Header[h]; ok {
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

func (o Open) Admin(next http.Handler) http.Handler {
	// Browsers on other sites must not drive an open port either.
	return CrossOrigin(o.with(RoleAdmin, next))
}

// Crew: connectors send no Origin, browsers on other sites do (the
// WebSocket handshake also checks it).
func (o Open) Crew(next http.Handler) http.Handler { return CrossOrigin(o.with(RoleCrew, next)) }

func (o Open) Session() SessionRoutes {
	always := Handle(func(w http.ResponseWriter, r *http.Request) error {
		return JSON(w, http.StatusOK, SessionState{Authenticated: true, Subject: "local", Permissions: permission.All})
	})
	none := Handle(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	// Through with: a rebinding page must not learn the port is open.
	return SessionRoutes{Login: o.with(RoleAdmin, always), Current: o.with(RoleAdmin, always), Logout: none}
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

func (t Tokens) Admin(next http.Handler) http.Handler {
	p := Principal{Organization: t.Organization, Role: RoleAdmin, Subject: "admin", Permissions: permission.All}
	return t.Sessions.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	}))
}

func (t Tokens) Crew(next http.Handler) http.Handler { return CrewAuth(t.CrewTokens, next) }

func (t Tokens) Session() SessionRoutes {
	return SessionRoutes{Login: t.Sessions.Login(), Current: t.Sessions.Current(), Logout: t.Sessions.Logout()}
}

// CrossOrigin rejects unsafe requests a browser sends from another origin.
func CrossOrigin(next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errx.Forbidden("cross-origin request rejected"))
	}))
	return csrf.Handler(next)
}
