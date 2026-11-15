package vault

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every method takes the organization right after ctx; a vault, agent, dock
// or conversation of another organization is NotFound.
type Commands interface {
	// Create makes a vault, at most the configured number per organization.
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Vault, error)
	Update(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, input Update) (Vault, error)
	// Delete removes the vault with its entries, versions and grants.
	Delete(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) error
	// GrantAgent sets (or changes) the agent's mode on the vault.
	GrantAgent(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, agent identity.AgentID, mode Mode) (Grants, error)
	RevokeAgent(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, agent identity.AgentID) error
	GrantDock(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, dock identity.DockID, mode Mode) (Grants, error)
	RevokeDock(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, dock identity.DockID) error
	// SaveEntry creates (Base 0) or edits an entry as a new version; used
	// by people (the admin API). Agents write through Writer.
	SaveEntry(ctx context.Context, organization identity.OrganizationID, input SaveEntry) (Entry, error)
	// DeleteEntry removes an entry (its versions stay until the vault goes).
	DeleteEntry(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID) (Entry, error)
	// Revert saves an older version's body as the entry's newest.
	Revert(ctx context.Context, organization identity.OrganizationID, input Revert) (Entry, error)
}

// Writer is what an agent may do to entries: the access check (a write
// grant on the vault, through the agent or through the dock of the named
// conversation) lives in the implementation, never in the adapter that
// serves the agent (TestVaultWritesCheckAccess). Provenance is filled by
// the implementation from the agent and the conversation.
type Writer interface {
	// WriteAs saves an entry as the agent; conversation (nil for none) is
	// the one it is working, which brings its dock's grants.
	WriteAs(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, conversation *identity.ConversationID, input SaveEntry) (Entry, error)
	DeleteAs(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, conversation *identity.ConversationID, vault identity.VaultID, name string) (Entry, error)
}

type Queries interface {
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Vault], error)
	Find(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) (Vault, error)
	Grants(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) (Grants, error)
	// Entries lists entries newest first, without their bodies.
	Entries(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, page query.Pagination) (query.Paginated[Summary], error)
	Entry(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID) (Entry, error)
	EntryByName(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, name string) (Entry, error)
	// Index lists up to limit entries of the vault newest first (≤ MaxIndex),
	// without their bodies: what the agent's `vaults` tool shows.
	Index(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, limit int) ([]Summary, error)
	// Versions lists an entry's versions newest first (also of a deleted
	// entry, until the vault goes).
	Versions(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID) ([]Version, error)
	// Search finds entries of the given vaults by words (full text; a query
	// with no words is matched as a substring), best first, at most limit
	// (≤ 0 or above MaxSearchHits: MaxSearchHits).
	Search(ctx context.Context, organization identity.OrganizationID, vaults []identity.VaultID, q string, limit int) ([]Hit, error)
	// AccessFor is the vaults an agent can use now: its own grants plus,
	// when conversation names one of a dock the agent works, that dock's,
	// the widest mode per vault. A conversation of another dock (or an
	// unknown one) adds nothing: no error.
	AccessFor(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, conversation *identity.ConversationID) ([]Access, error)
	// AnyFor reports whether the agent has any vault through itself or
	// through any dock it works (cheap: decides whether the tools exist).
	AnyFor(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (bool, error)
	// OfAgent and OfDock list the grants of an agent or a dock.
	OfAgent(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Access, error)
	OfDock(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]Access, error)
}

type Repository interface {
	// Create inserts the vault, counting the organization's under a lock
	// (limit ≤ 0: unlimited); a taken name is Conflict.
	Create(ctx context.Context, organization identity.OrganizationID, vault Vault, limit int) (Vault, error)
	Find(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) (Vault, error)
	FindMany(ctx context.Context, organization identity.OrganizationID, vaults []identity.VaultID) ([]Vault, error)
	List(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Vault], error)
	Update(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, input Update) (Vault, error)
	Delete(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) error
	SetAgentGrant(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, agent identity.AgentID, mode Mode) error
	DeleteAgentGrant(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, agent identity.AgentID) error
	SetDockGrant(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, dock identity.DockID, mode Mode) error
	DeleteDockGrant(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, dock identity.DockID) error
	Grants(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID) (Grants, error)
	AgentGrants(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Grant, error)
	DockGrants(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) ([]Grant, error)
	AnyForAgent(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (bool, error)
	// Save inserts or edits the entry and appends its version in one
	// transaction under the vault row's lock (at most limit entries per
	// vault, 0: unlimited): Base 0 on an existing name, or a Base that is
	// not the current version, is Conflict.
	Save(ctx context.Context, organization identity.OrganizationID, input SaveEntry, limit int) (Entry, error)
	// DropEntry deletes the entry row and answers it (versions stay).
	DropEntry(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID) (Entry, error)
	Entry(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID) (Entry, error)
	EntryByName(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, name string) (Entry, error)
	Entries(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, page query.Pagination) (query.Paginated[Summary], error)
	Versions(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID, limit int) ([]Version, error)
	Version(ctx context.Context, organization identity.OrganizationID, vault identity.VaultID, entry identity.EntryID, version int) (Version, error)
	Search(ctx context.Context, organization identity.OrganizationID, vaults []identity.VaultID, q string, limit int) ([]Hit, error)
}
