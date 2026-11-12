// Package crewhttp is the admin API of crew tokens.
package crewhttp

import (
	"context"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands crew.TokenCommands
	queries  crew.TokenQueries
}

func New(c crew.TokenCommands, q crew.TokenQueries) *Handler {
	return &Handler{commands: c, queries: q}
}

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/crew/tokens", httpx.RequireFunc(permission.CrewTokensRead, h.list))
	r.Post("/api/v1/crew/tokens", httpx.RequireFunc(permission.CrewTokensWrite, h.create))
	r.Delete("/api/v1/crew/tokens/:id", httpx.RequireFunc(permission.CrewTokensWrite, h.revoke))
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
	var filter crew.TokenFilter
	if raw := c.Query("agent_id"); raw != "" {
		id, err := identity.ParseAgentID(raw)
		if err != nil {
			return err
		}
		filter.Agent = &id
	}
	out, err := h.queries.List(c.Context(), org, filter, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type createRequest struct {
	Name      string           `json:"name"`
	Agent     identity.AgentID `json:"agent_id"`
	ExpiresAt *time.Time       `json:"expires_at"`
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
	t, err := h.commands.Create(c.Context(), p.Organization,
		crew.CreateToken{Name: req.Name, Agent: req.Agent, ExpiresAt: req.ExpiresAt, CreatedBy: by})
	if err != nil {
		return err
	}
	c.Set("Cache-Control", "no-store")
	return httpx.Created(c, t.ID.String(), t)
}

func (h *Handler) revoke(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseCrewTokenID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.commands.Revoke(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

// Status is the live agent connections of every process (the hub).
type Status interface {
	Status(ctx context.Context, organization identity.OrganizationID) (crew.Status, error)
}

// ServeStatus serves GET /api/v1/crew: who is connected (admin).
func ServeStatus(s Status) fiber.Handler {
	return func(c fiber.Ctx) error {
		org, err := httpx.Organization(c)
		if err != nil {
			return err
		}
		st, err := s.Status(c.Context(), org)
		if err != nil {
			return err
		}
		return httpx.JSON(c, http.StatusOK, st)
	}
}

// Resolver authenticates agents with crew tokens (httpx.CrewResolver).
type Resolver struct{ Tokens crew.TokenCommands }

var _ httpx.CrewResolver = Resolver{}

func (res Resolver) ResolveCrew(ctx context.Context, value string) (httpx.Principal, error) {
	t, err := res.Tokens.Authenticate(ctx, value)
	if err != nil {
		return httpx.Principal{}, err
	}
	p := httpx.Principal{Organization: t.Organization, Role: httpx.RoleCrew, Subject: "crew:" + t.Name,
		Credential: t.ID.String(), Agent: t.Agent, AgentName: t.AgentName, Slot: t.Slot}
	if t.ExpiresAt != nil {
		p.Expires = *t.ExpiresAt
	}
	return p, nil
}
