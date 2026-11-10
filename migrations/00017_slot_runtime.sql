-- +goose Up
-- Hosted slots run (phase 3 M3). A slot's container signs in with a crew
-- token of its own, minted at each start and revoked at stop: slot is the
-- index of the agent's slot it was minted for (0 = a token a person
-- created). Slot tokens don't count against the organization's crew token
-- limit (the slots are bounded by SEAPORT_MAX_SLOTS) and are not listed
-- with the people's tokens.
ALTER TABLE crew_tokens ADD COLUMN slot integer NOT NULL DEFAULT 0 CHECK (slot >= 0);

-- The slot's current run: its id (set by the start that claimed the slot;
-- the starter's writes match it, so one that lost the slot changes
-- nothing; '' when stopped), its token, and the environment it runs
-- (environment_version is that environment's): a slot running another
-- environment or version than its agent's current one is outdated and
-- restarted when idle.
ALTER TABLE agent_slots ADD COLUMN run text NOT NULL DEFAULT '';
ALTER TABLE agent_slots ADD COLUMN token_id uuid;
ALTER TABLE agent_slots ADD COLUMN environment_id uuid;

-- The supervisor counts each hosted agent's waiting and claimed ships.
CREATE INDEX ships_agent_work ON ships (organization_id, agent_id) WHERE status IN ('queued', 'claimed');

-- This deployment's id: its slots' containers and volumes carry it
-- (io.seaport.port), so ports sharing one Docker engine leave each
-- other's slots alone.
CREATE TABLE port_identity (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton)
);
INSERT INTO port_identity DEFAULT VALUES;

-- +goose Down
DROP TABLE port_identity;
DROP INDEX ships_agent_work;
ALTER TABLE agent_slots DROP COLUMN environment_id;
ALTER TABLE agent_slots DROP COLUMN token_id;
ALTER TABLE agent_slots DROP COLUMN run;
ALTER TABLE crew_tokens DROP COLUMN slot;
