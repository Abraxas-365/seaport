package stdhttp

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
)

// Handler mounts a net/http handler that stays net/http (the MCP SDK, the
// model gateway's ReverseProxy) on a Fiber route, behind the route's
// middleware.
//
//   - The request's context is Fiber's c.Context() (it carries the principal
//     the authenticator set, the request id Edges made and the deadline
//     Deadline set), never the fasthttp.RequestCtx the adaptor passes by
//     default: fasthttp recycles that object when the Fiber handler returns,
//     while a streaming handler (the gateway's answers) keeps running, so
//     reading it later races and sees nothing Fiber's middleware stored.
//     Fasthttp reads nothing from the socket while a handler runs, so the
//     context is not cancelled when the client leaves: a stream notices by a
//     failed write, which ends the copy and, for the gateway, cancels the
//     upstream call.
//   - The context ends when the handler does (cancel on return), as net/http's
//     does, also for a stream that outlives the Fiber handler.
//   - The edge headers are put in the handler's header map first, as
//     httpx.Edges did, so a header the handler sets replaces them.
//   - A panic ends as before: logged with its value, the generic 500 (or
//     http.ErrAbortHandler passed on, which closes the connection).
func Handler(h http.Handler) fiber.Handler {
	return adaptor.HTTPHandlerWithContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local, ok := adaptor.LocalContextFromHTTPRequest(r); ok {
			ctx, cancel := context.WithCancel(local)
			defer cancel()
			r = r.WithContext(ctx)
		}
		id := httpx.RequestID(r.Context())
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Error("panic", "request_id", id, "panic", rec)
				WriteError(w, r, errx.Internal(errx.GenericMessage))
			}
		}()
		for _, e := range httpx.EdgeHeaders(id, r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
			w.Header().Set(e[0], e[1])
		}
		h.ServeHTTP(w, r)
	}))
}
