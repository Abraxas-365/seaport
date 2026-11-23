package dockwebhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/gofiber/fiber/v3"
)

// WebhookPath is where providers call this port; the dock id follows.
const WebhookPath = "/hooks/"

// MaxWebhook bounds a webhook body: the default JSON limit, so the route
// needs no body tier of its own.
const MaxWebhook = httpx.MaxJSONBody

// MaxEvents bounds a JSON array body: one message per element.
const MaxEvents = 20

// PerMinute is how many calls a dock takes per minute (a burst of that
// many, then one a second); more are 429 with Retry-After.
const PerMinute = 60

// receiveWithin bounds the work of one event: Receive is quick, but it may
// open the dock's mirror first (ship.MirrorWithin).
const receiveWithin = 10*time.Second + ship.MirrorWithin

// Handler serves the webhook routes: the admin API of a webhook dock and
// the public endpoint.
type Handler struct {
	g     *Gateway
	docks dock.Queries
	limit *httpx.RateLimit
}

func NewHandler(g *Gateway, docks dock.Queries) *Handler {
	return &Handler{g: g, docks: docks, limit: httpx.NewRateLimit(1, PerMinute)}
}

// Register mounts the admin routes (behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/docks/:id/webhook", httpx.RequireFunc(permission.DocksRead, h.info))
	r.Put("/api/v1/docks/:id/webhook", httpx.RequireFunc(permission.DocksWrite, h.save))
	r.Post("/api/v1/docks/:id/webhook/secret", httpx.RequireFunc(permission.DocksWrite, h.rotate))
}

// RegisterPublic mounts the endpoint providers call without a Seaport
// credential: it checks the dock's secret.
func (h *Handler) RegisterPublic(r httpx.Router) {
	r.Post("/hooks/:dock", h.hook)
}

// webhookDock finds the caller's dock and checks it is a webhook one.
func (h *Handler) webhookDock(c fiber.Ctx) (dock.Dock, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return dock.Dock{}, err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return dock.Dock{}, err
	}
	d, err := h.docks.Find(c.Context(), org, id)
	if err != nil {
		return dock.Dock{}, err
	}
	if d.Channel != dock.ChannelWebhook {
		return dock.Dock{}, errx.Business("not a webhook dock")
	}
	return d, nil
}

// Info is the webhook side of a dock.
type Info struct {
	// URL is where providers call this port; Reachable is false when it
	// can't be on the internet (loopback, plain http).
	URL       string   `json:"url"`
	Reachable bool     `json:"reachable"`
	Template  Template `json:"template"`
	// HasSecret: the endpoint exists (a linked dock). The secret itself is
	// shown once, by the answer that made it (rotating), never read back.
	HasSecret bool       `json:"has_secret"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Secret    string     `json:"secret,omitempty"`
	// Preview is a sample body rendered with the template (PUT with a sample).
	Preview *Message `json:"preview,omitempty"`
}

func (h *Handler) base(id identity.DockID) Info {
	return Info{URL: h.g.cfg.PublicURL + WebhookPath + id.String(), Reachable: reachable(h.g.cfg.PublicURL)}
}

func (h *Handler) fill(out *Info, hook Hook) {
	out.Template, out.HasSecret = hook.Template, true
	created := hook.CreatedAt
	out.CreatedAt = &created
}

// reachable: a provider can call this URL (https, not loopback).
func reachable(public string) bool {
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1"
}

func (h *Handler) info(c fiber.Ctx) error {
	d, err := h.webhookDock(c)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	hook, err := h.g.store.Hook(c.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(c, http.StatusOK, out)
	case err != nil:
		return err
	}
	h.fill(&out, hook)
	return httpx.JSON(c, http.StatusOK, out)
}

type saveRequest struct {
	Template Template `json:"template"`
	// Sample is a body to render with the template: any JSON value (an
	// array renders its first element).
	Sample json.RawMessage `json:"sample"`
}

// save checks and stores the template and, with a sample, answers how the
// sample would look as a message.
func (h *Handler) save(c fiber.Ctx) error {
	d, err := h.webhookDock(c)
	if err != nil {
		return err
	}
	var req saveRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	prog, err := req.Template.Compile()
	if err != nil {
		return err
	}
	var preview *Message
	if len(bytes.TrimSpace(req.Sample)) > 0 && !bytes.Equal(bytes.TrimSpace(req.Sample), []byte("null")) {
		events, err := parseBody(req.Sample)
		if err != nil {
			return errx.InvalidField("sample", "sample must be a JSON object or array of objects")
		}
		m := prog.Render(Input{DockName: d.Name, Body: events[0].value, Raw: events[0].raw})
		preview = &m
	}
	if err := h.g.store.SaveTemplate(c.Context(), d.Organization, d.ID, req.Template); err != nil {
		return err
	}
	hook, err := h.g.store.Hook(c.Context(), d.ID)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	h.fill(&out, hook)
	out.Preview = preview
	return httpx.JSON(c, http.StatusOK, out)
}

// rotate replaces the secret and shows the new one once.
func (h *Handler) rotate(c fiber.Ctx) error {
	d, err := h.webhookDock(c)
	if err != nil {
		return err
	}
	secret, err := h.g.store.RotateSecret(c.Context(), d.Organization, d.ID)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	if hook, err := h.g.store.Hook(c.Context(), d.ID); err == nil {
		h.fill(&out, hook)
	}
	out.Secret = secret
	return httpx.JSON(c, http.StatusOK, out)
}

// validSignature checks "X-Seaport-Signature: sha256=<hex HMAC-SHA256 of
// the body>" with the secret, in constant time.
func validSignature(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// validToken compares a presented secret with the real one in constant
// time (hashed first, so the length leaks nothing either).
func validToken(secret, got string) bool {
	got = strings.TrimSpace(got)
	if got == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// validBearer checks "Authorization: Bearer <secret>".
func validBearer(secret, header string) bool {
	header = strings.TrimSpace(header)
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return false
	}
	return validToken(secret, header[len("Bearer "):])
}

// proven: the request carries the secret as a bearer, X-Seaport-Token, a
// signature of the body or (last resort: URLs end in logs) ?token=.
func proven(secret string, c fiber.Ctx, body []byte) bool {
	// Every check runs, so what fails first isn't observable.
	ok := validBearer(secret, c.Get("Authorization"))
	ok = validToken(secret, c.Get("X-Seaport-Token")) || ok
	ok = validSignature(secret, body, c.Get("X-Seaport-Signature")) || ok
	return validToken(secret, c.Query("token")) || ok
}

// event is one element of a body.
type event struct {
	value any
	raw   []byte
}

// parseBody reads a JSON object, or an array of objects, into events.
func parseBody(body []byte) ([]event, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errx.UnsupportedMediaType("the body must be JSON")
	}
	var raws []json.RawMessage
	switch body[0] {
	case '{':
		if !json.Valid(body) {
			return nil, errx.UnsupportedMediaType("the body must be JSON")
		}
		raws = []json.RawMessage{body}
	case '[':
		if err := json.Unmarshal(body, &raws); err != nil {
			return nil, errx.UnsupportedMediaType("the body must be JSON")
		}
	default:
		return nil, errx.UnsupportedMediaType("the body must be a JSON object or an array of objects")
	}
	if len(raws) == 0 {
		return nil, errx.Validation("the body has no events")
	}
	if len(raws) > MaxEvents {
		return nil, errx.Validation("the body has too many events (at most " + strconv.Itoa(MaxEvents) + ")")
	}
	out := make([]event, 0, len(raws))
	for _, raw := range raws {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, errx.UnsupportedMediaType("the body must be JSON")
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, errx.UnsupportedMediaType("the body must be a JSON object or an array of objects")
		}
		out = append(out, event{value: v, raw: bytes.TrimSpace(raw)})
	}
	return out, nil
}

// safeHeaders is the subset of the request's headers a template sees:
// content type, user agent and the provider's own X- headers, never what
// carries a credential.
func safeHeaders(c fiber.Ctx) map[string]string {
	out := map[string]string{}
	for name, values := range c.GetReqHeaders() {
		name = strings.ToLower(name)
		if len(values) == 0 {
			continue
		}
		switch {
		case name == "content-type" || name == "user-agent":
		case strings.HasPrefix(name, "x-") && !secretHeader(name):
		default:
			continue
		}
		out[name] = values[0]
	}
	return out
}

func secretHeader(name string) bool {
	for _, w := range []string{"auth", "token", "secret", "signature", "key", "password", "cookie"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// hook takes a provider's call for one dock. An unknown dock and a wrong
// secret answer the same 404: nothing says which docks exist.
func (h *Handler) hook(c fiber.Ctx) error {
	id, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return errx.NotFound("unknown webhook")
	}
	hook, err := h.g.store.Hook(c.Context(), id)
	if errx.IsType(err, errx.TypeNotFound) {
		// Same work as a known dock, so timing doesn't tell them apart.
		proven("unknown", c, c.Body())
		return errx.NotFound("unknown webhook")
	}
	if err != nil {
		return err
	}
	// The server's body tier already refused anything over MaxWebhook.
	body := c.Body()
	if wait := h.limit.Take(id.String()); wait > 0 {
		s := int(math.Ceil(wait.Seconds()))
		return errx.TooManyRequests("too many calls to this webhook; retry in "+strconv.Itoa(s)+"s").
			WithDetail("retry_after_seconds", s)
	}
	if !proven(hook.secret, c, body) {
		return errx.NotFound("unknown webhook")
	}
	receiver := h.g.bound()
	if receiver == nil {
		return errx.External("this port is starting; try again")
	}
	events, err := parseBody(body)
	if err != nil {
		return err
	}
	prog, err := hook.Template.Compile()
	if err != nil {
		// Saved templates are checked; this is one an older version allowed.
		slog.Warn("stored webhook template does not compile, defaults used", "dock", id, "err", err)
		prog, _ = Template{}.Compile()
	}
	d, err := h.docks.Find(c.Context(), hook.Organization, id)
	if err != nil {
		return err
	}
	headers := safeHeaders(c)
	accepted, ignored := 0, 0
	for _, e := range events {
		m := prog.Render(Input{DockName: d.Name, Body: e.value, Raw: e.raw, Headers: headers})
		if len(m.Errors) > 0 {
			h.g.templateFailed(id, m.Errors)
		}
		// Each event has its own budget. A failure part-way leaves the
		// earlier events stored: the caller retries the whole body and the
		// stored ones are ignored (same external id).
		ctx, cancel := context.WithTimeout(c.Context(), receiveWithin)
		_, created, err := receiver.Receive(ctx, hook.Organization, inbound(id, m))
		cancel()
		if err != nil {
			slog.Error("receive webhook message", "dock", id, "chat", m.ChatID, "err", err)
			return err
		}
		if created {
			accepted++
		} else {
			ignored++
		}
	}
	return httpx.JSON(c, http.StatusAccepted, map[string]int{"accepted": accepted, "ignored": ignored})
}

// inbound is a rendered event as the message the ship domain takes.
func inbound(id identity.DockID, m Message) ship.Inbound {
	return ship.Inbound{
		Dock: id, Channel: dock.ChannelWebhook,
		ChatID: m.ChatID, ChatKind: conversation.KindGroup, ChatTitle: m.ChatTitle,
		ExternalID: m.ExternalID, SenderID: m.SenderID, SenderName: m.SenderName,
		Text: m.Text, SentAt: time.Now(),
		ChatHandles:   append([]string{m.ChatID}, m.Tags...),
		SenderHandles: []string{m.SenderID, "webhook:" + short(id)},
		Addressed:     true,
	}
}

// short is the first group of a dock's id, enough to name it in a rule.
func short(id identity.DockID) string {
	s := id.String()
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	return s
}
