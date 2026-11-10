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
	// SaveProfile stores the next profile version and makes it current
	// (Conflict when input.Base is not the current one). Its connections
	// are told to pick it up.
	SaveProfile(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input SaveProfile) (Profile, error)
	// SetSecret creates or replaces a secret (sealed; never read back).
	SetSecret(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input SetSecret) (Secret, error)
	// DeleteSecret removes one (Conflict while the current profile uses it).
	DeleteSecret(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, name string) error
}

type Queries interface {
	// Profile is a version of the agent's profile (0: the current one; an
	// agent with none has an empty version 0).
	Profile(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, version int) (Profile, error)
	Versions(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Version, error)
	Secrets(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Secret, error)
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
	// ProfileChanged tells the agent's live connections, in every
	// process, that its kit changed (profile or secrets).
	ProfileChanged(organization identity.OrganizationID, agent identity.AgentID)
}

// Kits hands an agent's connections what they run with, secrets included:
// only the crew side (the agent's own credential) may call it
// (TestOnlyTheCrewEquips).
type Kits interface {
	// Equip is the agent's current kit (Kit.Revision names it).
	Equip(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (Kit, error)
	// Reveal opens the named secrets of the agent (name → value; a name
	// it doesn't have is missing from the answer): a hosted slot's repo
	// credentials (hostingsvc.OpenWorkspace; TestOnlyTheCrewEquips).
	Reveal(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, names []string) (map[string]string, error)
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

	// SaveProfile inserts version input.Base+1 and points the agent at it,
	// atomically; Conflict when the current version is not input.Base, or
	// when a ${NAME} the profile uses is not a secret of the agent.
	SaveProfile(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input SaveProfile) (Profile, error)
	Profile(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, version int) (Profile, error)
	Versions(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Version, error)
	// SetSecret seals and upserts a secret, at most limit per agent.
	SetSecret(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input SetSecret, limit int) (Secret, error)
	// DeleteSecret removes one; Conflict while the current profile uses it.
	DeleteSecret(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, name string) error
	Secrets(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Secret, error)
	// OpenSecrets are the agent's secrets with their values.
	OpenSecrets(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]SecretValue, error)
}
