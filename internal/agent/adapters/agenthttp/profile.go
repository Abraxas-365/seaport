package agenthttp

import (
	"net/http"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	version := 0
	if v := r.PathValue("version"); v != "" {
		if version, err = strconv.Atoi(v); err != nil || version <= 0 {
			return errx.InvalidField("version", "version must be a positive number")
		}
	}
	p, err := h.queries.Profile(r.Context(), org, id, version)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, p)
}

type profileRequest struct {
	// Base is the version the edit started from (the current one).
	Base         int               `json:"base"`
	Instructions string            `json:"instructions"`
	Skills       []agent.Skill     `json:"skills"`
	MCPServers   []agent.MCPServer `json:"mcp_servers"`
	Approve      string            `json:"approve"`
	Model        string            `json:"model"`
	Effort       string            `json:"effort"`
}

func (h *Handler) saveProfile(w http.ResponseWriter, r *http.Request) error {
	p, err := httpx.Caller(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req profileRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	by := p.Name
	if by == "" {
		by = p.Subject
	}
	out, err := h.commands.SaveProfile(r.Context(), p.Organization, id, agent.SaveProfile{Base: req.Base,
		Instructions: req.Instructions, Skills: req.Skills, MCPServers: req.MCPServers, Approve: req.Approve,
		Model: req.Model, Effort: req.Effort, CreatedBy: by})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Versions(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (h *Handler) secrets(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Secrets(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"secrets": out})
}

type secretRequest struct {
	Value string `json:"value"`
	Env   bool   `json:"env"`
}

// setSecret answers with the secret's name and flags, never its value.
func (h *Handler) setSecret(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req secretRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := h.commands.SetSecret(r.Context(), org, id, agent.SetSecret{Name: r.PathValue("name"), Value: req.Value, Env: req.Env})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteSecret(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.DeleteSecret(r.Context(), org, id, r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
