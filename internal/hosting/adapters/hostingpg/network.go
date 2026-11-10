package hostingpg

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// Network: model usage (the gateway) and egress denials (the proxy).

func (r *Repository) AddUsage(ctx context.Context, org identity.OrganizationID, u hosting.Usage) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO model_usage (organization_id, agent_id, day, provider, model,
			requests, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens)
		SELECT $1, $2, (now() AT TIME ZONE 'UTC')::date, $3, $4, 1, $5, $6, $7, $8
		WHERE EXISTS (SELECT 1 FROM agents WHERE organization_id = $1 AND id = $2)
		ON CONFLICT (organization_id, agent_id, day, provider, model) DO UPDATE SET
			requests = model_usage.requests + 1,
			input_tokens = model_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = model_usage.output_tokens + EXCLUDED.output_tokens,
			cache_write_tokens = model_usage.cache_write_tokens + EXCLUDED.cache_write_tokens,
			cache_read_tokens = model_usage.cache_read_tokens + EXCLUDED.cache_read_tokens`,
		org, u.Agent, u.Provider, u.Model, u.Input, u.Output, u.CacheWrite, u.CacheRead)
	return pgerr.Map(err, "add model usage")
}

func (r *Repository) Usage(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, days int) ([]hosting.UsageDay, error) {
	out := []hosting.UsageDay{}
	err := r.db.SelectContext(ctx, &out, `SELECT day::timestamptz AS day, provider, model, requests, input_tokens,
			output_tokens, cache_write_tokens, cache_read_tokens
		FROM model_usage
		WHERE organization_id = $1 AND agent_id = $2 AND day > (now() AT TIME ZONE 'UTC')::date - $3::int
		ORDER BY day DESC, provider, model`, org, agentID, days)
	return out, pgerr.Map(err, "read model usage")
}

func (r *Repository) AddDenial(ctx context.Context, org identity.OrganizationID, d hosting.Denied) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO egress_denials (organization_id, agent_id, idx, host)
		SELECT $1, $2, $3, $4
		WHERE EXISTS (SELECT 1 FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3)
		ON CONFLICT (organization_id, agent_id, idx, host) DO UPDATE SET
			count = egress_denials.count + 1, last_seen = now()`,
		org, d.Caller.Agent, d.Caller.Slot, d.Host)
	return pgerr.Map(err, "add egress denial")
}

func (r *Repository) Denials(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, limit int) ([]hosting.Denial, error) {
	out := []hosting.Denial{}
	err := r.db.SelectContext(ctx, &out, `SELECT idx, host, count, last_seen FROM egress_denials
		WHERE organization_id = $1 AND agent_id = $2
		ORDER BY last_seen DESC, idx, host LIMIT $3`, org, agentID, limit)
	return out, pgerr.Map(err, "read egress denials")
}
