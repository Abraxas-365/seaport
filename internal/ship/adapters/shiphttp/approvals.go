package shiphttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/gofiber/fiber/v3"
)

// approvals lists the organization's approvals (?status=).
func (h *Handler) approvals(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Approvals(c.Context(), org, ship.ApprovalFilter{Status: ship.ApprovalStatus(c.Query("status"))}, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) conversationApprovals(c fiber.Ctx) error {
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
	out, err := h.queries.ConversationApprovals(c.Context(), org, id, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) approval(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseApprovalID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	a, err := h.queries.FindApproval(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}

type answerRequest struct {
	Option string `json:"option"`
}

// answer records a person's choice from the web (or the CLI) as the
// caller; it wakes the agent unless an ask is blocked on it.
func (h *Handler) answer(c fiber.Ctx) error {
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseApprovalID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req answerRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	by := p.Name
	if by == "" {
		by = p.Subject
	}
	a, err := h.commands.AnswerApproval(c.Context(), p.Organization, id, ship.Answer{Option: req.Option, By: by})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}

// cancel withdraws a pending approval; the agent is told.
func (h *Handler) cancel(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseApprovalID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	a, err := h.commands.CancelApproval(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}
