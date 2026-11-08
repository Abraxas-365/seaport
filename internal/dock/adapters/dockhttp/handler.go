// Package dockhttp is the admin API of docks.
package dockhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands dock.Commands
	queries  dock.Queries
}

func New(c dock.Commands, q dock.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on mux (already behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/docks", httpx.RequireFunc(permission.DocksRead, h.list))
	mux.Handle("POST /api/v1/docks", httpx.RequireFunc(permission.DocksWrite, h.create))
	mux.Handle("GET /api/v1/docks/{id}", httpx.RequireFunc(permission.DocksRead, h.find))
	mux.Handle("POST /api/v1/docks/{id}/relink", httpx.RequireFunc(permission.DocksWrite, h.relink))
	mux.Handle("PATCH /api/v1/docks/{id}", httpx.RequireFunc(permission.DocksWrite, h.update))
	mux.Handle("POST /api/v1/docks/{id}/unlink", httpx.RequireFunc(permission.DocksWrite, h.unlink))
	mux.Handle("DELETE /api/v1/docks/{id}", httpx.RequireFunc(permission.DocksWrite, h.delete))
}

type updateRequest struct {
	Name *string `json:"name"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	d, err := h.commands.Update(r.Context(), org, id, dock.Update{Name: req.Name})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) unlink(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	d, err := h.commands.Unlink(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Delete(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type createRequest struct {
	Channel dock.Channel `json:"channel"`
	Name    string       `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	d, err := h.commands.Create(r.Context(), org, dock.Create{Channel: req.Channel, Name: req.Name})
	if err != nil {
		return err
	}
	return httpx.Created(w, r, d.ID.String(), d)
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

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	d, err := h.queries.Find(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) relink(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return err
	}
	d, err := h.commands.Relink(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}
