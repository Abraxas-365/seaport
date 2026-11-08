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
	mux.Handle("GET /api/v1/inbox", httpx.RequireFunc(permission.ConversationsRead, h.inbox))
	mux.Handle("GET /api/v1/conversations/{id}/ships", httpx.RequireFunc(permission.ShipsRead, h.history))
	mux.Handle("POST /api/v1/conversations/{id}/ships", httpx.RequireFunc(permission.ShipsWrite, h.send))
	mux.Handle("POST /api/v1/docks/{id}/ships", httpx.RequireFunc(permission.ShipsWrite, h.receive))
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.History(r.Context(), org, id, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

// inbox lists conversations with their last ship and counters
// (?status=allowed|pending|denied&dock_id=).
func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
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
	if p, err := httpx.Caller(r); err != nil || !p.Can(permission.ShipsRead) {
		for i := range out.Items {
			out.Items[i].Last = nil
		}
	}
	return httpx.JSON(w, http.StatusOK, out)
}

type sendRequest struct {
	Text string `json:"text"`
}

// send lets the owner write to a conversation as the agent would.
func (h *Handler) send(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req sendRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	s, err := h.commands.Send(r.Context(), org, ship.Outbound{Conversation: id, Text: req.Text})
	if err != nil {
		return err
	}
	return httpx.Created(w, r, s.ID.String(), s)
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
}

type receiveResponse struct {
	ship.Ship
	Duplicate bool `json:"duplicate"`
}

// receive injects an inbound message into an "api" dock.
func (h *Handler) receive(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req receiveRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
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
	s, created, err := h.commands.Receive(r.Context(), org, ship.Inbound{
		Dock: d.ID, Channel: d.Channel, ChatID: req.ChatID, ChatKind: req.ChatKind, ChatTitle: req.ChatTitle,
		ExternalID: req.MessageID, SenderID: req.SenderID, SenderName: req.SenderName, Text: req.Text, SentAt: sentAt,
		ChatHandles: req.ChatHandles, SenderHandles: req.SenderHandles, Addressed: req.Addressed,
	})
	if err != nil {
		return err
	}
	if !created {
		return httpx.JSON(w, http.StatusOK, receiveResponse{Ship: s, Duplicate: true})
	}
	return httpx.JSON(w, http.StatusCreated, receiveResponse{Ship: s})
}
