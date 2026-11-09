package crew

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Caller is the crew credential of a request: its organization and, with
// a crew token, the token's agent (zero in open mode).
type Caller struct {
	Organization identity.OrganizationID
	Agent        identity.AgentID
	AgentName    string
}

// ResolveAgent is the agent a crew request or connection works as: the
// token's (name, when given, must be it), or in open mode the agent name
// names (required).
func ResolveAgent(ctx context.Context, agents agent.Queries, c Caller, name string) (identity.AgentID, string, error) {
	if !c.Agent.IsZero() {
		if name != "" && name != c.AgentName {
			return identity.AgentID{}, "", errx.Forbidden("this crew token belongs to agent " + c.AgentName + ", not " + name)
		}
		return c.Agent, c.AgentName, nil
	}
	if name == "" {
		return identity.AgentID{}, "", errx.Validation("name the agent to work as: seaport connect -agent <name>")
	}
	a, err := agents.FindByName(ctx, c.Organization, name)
	if err != nil {
		return identity.AgentID{}, "", err
	}
	return a.ID, a.Name, nil
}
