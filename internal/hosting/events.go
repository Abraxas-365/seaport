package hosting

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the hosting domain publishes (see internal/event). Payloads carry
// no secrets (specs name secrets as ${NAME} only).
const (
	// EventEnvironmentUpdated: an environment was created or got a new
	// version. Data: Environment.
	EventEnvironmentUpdated = "environment.updated"
	// EventEnvironmentDeleted: Data: EnvironmentDeleted.
	EventEnvironmentDeleted = "environment.deleted"
	// EventSlotsUpdated: an agent was hosted, moved, unhosted or one of its
	// slots changed state. Data: Hosted (no environment and no slots when
	// it is no longer hosted).
	EventSlotsUpdated = "slot.updated"
)

// EnvironmentDeleted is the payload of EventEnvironmentDeleted.
type EnvironmentDeleted struct {
	ID identity.EnvironmentID `json:"id"`
}
