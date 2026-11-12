package crewhttp

import (
	"net/http"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

// Kit serves GET /crew/profile (behind crew auth): the caller's agent's
// kit, secrets included. It is the only way secrets leave the port, and
// only to the agent's own credential (or the local owner in open mode,
// ?agent=<name>).
type Kit struct {
	Kits   agent.Kits
	Agents agent.Queries
}

// Handle is the route handler.
func (k Kit) Handle(c fiber.Ctx) error {
	p, ok := httpx.PrincipalFrom(c.Context())
	if !ok {
		return errx.Unauthorized("missing crew principal")
	}
	id, _, err := crew.ResolveAgent(c.Context(), k.Agents,
		crew.Caller{Organization: p.Organization, Agent: p.Agent, AgentName: p.AgentName}, c.Query("agent"))
	if err != nil {
		return err
	}
	kit, err := k.Kits.Equip(c.Context(), p.Organization, id)
	if err != nil {
		return err
	}
	c.Set("Cache-Control", "no-store")
	return httpx.JSON(c, http.StatusOK, kit)
}
