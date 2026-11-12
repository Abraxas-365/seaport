package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

// The HTTP server is a Fiber app (AGENTS.md, "HTTP server"): this file is its
// shell (the edge headers and request ids, the error body, /healthz, the
// limits and the shutdown) and routes.go its routes.

// bodyLimit is the largest request body the server accepts. Fiber buffers a
// body whole before any handler runs, so it is one limit for every route; the
// handlers keep their own, smaller ones (httpx.MaxJSONBody, hosting.MaxHomeUpload,
// crew.MaxReplyBody). It has to cover the biggest of those: crew.MaxReplyBody
// (25 MiB of files as base64, plus 1 MiB; 34.3 MiB) and hosting.MaxHomeUpload
// (25 MiB). TestBodyLimitCoversTheBiggestBodies checks it.
const bodyLimit = 40 << 20

// Timeouts, as http.Server had them (header read 10 s, keep-alive idle
// 120 s, nothing on the write side: SSE and model streams live as long as
// they live) plus a bound on reading a body, which a buffering server needs
// (an upload is read whole before its handler, and so before authentication,
// runs; http.Server read nothing before the caller was authenticated).
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
func newServer(mount func(app *fiber.App)) *fiber.App {
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
	// its own bound.
	app.Server().HeaderReceived = func(*fasthttp.RequestHeader) fasthttp.RequestConfig {
		return fasthttp.RequestConfig{ReadTimeout: bodyTimeout}
	}
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
