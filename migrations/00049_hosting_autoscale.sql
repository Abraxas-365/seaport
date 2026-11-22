-- +goose Up
-- Autoscale (hostingsvc/supervisor.go): with it on, the supervisor starts
-- another of the agent's slots when a conversation has waited
-- hosting.ScaleAfter with every running slot full, and stops an extra
-- slot idle for hosting.ScaleDownAfter. Off (the default), the agent runs
-- one slot at a time unless a person starts more.
ALTER TABLE agent_hosting ADD COLUMN autoscale boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent_hosting DROP COLUMN autoscale;
