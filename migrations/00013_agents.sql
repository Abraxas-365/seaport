-- +goose Up
-- Agents (agent.Agent): the organization's library of agents. Every crew
-- token belongs to one; an agent works only through the docks it is
-- assigned to (dock_agents), and every inbound ship that goes to the crew
-- names the agent it is for (ships.agent_id, decided at arrival by
-- agent.Route). Deleting an agent is soft (deleted_at): its tokens are
-- revoked, its assignments dropped and its waiting ships held, while the
-- tokens and ships that name it keep their record.
CREATE TABLE agents (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name            text NOT NULL,
    description     text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,
    UNIQUE (organization_id, id)
);
CREATE UNIQUE INDEX agents_name ON agents (organization_id, name) WHERE deleted_at IS NULL;

-- The agents of a dock: with one it gets every message; with several an
-- @name picks one, else the conversation's previous agent, else the
-- default (at most one per dock), else the message is held.
CREATE TABLE dock_agents (
    organization_id uuid NOT NULL,
    dock_id         uuid NOT NULL,
    agent_id        uuid NOT NULL,
    is_default      boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, dock_id, agent_id),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id)
);
CREATE UNIQUE INDEX dock_agents_default ON dock_agents (organization_id, dock_id) WHERE is_default;
CREATE INDEX dock_agents_agent ON dock_agents (organization_id, agent_id);

-- Upgrade: an organization already in use gets agent "default", the
-- default of every dock and the owner of every crew token, so a running
-- setup keeps working.
INSERT INTO agents (id, organization_id, name, description, created_by)
SELECT gen_random_uuid(), o.organization_id, 'default', 'Created by the upgrade to agents', 'seaport'
FROM (SELECT organization_id FROM docks UNION SELECT organization_id FROM crew_tokens) o;
INSERT INTO dock_agents (organization_id, dock_id, agent_id, is_default)
SELECT d.organization_id, d.id, a.id, true FROM docks d
JOIN agents a ON a.organization_id = d.organization_id AND a.name = 'default';

ALTER TABLE crew_tokens ADD COLUMN agent_id uuid;
UPDATE crew_tokens t SET agent_id = a.id FROM agents a
WHERE a.organization_id = t.organization_id AND a.name = 'default';
ALTER TABLE crew_tokens ALTER COLUMN agent_id SET NOT NULL;
ALTER TABLE crew_tokens ADD CONSTRAINT crew_tokens_agent_fkey
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id);
CREATE INDEX crew_tokens_agent ON crew_tokens (organization_id, agent_id) WHERE revoked_at IS NULL;

-- Inbound: the agent the ship is for (required while it waits for or is
-- with the crew). Outbound: the agent that sent it (NULL: an admin).
ALTER TABLE ships ADD COLUMN agent_id uuid;
UPDATE ships s SET agent_id = a.id FROM agents a
WHERE a.organization_id = s.organization_id AND a.name = 'default'
  AND s.direction = 'in' AND s.status IN ('queued', 'claimed');
-- Waiting ships of an organization with no agent (impossible: they have
-- a dock) would break the check below; hold them.
UPDATE ships SET status = 'held', delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
WHERE direction = 'in' AND status IN ('queued', 'claimed') AND agent_id IS NULL;
ALTER TABLE ships ADD CONSTRAINT ships_agent_fkey
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id);
ALTER TABLE ships ADD CONSTRAINT ships_waiting_agent
    CHECK (status NOT IN ('queued', 'claimed') OR agent_id IS NOT NULL);
CREATE INDEX ships_agent_queue ON ships (organization_id, agent_id, created_at, id) WHERE status = 'queued';
CREATE INDEX ships_routed ON ships (organization_id, conversation_id, created_at DESC)
    WHERE agent_id IS NOT NULL;
-- Why a held ship was held (customs or routing), for people reading the
-- history; empty for ships held before this column.
ALTER TABLE ships ADD COLUMN hold_reason text NOT NULL DEFAULT '';

-- The agent a live connection works as.
ALTER TABLE crew_connections ADD COLUMN agent_id uuid;

-- +goose Down
ALTER TABLE crew_connections DROP COLUMN agent_id;
ALTER TABLE ships DROP COLUMN hold_reason;
DROP INDEX ships_routed;
DROP INDEX ships_agent_queue;
ALTER TABLE ships DROP CONSTRAINT ships_waiting_agent;
ALTER TABLE ships DROP CONSTRAINT ships_agent_fkey;
ALTER TABLE ships DROP COLUMN agent_id;
DROP INDEX crew_tokens_agent;
ALTER TABLE crew_tokens DROP CONSTRAINT crew_tokens_agent_fkey;
ALTER TABLE crew_tokens DROP COLUMN agent_id;
DROP TABLE dock_agents;
DROP TABLE agents;
