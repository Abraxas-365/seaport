package dock

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the dock domain publishes (see internal/event).
const (
	// EventUpdated: a dock was created or changed (status, name, address,
	// a new pairing QR). Data: Dock, never with its qr_code (events are
	// stored): a linking dock's QR comes from GET /api/v1/docks/{id}.
	EventUpdated = "dock.updated"
	// EventDeleted: a dock and everything under it is gone. Data: Deleted.
	EventDeleted = "dock.deleted"
)

// Deleted is the payload of EventDeleted.
type Deleted struct {
	ID identity.DockID `json:"id"`
}
