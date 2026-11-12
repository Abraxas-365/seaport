// Package shiphttp is the admin API of ships: history, replies, and inbound
// messages for "api" docks.
package shiphttp

import (
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/ship"
)

type Handler struct {
	commands ship.Commands
	queries  ship.Queries
	docks    dock.Queries
}

func New(c ship.Commands, q ship.Queries, docks dock.Queries) *Handler {
	return &Handler{commands: c, queries: q, docks: docks}
}

// Register mounts the routes on mux (already behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/inbox", stdhttp.RequireFunc(permission.ConversationsRead, h.inbox))
	mux.Handle("GET /api/v1/conversations/{id}/ships", stdhttp.RequireFunc(permission.ShipsRead, h.history))
	mux.Handle("GET /api/v1/conversations/{id}/turns", stdhttp.RequireFunc(permission.ShipsRead, h.turns))
	mux.Handle("POST /api/v1/conversations/{id}/ships", stdhttp.RequireFunc(permission.ShipsWrite, h.send))
	mux.Handle("POST /api/v1/docks/{id}/ships", stdhttp.RequireFunc(permission.ShipsWrite, h.receive))
	mux.Handle("GET /api/v1/attachments/{id}", stdhttp.RequireFunc(permission.ShipsRead, h.attachment))
}

// attachment serves a ship's attachment (an image shown in its bubble).
func (h *Handler) attachment(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAttachmentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	f, err := h.queries.OpenAttachment(r.Context(), org, ship.OpenAttachment{ID: id})
	if err != nil {
		return err
	}
	if f.Kind == ship.AttachmentDocument {
		return stdhttp.Download(w, f.MIME, f.Name, f.Data)
	}
	return stdhttp.File(w, f.MIME, f.Data)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	page, err := stdhttp.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.History(r.Context(), org, id, page)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

// turns lists what the agent did on each delivery of a conversation
// (ship.Turn), newest first.
func (h *Handler) turns(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	page, err := stdhttp.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Turns(r.Context(), org, id, page)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

// inbox lists conversations with their last ship and counters
// (?status=allowed|pending|denied&dock_id=).
func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	page, err := stdhttp.Pagination(r)
	if err != nil {
		return err
	}
	var f conversation.Filter
	if raw := r.URL.Query().Get("status"); raw != "" {
		st := conversation.Status(raw)
		f.Status = &st
	}
	if raw := r.URL.Query().Get("dock_id"); raw != "" {
		d, err := identity.ParseDockID(raw)
		if err != nil {
			return err
		}
		f.Dock = &d
	}
	out, err := h.queries.Inbox(r.Context(), org, f, page)
	if err != nil {
		return err
	}
	// The preview is message content: only for callers who may read ships.
	if p, err := stdhttp.Caller(r); err != nil || !p.Can(permission.ShipsRead) {
		for i := range out.Items {
			out.Items[i].Last = nil
		}
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

type sendRequest struct {
	Text string `json:"text"`
}

// send lets the owner write to a conversation as the agent would.
func (h *Handler) send(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req sendRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	s, err := h.commands.Send(r.Context(), org, ship.Outbound{Conversation: id, Text: req.Text})
	if err != nil {
		return err
	}
	return stdhttp.Created(w, r, s.ID.String(), s)
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
func (h *Handler) receive(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req receiveRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	d, err := h.docks.Find(r.Context(), org, id)
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
	s, created, err := h.commands.Receive(r.Context(), org, in)
	if err != nil {
		return err
	}
	if !created {
		return stdhttp.JSON(w, http.StatusOK, receiveResponse{Ship: s, Duplicate: true})
	}
	return stdhttp.JSON(w, http.StatusCreated, receiveResponse{Ship: s})
}
