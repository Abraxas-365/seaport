// Package hostinghttp is the admin API of environments and hosted agents.
package hostinghttp

import (
	"net/http"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

type Handler struct {
	commands hosting.Commands
	queries  hosting.Queries
	// runtime: this port runs hosted agents (SEAPORT_RUNTIME isn't off).
	runtime bool
}

func New(c hosting.Commands, q hosting.Queries, runtime bool) *Handler {
	return &Handler{commands: c, queries: q, runtime: runtime}
}

// Register mounts the routes on mux (already behind admin auth). A spec
// whose repo credentials name agent secrets directs them, so writing one,
// or hosting an agent on one, needs agents:write too (credentials).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/environments", stdhttp.RequireFunc(permission.EnvironmentsRead, h.list))
	mux.Handle("POST /api/v1/environments", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.create))
	mux.Handle("GET /api/v1/environments/{id}", stdhttp.RequireFunc(permission.EnvironmentsRead, h.find))
	mux.Handle("PUT /api/v1/environments/{id}", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.save))
	mux.Handle("DELETE /api/v1/environments/{id}", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.delete))
	mux.Handle("GET /api/v1/environments/{id}/versions", stdhttp.RequireFunc(permission.EnvironmentsRead, h.versions))
	mux.Handle("GET /api/v1/environments/{id}/versions/{version}", stdhttp.RequireFunc(permission.EnvironmentsRead, h.find))
	mux.Handle("POST /api/v1/environments/{id}/build", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.startBuild))
	mux.Handle("GET /api/v1/environments/{id}/build", stdhttp.RequireFunc(permission.EnvironmentsRead, h.build))
	mux.Handle("GET /api/v1/environments/{id}/versions/{version}/build", stdhttp.RequireFunc(permission.EnvironmentsRead, h.build))
	mux.Handle("POST /api/v1/homes", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.uploadHome))
	mux.Handle("GET /api/v1/homes/rules", stdhttp.RequireFunc(permission.EnvironmentsRead, h.homeRules))
	mux.Handle("GET /api/v1/homes/{digest}", stdhttp.RequireFunc(permission.EnvironmentsRead, h.home))
	mux.Handle("GET /api/v1/homes/{digest}/archive", stdhttp.RequireFunc(permission.EnvironmentsRead, h.homeArchive))
	mux.Handle("GET /api/v1/homes/{digest}/files/{path...}", stdhttp.RequireFunc(permission.EnvironmentsRead, h.homeFile))
	mux.Handle("GET /api/v1/hosting", stdhttp.RequireFunc(permission.EnvironmentsRead, h.hostedAgents))
	mux.Handle("GET /api/v1/agents/{id}/hosting", stdhttp.RequireFunc(permission.EnvironmentsRead, h.hosted))
	mux.Handle("PUT /api/v1/agents/{id}/hosting", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.host))
	mux.Handle("DELETE /api/v1/agents/{id}/hosting", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.unhost))
	mux.Handle("POST /api/v1/agents/{id}/hosting/slots/{slot}/start", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.startSlot))
	mux.Handle("POST /api/v1/agents/{id}/hosting/slots/{slot}/stop", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.stopSlot))
	mux.Handle("GET /api/v1/agents/{id}/hosting/egress", stdhttp.RequireFunc(permission.EnvironmentsRead, h.denials))
	// Logs: whatever the agent printed (a tool may echo an env secret), so
	// only who may change where it runs reads them.
	mux.Handle("GET /api/v1/agents/{id}/hosting/slots/{slot}/logs", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.slotLog))
	mux.Handle("GET /api/v1/agents/{id}/usage", stdhttp.RequireFunc(permission.AgentsRead, h.usage))
	// Subscription sign-in (experimental): signing a slot in decides whose
	// account it spends, so it is environments:write's, like starting it.
	mux.Handle("POST /api/v1/agents/{id}/hosting/slots/{slot}/login", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.startLogin))
	mux.Handle("GET /api/v1/agents/{id}/hosting/slots/{slot}/login", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.login))
	mux.Handle("POST /api/v1/agents/{id}/hosting/slots/{slot}/login/code", stdhttp.RequireFunc(permission.EnvironmentsWrite, h.loginCode))
	mux.Handle("GET /api/v1/agents/{id}/hosting/slots/{slot}/account", stdhttp.RequireFunc(permission.EnvironmentsRead, h.account))
}

// Workspace is GET /crew/workspace (behind crew auth): a hosted slot's
// plan for /workspace, its repo credentials resolved. Only a slot token
// gets it, and only its own slot's (TestOnlyTheCrewEquips).
func (h *Handler) Workspace() http.Handler {
	return stdhttp.Handle(func(w http.ResponseWriter, r *http.Request) error {
		p, err := stdhttp.Caller(r)
		if err != nil {
			return err
		}
		if p.Slot == 0 || p.Agent.IsZero() {
			return errx.Forbidden("only a hosted slot's own token can read its workspace")
		}
		out, err := h.commands.OpenWorkspace(r.Context(), p.Organization, p.Agent, p.Slot)
		if err != nil {
			return err
		}
		return stdhttp.JSON(w, http.StatusOK, out)
	})
}

// by names the caller in created_by / updated_by.
func by(p httpx.Principal) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Subject
}

// credentials: a spec naming agent secrets directs them (the repo URL
// gets the token, the gateway spends the model key), which is
// agents:write's to allow.
func credentials(p httpx.Principal, spec hosting.Spec) error {
	if len(spec.Secrets()) > 0 && !p.Can(permission.AgentsWrite) {
		return errx.Forbidden("missing permission " + permission.AgentsWrite + " (the spec's repo credentials or model keys name agent secrets)")
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
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	page, err := stdhttp.Pagination(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Environments(r.Context(), org, page)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

type createRequest struct {
	Name string       `json:"name"`
	Spec hosting.Spec `json:"spec"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	p, err := stdhttp.Caller(r)
	if err != nil {
		return err
	}
	var req createRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.CreateEnvironment(r.Context(), p.Organization, hosting.CreateEnvironment{Name: req.Name, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return stdhttp.Created(w, r, e.ID.String(), e)
}

func (h *Handler) find(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
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
	return stdhttp.JSON(w, http.StatusOK, e)
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
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, org)
	if err != nil {
		return err
	}
	var req buildRequest
	if r.ContentLength != 0 {
		if err := stdhttp.DecodeJSON(r, &req); err != nil {
			return err
		}
	}
	b, err := h.commands.StartBuild(r.Context(), org, id, hosting.BuildEnvironment{Version: req.Version, Force: req.Force})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusAccepted, b)
}

func (h *Handler) build(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
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
	return stdhttp.JSON(w, http.StatusOK, b)
}

type saveRequest struct {
	// Base is the version the edit started from (the current one).
	Base int          `json:"base"`
	Spec hosting.Spec `json:"spec"`
}

func (h *Handler) save(w http.ResponseWriter, r *http.Request) error {
	p, err := stdhttp.Caller(r)
	if err != nil {
		return err
	}
	id, err := h.environmentID(r, p.Organization)
	if err != nil {
		return err
	}
	var req saveRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.SaveEnvironment(r.Context(), p.Organization, id, hosting.SaveEnvironment{Base: req.Base, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, e)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
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
	org, err := stdhttp.Organization(r)
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
	return stdhttp.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (h *Handler) hostedAgents(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	out, err := h.queries.HostedAgents(r.Context(), org)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, map[string]any{"agents": out, "runtime": h.runtime})
}

func (h *Handler) hosted(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
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
	return stdhttp.JSON(w, http.StatusOK, out)
}

type hostRequest struct {
	// Environment is the environment's id or name.
	Environment string `json:"environment"`
	Slots       int    `json:"slots"`
}

func (h *Handler) host(w http.ResponseWriter, r *http.Request) error {
	p, err := stdhttp.Caller(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var req hostRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
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
	return stdhttp.JSON(w, http.StatusOK, out)
}

func (h *Handler) unhost(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
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

func (h *Handler) slotAction(r *http.Request) (httpx.Principal, hosting.SlotAction, error) {
	p, err := stdhttp.Caller(r)
	if err != nil {
		return p, hosting.SlotAction{}, err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return p, hosting.SlotAction{}, err
	}
	idx, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil {
		return p, hosting.SlotAction{}, errx.InvalidField("slot", "slot must be a number")
	}
	return p, hosting.SlotAction{Agent: id, Index: idx, By: by(p)}, nil
}

func (h *Handler) startSlot(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	out, err := h.commands.StartSlot(r.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusAccepted, out)
}

func (h *Handler) stopSlot(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	out, err := h.commands.StopSlot(r.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusAccepted, out)
}

// slotLog is GET /api/v1/agents/{id}/hosting/slots/{slot}/logs?lines=N
// (default 300): the tail of the slot's container output.
func (h *Handler) slotLog(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	q := hosting.LogQuery{Agent: in.Agent, Index: in.Index, Lines: 300}
	if v := r.URL.Query().Get("lines"); v != "" {
		if q.Lines, err = strconv.Atoi(v); err != nil {
			return errx.InvalidField("lines", "lines must be a number")
		}
	}
	out, err := h.queries.SlotLog(r.Context(), p.Organization, q)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

// usage is GET /api/v1/agents/{id}/usage?days=N (default 30): the
// agent's model use through the gateway.
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if days, err = strconv.Atoi(v); err != nil {
			return errx.InvalidField("days", "days must be a number")
		}
	}
	out, err := h.queries.Usage(r.Context(), org, id, hosting.UsageQuery{Days: days})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, map[string]any{"days": out})
}

// denials is GET /api/v1/agents/{id}/hosting/egress: the hosts the
// egress proxy refused the agent's slots.
func (h *Handler) denials(w http.ResponseWriter, r *http.Request) error {
	org, err := stdhttp.Organization(r)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Denials(r.Context(), org, id)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, map[string]any{"denials": out})
}

// startLogin is POST …/slots/{slot}/login: starts the slot's sign-in (or
// answers the one going); poll GET for its URL.
func (h *Handler) startLogin(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	out, err := h.commands.StartLogin(r.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusAccepted, out)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Login(r.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}

type codeRequest struct {
	Code string `json:"code"`
}

// loginCode is POST …/slots/{slot}/login/code {code}: the code the
// sign-in page showed, for the waiting sign-in.
func (h *Handler) loginCode(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	var req codeRequest
	if err := stdhttp.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := h.commands.SubmitLoginCode(r.Context(), p.Organization, hosting.LoginCode{Agent: in.Agent, Index: in.Index, Code: req.Code})
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusAccepted, out)
}

// account is GET …/slots/{slot}/account: how the slot's harness is
// signed in (asked of the harness).
func (h *Handler) account(w http.ResponseWriter, r *http.Request) error {
	p, in, err := h.slotAction(r)
	if err != nil {
		return err
	}
	out, err := h.queries.Account(r.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return stdhttp.JSON(w, http.StatusOK, out)
}
