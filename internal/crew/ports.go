package crew

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every tenant method takes the organization right after ctx; another
// organization's token is NotFound. Authenticate, FindByHash and Touch
// are system-wide: a token value names its organization
// (TestTenantFirstPorts).
type TokenCommands interface {
	// Create issues a token; its value is only in the result.
	Create(ctx context.Context, organization identity.OrganizationID, input CreateToken) (Issued, error)
	// Revoke ends a token and closes its live connections.
	Revoke(ctx context.Context, organization identity.OrganizationID, token identity.CrewTokenID) error
	// Authenticate resolves a token value (system: the value names its
	// organization). Unknown, revoked and expired values are Unauthorized.
	Authenticate(ctx context.Context, value string) (Token, error)
}

type TokenQueries interface {
	// List returns the organization's active (unrevoked) tokens.
	List(ctx context.Context, organization identity.OrganizationID, filter TokenFilter, page query.Pagination) (query.Paginated[Token], error)
}

type TokenRepository interface {
	// Create stores a token unless the organization already has limit active
	// (unrevoked, unexpired) ones: count and insert are atomic, so
	// concurrent creations can't overshoot (errx.QuotaExceeded when full).
	// The agent must exist (NotFound otherwise, or once deleted).
	Create(ctx context.Context, organization identity.OrganizationID, token identity.CrewTokenID, input CreateToken, hash, hint string, limit int) (Token, error)
	List(ctx context.Context, organization identity.OrganizationID, filter TokenFilter, page query.Pagination) (query.Paginated[Token], error)
	// Revoke marks the token revoked (NotFound if it is not active).
	Revoke(ctx context.Context, organization identity.OrganizationID, token identity.CrewTokenID) error
	// FindByHash finds an unrevoked token in any organization (system:
	// authentication).
	FindByHash(ctx context.Context, hash string) (Token, error)
	// Touch records a use (system: keyed by the authenticated token).
	Touch(ctx context.Context, token identity.CrewTokenID, at time.Time) error
}

// Disconnector closes the live connections opened with a token (the hub).
type Disconnector interface {
	Disconnect(organization identity.OrganizationID, token identity.CrewTokenID)
}

// Presence is the live connections of every process (crewpg): status,
// quota and the reaping of a dead process's connections, plus the signals
// processes send each other (wake, revoke).
type Presence interface {
	// Open records a connection before its handshake unless the
	// organization already has limit open ones in any process (limit ≤ 0:
	// unlimited; errx.QuotaExceeded). Count and insert are atomic.
	Open(ctx context.Context, organization identity.OrganizationID, conn Connection, limit int) error
	// Ready records the agent's hello (Status lists only ready ones).
	Ready(ctx context.Context, organization identity.OrganizationID, session string, agent Agent) error
	// SetDeliveries records what the connection is working on (status).
	SetDeliveries(ctx context.Context, organization identity.OrganizationID, session string, deliveries []AgentDelivery) error
	// Close forgets a connection.
	Close(ctx context.Context, organization identity.OrganizationID, session string) error
	// Status lists the organization's ready connections of every process.
	Status(ctx context.Context, organization identity.OrganizationID) (Status, error)
	// Touch marks a process's connections alive (system: keyed by the
	// process) and returns those it must close: reaped meanwhile, opened
	// with a token since revoked or expired, or working as an agent since
	// deleted.
	Touch(ctx context.Context, process string, sessions []string) (close []string, err error)
	// Reap deletes connections not touched for stale (by the database's
	// clock, the one Touch writes), in every organization (system: a dead
	// process's connections), and returns them.
	Reap(ctx context.Context, stale time.Duration) ([]Reaped, error)
	// Signal tells every process (system: the signal names its organization).
	Signal(ctx context.Context, signal Signal) error
	// Listen calls fn for every signal of any process until ctx ends, and
	// connected after each (re)connect: signals sent meanwhile are lost
	// (system: signals of every organization).
	Listen(ctx context.Context, connected func(), fn func(Signal))
}
