package agent

import (
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Events the agent domain publishes (see internal/event).
const (
	// EventUpdated: an agent was created, changed, assigned to or taken
	// off a dock. Data: Agent (with its docks). Making it a dock's default
	// demotes another agent, which gets no event of its own: clients
	// refetch the list.
	EventUpdated = "agent.updated"
	// EventDeleted: an agent left the library. Data: Deleted.
	EventDeleted = "agent.deleted"
	// EventTakeover: a takeover was asked, ended by a person or expired.
	// Data: TakeoverEvent. An audit trail of who asked a person into the
	// agent's browser and who answered: the agent's takeovers list (in
	// EventUpdated) is the current state only, and clears as they end.
	EventTakeover = "agent.takeover"
)

// Deleted is the payload of EventDeleted.
type Deleted struct {
	ID identity.AgentID `json:"id"`
}

// TakeoverState is what happened to a takeover (TakeoverEvent.State): asked,
// or one of the ends (the TakeoverEnd values, through TakeoverEnd.State).
type TakeoverState string

const (
	// TakeoverStateAsked: a takeover for a host with none open, or whose
	// open one asked for something else (login, then 2fa); asking again
	// for the same thing updates it and publishes nothing.
	TakeoverStateAsked TakeoverState = "asked"
	// TakeoverStateDone: a person said it is done (Listo, the DELETE);
	// EndedBy says who.
	TakeoverStateDone TakeoverState = "done"
	// TakeoverStateExpired: nobody came within TakeoverTTL.
	TakeoverStateExpired TakeoverState = "expired"
)

// State is the end as an event state.
func (e TakeoverEnd) State() TakeoverState { return TakeoverState(e) }

// TakeoverEvent is the payload of EventTakeover: the takeover as it was
// (host, kind, who asked, the page's path, the conversation it was about)
// and what happened to it. It carries no page content and no credential.
type TakeoverEvent struct {
	Agent identity.AgentID `json:"agent_id"`
	Host  string           `json:"host"`
	Kind  TakeoverKind     `json:"kind"`
	// By is who asked: the agent or the detector (Takeover.Asker).
	By   TakeoverBy `json:"by"`
	Path string     `json:"path,omitempty"`
	// Conversation is the one the takeover was about, when it said so.
	Conversation *identity.ConversationID `json:"conversation_id,omitempty"`
	// AskedAt is when it was asked (the takeover's At).
	AskedAt time.Time     `json:"asked_at"`
	State   TakeoverState `json:"state"`
	// EndedBy names the person who ended it (TakeoverStateDone only).
	EndedBy string `json:"ended_by,omitempty"`
}

// Event builds the EventTakeover payload of the takeover in state.
func (t Takeover) Event(agent identity.AgentID, state TakeoverState, endedBy string) TakeoverEvent {
	return TakeoverEvent{Agent: agent, Host: t.Host, Kind: t.Kind, By: t.Asker(), Path: t.Path,
		Conversation: t.Conversation, AskedAt: t.At, State: state, EndedBy: endedBy}
}
