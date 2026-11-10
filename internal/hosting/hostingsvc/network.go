package hostingsvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// slotEnvironment is the current environment of a caller's slot
// (NotFound when the agent isn't hosted or has no such slot).
func (s *Service) slotEnvironment(ctx context.Context, org identity.OrganizationID, c hosting.SlotCaller) (hosting.Environment, error) {
	h, err := s.repo.Hosted(ctx, org, c.Agent)
	if err != nil {
		return hosting.Environment{}, err
	}
	if !hasSlot(h, c.Slot) {
		return hosting.Environment{}, errx.NotFound("slot not found")
	}
	return s.repo.Environment(ctx, org, h.Environment, 0)
}

func (s *Service) ModelKey(ctx context.Context, org identity.OrganizationID, input hosting.KeyRequest) (hosting.ModelKey, error) {
	if err := input.Validate(); err != nil {
		return hosting.ModelKey{}, err
	}
	env, err := s.slotEnvironment(ctx, org, input.Caller)
	if err != nil {
		return hosting.ModelKey{}, err
	}
	name := env.Spec.Models.KeyName(input.Provider)
	if name == "" {
		return hosting.ModelKey{}, errx.NotFound("environment " + env.Name + " names no " + input.Provider + " key (models." + input.Provider + ")")
	}
	if s.slots.cfg.Kits == nil {
		return hosting.ModelKey{}, errx.Internal("no secrets to open model keys")
	}
	secrets, err := s.slots.cfg.Kits.Reveal(ctx, org, input.Caller.Agent, []string{name})
	if err != nil {
		return hosting.ModelKey{}, err
	}
	key, ok := secrets[name]
	if !ok || key == "" {
		return hosting.ModelKey{}, errx.Business("the agent has no secret " + name + " (environment " + env.Name + " names it as its " + input.Provider + " key)")
	}
	return hosting.ModelKey{Provider: input.Provider, Key: key}, nil
}

func (s *Service) RecordUsage(ctx context.Context, org identity.OrganizationID, input hosting.Usage) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if input.Empty() {
		return nil
	}
	input.Model = text.Clean(input.Model)
	if input.Model == "" {
		input.Model = "unknown"
	}
	return s.repo.AddUsage(ctx, org, input)
}

func (s *Service) Egress(ctx context.Context, org identity.OrganizationID, caller hosting.SlotCaller) ([]string, error) {
	if err := caller.Validate(); err != nil {
		return nil, err
	}
	env, err := s.slotEnvironment(ctx, org, caller)
	if err != nil {
		return nil, err
	}
	return env.Spec.EgressHosts(), nil
}

func (s *Service) RecordDenial(ctx context.Context, org identity.OrganizationID, input hosting.Denied) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.repo.AddDenial(ctx, org, input)
}

func (s *Service) Usage(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input hosting.UsageQuery) ([]hosting.UsageDay, error) {
	if agentID.IsZero() {
		return nil, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.agents.Find(ctx, org, agentID); err != nil {
		return nil, err
	}
	return s.repo.Usage(ctx, org, agentID, input.Days)
}

func (s *Service) Denials(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) ([]hosting.Denial, error) {
	if agentID.IsZero() {
		return nil, errx.InvalidField("agent_id", "agent_id is required")
	}
	if _, err := s.repo.Hosted(ctx, org, agentID); err != nil {
		return nil, err
	}
	return s.repo.Denials(ctx, org, agentID, hosting.MaxDenials)
}
