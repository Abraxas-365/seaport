// Package hostinghttp is the admin API of hosted agents: hosting, builds,
// slots, homes, sign-in and the browser viewer. What an agent runs (its
// harness and machine) is its config (agenthttp, /agents/{id}/config).
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

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Post("/api/v1/agents/:id/build", httpx.RequireFunc(permission.HostingWrite, h.startBuild))
	r.Get("/api/v1/agents/:id/build", httpx.RequireFunc(permission.HostingRead, h.build))
	// Homes are what an agent config names; uploading one is part of
	// writing a config (agents:write).
	r.Post("/api/v1/homes", httpx.RequireFunc(permission.AgentsWrite, h.uploadHome))
	r.Get("/api/v1/homes/rules", httpx.RequireFunc(permission.AgentsRead, h.homeRules))
	r.Get("/api/v1/homes/:digest", httpx.RequireFunc(permission.AgentsRead, h.home))
	r.Get("/api/v1/homes/:digest/archive", httpx.RequireFunc(permission.AgentsRead, h.homeArchive))
	r.Get("/api/v1/homes/:digest/files/*", httpx.RequireFunc(permission.AgentsRead, h.homeFile))
	r.Get("/api/v1/hosting", httpx.RequireFunc(permission.HostingRead, h.hostedAgents))
	r.Get("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.HostingRead, h.hosted))
	r.Put("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.HostingWrite, h.host))
	r.Delete("/api/v1/agents/:id/hosting", httpx.RequireFunc(permission.HostingWrite, h.unhost))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/start", httpx.RequireFunc(permission.HostingWrite, h.startSlot))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/stop", httpx.RequireFunc(permission.HostingWrite, h.stopSlot))
	r.Get("/api/v1/agents/:id/hosting/egress", httpx.RequireFunc(permission.HostingRead, h.denials))
	// Logs: whatever the agent printed (a tool may echo an env secret), so
	// only who may change where it runs reads them.
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/logs", httpx.RequireFunc(permission.HostingWrite, h.slotLog))
	r.Get("/api/v1/agents/:id/usage", httpx.RequireFunc(permission.AgentsRead, h.usage))
	// Subscription sign-in (experimental): signing a slot in decides whose
	// account it spends, so it is hosting:write's, like starting it.
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/login", httpx.RequireFunc(permission.HostingWrite, h.startLogin))
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/login", httpx.RequireFunc(permission.HostingWrite, h.login))
	r.Post("/api/v1/agents/:id/hosting/slots/:slot/login/code", httpx.RequireFunc(permission.HostingWrite, h.loginCode))
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/account", httpx.RequireFunc(permission.HostingRead, h.account))
	// The browser viewer (a WebSocket): whoever drives a slot's browser
	// signs in to sites as the agent, so it has a permission of its own.
	r.Get("/api/v1/agents/:id/hosting/slots/:slot/browser", httpx.RequireFunc(permission.BrowserUse, h.browser))
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

type buildRequest struct {
	Force bool `json:"force"`
}

// startBuild answers 202 with the build of the agent's current config
// (in progress, or as it is when already built or building); the body is
// optional.
func (h *Handler) startBuild(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var req buildRequest
	if c.Request().Header.ContentLength() != 0 {
		if err := httpx.DecodeJSON(c, &req); err != nil {
			return err
		}
	}
	b, err := h.commands.StartBuild(c.Context(), org, id, hosting.BuildAgent{Force: req.Force})
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
	id, err := identity.ParseAgentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	b, err := h.queries.Build(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, b)
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
	Slots int `json:"slots"`
}

// host is PUT /api/v1/agents/{id}/hosting {slots}: hosts the agent on its
// config's machine, or changes its slot count. The body is optional (one
// slot).
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
	if c.Request().Header.ContentLength() != 0 {
		if err := httpx.DecodeJSON(c, &req); err != nil {
			return err
		}
	}
	out, err := h.commands.Host(c.Context(), p.Organization, id, hosting.Host{Slots: req.Slots, By: by(p)})
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
