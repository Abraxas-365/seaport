// Package agenthttp is the admin API of agents and their dock assignments.
package agenthttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands agent.Commands
	queries  agent.Queries
}

func New(c agent.Commands, q agent.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on mux (already behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/agents", httpx.RequireFunc(permission.AgentsRead, h.list))
	mux.Handle("POST /api/v1/agents", httpx.RequireFunc(permission.AgentsWrite, h.create))
	mux.Handle("GET /api/v1/agents/{id}", httpx.RequireFunc(permission.AgentsRead, h.find))
	mux.Handle("PATCH /api/v1/agents/{id}", httpx.RequireFunc(permission.AgentsWrite, h.update))
	mux.Handle("DELETE /api/v1/agents/{id}", httpx.RequireFunc(permission.AgentsWrite, h.delete))
	mux.Handle("GET /api/v1/docks/{dock}/agents", httpx.RequireFunc(permission.AgentsRead, h.team))
	mux.Handle("PUT /api/v1/docks/{dock}/agents/{id}", httpx.RequireFunc(permission.AgentsWrite, h.assign))
	mux.Handle("DELETE /api/v1/docks/{dock}/agents/{id}", httpx.RequireFunc(permission.AgentsWrite, h.unassign))
	// Profiles and secrets: writing either can direct the agent's secrets
	// (an MCP header naming one), so both need agents:write.
	mux.Handle("GET /api/v1/agents/{id}/profile", httpx.RequireFunc(permission.AgentsRead, h.profile))
	mux.Handle("PUT /api/v1/agents/{id}/profile", httpx.RequireFunc(permission.AgentsWrite, h.saveProfile))
	mux.Handle("GET /api/v1/agents/{id}/profile/versions", httpx.RequireFunc(permission.AgentsRead, h.versions))
	mux.Handle("GET /api/v1/agents/{id}/profile/versions/{version}", httpx.RequireFunc(permission.AgentsRead, h.profile))
	mux.Handle("GET /api/v1/agents/{id}/secrets", httpx.RequireFunc(permission.AgentsRead, h.secrets))
	mux.Handle("PUT /api/v1/agents/{id}/secrets/{name}", httpx.RequireFunc(permission.AgentsWrite, h.setSecret))
	mux.Handle("DELETE /api/v1/agents/{id}/secrets/{name}", httpx.RequireFunc(permission.AgentsWrite, h.deleteSecret))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.List(r.Context(), org, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

type createRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	p, err := httpx.Caller(r)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	by := p.Name
	if by == "" {
		by = p.Subject
	}
	a, err := h.commands.Create(r.Context(), p.Organization, agent.Create{Name: req.Name, Description: req.Description, CreatedBy: by})
	if err != nil {
		return err
	}
	return httpx.Created(w, r, a.ID.String(), a)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	a, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, a)
}

type updateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	a, err := h.commands.Update(r.Context(), org, id, agent.Update{Name: req.Name, Description: req.Description})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Delete(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) team(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return err
	}
	team, err := h.queries.Team(r.Context(), org, dock)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"agents": team})
}

type assignRequest struct {
	Default bool `json:"default"`
}

func (h *Handler) assign(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req assignRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return err
		}
	}
	a, err := h.commands.Assign(r.Context(), org, agent.Assign{Dock: dock, Agent: id, Default: req.Default})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) unassign(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	a, err := h.commands.Unassign(r.Context(), org, dock, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, a)
}
