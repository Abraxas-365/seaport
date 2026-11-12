// Package shiphttp is the admin API of ships: history, replies, and inbound
// messages for "api" docks.
package shiphttp

import (
	"net/http"
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

type Handler struct {
	commands ship.Commands
	queries  ship.Queries
	docks    dock.Queries
}

func New(c ship.Commands, q ship.Queries, docks dock.Queries) *Handler {
	return &Handler{commands: c, queries: q, docks: docks}
}

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/inbox", httpx.RequireFunc(permission.ConversationsRead, h.inbox))
	r.Get("/api/v1/conversations/:id/ships", httpx.RequireFunc(permission.ShipsRead, h.history))
	r.Get("/api/v1/conversations/:id/turns", httpx.RequireFunc(permission.ShipsRead, h.turns))
	r.Post("/api/v1/conversations/:id/ships", httpx.RequireFunc(permission.ShipsWrite, h.send))
	r.Post("/api/v1/docks/:id/ships", httpx.RequireFunc(permission.ShipsWrite, h.receive))
	r.Get("/api/v1/attachments/:id", httpx.RequireFunc(permission.ShipsRead, h.attachment))
}

// attachment serves a ship's attachment (an image shown in its bubble).
func (h *Handler) attachment(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAttachmentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	f, err := h.queries.OpenAttachment(c.Context(), org, ship.OpenAttachment{ID: id})
	if err != nil {
		return err
	}
	if f.Kind == ship.AttachmentDocument {
		return httpx.Download(c, f.MIME, f.Name, f.Data)
	}
	return httpx.File(c, f.MIME, f.Data)
}

func (h *Handler) history(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.History(c.Context(), org, id, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

// turns lists what the agent did on each delivery of a conversation
// (ship.Turn), newest first.
func (h *Handler) turns(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Turns(c.Context(), org, id, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

// inbox lists conversations with their last ship and counters
// (?status=allowed|pending|denied&dock_id=).
func (h *Handler) inbox(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	var f conversation.Filter
	if raw := c.Query("status"); raw != "" {
		st := conversation.Status(raw)
		f.Status = &st
	}
	if raw := c.Query("dock_id"); raw != "" {
		d, err := identity.ParseDockID(raw)
		if err != nil {
			return err
		}
		f.Dock = &d
	}
	out, err := h.queries.Inbox(c.Context(), org, f, page)
	if err != nil {
		return err
	}
	// The preview is message content: only for callers who may read ships.
	if p, err := httpx.Caller(c); err != nil || !p.Can(permission.ShipsRead) {
		for i := range out.Items {
			out.Items[i].Last = nil
		}
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type sendRequest struct {
	Text string `json:"text"`
}

// send lets the owner write to a conversation as the agent would.
func (h *Handler) send(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req sendRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	s, err := h.commands.Send(c.Context(), org, ship.Outbound{Conversation: id, Text: req.Text})
	if err != nil {
		return err
	}
	return httpx.Created(c, s.ID.String(), s)
}

type receiveRequest struct {
	ChatID     string            `json:"chat_id"`
	ChatKind   conversation.Kind `json:"chat_kind"`
	ChatTitle  string            `json:"chat_title"`
	MessageID  string            `json:"message_id"`
	SenderID   string            `json:"sender_id"`
	SenderName string            `json:"sender_name"`
	Text       string            `json:"text"`
	SentAt     *time.Time        `json:"sent_at"`
	// Customs identities (optional): chat_id / sender_id are used when empty.
	ChatHandles   []string `json:"chat_handles"`
	SenderHandles []string `json:"sender_handles"`
	// Addressed: the message mentions the account (groups in mentions mode).
	Addressed bool `json:"addressed"`
	// Images come base64-encoded (the whole body stays within 1 MiB).
	Images []receiveImage `json:"images"`
}

type receiveImage struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type receiveResponse struct {
	ship.Ship
	Duplicate bool `json:"duplicate"`
}

// receive injects an inbound message into an "api" dock.
func (h *Handler) receive(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req receiveRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	d, err := h.docks.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	if d.Channel != dock.ChannelAPI {
		return errx.Business("messages can only be injected into api docks")
	}
	if req.ChatKind == "" {
		req.ChatKind = conversation.KindDirect
	}
	sentAt := time.Now()
	if req.SentAt != nil {
		sentAt = *req.SentAt
	}
	in := ship.Inbound{
		Dock: d.ID, Channel: d.Channel, ChatID: req.ChatID, ChatKind: req.ChatKind, ChatTitle: req.ChatTitle,
		ExternalID: req.MessageID, SenderID: req.SenderID, SenderName: req.SenderName, Text: req.Text, SentAt: sentAt,
		ChatHandles: req.ChatHandles, SenderHandles: req.SenderHandles, Addressed: req.Addressed,
	}
	for _, img := range req.Images {
		in.Media = append(in.Media, ship.Media{Kind: ship.AttachmentImage, Name: img.Name, Data: img.Data})
	}
	s, created, err := h.commands.Receive(c.Context(), org, in)
	if err != nil {
		return err
	}
	if !created {
		return httpx.JSON(c, http.StatusOK, receiveResponse{Ship: s, Duplicate: true})
	}
	return httpx.JSON(c, http.StatusCreated, receiveResponse{Ship: s})
}
