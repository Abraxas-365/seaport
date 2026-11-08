package dock

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the dock domain publishes (see internal/event).
const (
	// EventUpdated: a dock was created or changed (status, name, address,
	// a new pairing QR). Data: Dock (with qr_code while linking).
	EventUpdated = "dock.updated"
	// EventDeleted: a dock and everything under it is gone. Data: Deleted.
	EventDeleted = "dock.deleted"
)

// Deleted is the payload of EventDeleted.
type Deleted struct {
	ID identity.DockID `json:"id"`
}
