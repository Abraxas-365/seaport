-- +goose Up
-- Managed browser (plan browser-login.md): a profile version says whether
-- the agent uses the browser its environment gives its slots
-- (agent.Profile.Browser; the kit carries it, so turning it on or off
-- restarts the harness). login_requests are the sites the agent asked a
-- person to sign in to (MCP tool login_needed): a small list of
-- {host, reason, at}, newest per host, cleared from the web once done.
ALTER TABLE agent_profiles ADD COLUMN browser boolean NOT NULL DEFAULT false;
ALTER TABLE agents ADD COLUMN login_requests jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE agents DROP COLUMN login_requests;
ALTER TABLE agent_profiles DROP COLUMN browser;
