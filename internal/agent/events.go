package agent

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the agent domain publishes (see internal/event).
const (
	// EventUpdated: an agent was created, changed, assigned to or taken
	// off a dock. Data: Agent (with its docks). Making it a dock's default
	// demotes another agent, which gets no event of its own: clients
	// refetch the list.
	EventUpdated = "agent.updated"
	// EventDeleted: an agent left the library. Data: Deleted.
	EventDeleted = "agent.deleted"
)

// Deleted is the payload of EventDeleted.
type Deleted struct {
	ID identity.AgentID `json:"id"`
}
