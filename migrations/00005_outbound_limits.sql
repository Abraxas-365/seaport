-- +goose Up
-- Outbound limits count an organization's outbound ships of the last hour
-- on every reply (shippg.CreateOutbound).
CREATE INDEX ships_outbound_recent ON ships (organization_id, created_at) WHERE direction = 'out';

-- +goose Down
DROP INDEX ships_outbound_recent;
