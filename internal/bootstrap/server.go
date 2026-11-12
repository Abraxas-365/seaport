package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockgithub"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockjira"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockslack"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

// The HTTP server is a Fiber app (AGENTS.md, "HTTP server"): this file is its
// shell (the edge headers and request ids, the error body, /healthz, the
// limits and the shutdown) and routes.go its routes.

// Request bodies. fasthttp reads a body whole before any handler runs, so
// before authentication too, and anyone can send one: the limit is chosen per
// request from its headers (Server.HeaderReceived, bodyLimitFor) instead of
// one big number for every route. Everything is httpx.MaxJSONBody except the
// routes in bodyTiers, which take the bigger bodies their handlers accept
// (each handler keeps its own, exact check too); the big ones only when the
// request carries credentials (a stranger can't make the server buffer
// 64 MiB), the signed webhooks at their own size. bodyLimit, fasthttp's
// global limit, is the biggest tier and only a ceiling:
// TestBodyLimitCoversTheBiggestBodies checks every tier against the
// constants its routes use.
type bodyTier struct {
	method string // "" = any
	path   string
	prefix bool // path is a prefix (else exact)
	limit  int
	// authed: the big uploads, only for requests that carry a credential
	// when the server authenticates (not in AuthNone, where nobody does).
	authed bool
}

var bodyTiers = []bodyTier{
	{method: http.MethodPost, path: "/api/v1/homes", limit: hosting.MaxHomeUpload, authed: true},
	{method: http.MethodPost, path: "/crew/reply", limit: crew.MaxReplyBody, authed: true},
	{path: "/mcp", limit: crew.MaxReplyBody, authed: true},
	{path: hosting.GatewayPath + "/", prefix: true, limit: hosting.MaxGatewayBody, authed: true},
	// Webhooks carry their own proof (a signature), not a credential header.
	{method: http.MethodPost, path: dockgithub.WebhookPath, prefix: true, limit: dockgithub.MaxWebhook},
	{method: http.MethodPost, path: dockjira.WebhookPath, prefix: true, limit: dockjira.MaxWebhook},
	{method: http.MethodPost, path: dockslack.EventsPath, prefix: true, limit: dockslack.MaxEvent},
}

// bodyLimit is the largest limit of any tier: fasthttp's global ceiling.
var bodyLimit = func() int {
	n := httpx.MaxJSONBody
	for _, t := range bodyTiers {
		n = max(n, t.limit)
	}
	return n
}()

// bodyLimitFor is the body limit of a request: path is the normalized path,
// credentials whether it carries any (see bodyTier.authed).
func bodyLimitFor(method, path string, credentials bool) int {
	limit := httpx.MaxJSONBody
	for _, t := range bodyTiers {
		if (t.method != "" && t.method != method) || t.authed && !credentials {
			continue
		}
		if t.prefix && strings.HasPrefix(path, t.path) || !t.prefix && path == t.path {
			limit = max(limit, t.limit)
		}
	}
	return limit
}

// requestConfig is fasthttp's per-request configuration, from the headers:
// the body's limit and its read timeout.
func requestConfig(requireCredentials bool) func(*fasthttp.RequestHeader) fasthttp.RequestConfig {
	return func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
		// The path as the router will see it (decoded, dot segments gone).
		var u fasthttp.URI
		path := "/"
		if u.Parse(nil, h.RequestURI()) == nil {
			path = string(u.Path())
		}
		credentials := !requireCredentials || len(h.Peek(fiber.HeaderAuthorization)) > 0 ||
			len(h.Peek("X-Api-Key")) > 0 || len(h.Cookie(httpx.SessionCookie)) > 0
		return fasthttp.RequestConfig{ReadTimeout: bodyTimeout,
			MaxRequestBodySize: bodyLimitFor(string(h.Method()), path, credentials)}
	}
}

// Timeouts, as http.Server had them (header read 10 s, keep-alive idle
// 120 s, nothing on the write side: SSE and model streams live as long as
// they live) plus a bound on reading a body, which a buffering server needs
// (an upload is read whole before its handler, and so before authentication,
// runs; http.Server read nothing before the caller was authenticated; the
// per-request limits above keep what a stranger can make it read small).
const (
	headerTimeout   = 10 * time.Second
	bodyTimeout     = 5 * time.Minute
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 10 * time.Second
	// requestDeadline bounds the context of a request that isn't a stream.
	// net/http cancelled it when the client left; fasthttp can't notice that
	// while a handler runs, so a request's database and routed dock waits
	// (2 minutes at most) would otherwise outlive it.
	requestDeadline = 5 * time.Minute
)

// newServer builds the Fiber app: its shell and the routes mount registers.
// requireCredentials: the server authenticates (any mode but none), so its
// big uploads are only read for requests that carry a credential.
func newServer(requireCredentials bool, mount func(app *fiber.App)) *fiber.App {
	app := fiber.New(fiber.Config{
		// Pooled request buffers: what a handler keeps (a path parameter in
		// an event, a goroutine) must not change under it.
		Immutable:    true,
		BodyLimit:    bodyLimit,
		ErrorHandler: httpx.ErrorHandler,
		ReadTimeout:  headerTimeout,
		IdleTimeout:  idleTimeout,
		// The net/http mux is exact about case and trailing slashes.
		CaseSensitive: true,
		StrictRouting: true,
	})
	// ReadTimeout above covers the headers; once they are in, the body gets
	// its own bound and limit.
	app.Server().HeaderReceived = requestConfig(requireCredentials)
	app.Use(httpx.Edges())
	app.Use(httpx.Deadline(requestDeadline, isStream))
	// Public liveness: nothing about any organization (crew counts are per
	// organization at /api/v1/crew).
	app.Get("/healthz", func(c fiber.Ctx) error {
		return httpx.JSON(c, http.StatusOK, map[string]any{"status": "ok", "version": Version})
	})
	app.All("/healthz", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderAllow, "GET, HEAD")
		return errx.MethodNotAllowed("method not allowed")
	})
	if mount != nil {
		mount(app)
	}
	return app
}

// isStream: requests that live as long as their stream does (SSE, the crew
// WebSocket, the model gateway's streamed answers), which no deadline may cut.
func isStream(c fiber.Ctx) bool {
	p := c.Path()
	return p == "/api/v1/events" || p == "/crew/ws" || strings.HasPrefix(p, hosting.GatewayPath+"/")
}

// Serve runs the HTTP server on the configured address until ctx ends.
func (a *App) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.cfg.Addr)
	if err != nil {
		return errx.Wrap(err, "listen", errx.TypeInternal)
	}
	slog.Info("seaport listening", "addr", a.cfg.Addr, "public_url", a.cfg.PublicURL, "version", Version)
	return a.ServeListener(ctx, ln)
}

// ServeListener serves on ln until ctx ends, then shuts down. First the
// long-lived connections end: SSE streams, and the crew's WebSockets through
// Hub.Close (which releases their deliveries; App.Close runs it again,
// harmlessly). fasthttp's shutdown waits for hijacked connections, so with
// one open it would sit out its whole timeout. Then in-flight requests
// finish (10 s at most).
func (a *App) ServeListener(ctx context.Context, ln net.Listener) error {
	return serve(ctx, a.web, ln, func() {
		a.stopOnce()
		a.crew.Hub.Close()
	})
}

func serve(ctx context.Context, app *fiber.App, ln net.Listener, endStreams func()) error {
	errc := make(chan error, 1)
	go func() {
		errc <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true, ShutdownTimeout: shutdownTimeout})
	}()
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			return errx.Wrap(err, "listen", errx.TypeInternal)
		}
		return nil
	case <-ctx.Done():
		endStreams()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		err := app.ShutdownWithContext(sctx)
		<-errc
		return err
	}
}
