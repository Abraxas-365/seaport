// Package httpx holds the HTTP edges shared by every inbound adapter:
// the one error writer, strict JSON decoding, pagination parsing, request
// ids, security headers and bearer-token auth. stdlib net/http only.
package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/query"
)

// MaxJSONBody is the request body limit for JSON endpoints.
const MaxJSONBody = 1 << 20

type ctxKey int

const requestIDKey ctxKey = iota

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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
	id := RequestID(r.Context())
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

// JSON writes body with status.
func JSON(w http.ResponseWriter, status int, body any) error {
	writeJSON(w, status, body)
	return nil
}

// File answers 200 with stored bytes of a sniffed type (message media):
// never cached (decrypted media stays off disks), sniffed again or run by
// the browser.
func File(w http.ResponseWriter, contentType string, data []byte) error {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}

// Created answers 201 with Location = request path + "/" + rel.
func Created(w http.ResponseWriter, r *http.Request, rel string, body any) error {
	w.Header().Set("Location", strings.TrimRight(r.URL.Path, "/")+"/"+strings.TrimLeft(rel, "/"))
	return JSON(w, http.StatusCreated, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// RequestID returns the request id set by the Edges middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Edges wraps a handler with request id, security headers, Content-Encoding
// rejection, panic recovery and the JSON body limit.
func Edges(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		h := w.Header()
		h.Set("X-Request-ID", id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if ce := r.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
			WriteError(w, r, errx.UnsupportedMediaType("Content-Encoding is not supported"))
			return
		}
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Error("panic", "request_id", id, "panic", rec)
				WriteError(w, r, errx.Internal(errx.GenericMessage))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NotFound answers unknown routes with the standard error body.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, errx.NotFound("route not found"))
}

// RequireBearer rejects requests whose bearer token is not token
// (constant-time compare).
func RequireBearer(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), want) != 1 {
			WriteError(w, r, errx.Unauthorized("missing or invalid token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DecodeJSON decodes a single JSON object strictly: JSON content type,
// size limit, unknown fields rejected, no trailing data.
func DecodeJSON(r *http.Request, into any) error {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		return errx.UnsupportedMediaType("Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxJSONBody+1))
	if err != nil {
		return errx.Validation("could not read request body")
	}
	if len(body) > MaxJSONBody {
		return errx.PayloadTooLarge("request body is too large (max 1 MiB)")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errx.Validation("request body is required")
	}
	if bytes.TrimSpace(body)[0] != '{' {
		return errx.Validation("request body must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return jsonError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errx.Validation("request body must contain a single JSON value")
	}
	return nil
}

// ReadBody reads a raw request body (an upload) of one of the given
// content types, at most max bytes.
func ReadBody(r *http.Request, max int64, contentTypes ...string) ([]byte, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !slices.Contains(contentTypes, mt) {
		return nil, errx.UnsupportedMediaType("Content-Type must be " + strings.Join(contentTypes, " or "))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, errx.Validation("could not read request body")
	}
	if int64(len(body)) > max {
		return nil, errx.PayloadTooLarge("request body is too large (max " + strconv.FormatInt(max>>20, 10) + " MiB)")
	}
	if len(body) == 0 {
		return nil, errx.Validation("request body is required")
	}
	return body, nil
}

func jsonError(err error) error {
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		return errx.InvalidField(typ.Field, typ.Field+" has an invalid type")
	}
	var e *errx.Error
	if errors.As(err, &e) {
		return e
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		field = strings.Trim(field, `"`)
		return errx.InvalidField(field, `unknown field "`+field+`"`)
	}
	return errx.Validation("request body is not valid JSON")
}

// Pagination parses limit/offset strictly (bad values are 400, not ignored).
func Pagination(r *http.Request) (query.Pagination, error) {
	p := query.Pagination{}
	q := r.URL.Query()
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return p, errx.InvalidField("limit", "limit must be an integer of at least 1")
		}
		p.Limit = n
	}
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > query.MaxOffset {
			return p, errx.InvalidField("offset", "offset must be an integer between 0 and "+strconv.Itoa(query.MaxOffset))
		}
		p.Offset = n
	}
	return p.Normalize(), nil
}

// BoolQuery parses an optional boolean query parameter.
func BoolQuery(r *http.Request, name string) (*bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errx.InvalidField(name, name+" must be true or false")
	}
	return &b, nil
}
