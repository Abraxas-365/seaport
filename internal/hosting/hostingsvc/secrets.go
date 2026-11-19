package hostingsvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// reveal opens the named secrets of the agent for its slot (repo
// credentials, model keys: agent.Kits.Reveal, guarded by
// TestOnlyTheCrewEquips). A name it doesn't have is left out.
func (s *Service) reveal(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	if s.slots.cfg.Kits == nil {
		return nil, errx.Internal("no secrets to open the agent's credentials")
	}
	return s.slots.cfg.Kits.Reveal(ctx, org, agentID, names)
}
