// Package vaultsvc implements the vault ports: validation, the quotas, the
// grant checks (agents and docks through their domains' ports) and the
// access-checked write path of agents.
package vaultsvc

import (
	"context"
	"slices"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
	"github.com/Abraxas-365/seaport/internal/vault"
)

type Service struct {
	repo   vault.Repository
	agents agent.Queries
	docks  dock.Queries
	convs  conversation.Queries
	events event.Publisher
	// max bounds an organization's vaults (≤ 0: unlimited).
	max int
}

func New(repo vault.Repository, agents agent.Queries, docks dock.Queries, convs conversation.Queries, events event.Publisher, max int) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, agents: agents, docks: docks, convs: convs, events: events, max: max}
}

var (
	_ vault.Commands = (*Service)(nil)
	_ vault.Queries  = (*Service)(nil)
	_ vault.Writer   = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input vault.Create) (vault.Vault, error) {
	if err := input.Validate(); err != nil {
		return vault.Vault{}, err
	}
	v, err := s.repo.Create(ctx, org, vault.Vault{ID: identity.NewVaultID(), Name: input.Name,
		Title: text.Clean(input.Title), Description: text.Clean(input.Description)}, s.max)
	if err != nil {
		return vault.Vault{}, errx.Wrap(err, "create vault", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventCreated, v)
	return v, nil
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.VaultID, input vault.Update) (vault.Vault, error) {
	if _, err := s.repo.Find(ctx, org, id); err != nil {
		return vault.Vault{}, errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := input.Validate(); err != nil {
		return vault.Vault{}, err
	}
	if input.Title != nil {
		t := text.Clean(*input.Title)
		input.Title = &t
	}
	if input.Description != nil {
		d := text.Clean(*input.Description)
		input.Description = &d
	}
	v, err := s.repo.Update(ctx, org, id, input)
	if err != nil {
		return vault.Vault{}, errx.Wrap(err, "update vault", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventUpdated, v)
	return v, nil
}

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id identity.VaultID) error {
	v, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := s.repo.Delete(ctx, org, id); err != nil {
		return errx.Wrap(err, "delete vault", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventDeleted, v)
	return nil
}

func (s *Service) GrantAgent(ctx context.Context, org identity.OrganizationID, id identity.VaultID, agentID identity.AgentID, mode vault.Mode) (vault.Grants, error) {
	// The vault first: another organization's is NotFound whatever the input.
	v, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return vault.Grants{}, errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := mode.Validate(); err != nil {
		return vault.Grants{}, err
	}
	if _, err := s.agents.Find(ctx, org, agentID); err != nil {
		return vault.Grants{}, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	if err := s.repo.SetAgentGrant(ctx, org, id, agentID, mode); err != nil {
		return vault.Grants{}, errx.Wrap(err, "grant vault to agent", errx.TypeInternal)
	}
	return s.granted(ctx, org, v)
}

func (s *Service) RevokeAgent(ctx context.Context, org identity.OrganizationID, id identity.VaultID, agentID identity.AgentID) error {
	v, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := s.repo.DeleteAgentGrant(ctx, org, id, agentID); err != nil {
		return errx.Wrap(err, "revoke vault from agent", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventUpdated, v)
	return nil
}

func (s *Service) GrantDock(ctx context.Context, org identity.OrganizationID, id identity.VaultID, dockID identity.DockID, mode vault.Mode) (vault.Grants, error) {
	// The vault first: another organization's is NotFound whatever the input.
	v, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return vault.Grants{}, errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := mode.Validate(); err != nil {
		return vault.Grants{}, err
	}
	if _, err := s.docks.Find(ctx, org, dockID); err != nil {
		return vault.Grants{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if err := s.repo.SetDockGrant(ctx, org, id, dockID, mode); err != nil {
		return vault.Grants{}, errx.Wrap(err, "grant vault to dock", errx.TypeInternal)
	}
	return s.granted(ctx, org, v)
}

func (s *Service) RevokeDock(ctx context.Context, org identity.OrganizationID, id identity.VaultID, dockID identity.DockID) error {
	v, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "find vault", errx.TypeInternal)
	}
	if err := s.repo.DeleteDockGrant(ctx, org, id, dockID); err != nil {
		return errx.Wrap(err, "revoke vault from dock", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventUpdated, v)
	return nil
}

// granted announces a grant change and answers the vault's grants.
func (s *Service) granted(ctx context.Context, org identity.OrganizationID, v vault.Vault) (vault.Grants, error) {
	s.events.Publish(org, vault.EventUpdated, v)
	g, err := s.repo.Grants(ctx, org, v.ID)
	if err != nil {
		return vault.Grants{}, errx.Wrap(err, "list grants", errx.TypeInternal)
	}
	return g, nil
}

// SaveEntry is the people's write (the admin API): whoever may write
// vaults. Agents go through WriteAs.
func (s *Service) SaveEntry(ctx context.Context, org identity.OrganizationID, input vault.SaveEntry) (vault.Entry, error) {
	if err := input.Validate(); err != nil {
		return vault.Entry{}, err
	}
	return s.save(ctx, org, input)
}

func (s *Service) save(ctx context.Context, org identity.OrganizationID, input vault.SaveEntry) (vault.Entry, error) {
	input.Body = text.Clean(input.Body)
	e, err := s.repo.Save(ctx, org, input, vault.MaxEntries)
	if err != nil {
		return vault.Entry{}, errx.Wrap(err, "save entry", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventEntry, change(e, false))
	return e, nil
}

func (s *Service) DeleteEntry(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID) (vault.Entry, error) {
	e, err := s.repo.DropEntry(ctx, org, id, entry)
	if err != nil {
		return vault.Entry{}, errx.Wrap(err, "delete entry", errx.TypeInternal)
	}
	s.events.Publish(org, vault.EventEntry, change(e, true))
	return e, nil
}

func (s *Service) Revert(ctx context.Context, org identity.OrganizationID, input vault.Revert) (vault.Entry, error) {
	if err := input.Validate(); err != nil {
		return vault.Entry{}, err
	}
	cur, err := s.repo.Entry(ctx, org, input.Vault, input.Entry)
	if err != nil {
		return vault.Entry{}, errx.Wrap(err, "find entry", errx.TypeInternal)
	}
	old, err := s.repo.Version(ctx, org, input.Vault, input.Entry, input.Version)
	if err != nil {
		return vault.Entry{}, errx.Wrap(err, "find version", errx.TypeInternal)
	}
	// The old body passes today's rules too (the secret patterns may have
	// grown since it was written).
	if err := vault.ValidBody(old.Body); err != nil {
		return vault.Entry{}, err
	}
	return s.save(ctx, org, vault.SaveEntry{Vault: input.Vault, Name: cur.Name, Body: old.Body, Base: cur.Version, Provenance: input.Provenance})
}

// scope is what an agent may use while it works (optionally) one
// conversation: its grants and where the work happens.
type scope struct {
	agent agent.Agent
	// conversation and dock are set only when the conversation belongs to a
	// dock the agent works.
	conversation *identity.ConversationID
	dock         *identity.DockID
	grants       []vault.Grant
}

func (s *Service) scope(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID) (scope, error) {
	if agentID.IsZero() {
		return scope{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	a, err := s.agents.Find(ctx, org, agentID)
	if err != nil {
		return scope{}, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	own, err := s.repo.AgentGrants(ctx, org, agentID)
	if err != nil {
		return scope{}, errx.Wrap(err, "agent grants", errx.TypeInternal)
	}
	sc := scope{agent: a}
	var viaDock []vault.Grant
	if conv != nil && !conv.IsZero() {
		c, err := s.convs.Find(ctx, org, *conv)
		switch {
		case err == nil:
			if slices.ContainsFunc(a.Docks, func(as agent.Assignment) bool { return as.Dock == c.Dock }) {
				sc.conversation, sc.dock = conv, &c.Dock
				if viaDock, err = s.repo.DockGrants(ctx, org, c.Dock); err != nil {
					return scope{}, errx.Wrap(err, "dock grants", errx.TypeInternal)
				}
			}
		case errx.IsType(err, errx.TypeNotFound):
			// an unknown conversation adds nothing
		default:
			return scope{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
		}
	}
	sc.grants = vault.Resolve(own, viaDock)
	return sc, nil
}

func (s *Service) AccessFor(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID) ([]vault.Access, error) {
	sc, err := s.scope(ctx, org, agentID, conv)
	if err != nil {
		return nil, err
	}
	return s.accessOf(ctx, org, sc.grants)
}

// accessOf attaches the vaults to their grants, ordered by name.
func (s *Service) accessOf(ctx context.Context, org identity.OrganizationID, grants []vault.Grant) ([]vault.Access, error) {
	out := []vault.Access{}
	if len(grants) == 0 {
		return out, nil
	}
	ids := make([]identity.VaultID, len(grants))
	for i, g := range grants {
		ids[i] = g.Vault
	}
	vs, err := s.repo.FindMany(ctx, org, ids)
	if err != nil {
		return nil, errx.Wrap(err, "find vaults", errx.TypeInternal)
	}
	for _, v := range vs {
		for _, g := range grants {
			if g.Vault == v.ID {
				out = append(out, vault.Access{Vault: v, Mode: g.Mode})
			}
		}
	}
	slices.SortFunc(out, func(a, b vault.Access) int { return strings.Compare(a.Vault.Name, b.Vault.Name) })
	return out, nil
}

func (s *Service) AnyFor(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (bool, error) {
	if agentID.IsZero() {
		return false, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.AnyForAgent(ctx, org, agentID)
}

// WriteAs is the agents' write: the vault must be one the agent holds with
// write mode here (its own, or its dock's for the named conversation); the
// entry carries who wrote it and from where.
func (s *Service) WriteAs(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID, input vault.SaveEntry) (vault.Entry, error) {
	sc, err := s.canWrite(ctx, org, agentID, conv, input.Vault)
	if err != nil {
		return vault.Entry{}, err
	}
	input.Provenance = sc.provenance()
	if err := input.Validate(); err != nil {
		return vault.Entry{}, err
	}
	return s.save(ctx, org, input)
}

// DeleteAs removes the entry called name, under the same check as WriteAs.
func (s *Service) DeleteAs(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID, id identity.VaultID, name string) (vault.Entry, error) {
	if err := vault.ValidName("name", name); err != nil {
		return vault.Entry{}, err
	}
	if _, err := s.canWrite(ctx, org, agentID, conv, id); err != nil {
		return vault.Entry{}, err
	}
	cur, err := s.repo.EntryByName(ctx, org, id, name)
	if err != nil {
		return vault.Entry{}, errx.Wrap(err, "find entry", errx.TypeInternal)
	}
	return s.DeleteEntry(ctx, org, id, cur.ID)
}

func (s *Service) canWrite(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID, id identity.VaultID) (scope, error) {
	sc, err := s.scope(ctx, org, agentID, conv)
	if err != nil {
		return scope{}, err
	}
	i := slices.IndexFunc(sc.grants, func(g vault.Grant) bool { return g.Vault == id })
	switch {
	case i < 0:
		return scope{}, errx.NotFound("vault not found")
	case !sc.grants[i].Mode.Allows(true):
		return scope{}, errx.Forbidden("this vault is read-only for you here; to write, work a conversation of a dock with write access and pass its conversation_id")
	}
	return sc, nil
}

func (sc scope) provenance() vault.Provenance {
	return vault.Provenance{Kind: vault.WrittenByAgent, By: sc.agent.Name, Conversation: sc.conversation, Dock: sc.dock}
}

func change(e vault.Entry, deleted bool) vault.EntryChange {
	return vault.EntryChange{Vault: e.Vault, Entry: e.ID, Name: e.Name, Version: e.Version, Deleted: deleted, Provenance: e.Provenance}
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[vault.Vault], error) {
	return s.repo.List(ctx, org, page.Normalize())
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.VaultID) (vault.Vault, error) {
	if id.IsZero() {
		return vault.Vault{}, errx.InvalidField("vault_id", "vault_id is required")
	}
	return s.repo.Find(ctx, org, id)
}

func (s *Service) Grants(ctx context.Context, org identity.OrganizationID, id identity.VaultID) (vault.Grants, error) {
	if _, err := s.repo.Find(ctx, org, id); err != nil {
		return vault.Grants{}, err
	}
	return s.repo.Grants(ctx, org, id)
}

func (s *Service) Entries(ctx context.Context, org identity.OrganizationID, id identity.VaultID, page query.Pagination) (query.Paginated[vault.Summary], error) {
	if _, err := s.repo.Find(ctx, org, id); err != nil {
		return query.Paginated[vault.Summary]{}, err
	}
	return s.repo.Entries(ctx, org, id, page.Normalize())
}

func (s *Service) Index(ctx context.Context, org identity.OrganizationID, id identity.VaultID, limit int) ([]vault.Summary, error) {
	if limit <= 0 || limit > vault.MaxIndex {
		limit = vault.MaxIndex
	}
	page, err := s.repo.Entries(ctx, org, id, query.Pagination{Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (s *Service) Entry(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID) (vault.Entry, error) {
	return s.repo.Entry(ctx, org, id, entry)
}

func (s *Service) EntryByName(ctx context.Context, org identity.OrganizationID, id identity.VaultID, name string) (vault.Entry, error) {
	if err := vault.ValidName("name", name); err != nil {
		return vault.Entry{}, err
	}
	return s.repo.EntryByName(ctx, org, id, name)
}

func (s *Service) Versions(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID) ([]vault.Version, error) {
	if _, err := s.repo.Find(ctx, org, id); err != nil {
		return nil, err
	}
	return s.repo.Versions(ctx, org, id, entry, vault.MaxVersions)
}

func (s *Service) Search(ctx context.Context, org identity.OrganizationID, vaults []identity.VaultID, q string, limit int) ([]vault.Hit, error) {
	if err := text.Required("q", q, text.MaxSearch); err != nil {
		return nil, err
	}
	if len(vaults) == 0 {
		return []vault.Hit{}, nil
	}
	if limit <= 0 || limit > vault.MaxSearchHits {
		limit = vault.MaxSearchHits
	}
	return s.repo.Search(ctx, org, vaults, text.Clean(q), limit)
}

func (s *Service) OfAgent(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) ([]vault.Access, error) {
	if _, err := s.agents.Find(ctx, org, agentID); err != nil {
		return nil, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	g, err := s.repo.AgentGrants(ctx, org, agentID)
	if err != nil {
		return nil, errx.Wrap(err, "agent grants", errx.TypeInternal)
	}
	return s.accessOf(ctx, org, g)
}

func (s *Service) OfDock(ctx context.Context, org identity.OrganizationID, dockID identity.DockID) ([]vault.Access, error) {
	if _, err := s.docks.Find(ctx, org, dockID); err != nil {
		return nil, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	g, err := s.repo.DockGrants(ctx, org, dockID)
	if err != nil {
		return nil, errx.Wrap(err, "dock grants", errx.TypeInternal)
	}
	return s.accessOf(ctx, org, g)
}
