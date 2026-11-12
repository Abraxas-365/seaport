package bootstrap

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/auth/adapters/authhttp"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/errx"
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
	// events is the SSE stream (GET /api/v1/events).
	events fiber.Handler
	// ws is the crew hub's WebSocket (GET /crew/ws).
	ws fiber.Handler
	// mcp is the MCP SDK's handler (POST /mcp) and gateway the model
	// gateway's ReverseProxy: net/http inside (stdhttp.Handler).
	mcp, gateway fiber.Handler
	// The crew's own endpoints (GET /crew/profile, GET /crew/attachments/:id, POST /crew/reply).
	kit, attachment, reply, workspace fiber.Handler
	// ui is the admin web app: every GET the routes above leave unanswered.
	ui fiber.Handler
}

// mountNative registers every route of the server, in the order that matters
// (Fiber routes by registration order): the public ones, then the admin
// middleware of /api/ and everything behind it, then the crew routes, then
// the web app for what is left.
func mountNative(app *fiber.App, r nativeRoutes) {
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
	app.Get("/api/v1/events", r.events)

	// The crew's endpoints: crew authentication, then the rate limit.
	app.Get("/crew/ws", auth.Crew(), limit, r.ws)
	app.All("/mcp", auth.Crew(), limit, r.mcp)
	app.Get("/crew/profile", auth.Crew(), limit, r.kit)
	app.Get("/crew/attachments/:id", auth.Crew(), limit, r.attachment)
	app.Post("/crew/reply", auth.Crew(), limit, r.reply)
	app.Get("/crew/workspace", auth.Crew(), limit, r.workspace)
	// App tokens for the agent's repos (gh and git inside its slot).
	app.Post("/crew/github/token", auth.Crew(), limit, r.githubToken)
	// The model gateway: hosted slots call their provider with their slot
	// token (as x-api-key or bearer); not rate limited per request (one
	// agent turn makes many calls; the provider limits the key).
	app.All(hosting.GatewayPath+"/*", httpx.APIKeyAsBearer(), auth.Crew(), r.gateway)

	// What no route answered. Under /api/ (past the admin authenticator, so
	// a stranger learns nothing about which routes exist) it is a 404 in the
	// standard body, never the web app; the rest is the web app's: GETs get
	// index.html or a static file, other methods 404. Any method matches
	// these catch-alls, so a known path with the wrong method is a 404 too,
	// as it has been since the mux.
	app.All("/api/*", func(fiber.Ctx) error { return errx.NotFound("route not found") })
	app.All("/*", r.ui)
}
