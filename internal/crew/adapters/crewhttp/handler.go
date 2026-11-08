// Package crewhttp is the admin API of crew tokens.
package crewhttp

import (
	"context"
	"net/http"
	"time"

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

// Register mounts the routes on mux (already behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/crew/tokens", httpx.RequireFunc(permission.CrewTokensRead, h.list))
	mux.Handle("POST /api/v1/crew/tokens", httpx.RequireFunc(permission.CrewTokensWrite, h.create))
	mux.Handle("DELETE /api/v1/crew/tokens/{id}", httpx.RequireFunc(permission.CrewTokensWrite, h.revoke))
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
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
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
	t, err := h.commands.Create(r.Context(), p.Organization,
		crew.CreateToken{Name: req.Name, ExpiresAt: req.ExpiresAt, CreatedBy: by})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return httpx.Created(w, r, t.ID.String(), t)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseCrewTokenID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Revoke(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Resolver authenticates agents with crew tokens (httpx.CrewResolver).
type Resolver struct{ Tokens crew.TokenCommands }

var _ httpx.CrewResolver = Resolver{}

func (res Resolver) ResolveCrew(ctx context.Context, value string) (httpx.Principal, error) {
	t, err := res.Tokens.Authenticate(ctx, value)
	if err != nil {
		return httpx.Principal{}, err
	}
	p := httpx.Principal{Organization: t.Organization, Role: httpx.RoleCrew, Subject: "crew:" + t.Name, Credential: t.ID.String()}
	if t.ExpiresAt != nil {
		p.Expires = *t.ExpiresAt
	}
	return p, nil
}
