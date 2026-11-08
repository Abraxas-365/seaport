package conversation

// Events the conversation domain publishes (see internal/event).
const (
	// EventUpdated: a conversation was created or changed (status, access,
	// title, last activity). Data: Conversation.
	EventUpdated = "conversation.updated"
	// EventCustoms: a dock's customs (policy or rules) changed. Data:
	// CustomsEvent.
	EventCustoms = "customs.updated"
)

// CustomsEvent is the payload of EventCustoms.
type CustomsEvent struct {
	Customs
}
