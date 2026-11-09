// Package hostingsvc implements the hosting ports.
package hostingsvc

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo   hosting.Repository
	agents agent.Queries
	limits hosting.Limits
	events event.Publisher
	builds *builds
}

func New(repo hosting.Repository, agents agent.Queries, limits hosting.Limits, events event.Publisher, builder Builder) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, agents: agents, limits: limits, events: events, builds: newBuilds(builder)}
}

var (
	_ hosting.Commands = (*Service)(nil)
	_ hosting.Queries  = (*Service)(nil)
)

func (s *Service) CreateEnvironment(ctx context.Context, org identity.OrganizationID, input hosting.CreateEnvironment) (hosting.Environment, error) {
	if err := input.Validate(); err != nil {
		return hosting.Environment{}, err
	}
	input.Spec = hosting.Normalize(input.Spec)
	if err := s.limits.Check(input.Spec.Resources); err != nil {
		return hosting.Environment{}, err
	}
	input.CreatedBy = text.Clean(input.CreatedBy)
	e, err := s.repo.CreateEnvironment(ctx, org, identity.NewEnvironmentID(), input, hosting.MaxEnvironments)
	if err != nil {
		return hosting.Environment{}, errx.Wrap(err, "create environment", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventEnvironmentUpdated, e)
	return e, nil
}

func (s *Service) SaveEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, input hosting.SaveEnvironment) (hosting.Environment, error) {
	if id.IsZero() {
		return hosting.Environment{}, errx.InvalidField("environment_id", "environment_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.Environment{}, err
	}
	input.Spec = hosting.Normalize(input.Spec)
	if err := s.limits.Check(input.Spec.Resources); err != nil {
		return hosting.Environment{}, err
	}
	input.CreatedBy = text.Clean(input.CreatedBy)
	current, err := s.repo.Environment(ctx, org, id, 0)
	if err != nil {
		return hosting.Environment{}, err
	}
	if current.Version == input.Base && reflect.DeepEqual(current.Spec, input.Spec) {
		return current, nil // nothing changed: no new version
	}
	// The agents already on it must have the secrets it now names.
	hosted, err := s.repo.HostedAgents(ctx, org)
	if err != nil {
		return hosting.Environment{}, errx.Wrap(err, "list hosted agents", errx.TypeInternal)
	}
	for _, h := range hosted {
		if h.Environment == id {
			if err := s.checkSecrets(ctx, org, h.Agent, input.Spec); err != nil {
				return hosting.Environment{}, err
			}
		}
	}
	e, err := s.repo.SaveEnvironment(ctx, org, id, input)
	if err != nil {
		return hosting.Environment{}, errx.Wrap(err, "save environment", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventEnvironmentUpdated, e)
	return e, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) error {
	if id.IsZero() {
		return errx.InvalidField("environment_id", "environment_id is required")
	}
	if err := s.repo.DeleteEnvironment(ctx, org, id); err != nil {
		return errx.Wrap(err, "delete environment", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventEnvironmentDeleted, hosting.EnvironmentDeleted{ID: id})
	return nil
}

// checkSecrets refuses a spec whose repo credentials name secrets the
// agent doesn't have.
func (s *Service) checkSecrets(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, spec hosting.Spec) error {
	need := spec.Secrets()
	if len(need) == 0 {
		return nil
	}
	have, err := s.agents.Secrets(ctx, org, agentID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(have))
	for _, sec := range have {
		names = append(names, sec.Name)
	}
	var missing []string
	for _, n := range need {
		if !slices.Contains(names, n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		a, err := s.agents.Find(ctx, org, agentID)
		if err != nil {
			return err
		}
		return errx.Business("agent " + a.Name + " has no secret " + strings.Join(missing, ", ") +
			" (the environment's repo credentials name it): seaport agents secrets set " + a.Name + " " + missing[0])
	}
	return nil
}

func (s *Service) Host(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input hosting.Host) (hosting.Hosted, error) {
	if agentID.IsZero() {
		return hosting.Hosted{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.Hosted{}, err
	}
	input.By = text.Clean(input.By)
	env, err := s.repo.Environment(ctx, org, input.Environment, 0)
	if err != nil {
		return hosting.Hosted{}, err
	}
	if err := s.checkSecrets(ctx, org, agentID, env.Spec); err != nil {
		return hosting.Hosted{}, err
	}
	h, err := s.repo.Host(ctx, org, agentID, input, s.limits.MaxSlots)
	if err != nil {
		return hosting.Hosted{}, errx.Wrap(err, "host agent", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, h)
	return h, nil
}

func (s *Service) Unhost(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) error {
	if agentID.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := s.repo.Unhost(ctx, org, agentID); err != nil {
		return errx.Wrap(err, "unhost agent", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, hosting.Hosted{Agent: agentID, Slots: []hosting.Slot{}})
	return nil
}

func (s *Service) Environment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, version int) (hosting.Environment, error) {
	if id.IsZero() {
		return hosting.Environment{}, errx.InvalidField("environment_id", "environment_id is required")
	}
	if version < 0 {
		return hosting.Environment{}, errx.InvalidField("version", "version must be positive (0: the current one)")
	}
	return s.repo.Environment(ctx, org, id, version)
}

func (s *Service) EnvironmentByName(ctx context.Context, org identity.OrganizationID, name string) (hosting.Environment, error) {
	if err := hosting.ValidName(name); err != nil {
		return hosting.Environment{}, err
	}
	return s.repo.EnvironmentByName(ctx, org, name)
}

func (s *Service) Environments(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[hosting.Environment], error) {
	return s.repo.Environments(ctx, org, page.Normalize())
}

func (s *Service) Versions(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) ([]hosting.Version, error) {
	if id.IsZero() {
		return nil, errx.InvalidField("environment_id", "environment_id is required")
	}
	return s.repo.Versions(ctx, org, id)
}

func (s *Service) Hosted(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	if agentID.IsZero() {
		return hosting.Hosted{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.Hosted(ctx, org, agentID)
}

func (s *Service) HostedAgents(ctx context.Context, org identity.OrganizationID) ([]hosting.Hosted, error) {
	return s.repo.HostedAgents(ctx, org)
}
