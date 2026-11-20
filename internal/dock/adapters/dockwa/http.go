package dockwa

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

// Handler serves the admin routes of a WhatsApp dock's settings.
type Handler struct {
	settings *SettingsStore
	docks    dock.Queries
}

func NewHandler(settings *SettingsStore, docks dock.Queries) *Handler {
	return &Handler{settings: settings, docks: docks}
}

// Register mounts the routes (behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/docks/:id/whatsapp", httpx.RequireFunc(permission.DocksRead, h.get))
	r.Put("/api/v1/docks/:id/whatsapp", httpx.RequireFunc(permission.DocksWrite, h.save))
}

// whatsappDock finds the caller's dock and checks it is a WhatsApp one.
func (h *Handler) whatsappDock(c fiber.Ctx) (dock.Dock, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return dock.Dock{}, err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return dock.Dock{}, err
	}
	d, err := h.docks.Find(c.Context(), org, id)
	if err != nil {
		return dock.Dock{}, err
	}
	if d.Channel != dock.ChannelWhatsApp {
		return dock.Dock{}, errx.Business("not a whatsapp dock")
	}
	return d, nil
}

func (h *Handler) get(c fiber.Ctx) error {
	d, err := h.whatsappDock(c)
	if err != nil {
		return err
	}
	s, err := h.settings.Get(c.Context(), d.Organization, d.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, s)
}

// save changes the settings the body names and answers them all.
func (h *Handler) save(c fiber.Ctx) error {
	d, err := h.whatsappDock(c)
	if err != nil {
		return err
	}
	var p SettingsPatch
	if err := httpx.DecodeJSON(c, &p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	s, err := h.settings.Update(c.Context(), d.Organization, d.ID, p)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, s)
}
