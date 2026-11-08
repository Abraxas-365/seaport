package httpx

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
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
}

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
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return identity.OrganizationID{}, errx.Unauthorized("missing or invalid token")
	}
	return p.Organization, nil
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

// SessionRoutes are the browser session endpoints of a mode.
type SessionRoutes struct {
	Login, Current, Logout http.Handler
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
	return crossOrigin(o.with(RoleAdmin, next))
}

// Crew: connectors send no Origin, browsers on other sites do (the
// WebSocket handshake also checks it).
func (o Open) Crew(next http.Handler) http.Handler { return crossOrigin(o.with(RoleCrew, next)) }

func (o Open) Session() SessionRoutes {
	always := Handle(func(w http.ResponseWriter, r *http.Request) error {
		return JSON(w, http.StatusOK, sessionResponse{Authenticated: true})
	})
	none := Handle(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	// Through with: a rebinding page must not learn the port is open.
	return SessionRoutes{Login: o.with(RoleAdmin, always), Current: o.with(RoleAdmin, always), Logout: none}
}

// Tokens is SEAPORT_AUTH=token: a static admin token (bearer, or traded
// for a session cookie) and a static crew token, one organization.
type Tokens struct {
	Organization identity.OrganizationID
	Sessions     Sessions // admin token + cookie settings
	CrewToken    string
}

var _ Authenticator = Tokens{}

func (t Tokens) Admin(next http.Handler) http.Handler {
	p := Principal{Organization: t.Organization, Role: RoleAdmin, Subject: "admin"}
	return t.Sessions.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	}))
}

func (t Tokens) Crew(next http.Handler) http.Handler {
	p := Principal{Organization: t.Organization, Role: RoleCrew, Subject: "crew"}
	return RequireBearer(t.CrewToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	}))
}

func (t Tokens) Session() SessionRoutes {
	return SessionRoutes{Login: t.Sessions.Login(), Current: t.Sessions.Current(), Logout: t.Sessions.Logout()}
}

// crossOrigin rejects unsafe requests a browser sends from another origin.
func crossOrigin(next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errx.Forbidden("cross-origin request rejected"))
	}))
	return csrf.Handler(next)
}
