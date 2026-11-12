// Package conversationhttp is the admin API of conversations (customs).
package conversationhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
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

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/conversations", httpx.RequireFunc(permission.ConversationsRead, h.list))
	r.Get("/api/v1/conversations/:id", httpx.RequireFunc(permission.ConversationsRead, h.find))
	r.Patch("/api/v1/conversations/:id", httpx.RequireFunc(permission.ConversationsWrite, h.update))
	r.Get("/api/v1/docks/:id/customs", httpx.RequireFunc(permission.CustomsRead, h.getCustoms))
	r.Patch("/api/v1/docks/:id/customs/policy", httpx.RequireFunc(permission.CustomsWrite, h.setPolicy))
	r.Post("/api/v1/docks/:id/customs/rules", httpx.RequireFunc(permission.CustomsWrite, h.addRule))
	r.Delete("/api/v1/customs/rules/:id", httpx.RequireFunc(permission.CustomsWrite, h.removeRule))
}

func (h *Handler) list(c fiber.Ctx) error {
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
		id, err := identity.ParseDockID(raw)
		if err != nil {
			return err
		}
		f.Dock = &id
	}
	out, err := h.queries.List(c.Context(), org, f, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) find(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type updateRequest struct {
	Access    *conversation.Access    `json:"access"`
	GroupMode *conversation.GroupMode `json:"group_mode"`
	Title     *string                 `json:"title"`
}

func (h *Handler) update(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.Update(c.Context(), org, id, conversation.Update{Access: req.Access, GroupMode: req.GroupMode, Title: req.Title})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) getCustoms(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.customsQueries.Customs(c.Context(), org, dock)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type policyRequest struct {
	Default   *conversation.DefaultAccess `json:"default"`
	GroupMode *conversation.GroupMode     `json:"group_mode"`
}

func (h *Handler) setPolicy(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req policyRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.customs.SetPolicy(c.Context(), org, dock, conversation.SetPolicy{Default: req.Default, GroupMode: req.GroupMode})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type ruleRequest struct {
	Effect  conversation.Effect `json:"effect"`
	Pattern string              `json:"pattern"`
}

func (h *Handler) addRule(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req ruleRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	rule, err := h.customs.AddRule(c.Context(), org, conversation.AddRule{Dock: dock, Effect: req.Effect, Pattern: req.Pattern})
	if err != nil {
		return err
	}
	c.Set("Location", "/api/v1/customs/rules/"+rule.ID.String())
	return httpx.JSON(c, http.StatusCreated, rule)
}

func (h *Handler) removeRule(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseRuleID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.customs.RemoveRule(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}
