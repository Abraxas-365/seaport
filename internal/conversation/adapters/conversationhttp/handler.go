// Package conversationhttp is the admin API of conversations (customs).
package conversationhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands       conversation.Commands
	queries        conversation.Queries
	customs        conversation.CustomsCommands
	customsQueries conversation.CustomsQueries
}

func New(c conversation.Commands, q conversation.Queries, cc conversation.CustomsCommands, cq conversation.CustomsQueries) *Handler {
	return &Handler{commands: c, queries: q, customs: cc, customsQueries: cq}
}

// Register mounts the routes on mux (already behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/conversations", stdhttp.RequireFunc(permission.ConversationsRead, h.list))
	mux.Handle("GET /api/v1/conversations/{id}", stdhttp.RequireFunc(permission.ConversationsRead, h.find))
	mux.Handle("PATCH /api/v1/conversations/{id}", stdhttp.RequireFunc(permission.ConversationsWrite, h.update))
	mux.Handle("GET /api/v1/docks/{id}/customs", stdhttp.RequireFunc(permission.CustomsRead, h.getCustoms))
	mux.Handle("PATCH /api/v1/docks/{id}/customs/policy", stdhttp.RequireFunc(permission.CustomsWrite, h.setPolicy))
	mux.Handle("POST /api/v1/docks/{id}/customs/rules", stdhttp.RequireFunc(permission.CustomsWrite, h.addRule))
	mux.Handle("DELETE /api/v1/customs/rules/{id}", stdhttp.RequireFunc(permission.CustomsWrite, h.removeRule))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
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
		id, err := identity.ParseDockID(raw)
		if err != nil {
			return err
		}
		f.Dock = &id
	}
	out, err := h.queries.List(r.Context(), org, f, page)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	c, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, c)
}

type updateRequest struct {
	Access    *conversation.Access    `json:"access"`
	GroupMode *conversation.GroupMode `json:"group_mode"`
	Title     *string                 `json:"title"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	c, err := h.commands.Update(r.Context(), org, id, conversation.Update{Access: req.Access, GroupMode: req.GroupMode, Title: req.Title})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, c)
}

func (h *Handler) getCustoms(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	c, err := h.customsQueries.Customs(r.Context(), org, dock)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, c)
}

type policyRequest struct {
	Default   *conversation.DefaultAccess `json:"default"`
	GroupMode *conversation.GroupMode     `json:"group_mode"`
}

func (h *Handler) setPolicy(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req policyRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	c, err := h.customs.SetPolicy(r.Context(), org, dock, conversation.SetPolicy{Default: req.Default, GroupMode: req.GroupMode})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, c)
}

type ruleRequest struct {
	Effect  conversation.Effect `json:"effect"`
	Pattern string              `json:"pattern"`
}

func (h *Handler) addRule(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req ruleRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	rule, err := h.customs.AddRule(r.Context(), org, conversation.AddRule{Dock: dock, Effect: req.Effect, Pattern: req.Pattern})
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/customs/rules/"+rule.ID.String())
	return stdhttp.JSON(w, http.StatusCreated, rule)
}

func (h *Handler) removeRule(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseRuleID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.customs.RemoveRule(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
