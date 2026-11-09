// Package hostinghttp is the admin API of environments and hosted agents.
package hostinghttp

import (
	"net/http"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands hosting.Commands
	queries  hosting.Queries
}

func New(c hosting.Commands, q hosting.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on mux (already behind admin auth). A spec
// whose repo credentials name agent secrets directs them, so writing one,
// or hosting an agent on one, needs agents:write too (credentials).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/environments", httpx.RequireFunc(permission.EnvironmentsRead, h.list))
	mux.Handle("POST /api/v1/environments", httpx.RequireFunc(permission.EnvironmentsWrite, h.create))
	mux.Handle("GET /api/v1/environments/{id}", httpx.RequireFunc(permission.EnvironmentsRead, h.find))
	mux.Handle("PUT /api/v1/environments/{id}", httpx.RequireFunc(permission.EnvironmentsWrite, h.save))
	mux.Handle("DELETE /api/v1/environments/{id}", httpx.RequireFunc(permission.EnvironmentsWrite, h.delete))
	mux.Handle("GET /api/v1/environments/{id}/versions", httpx.RequireFunc(permission.EnvironmentsRead, h.versions))
	mux.Handle("GET /api/v1/environments/{id}/versions/{version}", httpx.RequireFunc(permission.EnvironmentsRead, h.find))
	mux.Handle("POST /api/v1/environments/{id}/build", httpx.RequireFunc(permission.EnvironmentsWrite, h.startBuild))
	mux.Handle("GET /api/v1/environments/{id}/build", httpx.RequireFunc(permission.EnvironmentsRead, h.build))
	mux.Handle("GET /api/v1/environments/{id}/versions/{version}/build", httpx.RequireFunc(permission.EnvironmentsRead, h.build))
	mux.Handle("GET /api/v1/hosting", httpx.RequireFunc(permission.EnvironmentsRead, h.hostedAgents))
	mux.Handle("GET /api/v1/agents/{id}/hosting", httpx.RequireFunc(permission.EnvironmentsRead, h.hosted))
	mux.Handle("PUT /api/v1/agents/{id}/hosting", httpx.RequireFunc(permission.EnvironmentsWrite, h.host))
	mux.Handle("DELETE /api/v1/agents/{id}/hosting", httpx.RequireFunc(permission.EnvironmentsWrite, h.unhost))
}

// by names the caller in created_by / updated_by.
func by(p httpx.Principal) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Subject
}

// credentials: a spec naming agent secrets directs them (the repo URL
// gets the token), which is agents:write's to allow.
func credentials(p httpx.Principal, spec hosting.Spec) error {
	if len(spec.Secrets()) > 0 && !p.Can(permission.AgentsWrite) {
		return errx.Forbidden("missing permission " + permission.AgentsWrite + " (the spec's repo credentials name agent secrets)")
	}
	return nil
}

// environmentID resolves {id}: an environment id or name.
func (h *Handler) environmentID(r *http.Request, org identity.OrganizationID) (identity.EnvironmentID, error) {
	raw := r.PathValue("id")
	if id, err := identity.ParseEnvironmentID(raw); err == nil {
		return id, nil
	}
	e, err := h.queries.EnvironmentByName(r.Context(), org, raw)
	if err != nil {
		return identity.EnvironmentID{}, err
	}
	return e.ID, nil
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
	out, err := h.queries.Environments(r.Context(), org, page)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

type createRequest struct {
	Name string       `json:"name"`
	Spec hosting.Spec `json:"spec"`
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
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.CreateEnvironment(r.Context(), p.Organization, hosting.CreateEnvironment{Name: req.Name, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.Created(w, r, e.ID.String(), e)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	version, err := pathVersion(r)
	if err != nil {
		return err
	}
	e, err := h.queries.Environment(r.Context(), org, id, version)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, e)
}

// pathVersion is {version} (0 when the route has none).
func pathVersion(r *http.Request) (int, error) {
	v := r.PathValue("version")
	if v == "" {
		return 0, nil
	}
	version, err := strconv.Atoi(v)
	if err != nil || version <= 0 {
		return 0, errx.InvalidField("version", "version must be a positive number")
	}
	return version, nil
}

type buildRequest struct {
	// Version to build (0: the current one).
	Version int  `json:"version"`
	Force   bool `json:"force"`
}

// startBuild answers 202 with the build (in progress, or as it is when
// already built or building); the body is optional.
func (h *Handler) startBuild(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	var req buildRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return err
		}
	}
	b, err := h.commands.StartBuild(r.Context(), org, id, hosting.BuildEnvironment{Version: req.Version, Force: req.Force})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, b)
}

func (h *Handler) build(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	version, err := pathVersion(r)
	if err != nil {
		return err
	}
	b, err := h.queries.Build(r.Context(), org, id, version)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, b)
}

type saveRequest struct {
	// Base is the version the edit started from (the current one).
	Base int          `json:"base"`
	Spec hosting.Spec `json:"spec"`
}

func (h *Handler) save(w http.ResponseWriter, r *http.Request) error {
	p, err := httpx.Caller(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, p.Organization)
	if err != nil {
		return err
	}
	var req saveRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.SaveEnvironment(r.Context(), p.Organization, id, hosting.SaveEnvironment{Base: req.Base, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	if err := h.commands.DeleteEnvironment(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	out, err := h.queries.Versions(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (h *Handler) hostedAgents(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	out, err := h.queries.HostedAgents(r.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (h *Handler) hosted(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Hosted(r.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

type hostRequest struct {
	// Environment is the environment's id or name.
	Environment string `json:"environment"`
	Slots       int    `json:"slots"`
}

func (h *Handler) host(w http.ResponseWriter, r *http.Request) error {
	p, err := httpx.Caller(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req hostRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Environment == "" {
		return errx.InvalidField("environment", "environment (id or name) is required")
	}
	var env hosting.Environment
	if envID, perr := identity.ParseEnvironmentID(req.Environment); perr == nil {
		env, err = h.queries.Environment(r.Context(), p.Organization, envID, 0)
	} else {
		env, err = h.queries.EnvironmentByName(r.Context(), p.Organization, req.Environment)
	}
	if err != nil {
		return err
	}
	if err := credentials(p, env.Spec); err != nil {
		return err
	}
	out, err := h.commands.Host(r.Context(), p.Organization, id, hosting.Host{Environment: env.ID, Slots: req.Slots, By: by(p)})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) unhost(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := h.commands.Unhost(r.Context(), org, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
