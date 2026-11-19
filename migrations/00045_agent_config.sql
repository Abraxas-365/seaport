-- +goose Up
-- The agent is the whole configuration (plan agent-centric): its profile
-- versions also say where and how it runs. harness is the agent program
-- (rness | claude-code; '' = not set, a remote agent saved before this
-- release); machine is the normalized hosting.Machine (image, packages,
-- setup, repos, home, models and their access, env, egress, browser,
-- resources, workspace, idle stop; NULL = none). The agent domain keeps
-- it opaque: hosting validates and normalizes it (agent.MachineCheck) and
-- gives machine_digest (what a slot runs: a running slot whose digest is
-- not its agent's current one is outdated), machine_secrets (the secrets
-- it names: models and repo credentials, which must be the agent's and
-- can't be deleted while named) and home (the uploaded home it bakes in;
-- the FK keeps the sweep from deleting a named home).
ALTER TABLE agent_profiles ADD COLUMN harness text NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN machine jsonb;
ALTER TABLE agent_profiles ADD COLUMN machine_digest text NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN machine_secrets text[] NOT NULL DEFAULT '{}';
ALTER TABLE agent_profiles ADD COLUMN home text;
ALTER TABLE agent_profiles ADD CONSTRAINT agent_profiles_home_fkey
    FOREIGN KEY (organization_id, home) REFERENCES hosting_homes (organization_id, digest);
CREATE INDEX agent_profiles_home ON agent_profiles (organization_id, home) WHERE home IS NOT NULL;

-- Images are built per content (hosting.BuildRequest.Digest: harness,
-- base, Dockerfile, setup), so agents with the same machine share one.
-- agent_id is the agent whose start or build claimed it last (events).
CREATE TABLE hosting_builds (
    organization_id uuid NOT NULL,
    digest          text NOT NULL,
    agent_id        uuid,
    state           text NOT NULL CHECK (state IN ('building', 'ready', 'failed')),
    base            text NOT NULL,
    image           text NOT NULL DEFAULT '',
    detail          text NOT NULL DEFAULT '',
    log             text NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, digest)
);

-- What a slot's run runs: the machine digest and the config version (for
-- display). '' (every slot running when this release starts) is outdated.
ALTER TABLE agent_slots ADD COLUMN machine_digest text NOT NULL DEFAULT '';
ALTER TABLE agent_slots ADD COLUMN config_version integer NOT NULL DEFAULT 0;

-- Data steps the port runs in Go between migrations (bootstrap): one row
-- each once done. 'environments': every hosted agent's environment moved
-- into its config and secrets (00046 drops the environments after it).
CREATE TABLE migration_marks (
    name    text PRIMARY KEY,
    done_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE migration_marks;
ALTER TABLE agent_slots DROP COLUMN config_version;
ALTER TABLE agent_slots DROP COLUMN machine_digest;
DROP TABLE hosting_builds;
DROP INDEX agent_profiles_home;
ALTER TABLE agent_profiles DROP CONSTRAINT agent_profiles_home_fkey;
ALTER TABLE agent_profiles DROP COLUMN home;
ALTER TABLE agent_profiles DROP COLUMN machine_secrets;
ALTER TABLE agent_profiles DROP COLUMN machine_digest;
ALTER TABLE agent_profiles DROP COLUMN machine;
ALTER TABLE agent_profiles DROP COLUMN harness;
