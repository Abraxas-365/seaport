package dock

import (
	"context"

	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
)

// Every tenant method takes the organization right after ctx; a dock of
// another organization is NotFound. The methods without one are
// system-wide (gateway callbacks about a dock the gateway itself runs,
// process start), see TestTenantFirstPorts.
type Commands interface {
	// Create stores a dock and starts linking it (WhatsApp: QR pairing).
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Dock, error)
	// Relink restarts pairing of a dock that is not linked.
	Relink(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (Dock, error)
	Update(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, input Update) (Dock, error)
	// Unlink logs the dock out of its channel (WhatsApp: removes the linked
	// device) and keeps its history; Relink pairs it again.
	Unlink(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (Dock, error)
	// Delete unlinks the dock and deletes it with its conversations,
	// ships and customs.
	Delete(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) error
	// PairingChanged announces a new (or expired) pairing code (system:
	// called by gateways).
	PairingChanged(ctx context.Context, dock identity.DockID)
	// SetLinked records a successful pairing (system: called by gateways).
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
	// SetStatus records a link state change (system: called by gateways).
	SetStatus(ctx context.Context, dock identity.DockID, status Status) error
	// RestoreAll reconnects every linked dock (system: process start).
	RestoreAll(ctx context.Context) error
}

type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (Dock, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Dock], error)
}

// Messenger sends messages through a dock; other domains use it.
type Messenger interface {
	// NewMessageID returns the channel message id the next Send will use,
	// so callers can store it first and recognize the echo.
	NewMessageID(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (string, error)
	Send(ctx context.Context, organization identity.OrganizationID, msg Outgoing) error
}

type Repository interface {
	Create(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, input Create, status Status) (Dock, error)
	Find(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (Dock, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Dock], error)
	Update(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, input Update) (Dock, error)
	Delete(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) error
	// Get finds a dock in any organization (system: gateway callbacks).
	Get(ctx context.Context, dock identity.DockID) (Dock, error)
	// ListByStatus spans every organization (system: process start).
	ListByStatus(ctx context.Context, status Status) ([]Dock, error)
	SetStatus(ctx context.Context, dock identity.DockID, status Status) error
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
}

// Gateway is the I/O of one channel (infrastructure, one per Channel).
// Asynchronous link results are reported through Commands.SetLinked/SetStatus.
type Gateway interface {
	Channel() Channel
	// Link starts pairing and returns the dock's status right after
	// (linking, or linked for channels that need no pairing).
	Link(ctx context.Context, dock Dock) (Status, error)
	// QR returns the current pairing code of a linking dock, if any.
	QR(dock identity.DockID) string
	// Restore reconnects a linked dock.
	Restore(ctx context.Context, dock Dock) error
	// Unlink ends the dock's link (logout, pairing) and forgets its
	// credentials. Unlinking a dock that is not connected is not an error.
	Unlink(ctx context.Context, dock Dock) error
	NewMessageID(dock identity.DockID) (string, error)
	Send(ctx context.Context, msg Outgoing) error
}
