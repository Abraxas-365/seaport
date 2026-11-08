package crew

import (
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Connection is a live agent connection as every process sees it
// (Presence): opened before the WebSocket handshake, ready after hello.
type Connection struct {
	Session string
	// Process is the port process holding it.
	Process string
	// Credential is the crew token id it connected with ("" for the static
	// token and open mode).
	Credential  string
	Remote      string
	ConnectedAt time.Time
}

// Reaped is a connection whose process stopped touching it (it died): the
// ships it claimed go back to the queue (ship.Commands.ReleaseSession).
type Reaped struct {
	Organization identity.OrganizationID
	Session      string
}

// SignalKind says what a Signal asks every process to do.
type SignalKind string

const (
	// SignalWake: ships are waiting in the organization; claim.
	SignalWake SignalKind = "wake"
	// SignalRevoke: close the organization's connections of Token.
	SignalRevoke SignalKind = "revoke"
)

// Signal crosses processes (Presence.Send/Listen).
type Signal struct {
	Kind         SignalKind              `json:"k"`
	Organization identity.OrganizationID `json:"o"`
	Token        identity.CrewTokenID    `json:"t,omitzero"`
	// From is the sending process (it acted locally already).
	From string `json:"p"`
}
