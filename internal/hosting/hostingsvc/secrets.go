package hostingsvc

import (
	"context"
	"slices"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func (s *Service) SetSecret(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, input hosting.SetEnvironmentSecret) (hosting.EnvironmentSecret, error) {
	if id.IsZero() {
		return hosting.EnvironmentSecret{}, errx.InvalidField("environment_id", "environment_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.EnvironmentSecret{}, err
	}
	sec, err := s.repo.SetSecret(ctx, org, id, input, hosting.MaxSecrets)
	if err != nil {
		return hosting.EnvironmentSecret{}, errx.Wrap(err, "set environment secret", errx.TypeInternal)
	}
	s.announceEnvironment(ctx, org, id)
	return sec, nil
}

func (s *Service) DeleteSecret(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID, name string) error {
	if id.IsZero() {
		return errx.InvalidField("environment_id", "environment_id is required")
	}
	if name == "" {
		return errx.InvalidField("name", "name is required")
	}
	if err := s.repo.DeleteSecret(ctx, org, id, name); err != nil {
		return errx.Wrap(err, "delete environment secret", errx.TypeInternal)
	}
	s.announceEnvironment(ctx, org, id)
	return nil
}

func (s *Service) Secrets(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) ([]hosting.EnvironmentSecret, error) {
	if id.IsZero() {
		return nil, errx.InvalidField("environment_id", "environment_id is required")
	}
	return s.repo.Secrets(ctx, org, id)
}

// announceEnvironment publishes the environment (no secret values: names are read
// back by the web) after a secret changed.
func (s *Service) announceEnvironment(ctx context.Context, org identity.OrganizationID, id identity.EnvironmentID) {
	if e, err := s.repo.Environment(ctx, org, id, 0); err == nil {
		s.events.Publish(org, hosting.EventEnvironmentUpdated, e)
	}
}

// reveal opens the named secrets for a slot: the environment's first, then
// the agent's for what the environment lacks (specs from before environment
// secrets name the agent's). A name neither has is left out.
func (s *Service) reveal(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, agentID identity.AgentID, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	values, err := s.repo.OpenEnvironmentSecrets(ctx, org, env)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if i := slices.IndexFunc(values, func(v hosting.EnvironmentSecretValue) bool { return v.Name == n }); i >= 0 {
			out[n] = values[i].Value
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if s.slots.cfg.Kits == nil {
		return nil, errx.Internal("no secrets to open the agent's credentials")
	}
	theirs, err := s.slots.cfg.Kits.Reveal(ctx, org, agentID, missing)
	if err != nil {
		return nil, err
	}
	for n, v := range theirs {
		out[n] = v
	}
	return out, nil
}

// checkSecrets refuses a spec whose repo credentials or model keys name
// secrets neither the environment nor the agent has.
func (s *Service) checkSecrets(ctx context.Context, org identity.OrganizationID, env identity.EnvironmentID, agentID identity.AgentID, spec hosting.Spec) error {
	need := spec.Secrets()
	if len(need) == 0 {
		return nil
	}
	var names []string
	if !env.IsZero() {
		ours, err := s.repo.Secrets(ctx, org, env)
		if err != nil {
			return err
		}
		for _, sec := range ours {
			names = append(names, sec.Name)
		}
	}
	theirs, err := s.agents.Secrets(ctx, org, agentID)
	if err != nil {
		return err
	}
	for _, sec := range theirs {
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
		return errx.Business("the environment names secret " + strings.Join(missing, ", ") +
			" (a repo credential or model key) that neither it nor agent " + a.Name + " has: seaport env secrets set <environment> " + missing[0])
	}
	return nil
}
