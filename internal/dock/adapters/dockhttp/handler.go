// Package dockhttp is the admin API of docks.
package dockhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	commands dock.Commands
	queries  dock.Queries
}

func New(c dock.Commands, q dock.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/docks", httpx.RequireFunc(permission.DocksRead, h.list))
	r.Post("/api/v1/docks", httpx.RequireFunc(permission.DocksWrite, h.create))
	r.Get("/api/v1/docks/:id", httpx.RequireFunc(permission.DocksRead, h.find))
	r.Post("/api/v1/docks/:id/relink", httpx.RequireFunc(permission.DocksWrite, h.relink))
	r.Patch("/api/v1/docks/:id", httpx.RequireFunc(permission.DocksWrite, h.update))
	r.Post("/api/v1/docks/:id/unlink", httpx.RequireFunc(permission.DocksWrite, h.unlink))
	r.Delete("/api/v1/docks/:id", httpx.RequireFunc(permission.DocksWrite, h.delete))
}

type updateRequest struct {
	Name *string `json:"name"`
}

func (h *Handler) update(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	d, err := h.commands.Update(c.Context(), org, id, dock.Update{Name: req.Name})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, d)
}

func (h *Handler) unlink(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	d, err := h.commands.Unlink(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, d)
}

func (h *Handler) delete(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.commands.Delete(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

type createRequest struct {
	Channel dock.Channel `json:"channel"`
	Name    string       `json:"name"`
}

func (h *Handler) create(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	d, err := h.commands.Create(c.Context(), org, dock.Create{Channel: req.Channel, Name: req.Name})
	if err != nil {
		return err
	}
	return httpx.Created(c, d.ID.String(), d)
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

func (h *Handler) find(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	d, err := h.queries.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, d)
}

func (h *Handler) relink(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	d, err := h.commands.Relink(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, d)
}
