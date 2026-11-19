package hosting

// Events the hosting domain publishes (see internal/event). Payloads carry
// no secrets (machines name secrets as ${NAME} only).
const (
	// EventSlotsUpdated: an agent was hosted, unhosted or one of its slots
	// changed state. Data: Hosted (no slots when it is no longer hosted).
	EventSlotsUpdated = "slot.updated"
	// EventAgentBuild: a build of an agent's image started, finished or
	// failed. Data: Build (without its log).
	EventAgentBuild = "agent.build"
	// EventSlotBrowser: someone opened or closed a slot's browser viewer.
	// Data: BrowserEvent.
	EventSlotBrowser = "slot.browser"
)
