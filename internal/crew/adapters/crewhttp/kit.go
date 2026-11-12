package crewhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
)

// Kit serves GET /crew/profile (behind crew auth): the caller's agent's
// kit, secrets included. It is the only way secrets leave the port, and
// only to the agent's own credential (or the local owner in open mode,
// ?agent=<name>).
type Kit struct {
	Kits   agent.Kits
	Agents agent.Queries
}

func (k Kit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stdhttp.Handle(func(w http.ResponseWriter, r *http.Request) error {
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			return errx.Unauthorized("missing crew principal")
		}
		id, _, err := crew.ResolveAgent(r.Context(), k.Agents,
			crew.Caller{Organization: p.Organization, Agent: p.Agent, AgentName: p.AgentName}, r.URL.Query().Get("agent"))
		if err != nil {
			return err
		}
		kit, err := k.Kits.Equip(r.Context(), p.Organization, id)
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "no-store")
		return stdhttp.JSON(w, http.StatusOK, kit)
	}).ServeHTTP(w, r)
}
