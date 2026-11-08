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
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Token], error)
}

type TokenRepository interface {
	Create(ctx context.Context, organization identity.OrganizationID, token identity.CrewTokenID, input CreateToken, hash, hint string) (Token, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Token], error)
	// CountActive counts unrevoked, unexpired tokens.
	CountActive(ctx context.Context, organization identity.OrganizationID) (int, error)
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
