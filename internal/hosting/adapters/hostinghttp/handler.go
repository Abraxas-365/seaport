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
	"github.com/gofiber/fiber/v3"
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

// Register mounts the routes on r (already behind admin auth). A spec
// whose repo credentials name agent secrets directs them, so writing one,
// or hosting an agent on one, needs agents:write too (credentials).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/environments", httpx.RequireFunc(permission.EnvironmentsRead, h.list))
	r.Post("/api/v1/environments", httpx.RequireFunc(permission.EnvironmentsWrite, h.create))
	r.Get("/api/v1/environments/:id", httpx.RequireFunc(permission.EnvironmentsRead, h.find))
	r.Put("/api/v1/environments/:id", httpx.RequireFunc(permission.EnvironmentsWrite, h.save))
	r.Delete("/api/v1/environments/:id", httpx.RequireFunc(permission.EnvironmentsWrite, h.delete))
	r.Get("/api/v1/environments/:id/versions", httpx.RequireFunc(permission.EnvironmentsRead, h.versions))
	r.Get("/api/v1/environments/:id/versions/:version", httpx.RequireFunc(permission.EnvironmentsRead, h.find))
	r.Post("/api/v1/environments/:id/build", httpx.RequireFunc(permission.EnvironmentsWrite, h.startBuild))
	r.Get("/api/v1/environments/:id/build", httpx.RequireFunc(permission.EnvironmentsRead, h.build))
	r.Get("/api/v1/environments/:id/versions/:version/build", httpx.RequireFunc(permission.EnvironmentsRead, h.build))
	r.Post("/api/v1/homes", httpx.RequireFunc(permission.EnvironmentsWrite, h.uploadHome))
	r.Get("/api/v1/homes/rules", httpx.RequireFunc(permission.EnvironmentsRead, h.homeRules))
	r.Get("/api/v1/homes/:digest", httpx.RequireFunc(permission.EnvironmentsRead, h.home))
	r.Get("/api/v1/homes/:digest/archive", httpx.RequireFunc(permission.EnvironmentsRead, h.homeArchive))
	r.Get("/api/v1/homes/:digest/files/*", httpx.RequireFunc(permission.EnvironmentsRead, h.homeFile))
	r.Get("/api/v1/hosting", httpx.RequireFunc(permission.EnvironmentsRead, h.hostedAgents))
	r.Get("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.EnvironmentsRead, h.hosted))
	r.Put("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.EnvironmentsWrite, h.host))
	r.Delete("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.EnvironmentsWrite, h.unhost))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/start", httpx.RequireFunc(permission.EnvironmentsWrite, h.startSlot))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/stop", httpx.RequireFunc(permission.EnvironmentsWrite, h.stopSlot))
	r.Get("/api/v1/agents/:id/hosting/egress", httpx.RequireFunc(permission.EnvironmentsRead, h.denials))
	// Logs: whatever the agent printed (a tool may echo an env secret), so
	// only who may change where it runs reads them.
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/logs", httpx.RequireFunc(permission.EnvironmentsWrite, h.slotLog))
	r.Get("/api/v1/agents/:id/usage", httpx.RequireFunc(permission.AgentsRead, h.usage))
	// Subscription sign-in (experimental): signing a slot in decides whose
	// account it spends, so it is environments:write's, like starting it.
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/login", httpx.RequireFunc(permission.EnvironmentsWrite, h.startLogin))
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/login", httpx.RequireFunc(permission.EnvironmentsWrite, h.login))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/login/code", httpx.RequireFunc(permission.EnvironmentsWrite, h.loginCode))
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/account", httpx.RequireFunc(permission.EnvironmentsRead, h.account))
}

// Workspace is GET /crew/workspace (behind crew auth): a hosted slot's
// plan for /workspace, its repo credentials resolved. Only a slot token
// gets it, and only its own slot's (TestOnlyTheCrewEquips).
func (h *Handler) Workspace() fiber.Handler {
	return func(c fiber.Ctx) error {
		p, err := httpx.Caller(c)
		if err != nil {
			return err
		}
		if p.Slot == 0 || p.Agent.IsZero() {
			return errx.Forbidden("only a hosted slot's own token can read its workspace")
		}
		out, err := h.commands.OpenWorkspace(c.Context(), p.Organization, p.Agent, p.Slot)
		if err != nil {
			return err
		}
		return httpx.JSON(c, http.StatusOK, out)
	}
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
func (h *Handler) environmentID(c fiber.Ctx, org identity.OrganizationID) (identity.EnvironmentID, error) {
	raw := httpx.Param(c, "id")
	if id, err := identity.ParseEnvironmentID(raw); err == nil {
		return id, nil
	}
	e, err := h.queries.EnvironmentByName(c.Context(), org, raw)
	if err != nil {
		return identity.EnvironmentID{}, err
	}
	return e.ID, nil
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
	out, err := h.queries.Environments(c.Context(), org, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type createRequest struct {
	Name string       `json:"name"`
	Spec hosting.Spec `json:"spec"`
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
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.CreateEnvironment(c.Context(), p.Organization, hosting.CreateEnvironment{Name: req.Name, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.Created(c, e.ID.String(), e)
}

func (h *Handler) find(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, org)
	if err != nil {
		return err
	}
	version, err := pathVersion(c)
	if err != nil {
		return err
	}
	e, err := h.queries.Environment(c.Context(), org, id, version)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, e)
}

// pathVersion is {version} (0 when the route has none).
func pathVersion(c fiber.Ctx) (int, error) {
	v := httpx.Param(c, "version")
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
func (h *Handler) startBuild(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, org)
	if err != nil {
		return err
	}
	var req buildRequest
	if c.Request().Header.ContentLength() != 0 {
		if err := httpx.DecodeJSON(c, &req); err != nil {
			return err
		}
	}
	b, err := h.commands.StartBuild(c.Context(), org, id, hosting.BuildEnvironment{Version: req.Version, Force: req.Force})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, b)
}

func (h *Handler) build(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, org)
	if err != nil {
		return err
	}
	version, err := pathVersion(c)
	if err != nil {
		return err
	}
	b, err := h.queries.Build(c.Context(), org, id, version)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, b)
}

type saveRequest struct {
	// Base is the version the edit started from (the current one).
	Base int          `json:"base"`
	Spec hosting.Spec `json:"spec"`
}

func (h *Handler) save(c fiber.Ctx) error {
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, p.Organization)
	if err != nil {
		return err
	}
	var req saveRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	if err := credentials(p, req.Spec); err != nil {
		return err
	}
	e, err := h.commands.SaveEnvironment(c.Context(), p.Organization, id, hosting.SaveEnvironment{Base: req.Base, Spec: req.Spec, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, e)
}

func (h *Handler) delete(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, org)
	if err != nil {
		return err
	}
	if err := h.commands.DeleteEnvironment(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (h *Handler) versions(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := h.environmentID(c, org)
	if err != nil {
		return err
	}
	out, err := h.queries.Versions(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, map[string]any{"versions": out})
}

func (h *Handler) hostedAgents(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	out, err := h.queries.HostedAgents(c.Context(), org)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, map[string]any{"agents": out, "runtime": h.runtime})
}

func (h *Handler) hosted(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Hosted(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type hostRequest struct {
	// Environment is the environment's id or name.
	Environment string `json:"environment"`
	Slots       int    `json:"slots"`
}

func (h *Handler) host(c fiber.Ctx) error {
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req hostRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	if req.Environment == "" {
		return errx.InvalidField("environment", "environment (id or name) is required")
	}
	var env hosting.Environment
	if envID, perr := identity.ParseEnvironmentID(req.Environment); perr == nil {
		env, err = h.queries.Environment(c.Context(), p.Organization, envID, 0)
	} else {
		env, err = h.queries.EnvironmentByName(c.Context(), p.Organization, req.Environment)
	}
	if err != nil {
		return err
	}
	if err := credentials(p, env.Spec); err != nil {
		return err
	}
	out, err := h.commands.Host(c.Context(), p.Organization, id, hosting.Host{Environment: env.ID, Slots: req.Slots, By: by(p)})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) unhost(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	if err := h.commands.Unhost(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (h *Handler) slotAction(c fiber.Ctx) (httpx.Principal, hosting.SlotAction, error) {
	p, err := httpx.Caller(c)
	if err != nil {
		return p, hosting.SlotAction{}, err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return p, hosting.SlotAction{}, err
	}
	idx, err := strconv.Atoi(httpx.Param(c, "slot"))
	if err != nil {
		return p, hosting.SlotAction{}, errx.InvalidField("slot", "slot must be a number")
	}
	return p, hosting.SlotAction{Agent: id, Index: idx, By: by(p)}, nil
}

func (h *Handler) startSlot(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	out, err := h.commands.StartSlot(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, out)
}

func (h *Handler) stopSlot(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	out, err := h.commands.StopSlot(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, out)
}

// slotLog is GET /api/v1/agents/{id}/hosting/slots/{slot}/logs?lines=N
// (default 300): the tail of the slot's container output.
func (h *Handler) slotLog(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	q := hosting.LogQuery{Agent: in.Agent, Index: in.Index, Lines: 300}
	if v := c.Query("lines"); v != "" {
		if q.Lines, err = strconv.Atoi(v); err != nil {
			return errx.InvalidField("lines", "lines must be a number")
		}
	}
	out, err := h.queries.SlotLog(c.Context(), p.Organization, q)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

// usage is GET /api/v1/agents/{id}/usage?days=N (default 30): the
// agent's model use through the gateway.
func (h *Handler) usage(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	days := 30
	if v := c.Query("days"); v != "" {
		if days, err = strconv.Atoi(v); err != nil {
			return errx.InvalidField("days", "days must be a number")
		}
	}
	out, err := h.queries.Usage(c.Context(), org, id, hosting.UsageQuery{Days: days})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, map[string]any{"days": out})
}

// denials is GET /api/v1/agents/{id}/hosting/egress: the hosts the
// egress proxy refused the agent's slots.
func (h *Handler) denials(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	out, err := h.queries.Denials(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, map[string]any{"denials": out})
}

// startLogin is POST …/slots/{slot}/login: starts the slot's sign-in (or
// answers the one going); poll GET for its URL.
func (h *Handler) startLogin(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	out, err := h.commands.StartLogin(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, out)
}

func (h *Handler) login(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Login(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type codeRequest struct {
	Code string `json:"code"`
}

// loginCode is POST …/slots/{slot}/login/code {code}: the code the
// sign-in page showed, for the waiting sign-in.
func (h *Handler) loginCode(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	var req codeRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := h.commands.SubmitLoginCode(c.Context(), p.Organization, hosting.LoginCode{Agent: in.Agent, Index: in.Index, Code: req.Code})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusAccepted, out)
}

// account is GET …/slots/{slot}/account: how the slot's harness is
// signed in (asked of the harness).
func (h *Handler) account(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Account(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}
