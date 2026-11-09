-- +goose Up
-- Agent profiles (agent.Profile): immutable versions of what an agent's
-- harness is told and given (instructions, skills, MCP servers, approve
-- policy, model, effort). agents.profile_version is the one it runs (0:
-- none yet); saving makes version current+1 current, under the agent's
-- row lock.
CREATE TABLE agent_profiles (
    organization_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    version         integer NOT NULL CHECK (version > 0),
    instructions    text NOT NULL DEFAULT '',
    skills          jsonb NOT NULL DEFAULT '[]',
    mcp_servers     jsonb NOT NULL DEFAULT '[]',
    approve         text NOT NULL DEFAULT '',
    model           text NOT NULL DEFAULT '',
    effort          text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id, version),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id)
);
ALTER TABLE agents ADD COLUMN profile_version integer NOT NULL DEFAULT 0;

-- An agent's secrets (LLM keys, API tokens): sealed with the port's
-- secret key (internal/seal, info "seaport agent secrets v1", purpose
-- naming organization, agent and secret), never shown again after they
-- are written. MCP headers use them as ${NAME}; env ones also reach the
-- harness's environment. Only the agent's own crew credential gets them
-- (GET /crew/profile). Deleting the agent deletes them.
CREATE TABLE agent_secrets (
    organization_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    name            text NOT NULL,
    sealed          bytea NOT NULL,
    env             boolean NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id, name),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id)
);

-- The profile version a live connection runs (hello.profile_version), so
-- the crew view shows connections running an older one.
ALTER TABLE crew_connections ADD COLUMN profile_version integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE crew_connections DROP COLUMN profile_version;
DROP TABLE agent_secrets;
ALTER TABLE agents DROP COLUMN profile_version;
DROP TABLE agent_profiles;
