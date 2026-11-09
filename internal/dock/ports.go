package dock

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every tenant method takes the organization right after ctx; a dock of
// another organization is NotFound. The methods without one are
// system-wide (gateway callbacks about a dock the gateway itself runs,
// ownership across processes), see TestTenantFirstPorts.
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
	// Create stores a dock unless the organization already has limit docks
	// (limit ≤ 0: unlimited); count and insert are atomic
	// (errx.QuotaExceeded when full).
	Create(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, input Create, status Status, limit int) (Dock, error)
	Find(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (Dock, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Dock], error)
	Update(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, input Update) (Dock, error)
	Delete(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) error
	// Get finds a dock in any organization (system: gateway callbacks,
	// owner reconciliation).
	Get(ctx context.Context, dock identity.DockID) (Dock, error)
	SetStatus(ctx context.Context, dock identity.DockID, status Status) error
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
}

// Owners leases the docks of exclusive channels (Gateway.Exclusive) to
// processes: a dock is run by the process holding its live lease.
type Owners interface {
	// Acquire returns the dock's owner: process itself when it held the
	// lease (renewed) or the dock had no live owner (taken for ttl), else
	// the live owner.
	Acquire(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, process string, ttl time.Duration) (owner string, err error)
	// Release gives up process's lease of the dock (none held: no error).
	Release(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, process string) error
	// Renew extends process's live leases of these docks and returns the
	// ones it still holds; a lease that already ran out is not renewed
	// (another process may hold it). Leases of other docks run out.
	Renew(ctx context.Context, process string, docks []identity.DockID, ttl time.Duration) ([]identity.DockID, error)
	// Claim takes for process up to limit docks of these channels and
	// statuses that have no live owner, of every organization.
	Claim(ctx context.Context, process string, ttl time.Duration, channels []Channel, statuses []Status, limit int) ([]Dock, error)
	// ReleaseAll gives up every lease of process (it is stopping).
	ReleaseAll(ctx context.Context, process string) error
}

// Relay carries work to the process owning a dock.
type Relay interface {
	// Call hands request to process and waits for its reply until ctx
	// ends; Moved when process lost the dock before taking the request.
	Call(ctx context.Context, organization identity.OrganizationID, process string, request Request) (Reply, error)
	// Serve runs handle for every request addressed to process (of every
	// organization) until ctx ends.
	Serve(ctx context.Context, process string, handle func(ctx context.Context, organization identity.OrganizationID, request Request) Reply)
}

// Gateway is the I/O of one channel (infrastructure, one per Channel).
// Asynchronous link results are reported through Commands.SetLinked/SetStatus.
type Gateway interface {
	Channel() Channel
	// Exclusive: a dock keeps one live connection, so one process (its
	// owner) runs Link, Restore, Unlink and Send for it.
	Exclusive() bool
	// Link starts pairing and returns the dock's status right after
	// (linking, or linked for channels that need no pairing).
	Link(ctx context.Context, dock Dock) (Status, error)
	// QR returns the current pairing code of a linking dock, if any (any
	// process can read it).
	QR(ctx context.Context, dock identity.DockID) (string, error)
	// Restore reconnects a linked dock.
	Restore(ctx context.Context, dock Dock) error
	// Running: this process holds the dock's connection (or pairing).
	Running(dock identity.DockID) bool
	// RunningDocks are the docks Running in this process.
	RunningDocks() []identity.DockID
	// Stop drops this process's connection of the dock and keeps its
	// credentials (the dock moves to another process).
	Stop(dock identity.DockID)
	// Unlink ends the dock's link (logout, pairing) and forgets its
	// credentials. Unlinking a dock that is not connected is not an error.
	Unlink(ctx context.Context, dock Dock) error
	// NewMessageID needs no connection: any process may call it.
	NewMessageID(dock identity.DockID) (string, error)
	Send(ctx context.Context, msg Outgoing) error
}
