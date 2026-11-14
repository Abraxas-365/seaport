package crewhttp

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Approvals serves POST /crew/approvals and GET /crew/approvals/:id
// (behind crew auth): the MCP ask_approval and approval tools over plain
// HTTP, for `seaport ask`. Agent tokens only: the question is the agent's.
type Approvals struct {
	Ships interface {
		ship.Commands
		ship.Queries
	}
}

func agentOf(c fiber.Ctx) (httpx.Principal, error) {
	p, ok := httpx.PrincipalFrom(c.Context())
	if !ok {
		return p, errx.Unauthorized("missing crew principal")
	}
	if p.Agent.IsZero() {
		return p, errx.Forbidden("approvals need an agent's crew token")
	}
	return p, nil
}

// Ask is POST /crew/approvals.
func (h Approvals) Ask(c fiber.Ctx) error {
	p, err := agentOf(c)
	if err != nil {
		return err
	}
	var in crew.AskRequest
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	a, err := h.Ships.AskApproval(c.Context(), p.Organization, in.Ask(p.Agent))
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusCreated, a)
}

// Get is GET /crew/approvals/:id[?wait_seconds=N].
func (h Approvals) Get(c fiber.Ctx) error {
	p, err := agentOf(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseApprovalID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	var wait time.Duration
	if raw := c.Query("wait_seconds"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return errx.InvalidField("wait_seconds", "wait_seconds must be a whole number ≥ 0")
		}
		wait = time.Duration(n) * time.Second
	}
	// An agent reads only its own approvals; another's is not found.
	cur, err := h.Ships.FindApproval(c.Context(), p.Organization, id)
	if err != nil {
		return err
	}
	if cur.Agent != p.Agent {
		return errx.NotFound("approval not found")
	}
	a, err := h.Ships.AwaitApproval(c.Context(), p.Organization, id, wait)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, a)
}
