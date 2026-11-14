package schedule

// Events the schedule domain publishes (see internal/event). The data is
// always the Schedule (a deleted one as it was).
const (
	EventCreated = "schedule.created"
	EventUpdated = "schedule.updated"
	EventDeleted = "schedule.deleted"
	// EventFired: the runner (or a manual run) posted the schedule's
	// message; LastAt and LastShip say when and which ship.
	EventFired = "schedule.fired"
)
