// Package stdhttp is the net/http side of the server: the few handlers that
// stay net/http because a library hands them over as one (the MCP SDK, the
// model gateway's ReverseProxy), mounted on Fiber routes by Handler. The
// helpers here are what such a handler needs: Handle and WriteError for its
// errors, Caller for its principal. Everything else is httpx, over Fiber.
package stdhttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

// HandlerFunc is an http handler that returns its error instead of writing it.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts a HandlerFunc: a returned error goes through WriteError.
func Handle(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	})
}

// WriteError is the only place errors become responses.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *errx.Error
	if !errors.As(err, &e) {
		e = errx.Internal(errx.GenericMessage)
	}
	id := httpx.RequestID(r.Context())
	if e.HTTPStatus >= 500 {
		slog.Error("request failed", "request_id", id, "method", r.Method, "path", r.URL.Path,
			"status", e.HTTPStatus, "code", e.Code, "err", err)
	}
	if e.HTTPStatus == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if s, ok := e.Details["retry_after_seconds"].(int); ok && e.HTTPStatus == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(s))
	}
	writeJSON(w, e.HTTPStatus, map[string]any{"error": e.ToResponse(id)})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Caller is the request's principal (401 when missing).
func Caller(r *http.Request) (httpx.Principal, error) {
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok {
		return httpx.Principal{}, errx.Unauthorized("missing or invalid token")
	}
	return p, nil
}
