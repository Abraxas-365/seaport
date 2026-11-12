// Package agenthttp is the admin API of agents and their dock assignments.
package agenthttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	commands agent.Commands
	queries  agent.Queries
}

func New(c agent.Commands, q agent.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/agents", httpx.RequireFunc(permission.AgentsRead, h.list))
	r.Post("/api/v1/agents", httpx.RequireFunc(permission.AgentsWrite, h.create))
	r.Get("/api/v1/agents/:id", httpx.RequireFunc(permission.AgentsRead, h.find))
	r.Patch("/api/v1/agents/:id", httpx.RequireFunc(permission.AgentsWrite, h.update))
	r.Delete("/api/v1/agents/:id", httpx.RequireFunc(permission.AgentsWrite, h.delete))
	r.Get("/api/v1/docks/:dock/agents", httpx.RequireFunc(permission.AgentsRead, h.team))
	r.Put("/api/v1/docks/:dock/agents/:id", httpx.RequireFunc(permission.AgentsWrite, h.assign))
	r.Delete("/api/v1/docks/:dock/agents/:id", httpx.RequireFunc(permission.AgentsWrite, h.unassign))
	// Profiles and secrets: writing either can direct the agent's secrets
	// (an MCP header naming one), so both need agents:write.
	r.Get("/api/v1/agents/:id/profile", httpx.RequireFunc(permission.AgentsRead, h.profile))
	r.Put("/api/v1/agents/:id/profile", httpx.RequireFunc(permission.AgentsWrite, h.saveProfile))
	r.Get("/api/v1/agents/:id/profile/versions", httpx.RequireFunc(permission.AgentsRead, h.versions))
	r.Get("/api/v1/agents/:id/profile/versions/:version", httpx.RequireFunc(permission.AgentsRead, h.profile))
	r.Get("/api/v1/agents/:id/secrets", httpx.RequireFunc(permission.AgentsRead, h.secrets))
	r.Put("/api/v1/agents/:id/secrets/:name", httpx.RequireFunc(permission.AgentsWrite, h.setSecret))
	r.Delete("/api/v1/agents/:id/secrets/:name", httpx.RequireFunc(permission.AgentsWrite, h.deleteSecret))
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
	out, err := h.queries.List(c.Context(), org, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type createRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) create(c fiber.Ctx) error {
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	by := p.Name
	if by == "" {
		by = p.Subject
	}
	a, err := h.commands.Create(c.Context(), p.Organization, agent.Create{Name: req.Name, Description: req.Description, CreatedBy: by})
	if err != nil {
		return err
	}
	return httpx.Created(c, a.ID.String(), a)
}

func (h *Handler) find(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	a, err := h.queries.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}

type updateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) update(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	a, err := h.commands.Update(c.Context(), org, id, agent.Update{Name: req.Name, Description: req.Description})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}

func (h *Handler) delete(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.commands.Delete(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (h *Handler) team(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return err
	}
	team, err := h.queries.Team(c.Context(), org, dock)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, map[string]any{"agents": team})
}

type assignRequest struct {
	Default bool `json:"default"`
}

func (h *Handler) assign(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req assignRequest
	if c.Request().Header.ContentLength() != 0 {
		if err := httpx.DecodeJSON(c, &req); err != nil {
			return err
		}
	}
	a, err := h.commands.Assign(c.Context(), org, agent.Assign{Dock: dock, Agent: id, Default: req.Default})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}

func (h *Handler) unassign(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	a, err := h.commands.Unassign(c.Context(), org, dock, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}
