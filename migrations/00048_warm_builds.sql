-- +goose Up
-- The image build a hosted agent's current config needs on this port
-- (hostingsvc/warm.go), recorded by the supervisor, which builds it ahead
-- of any work. A build is keyed by BuildRequest.Digest: the machine and
-- the port's base image, so a new Seaport release (a new base tag) needs
-- a new build even when the machine didn't change.
--   build_digest   that build ('': not known yet)
--   build_machine  the machine digest it was worked out for (a config
--                  saved since makes it stale until the next round)
-- A running slot of that machine whose image isn't that build's, once
-- ready, is outdated: the supervisor restarts it once idle.
ALTER TABLE agent_hosting ADD COLUMN build_digest text NOT NULL DEFAULT '';
ALTER TABLE agent_hosting ADD COLUMN build_machine text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_hosting DROP COLUMN build_machine;
ALTER TABLE agent_hosting DROP COLUMN build_digest;
