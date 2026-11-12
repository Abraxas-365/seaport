// Package httpx holds the HTTP edges shared by every inbound adapter, over
// Fiber: the one error body, strict JSON decoding, pagination parsing,
// request ids, the principal and bearer-token auth. Handlers are Fiber
// handlers (func(fiber.Ctx) error) that return their errors; fiber.go is the
// server shell (Edges, ErrorHandler, Deadline), stdhttp the net/http form of
// the helpers for handlers that haven't moved yet.
package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/gofiber/fiber/v3"
)

// MaxJSONBody is the request body limit for JSON endpoints.
const MaxJSONBody = 1 << 20

type ctxKey int

const requestIDKey ctxKey = iota

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Router is where a domain's routes are registered (the server's Fiber app).
// Modules and adapters name it through httpx so that only the adapters
// that read the request import Fiber.
type Router = fiber.Router

// Handler and Ctx name Fiber's types for assemblers (modules), which
// may not import Fiber themselves.
type (
	Handler = fiber.Handler
	Ctx     = fiber.Ctx
)

// JSON writes body with status (Content-Type without charset, a trailing
// newline: the bytes net/http handlers always wrote).
func JSON(c fiber.Ctx, status int, body any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	c.Status(status)
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Send(buf.Bytes())
}

// File answers 200 with stored bytes of a sniffed type (message media):
// never cached (decrypted media stays off disks), sniffed again or run by
// the browser.
func File(c fiber.Ctx, contentType string, data []byte) error {
	c.Set("Content-Type", contentType)
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cache-Control", "private, no-store")
	c.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Status(fiber.StatusOK)
	return c.Send(data)
}

// Download answers a file to save, never to show: like File, plus a
// Content-Disposition naming it.
func Download(c fiber.Ctx, contentType, name string, data []byte) error {
	c.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	return File(c, contentType, data)
}

// Created answers 201 with Location = request path + "/" + rel.
func Created(c fiber.Ctx, rel string, body any) error {
	c.Set("Location", strings.TrimRight(c.Path(), "/")+"/"+strings.TrimLeft(rel, "/"))
	return JSON(c, fiber.StatusCreated, body)
}

// Param is a path parameter, percent-decoded as net/http's PathValue was
// (Fiber routes on the raw path; "*" is the tail of a wildcard route, without
// its leading slash).
func Param(c fiber.Ctx, key string) string {
	v := c.Params(key)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

// RequestID returns the request id set by the Edges middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID returns ctx carrying the request id (RequestID reads it).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DecodeJSON decodes a single JSON object strictly: JSON content type,
// size limit (MaxJSONBody), unknown fields rejected, no trailing data.
func DecodeJSON(c fiber.Ctx, into any) error {
	return DecodeJSONMax(c, into, MaxJSONBody)
}

// DecodeJSONMax is DecodeJSON with a body limit of max bytes (requests
// that carry files as base64).
func DecodeJSONMax(c fiber.Ctx, into any, max int64) error {
	mt, _, _ := mime.ParseMediaType(c.Get("Content-Type"))
	if mt != "application/json" {
		return errx.UnsupportedMediaType("Content-Type must be application/json")
	}
	body := c.Body()
	if int64(len(body)) > max {
		return errx.PayloadTooLarge("request body is too large (max " + strconv.FormatInt(max>>20, 10) + " MiB)")
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
func ReadBody(c fiber.Ctx, max int64, contentTypes ...string) ([]byte, error) {
	mt, _, _ := mime.ParseMediaType(c.Get("Content-Type"))
	if !slices.Contains(contentTypes, mt) {
		return nil, errx.UnsupportedMediaType("Content-Type must be " + strings.Join(contentTypes, " or "))
	}
	body := c.Body()
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
func Pagination(c fiber.Ctx) (query.Pagination, error) {
	p := query.Pagination{}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return p, errx.InvalidField("limit", "limit must be an integer of at least 1")
		}
		p.Limit = n
	}
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > query.MaxOffset {
			return p, errx.InvalidField("offset", "offset must be an integer between 0 and "+strconv.Itoa(query.MaxOffset))
		}
		p.Offset = n
	}
	return p.Normalize(), nil
}

// BoolQuery parses an optional boolean query parameter.
func BoolQuery(c fiber.Ctx, name string) (*bool, error) {
	raw := c.Query(name)
	if raw == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errx.InvalidField(name, name+" must be true or false")
	}
	return &b, nil
}
