package bootstrap

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/auth/adapters/authhttp"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
)

// nativeRoutes is what the Fiber routes need from Build.
type nativeRoutes struct {
	cfg   config.Server
	auth  httpx.Authenticator
	limit *httpx.RateLimit
	// admin: the Register of each migrated domain's admin routes.
	admin []func(httpx.Router)
	// public: routes outside /api/ that carry their own proof (a sealed
	// state, a signature): the GitHub, Jira and Slack docks'.
	public      []func(httpx.Router)
	githubToken fiber.Handler
	// The crew's own endpoints (GET /crew/profile, GET /crew/attachments/:id, POST /crew/reply).
	kit, attachment, reply, workspace fiber.Handler
}

// mountNative registers the routes served by Fiber handlers, in the order
// that matters (Fiber routes by registration order): the public ones, then
// the admin middleware of /api/ and everything behind it, then the crew
// routes. What a domain hasn't moved yet is reached through the fallback
// registered after this (server.go) with the principal already in the
// request's context.
func mountNative(app *fiber.App, legacy fiber.Handler, r nativeRoutes) {
	auth := r.auth
	session := auth.Session()
	limit := r.limit.Middleware()
	// Public: how the UI must sign in, and session login/state (they check
	// credentials themselves).
	body := map[string]any{"auth": r.cfg.Auth, "version": Version}
	app.Get("/api/v1/config", func(c fiber.Ctx) error { return httpx.JSON(c, http.StatusOK, body) })
	app.Post("/api/v1/session", session.Login)
	app.Get("/api/v1/session", session.Current)
	if session.SignIn != nil {
		app.Get("/api/v1/session/login", session.SignIn)
		app.Get(authhttp.CallbackPath, session.Callback)
	}

	for _, register := range r.public {
		register(app)
	}

	// The admin API: authentication, then the per-caller rate limit (it keys
	// on the principal).
	app.Use("/api/", auth.Admin(), limit)
	app.Delete("/api/v1/session", session.Logout)
	for _, register := range r.admin {
		register(app)
	}

	// The crew's endpoints: crew authentication, then the rate limit.
	app.All("/crew/ws", auth.Crew(), limit, legacy)
	app.All("/mcp", auth.Crew(), limit, legacy)
	app.Get("/crew/profile", auth.Crew(), limit, r.kit)
	app.Get("/crew/attachments/:id", auth.Crew(), limit, r.attachment)
	app.Post("/crew/reply", auth.Crew(), limit, r.reply)
	app.Get("/crew/workspace", auth.Crew(), limit, r.workspace)
	// App tokens for the agent's repos (gh and git inside its slot).
	app.Post("/crew/github/token", auth.Crew(), limit, r.githubToken)
	// The model gateway: hosted slots call their provider with their slot
	// token (as x-api-key or bearer); not rate limited per request (one
	// agent turn makes many calls; the provider limits the key).
	app.All(hosting.GatewayPath+"/*", httpx.APIKeyAsBearer(), auth.Crew(), legacy)
}
