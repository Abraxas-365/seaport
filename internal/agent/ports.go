package agent

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every method takes the organization right after ctx; another
// organization's agent or dock is NotFound (TestTenantFirstPorts).
type Commands interface {
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Agent, error)
	Update(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input Update) (Agent, error)
	// Delete removes the agent from the library: its crew tokens are
	// revoked (live connections closed), it leaves every dock, and the
	// ships waiting for it are held. Tokens and ships keep naming it.
	Delete(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) error
	// Assign puts the agent on a dock (idempotent; sets Default).
	Assign(ctx context.Context, organization identity.OrganizationID, input Assign) (Agent, error)
	// Unassign takes it off a dock; the ships of that dock waiting for it
	// are held.
	Unassign(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, agent identity.AgentID) (Agent, error)
}

type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (Agent, error)
	// FindByName finds an agent by its name (NotFound if none).
	FindByName(ctx context.Context, organization identity.OrganizationID, name string) (Agent, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Agent], error)
	// Team is the agents assigned to a dock (Route's input).
	Team(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]Member, error)
}

// Revoker closes every live connection working as an agent, in every
// process (the crew hub): a deleted agent's, whatever their credential.
type Revoker interface {
	DisconnectAgent(organization identity.OrganizationID, agent identity.AgentID)
}

type Repository interface {
	// Create stores an agent unless the organization already has limit;
	// count and insert are atomic (errx.QuotaExceeded when full).
	Create(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input Create, limit int) (Agent, error)
	Find(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (Agent, error)
	FindByName(ctx context.Context, organization identity.OrganizationID, name string) (Agent, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Agent], error)
	Update(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input Update) (Agent, error)
	// Delete marks the agent deleted, drops its assignments, revokes its
	// crew tokens and holds its waiting ships, in one transaction.
	Delete(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) error
	Assign(ctx context.Context, organization identity.OrganizationID, input Assign) error
	// Unassign drops the assignment and holds the dock's ships waiting for
	// the agent, in one transaction (NotFound if it was not assigned).
	Unassign(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, agent identity.AgentID) error
	Team(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]Member, error)
}
