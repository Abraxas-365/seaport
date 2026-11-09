package agentsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

var _ agent.Kits = (*Service)(nil)

func (s *Service) SaveProfile(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.SaveProfile) (agent.Profile, error) {
	if id.IsZero() {
		return agent.Profile{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return agent.Profile{}, err
	}
	input.Instructions, input.CreatedBy = text.Clean(input.Instructions), text.Clean(input.CreatedBy)
	input.Skills = slices.Clone(input.Skills)
	for i := range input.Skills {
		input.Skills[i].Description = text.Clean(input.Skills[i].Description)
		input.Skills[i].Body = strings.TrimSpace(input.Skills[i].Body)
	}
	if input.Skills == nil {
		input.Skills = []agent.Skill{}
	}
	if input.MCPServers == nil {
		input.MCPServers = []agent.MCPServer{}
	}
	for i := range input.MCPServers {
		if input.MCPServers[i].Headers == nil {
			input.MCPServers[i].Headers = []agent.Header{}
		}
	}
	p, err := s.repo.SaveProfile(ctx, org, id, input)
	if err != nil {
		return agent.Profile{}, errx.Wrap(err, "save profile", errx.TypeInternal)
	}
	s.kitChanged(ctx, org, id)
	return p, nil
}

func (s *Service) SetSecret(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.SetSecret) (agent.Secret, error) {
	if id.IsZero() {
		return agent.Secret{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return agent.Secret{}, err
	}
	sec, err := s.repo.SetSecret(ctx, org, id, input, agent.MaxSecrets)
	if err != nil {
		return agent.Secret{}, errx.Wrap(err, "set secret", errx.TypeInternal)
	}
	s.kitChanged(ctx, org, id)
	return sec, nil
}

func (s *Service) DeleteSecret(ctx context.Context, org identity.OrganizationID, id identity.AgentID, name string) error {
	if id.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := agent.ValidSecretName(name); err != nil {
		return err
	}
	if err := s.repo.DeleteSecret(ctx, org, id, name); err != nil {
		return errx.Wrap(err, "delete secret", errx.TypeInternal)
	}
	s.kitChanged(ctx, org, id)
	return nil
}

// kitChanged tells the agent's connections and the web: the agent (its
// profile version) is published, never a secret.
func (s *Service) kitChanged(ctx context.Context, org identity.OrganizationID, id identity.AgentID) {
	if s.revoker != nil {
		s.revoker.ProfileChanged(org, id)
	}
	_, _ = s.announce(ctx, org, id)
}

func (s *Service) Profile(ctx context.Context, org identity.OrganizationID, id identity.AgentID, version int) (agent.Profile, error) {
	if id.IsZero() {
		return agent.Profile{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if version < 0 {
		return agent.Profile{}, errx.InvalidField("version", "version must be positive (0: the current one)")
	}
	return s.repo.Profile(ctx, org, id, version)
}

func (s *Service) Versions(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]agent.Version, error) {
	if id.IsZero() {
		return nil, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.Versions(ctx, org, id)
}

func (s *Service) Secrets(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]agent.Secret, error) {
	if id.IsZero() {
		return nil, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.Secrets(ctx, org, id)
}

func (s *Service) Equip(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Kit, error) {
	if id.IsZero() {
		return agent.Kit{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	a, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return agent.Kit{}, err
	}
	p, err := s.repo.Profile(ctx, org, id, a.ProfileVersion)
	if err != nil {
		return agent.Kit{}, errx.Wrap(err, "find profile", errx.TypeInternal)
	}
	secrets, err := s.repo.OpenSecrets(ctx, org, id)
	if err != nil {
		return agent.Kit{}, errx.Wrap(err, "open secrets", errx.TypeInternal)
	}
	return assemble(a, p, secrets)
}

// assemble builds an agent's kit from its profile and opened secrets.
func assemble(a agent.Agent, p agent.Profile, secrets []agent.SecretValue) (agent.Kit, error) {
	values := make(map[string]string, len(secrets))
	k := agent.Kit{Agent: a.ID, Name: a.Name, Version: p.Version, Instructions: p.Instructions,
		Skills: make([]agent.Skill, 0, len(p.Skills)), MCPServers: make([]agent.MCPServer, 0, len(p.MCPServers)),
		Approve: p.Approve, Model: p.Model, Effort: p.Effort, Env: map[string]string{}}
	for _, sec := range secrets {
		values[sec.Name] = sec.Value
		if sec.Env {
			k.Env[sec.Name] = sec.Value
		}
	}
	for _, sk := range p.Skills {
		k.Skills = append(k.Skills, agent.Skill{Name: sk.Name, Description: sk.Description})
	}
	for _, srv := range p.MCPServers {
		out := agent.MCPServer{Name: srv.Name, URL: srv.URL, Headers: make([]agent.Header, 0, len(srv.Headers))}
		for _, h := range srv.Headers {
			v, err := agent.Resolve(h.Value, values)
			if err != nil {
				return agent.Kit{}, err
			}
			out.Headers = append(out.Headers, agent.Header{Name: h.Name, Value: v})
		}
		k.MCPServers = append(k.MCPServers, out)
	}
	// The revision hashes everything the connector runs with, skill bodies
	// included (the harness may have read one), secrets resolved.
	sum := sha256.New()
	raw, _ := json.Marshal(struct {
		Kit    agent.Kit
		Skills []agent.Skill
	}{k, p.Skills})
	sum.Write(raw)
	k.Revision = hex.EncodeToString(sum.Sum(nil))[:16]
	return k, nil
}
