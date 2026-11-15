package dockdatadog

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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

// WebhookPath is where Datadog calls this port; the dock id follows.
const WebhookPath = "/datadog/webhook/"

// PerMinute is how many calls a dock takes per minute (a burst of that
// many, then one a second); more are 429 with Retry-After.
const PerMinute = 60

// receiveWithin bounds the work of one alert (Receive is quick).
const receiveWithin = 10 * time.Second

// mirrorWithin bounds the opening of the mirrored conversation (a post to
// another channel).
const mirrorWithin = 30 * time.Second

// SenderID is the sender of every alert (a rule `system:datadog` matches
// them all).
const SenderID = "system:datadog"

// Handler serves the Datadog routes: the admin API of a Datadog dock and
// the public webhook.
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
	r.Get("/api/v1/docks/:id/datadog", httpx.RequireFunc(permission.DocksRead, h.info))
	r.Put("/api/v1/docks/:id/datadog", httpx.RequireFunc(permission.DocksWrite, h.save))
	r.Post("/api/v1/docks/:id/datadog/secret", httpx.RequireFunc(permission.DocksWrite, h.rotate))
}

// RegisterPublic mounts the endpoint Datadog calls without a Seaport
// credential: it checks the dock's secret.
func (h *Handler) RegisterPublic(r httpx.Router) {
	r.Post("/datadog/webhook/:dock", h.hook)
}

// datadogDock finds the caller's dock and checks it is a Datadog one.
func (h *Handler) datadogDock(c fiber.Ctx) (dock.Dock, error) {
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
	if d.Channel != dock.ChannelDatadog {
		return dock.Dock{}, errx.Business("not a datadog dock")
	}
	return d, nil
}

// Info is the Datadog side of a dock.
type Info struct {
	// URL is where Datadog calls this port; Reachable is false when it
	// can't be on the internet (loopback, plain http).
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	// Payload is the body to paste in Datadog's webhook integration.
	Payload string `json:"payload"`
	// Mirror is where new alert threads are opened (null: nowhere).
	Mirror *Mirror `json:"mirror"`
	// HasSecret: the endpoint exists (a linked dock). The secret itself is
	// shown once, by the answer that made it (rotating), never read back.
	HasSecret bool       `json:"has_secret"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Secret    string     `json:"secret,omitempty"`
}

// PayloadTemplate is the custom payload of Datadog's webhook integration
// that the parser reads (Datadog fills the $VARIABLES).
const PayloadTemplate = `{
  "id": "$ID",
  "alert_id": "$ALERT_ID",
  "transition": "$ALERT_TRANSITION",
  "title": "$ALERT_TITLE",
  "status": "$ALERT_STATUS",
  "scope": "$ALERT_SCOPE",
  "tags": "$TAGS",
  "message": "$EVENT_MSG",
  "link": "$LINK",
  "priority": "$PRIORITY",
  "date": "$DATE",
  "org": "$ORG_NAME"
}`

func (h *Handler) base(id identity.DockID) Info {
	return Info{URL: h.g.cfg.PublicURL + WebhookPath + id.String(), Reachable: reachable(h.g.cfg.PublicURL), Payload: PayloadTemplate}
}

func (h *Handler) fill(out *Info, e Endpoint) {
	out.HasSecret, out.Mirror = true, e.Mirror
	created := e.CreatedAt
	out.CreatedAt = &created
}

// reachable: Datadog can call this URL (https, not loopback).
func reachable(public string) bool {
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1"
}

func (h *Handler) info(c fiber.Ctx) error {
	d, err := h.datadogDock(c)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	e, err := h.g.store.Endpoint(c.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(c, http.StatusOK, out)
	case err != nil:
		return err
	}
	h.fill(&out, e)
	return httpx.JSON(c, http.StatusOK, out)
}

type saveRequest struct {
	// Mirror names the destination new alert threads are opened at; null
	// or absent clears it.
	Mirror *Mirror `json:"mirror"`
}

func (r saveRequest) Validate() error {
	if r.Mirror == nil {
		return nil
	}
	if r.Mirror.Dock.IsZero() {
		return errx.InvalidField("mirror.dock_id", "mirror.dock_id is required")
	}
	if err := dock.ValidDestinationName(r.Mirror.Destination); err != nil {
		return errx.InvalidField("mirror.destination", "mirror.destination must be a destination name of that dock")
	}
	return nil
}

// save sets the mirror: a destination of a dock of this organization
// that replies (a Datadog dock can't be its own mirror).
func (h *Handler) save(c fiber.Ctx) error {
	d, err := h.datadogDock(c)
	if err != nil {
		return err
	}
	var req saveRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if req.Mirror != nil {
		target, err := h.docks.Find(c.Context(), d.Organization, req.Mirror.Dock)
		if err != nil {
			return errx.Wrap(err, "find mirror dock", errx.TypeInternal)
		}
		if !target.CanReply() {
			return errx.Business("the mirror must be a dock that replies")
		}
		if _, err := h.docks.Destination(c.Context(), d.Organization, req.Mirror.Dock, req.Mirror.Destination); err != nil {
			return errx.Wrap(err, "find mirror destination", errx.TypeInternal)
		}
	}
	if err := h.g.store.SaveMirror(c.Context(), d.Organization, d.ID, req.Mirror); err != nil {
		return err
	}
	e, err := h.g.store.Endpoint(c.Context(), d.ID)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	h.fill(&out, e)
	return httpx.JSON(c, http.StatusOK, out)
}

// rotate replaces the secret and shows the new one once.
func (h *Handler) rotate(c fiber.Ctx) error {
	d, err := h.datadogDock(c)
	if err != nil {
		return err
	}
	secret, err := h.g.store.RotateSecret(c.Context(), d.Organization, d.ID)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	if e, err := h.g.store.Endpoint(c.Context(), d.ID); err == nil {
		h.fill(&out, e)
	}
	out.Secret = secret
	return httpx.JSON(c, http.StatusOK, out)
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

// proven: the request carries the secret as X-Seaport-Token (Datadog's
// webhook integration takes custom headers) or a bearer.
func proven(secret string, c fiber.Ctx) bool {
	// Both checks run, so what fails first isn't observable.
	ok := validBearer(secret, c.Get("Authorization"))
	return validToken(secret, c.Get("X-Seaport-Token")) || ok
}

// hook takes Datadog's call for one dock. An unknown dock and a wrong
// secret answer the same 404: nothing says which docks exist.
func (h *Handler) hook(c fiber.Ctx) error {
	id, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return errx.NotFound("unknown webhook")
	}
	e, err := h.g.store.Endpoint(c.Context(), id)
	if errx.IsType(err, errx.TypeNotFound) {
		// Same work as a known dock, so timing doesn't tell them apart.
		proven("unknown", c)
		return errx.NotFound("unknown webhook")
	}
	if err != nil {
		return err
	}
	if wait := h.limit.Take(id.String()); wait > 0 {
		s := int(math.Ceil(wait.Seconds()))
		return errx.TooManyRequests("too many calls to this webhook; retry in "+strconv.Itoa(s)+"s").
			WithDetail("retry_after_seconds", s)
	}
	if !proven(e.secret, c) {
		return errx.NotFound("unknown webhook")
	}
	receiver, opener, convs := h.g.bound()
	if receiver == nil {
		return errx.External("this port is starting; try again")
	}
	p, err := Parse(c.Body())
	if err != nil {
		return err
	}
	m := Render(p, time.Now())
	in := inbound(id, m)
	mirrored := false
	if e.Mirror != nil && opener != nil && convs != nil {
		mirrored = h.mirror(c.Context(), e, in, opener, convs)
	}
	ctx, cancel := context.WithTimeout(c.Context(), receiveWithin)
	defer cancel()
	s, created, err := receiver.Receive(ctx, e.Organization, in)
	if err != nil {
		slog.Error("receive datadog alert", "dock", id, "chat", m.ChatID, "err", err)
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, map[string]any{"accepted": created, "mirrored": mirrored, "conversation_id": s.Conversation})
}

// mirror opens the alert's conversation at the mirror when the alert
// starts a new thread: its conversation (made here, before the message,
// by Arrive) is a thread of one — the first alert of a monitor and
// scope, or one after the thread was unlinked. It happens before the
// message is stored, so the crew wakes to the whole thread and its
// session is keyed by the thread it keeps. A held alert (customs) opens
// nothing: the owner didn't let it in. Seaport opens as itself (no
// agent: the owner named the place). Failures are logged; the alert
// still arrives.
func (h *Handler) mirror(ctx context.Context, e Endpoint, in ship.Inbound, opener Opener, convs Conversations) bool {
	ctx, cancel := context.WithTimeout(ctx, mirrorWithin)
	defer cancel()
	arrival, err := convs.Arrive(ctx, e.Organization, in.Arrival())
	if err != nil {
		slog.Error("mirror datadog alert: arrive", "dock", e.Dock, "chat", in.ChatID, "err", err)
		return false
	}
	if arrival.Hold != conversation.HoldNone {
		return false
	}
	doors, err := convs.Thread(ctx, e.Organization, arrival.Conversation.ID)
	if err != nil {
		slog.Error("mirror datadog alert: read thread", "dock", e.Dock, "conversation", arrival.Conversation.ID, "err", err)
		return false
	}
	if len(doors) > 1 {
		return false
	}
	if _, err := opener.Open(ctx, e.Organization, ship.Open{
		Dock: e.Mirror.Dock, Destination: e.Mirror.Destination,
		Text:     in.Text,
		ThreadOf: arrival.Conversation.ID,
	}); err != nil {
		slog.Error("mirror datadog alert", "dock", e.Dock, "mirror", e.Mirror.Dock, "destination", e.Mirror.Destination,
			"conversation", arrival.Conversation.ID, "err", err)
		return false
	}
	return true
}

// inbound is a rendered alert as the message the ship domain takes.
func inbound(id identity.DockID, m Message) ship.Inbound {
	return ship.Inbound{
		Dock: id, Channel: dock.ChannelDatadog,
		ChatID: m.ChatID, ChatKind: conversation.KindGroup, ChatTitle: m.ChatTitle,
		ExternalID: m.ExternalID, SenderID: SenderID, SenderName: "Datadog",
		Text: m.Text, SentAt: time.Now(),
		ChatHandles:   m.Handles,
		SenderHandles: []string{SenderID, "datadog:" + shortID(id)},
		Addressed:     true,
	}
}
