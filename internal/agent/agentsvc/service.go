// Package agentsvc implements the agent ports.
package agentsvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo    agent.Repository
	revoker agent.Revoker
	events  event.Publisher
}

func New(repo agent.Repository, revoker agent.Revoker, events event.Publisher) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, revoker: revoker, events: events}
}

var (
	_ agent.Commands = (*Service)(nil)
	_ agent.Queries  = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input agent.Create) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	input.Description, input.CreatedBy = text.Clean(input.Description), text.Clean(input.CreatedBy)
	a, err := s.repo.Create(ctx, org, identity.NewAgentID(), input, agent.MaxAgents)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "create agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.Update) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if input.Description != nil {
		d := text.Clean(*input.Description)
		input.Description = &d
	}
	a, err := s.repo.Update(ctx, org, id, input)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "update agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id identity.AgentID) error {
	if id.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := s.repo.Delete(ctx, org, id); err != nil {
		return errx.Wrap(err, "delete agent", errx.TypeInternal)
	}
	if s.revoker != nil {
		s.revoker.DisconnectAgent(org, id)
	}
	s.events.Publish(org, agent.EventDeleted, agent.Deleted{ID: id})
	return nil
}

func (s *Service) Assign(ctx context.Context, org identity.OrganizationID, input agent.Assign) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if err := s.repo.Assign(ctx, org, input); err != nil {
		return agent.Agent{}, errx.Wrap(err, "assign agent", errx.TypeInternal)
	}
	return s.announce(ctx, org, input.Agent)
}

func (s *Service) Unassign(ctx context.Context, org identity.OrganizationID, dock identity.DockID, id identity.AgentID) (agent.Agent, error) {
	if dock.IsZero() {
		return agent.Agent{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	if id.IsZero() {
		return agent.Agent{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := s.repo.Unassign(ctx, org, dock, id); err != nil {
		return agent.Agent{}, errx.Wrap(err, "unassign agent", errx.TypeInternal)
	}
	return s.announce(ctx, org, id)
}

// announce publishes the agent as it is now. Assigning a default demotes
// another agent: the web refetches the list on every agent.updated.
func (s *Service) announce(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Agent, error) {
	a, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Agent, error) {
	if id.IsZero() {
		return agent.Agent{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.Find(ctx, org, id)
}

func (s *Service) FindByName(ctx context.Context, org identity.OrganizationID, name string) (agent.Agent, error) {
	if err := agent.ValidName(name); err != nil {
		return agent.Agent{}, err
	}
	return s.repo.FindByName(ctx, org, name)
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[agent.Agent], error) {
	return s.repo.List(ctx, org, page.Normalize())
}

func (s *Service) Team(ctx context.Context, org identity.OrganizationID, dock identity.DockID) ([]agent.Member, error) {
	if dock.IsZero() {
		return nil, errx.InvalidField("dock_id", "dock_id is required")
	}
	return s.repo.Team(ctx, org, dock)
}
