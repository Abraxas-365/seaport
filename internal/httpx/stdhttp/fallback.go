package stdhttp

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
)

// Fallback serves h, the net/http routes not migrated yet, behind the Fiber
// app (the strangler).
//
//   - The request's context is Fiber's c.Context() (it carries the request id
//     Edges made and the deadline Deadline set), never the fasthttp.RequestCtx the
//     adaptor passes by default: fasthttp recycles that object when the Fiber
//     handler returns, while a streaming handler (SSE, the model gateway) and a
//     hijacked connection (the crew WebSocket) keep running, so reading it
//     later races and sees nothing Fiber's middleware stored. Fasthttp reads
//     nothing from the socket while a handler runs, so the context is not
//     cancelled when the client leaves: a stream notices by a failed write
//     (SSE: at the next event or heartbeat, not at once).
//   - The context ends when the handler does (cancel on return), as net/http's
//     does, also for a stream that outlives the Fiber handler.
//   - The edge headers are put in the handler's header map first, as
//     httpx.Edges did, so a header the handler sets replaces them (and they
//     also go out with a WebSocket's 101).
//   - A panic ends as before: logged with its value, the generic 500 (or
//     http.ErrAbortHandler passed on, which closes the connection).
func Fallback(h http.Handler) fiber.Handler {
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
		h.ServeHTTP(&upgradeWriter{ResponseWriter: w}, r)
	}))
}

// upgradeWriter completes a protocol switch the adaptor's writer drops:
// net/http writes the "101 Switching Protocols" header block when the handler
// hijacks the connection, the adaptor's Hijack writes nothing (it was made
// for handlers that take over a connection they answered themselves), so a
// WebSocket handshake (WriteHeader(101), then Hijack) never got its answer
// and the client waited forever. Everything else passes through.
type upgradeWriter struct {
	http.ResponseWriter
	switching bool
}

func (u *upgradeWriter) WriteHeader(code int) {
	u.switching = code == http.StatusSwitchingProtocols
	u.ResponseWriter.WriteHeader(code)
}

func (u *upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := u.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hj.Hijack()
	if err != nil || !u.switching {
		return conn, rw, err
	}
	// Written to the connection itself, ahead of anything the hijacker
	// buffers in rw.Writer (nothing yet: it only has what was written
	// before WriteHeader).
	head := bufio.NewWriter(conn)
	_, _ = head.WriteString("HTTP/1.1 101 " + http.StatusText(http.StatusSwitchingProtocols) + "\r\n")
	if err := u.Header().Write(head); err == nil {
		_, err = head.WriteString("\r\n")
	}
	if err := head.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

// Unwrap lets http.ResponseController reach the adaptor's writer (Flush).
func (u *upgradeWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }
