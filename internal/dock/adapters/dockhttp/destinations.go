package dockhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

// registerDestinations mounts the destination routes (dock.Destination).
func (h *Handler) registerDestinations(r httpx.Router) {
	r.Get("/api/v1/docks/:id/destinations", httpx.RequireFunc(permission.DocksRead, h.destinations))
	r.Put("/api/v1/docks/:id/destinations/:name", httpx.RequireFunc(permission.DocksWrite, h.putDestination))
	r.Delete("/api/v1/docks/:id/destinations/:name", httpx.RequireFunc(permission.DocksWrite, h.removeDestination))
}

type destinationRequest struct {
	Target string `json:"target"`
	Title  string `json:"title"`
}

func (h *Handler) destinations(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Destinations(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) putDestination(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req destinationRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.PutDestination(c.Context(), org, dock.AddDestination{Dock: id, Name: httpx.Param(c, "name"), Target: req.Target, Title: req.Title})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) removeDestination(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.commands.RemoveDestination(c.Context(), org, id, httpx.Param(c, "name")); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}
