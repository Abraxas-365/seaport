-- +goose Up
-- Environments (hosting.Environment): where an organization's hosted
-- agents run (harness, image, packages, setup, repos, network, resources,
-- workspace, idle stop). The spec is versioned like agent profiles:
-- environment_versions keeps every one, environments.version is the
-- current one; saving makes current+1 current under the row lock. Specs
-- hold no secrets (repo credentials name an agent secret as ${NAME}).
CREATE TABLE environments (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name            text NOT NULL,
    version         integer NOT NULL CHECK (version > 0),
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name)
);

CREATE TABLE environment_versions (
    organization_id uuid NOT NULL,
    environment_id  uuid NOT NULL,
    version         integer NOT NULL CHECK (version > 0),
    spec            jsonb NOT NULL,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, environment_id, version),
    FOREIGN KEY (organization_id, environment_id) REFERENCES environments (organization_id, id) ON DELETE CASCADE
);

-- Hosted agents: the port runs the agent on an environment, in slots
-- (stable instances <agent>-<idx>, each with its own state volume). An
-- environment can't be deleted while an agent is hosted on it; deleting
-- the agent unhosts it.
CREATE TABLE agent_hosting (
    organization_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    environment_id  uuid NOT NULL,
    updated_by      text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id),
    FOREIGN KEY (organization_id, environment_id) REFERENCES environments (organization_id, id)
);
CREATE INDEX agent_hosting_environment ON agent_hosting (organization_id, environment_id);

CREATE TABLE agent_slots (
    organization_id     uuid NOT NULL,
    agent_id            uuid NOT NULL,
    idx                 integer NOT NULL CHECK (idx > 0),
    state               text NOT NULL DEFAULT 'stopped'
                        CHECK (state IN ('stopped', 'starting', 'running', 'stopping', 'failed')),
    detail              text NOT NULL DEFAULT '',
    image               text NOT NULL DEFAULT '',
    environment_version integer NOT NULL DEFAULT 0,
    started_at          timestamptz,
    active_at           timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id, idx),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agent_hosting (organization_id, agent_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE agent_slots;
DROP TABLE agent_hosting;
DROP TABLE environment_versions;
DROP TABLE environments;
