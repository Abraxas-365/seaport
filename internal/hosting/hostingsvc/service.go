// Package hostingsvc implements the hosting ports.
package hostingsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo   hosting.Repository
	agents agent.Queries
	limits hosting.Limits
	events event.Publisher
	builds *builds
	slots  *slots
	homes  homes
	// viewers are the browser viewers open in this process.
	viewers viewers
}

func New(repo hosting.Repository, agents agent.Queries, limits hosting.Limits, events event.Publisher, builder Builder, run Slots, home Homes) (*Service, error) {
	if events == nil {
		events = event.Nop{}
	}
	h, err := newHomes(home)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, agents: agents, limits: limits, events: events, builds: newBuilds(builder),
		slots: &slots{cfg: run, poke: make(chan struct{}, 1)}, homes: h}, nil
}

var (
	_ hosting.Commands   = (*Service)(nil)
	_ hosting.Queries    = (*Service)(nil)
	_ agent.MachineCheck = (*Service)(nil)
	_ agent.HostingClone = (*Service)(nil)
)

// CloneHosting hosts a cloned agent like its source (agent.HostingClone):
// the same number of slots, all stopped. A source that isn't hosted is
// not an error.
func (s *Service) CloneHosting(ctx context.Context, org identity.OrganizationID, source, clone identity.AgentID, by string) error {
	h, err := s.repo.Hosted(ctx, org, source)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil {
		return errx.Wrap(err, "read the source's hosting", errx.TypeInternal)
	}
	_, err = s.Host(ctx, org, clone, hosting.Host{Slots: len(h.Slots), By: by})
	return err
}

// CheckMachine checks and normalizes an agent config's harness and
// machine (agent.MachineCheck): the spec's own rules, the port's resource
// limits and its home (uploaded, for the same harness; touched so the
// sweep keeps it). Whether a hosted agent may drop its machine or turn
// the browser on is checked as the config is stored (under the agent's
// row lock).
func (s *Service) CheckMachine(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input agent.MachineInput) (agent.CheckedMachine, error) {
	if agentID.IsZero() {
		return agent.CheckedMachine{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := hosting.ValidHarness(input.Harness); err != nil {
		return agent.CheckedMachine{}, err
	}
	if len(input.Machine) == 0 {
		return agent.CheckedMachine{Secrets: []string{}}, nil
	}
	if input.Harness == "" {
		return agent.CheckedMachine{}, errx.InvalidField("harness", "a machine needs a harness (rness or claude-code)")
	}
	var m hosting.Machine
	dec := json.NewDecoder(bytes.NewReader(input.Machine))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return agent.CheckedMachine{}, errx.InvalidField("machine", "the machine is not valid: "+jsonProblem(err))
	}
	spec := hosting.Spec{Harness: input.Harness, Machine: m}
	if err := spec.Validate(); err != nil {
		return agent.CheckedMachine{}, err
	}
	spec = hosting.Normalize(spec)
	if err := s.limits.Check(spec.Resources); err != nil {
		return agent.CheckedMachine{}, err
	}
	if err := s.checkHome(ctx, org, spec); err != nil {
		return agent.CheckedMachine{}, err
	}
	raw, err := json.Marshal(spec.Machine)
	if err != nil {
		return agent.CheckedMachine{}, errx.Wrap(err, "encode machine", errx.TypeInternal)
	}
	return agent.CheckedMachine{Machine: raw, Digest: spec.Digest(), Secrets: spec.Secrets(), Home: spec.Home,
		Browser: spec.Browser.Enabled()}, nil
}

// jsonProblem is a decode error as people read it (no Go type names).
func jsonProblem(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return typeErr.Field + " has the wrong type"
	}
	return err.Error()
}

// checkHome: the home a spec names must be the organization's, for its
// harness; it is touched (under the hosting lock) so the sweep keeps it
// until the config naming it is stored (its FK keeps it from then on).
func (s *Service) checkHome(ctx context.Context, org identity.OrganizationID, spec hosting.Spec) error {
	if spec.Home == "" {
		return nil
	}
	h, err := s.repo.Home(ctx, org, spec.Home)
	if errx.IsType(err, errx.TypeNotFound) {
		return errx.InvalidField("home", "home "+spec.Home+" was never uploaded (seaport agents home push)")
	}
	if err != nil {
		return errx.Wrap(err, "find home", errx.TypeInternal)
	}
	if h.Harness != spec.Harness {
		return errx.InvalidField("home", "home "+spec.Home+" is a "+h.Harness+" home; the agent's harness is "+spec.Harness)
	}
	if err := s.repo.TouchHome(ctx, org, spec.Home); err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return errx.InvalidField("home", "home "+spec.Home+" was never uploaded (seaport agents home push)")
		}
		return errx.Wrap(err, "touch home", errx.TypeInternal)
	}
	return nil
}

// spec is the agent's current config's harness and machine, normalized
// (Business when it has none: the agent can't be hosted yet).
func (s *Service) spec(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Spec, error) {
	p, err := s.agents.Profile(ctx, org, agentID, 0)
	if err != nil {
		return hosting.Spec{}, err
	}
	spec, err := hosting.SpecOf(p.Harness, p.Machine)
	if err != nil {
		return hosting.Spec{}, err
	}
	return hosting.Normalize(spec), nil
}

func (s *Service) Host(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input hosting.Host) (hosting.Hosted, error) {
	if agentID.IsZero() {
		return hosting.Hosted{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.Hosted{}, err
	}
	input.By = text.Clean(input.By)
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

func (s *Service) UpdateHosting(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input hosting.HostingPatch) (hosting.Hosted, error) {
	if agentID.IsZero() {
		return hosting.Hosted{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.Hosted{}, err
	}
	h, err := s.repo.UpdateHosting(ctx, org, agentID, input)
	if err != nil {
		return hosting.Hosted{}, errx.Wrap(err, "update hosting", errx.TypeInternal)
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, h)
	s.Wake(org, agentID) // autoscale on: look at the queue now
	return h, nil
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
