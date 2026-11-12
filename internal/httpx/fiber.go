package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/gofiber/fiber/v3"
)

// The Fiber shell of the server (see AGENTS.md, "HTTP server"): Edges and
// ErrorHandler are what every request gets.

// Edges is the first middleware of the server: it gives the request an id
// (X-Request-ID when valid, else random; read back with RequestID(c.Context())),
// sets the security headers, rejects a Content-Encoding other than identity
// and turns a panic of a native handler into the generic 500.
//
// The headers are set after the rest of the chain ran, and only where the
// response has none: a handler that sets its own (the SPA's
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
			for _, h := range EdgeHeaders(id, c.RequestCtx().IsTLS() || c.Get("X-Forwarded-Proto") == "https") {
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

// EdgeHeaders are the headers every response carries.
func EdgeHeaders(id string, https bool) [][2]string {
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
	return JSON(c, e.HTTPStatus, map[string]any{"error": e.ToResponse(id)})
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

// Deadline bounds the context services receive (c.Context()) to d, except
// for the requests skip names (streams, which end by themselves). Fiber's
// context never ends when the client leaves (fasthttp reads nothing from the
// socket while a handler runs), so without a bound a request waiting on the
// database or on another process would outlive its caller. The context is
// cancelled when the handler returns; when the response is still being
// produced (a body stream, a hijacked connection) it ends at its deadline,
// or earlier when the code producing the response cancels its own copy
// (stdhttp.Handler does).
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
