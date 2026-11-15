// Package vaulthttp is the admin API of vaults. Every handler resolves the
// vault (or agent, dock) in the caller's organization before looking at the
// rest of the request, so another organization's id is always 404.
package vaulthttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	commands vault.Commands
	queries  vault.Queries
}

func New(c vault.Commands, q vault.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/vaults", httpx.RequireFunc(permission.VaultsRead, h.list))
	r.Post("/api/v1/vaults", httpx.RequireFunc(permission.VaultsWrite, h.create))
	r.Get("/api/v1/vaults/:id", httpx.RequireFunc(permission.VaultsRead, h.find))
	r.Patch("/api/v1/vaults/:id", httpx.RequireFunc(permission.VaultsWrite, h.update))
	r.Delete("/api/v1/vaults/:id", httpx.RequireFunc(permission.VaultsWrite, h.delete))
	r.Put("/api/v1/vaults/:id/agents/:agent", httpx.RequireFunc(permission.VaultsWrite, h.grantAgent))
	r.Delete("/api/v1/vaults/:id/agents/:agent", httpx.RequireFunc(permission.VaultsWrite, h.revokeAgent))
	r.Put("/api/v1/vaults/:id/docks/:dock", httpx.RequireFunc(permission.VaultsWrite, h.grantDock))
	r.Delete("/api/v1/vaults/:id/docks/:dock", httpx.RequireFunc(permission.VaultsWrite, h.revokeDock))
	r.Get("/api/v1/vaults/:id/entries", httpx.RequireFunc(permission.VaultsRead, h.entries))
	r.Post("/api/v1/vaults/:id/entries", httpx.RequireFunc(permission.VaultsWrite, h.createEntry))
	r.Get("/api/v1/vaults/:id/entries/:entry", httpx.RequireFunc(permission.VaultsRead, h.entry))
	r.Put("/api/v1/vaults/:id/entries/:entry", httpx.RequireFunc(permission.VaultsWrite, h.saveEntry))
	r.Delete("/api/v1/vaults/:id/entries/:entry", httpx.RequireFunc(permission.VaultsWrite, h.deleteEntry))
	r.Get("/api/v1/vaults/:id/entries/:entry/versions", httpx.RequireFunc(permission.VaultsRead, h.versions))
	r.Post("/api/v1/vaults/:id/entries/:entry/revert", httpx.RequireFunc(permission.VaultsWrite, h.revert))
	r.Get("/api/v1/agents/:id/vaults", httpx.RequireFunc(permission.VaultsRead, h.ofAgent))
	r.Get("/api/v1/docks/:id/vaults", httpx.RequireFunc(permission.VaultsRead, h.ofDock))
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
	Title       string `json:"title"`
	Description string `json:"description"`
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
	out, err := h.commands.Create(c.Context(), org, vault.Create{Name: req.Name, Title: req.Title, Description: req.Description})
	if err != nil {
		return err
	}
	return httpx.Created(c, out.ID.String(), out)
}

// target resolves the vault of the route in the caller's organization.
func (h *Handler) target(c fiber.Ctx) (identity.OrganizationID, vault.Vault, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return org, vault.Vault{}, err
	}
	id, err := identity.ParseVaultID(httpx.Param(c, "id"))
	if err != nil {
		return org, vault.Vault{}, err
	}
	v, err := h.queries.Find(c.Context(), org, id)
	return org, v, err
}

// detail is a vault with who it is granted to.
type detail struct {
	vault.Vault
	vault.Grants
}

func (h *Handler) find(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	g, err := h.queries.Grants(c.Context(), org, v.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, detail{v, g})
}

type updateRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

func (h *Handler) update(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.Update(c.Context(), org, v.ID, vault.Update{Title: req.Title, Description: req.Description})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) delete(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	if err := h.commands.Delete(c.Context(), org, v.ID); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

type grantRequest struct {
	Mode vault.Mode `json:"mode"`
}

func (h *Handler) grantAgent(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	agent, err := identity.ParseAgentID(httpx.Param(c, "agent"))
	if err != nil {
		return err
	}
	var req grantRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.GrantAgent(c.Context(), org, v.ID, agent, req.Mode)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) revokeAgent(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	agent, err := identity.ParseAgentID(httpx.Param(c, "agent"))
	if err != nil {
		return err
	}
	if err := h.commands.RevokeAgent(c.Context(), org, v.ID, agent); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (h *Handler) grantDock(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return err
	}
	var req grantRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.GrantDock(c.Context(), org, v.ID, dock, req.Mode)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) revokeDock(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	dock, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return err
	}
	if err := h.commands.RevokeDock(c.Context(), org, v.ID, dock); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

// entries lists the vault's entries, or searches them with ?q=.
func (h *Handler) entries(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	if q := c.Query("q"); q != "" {
		hits, err := h.queries.Search(c.Context(), org, []identity.VaultID{v.ID}, q, 0)
		if err != nil {
			return err
		}
		return httpx.JSON(c, http.StatusOK, struct {
			Hits []vault.Hit `json:"hits"`
		}{hits})
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Entries(c.Context(), org, v.ID, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

// by is who the principal is for the provenance of a person's write.
func by(c fiber.Ctx) (vault.Provenance, error) {
	p, err := httpx.Caller(c)
	if err != nil {
		return vault.Provenance{}, err
	}
	name := p.Name
	if name == "" {
		name = p.Subject
	}
	return vault.Provenance{Kind: vault.WrittenByUser, By: name}, nil
}

type entryRequest struct {
	Name string `json:"name"`
	Body string `json:"body"`
	Base int    `json:"base"`
}

func (h *Handler) createEntry(c fiber.Ctx) error {
	org, v, err := h.target(c)
	if err != nil {
		return err
	}
	var req entryRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	p, err := by(c)
	if err != nil {
		return err
	}
	out, err := h.commands.SaveEntry(c.Context(), org, vault.SaveEntry{Vault: v.ID, Name: req.Name, Body: req.Body, Provenance: p})
	if err != nil {
		return err
	}
	return httpx.Created(c, out.ID.String(), out)
}

// entryOf resolves the vault and the entry of the route.
func (h *Handler) entryOf(c fiber.Ctx) (identity.OrganizationID, vault.Entry, error) {
	org, v, err := h.target(c)
	if err != nil {
		return org, vault.Entry{}, err
	}
	id, err := identity.ParseEntryID(httpx.Param(c, "entry"))
	if err != nil {
		return org, vault.Entry{}, err
	}
	e, err := h.queries.Entry(c.Context(), org, v.ID, id)
	return org, e, err
}

func (h *Handler) entry(c fiber.Ctx) error {
	_, e, err := h.entryOf(c)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, e)
}

// saveEntry edits an entry: {body, base} (base is the version edited).
func (h *Handler) saveEntry(c fiber.Ctx) error {
	org, cur, err := h.entryOf(c)
	if err != nil {
		return err
	}
	var req entryRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	p, err := by(c)
	if err != nil {
		return err
	}
	out, err := h.commands.SaveEntry(c.Context(), org, vault.SaveEntry{Vault: cur.Vault, Name: cur.Name, Body: req.Body, Base: req.Base, Provenance: p})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) deleteEntry(c fiber.Ctx) error {
	org, e, err := h.entryOf(c)
	if err != nil {
		return err
	}
	if _, err := h.commands.DeleteEntry(c.Context(), org, e.Vault, e.ID); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (h *Handler) versions(c fiber.Ctx) error {
	org, e, err := h.entryOf(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Versions(c.Context(), org, e.Vault, e.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, struct {
		Versions []vault.Version `json:"versions"`
	}{out})
}

type revertRequest struct {
	Version int `json:"version"`
}

func (h *Handler) revert(c fiber.Ctx) error {
	org, e, err := h.entryOf(c)
	if err != nil {
		return err
	}
	var req revertRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	p, err := by(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Revert(c.Context(), org, vault.Revert{Vault: e.Vault, Entry: e.ID, Version: req.Version, Provenance: p})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) ofAgent(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.OfAgent(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, struct {
		Vaults []vault.Access `json:"vaults"`
	}{out})
}

func (h *Handler) ofDock(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.OfDock(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, struct {
		Vaults []vault.Access `json:"vaults"`
	}{out})
}
