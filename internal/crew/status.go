package crew

import (
	"time"

	"github.com/Abraxas-365/puerto/internal/identity"
)

// EventChanged: an agent connected or disconnected, or its deliveries
// changed. Data: Status (the whole crew; it is small).
const EventChanged = "crew.changed"

// Status is who is working at the port right now.
type Status struct {
	Agents []Agent `json:"agents"`
}

// Agent is one connected connector (`puerto connect`).
type Agent struct {
	// Session identifies this connection (a reconnect gets a new one).
	Session     string    `json:"session"`
	Name        string    `json:"name"`
	Concurrency int       `json:"concurrency"`
	Steer       bool      `json:"steer"`
	Remote      string    `json:"remote"`
	ConnectedAt time.Time `json:"connected_at"`
	// Deliveries are the conversations it is working on.
	Deliveries []AgentDelivery `json:"deliveries"`
}

// AgentDelivery is a delivery in flight on a connection.
type AgentDelivery struct {
	ID           identity.DeliveryID     `json:"id"`
	Conversation identity.ConversationID `json:"conversation_id"`
	Ships        int                     `json:"ships"`
	Since        time.Time               `json:"since"`
}
