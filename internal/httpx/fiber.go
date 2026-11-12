package httpx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
)

// The Fiber shell of the server (see AGENTS.md, "HTTP server"): Edges and
// ErrorHandler are the Fiber form of what every request gets, Fallback
// hands everything the shell doesn't serve natively to the net/http handlers.

// Edges is the first middleware of the server: it gives the request an id
// (X-Request-ID when valid, else random; read back with RequestID(c.Context())),
// sets the security headers, rejects a Content-Encoding other than identity
// and turns a panic of a native handler into the generic 500.
//
// The headers are set after the rest of the chain ran, and only where the
// response has none: a handler served through Fallback sets its own (the SPA's
// Cache-Control), and the adaptor adds a handler's headers to the response
// instead of replacing them, so setting first would send both.
func Edges() fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		id := c.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		c.SetContext(WithRequestID(c.Context(), id))
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "request_id", id, "panic", rec)
				err = errx.Internal(errx.GenericMessage)
			}
			for _, h := range edgeHeaders(id, c.RequestCtx().IsTLS() || c.Get("X-Forwarded-Proto") == "https") {
				if c.GetRespHeader(h[0]) == "" {
					c.Set(h[0], h[1])
				}
			}
		}()
		if ce := c.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
			return errx.UnsupportedMediaType("Content-Encoding is not supported")
		}
		return c.Next()
	}
}

// edgeHeaders are the headers every response carries.
func edgeHeaders(id string, https bool) [][2]string {
	h := [][2]string{{"X-Request-ID", id}, {"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"}, {"Cache-Control", "no-store"}}
	if https {
		h = append(h, [2]string{"Strict-Transport-Security", "max-age=31536000"})
	}
	return h
}

// ErrorHandler is the Fiber app's: every error becomes the body WriteError
// writes (typed errors as they are, Fiber's own statuses through
// errx.FromStatus, anything else the generic 500).
func ErrorHandler(c fiber.Ctx, err error) error {
	var e *errx.Error
	var fe *fiber.Error
	switch {
	case errors.As(err, &e):
	case errors.As(err, &fe):
		e = errx.FromStatus(fe.Code, transportMessage(fe))
	default:
		e = errx.Internal(errx.GenericMessage)
	}
	id := RequestID(c.Context())
	if e.HTTPStatus >= 500 {
		slog.Error("request failed", "request_id", id, "method", c.Method(), "path", c.Path(),
			"status", e.HTTPStatus, "code", e.Code, "err", err)
	}
	if e.HTTPStatus == http.StatusUnauthorized {
		c.Set("WWW-Authenticate", "Bearer")
	}
	if s, ok := e.Details["retry_after_seconds"].(int); ok && e.HTTPStatus == http.StatusTooManyRequests {
		c.Set("Retry-After", strconv.Itoa(s))
	}
	return SendJSON(c, e.HTTPStatus, map[string]any{"error": e.ToResponse(id)})
}

// transportMessage is the text of a status the transport produced (the
// router's 404/405 say what NotFound and the 405 guard say today).
func transportMessage(fe *fiber.Error) string {
	switch fe.Code {
	case http.StatusNotFound:
		return "route not found"
	case http.StatusMethodNotAllowed:
		return "method not allowed"
	case http.StatusRequestEntityTooLarge:
		return "request body is too large"
	}
	return fe.Message
}

// SendJSON writes body with status, byte for byte what JSON does for a
// net/http handler (Content-Type without charset, trailing newline).
func SendJSON(c fiber.Ctx, status int, body any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	c.Status(status)
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Send(buf.Bytes())
}

// Deadline bounds the context services receive (c.Context()) to d, except
// for the requests skip names (streams, which end by themselves). Fiber's
// context never ends when the client leaves (fasthttp reads nothing from the
// socket while a handler runs), so without a bound a request waiting on the
// database or on another process would outlive its caller. The context is
// cancelled when the handler returns; when the response is still being
// produced (a body stream, a hijacked connection) it ends at its deadline,
// or earlier when the code producing the response cancels its own copy
// (Fallback does).
func Deadline(d time.Duration, skip func(fiber.Ctx) bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		if skip != nil && skip(c) {
			return c.Next()
		}
		ctx, cancel := context.WithTimeout(c.Context(), d)
		c.SetContext(ctx)
		keep := false
		defer func() {
			if !keep {
				cancel()
			}
		}()
		err := c.Next()
		keep = err == nil && (c.Response().IsBodyStream() || c.RequestCtx().Hijacked())
		return err
	}
}

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
		id := RequestID(r.Context())
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Error("panic", "request_id", id, "panic", rec)
				WriteError(w, r, errx.Internal(errx.GenericMessage))
			}
		}()
		for _, e := range edgeHeaders(id, r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
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
