-- +goose Up
-- Environments are gone: each hosted agent's environment was moved into
-- its config and secrets by the port's data step (bootstrap,
-- upgradeEnvironments), which runs between 00045 and this one and marks
-- 'environments' done. Without the mark this refuses rather than drop
-- data nobody moved.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM environments)
       AND NOT EXISTS (SELECT 1 FROM migration_marks WHERE name = 'environments') THEN
        RAISE EXCEPTION 'environments were not moved into their agents: start this release''s seaport (with its secret key) to upgrade';
    END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE agent_slots DROP COLUMN environment_id;
ALTER TABLE agent_slots DROP COLUMN environment_version;
ALTER TABLE agent_hosting DROP COLUMN environment_id;
DROP TABLE environment_builds;
DROP TABLE environment_secrets;
DROP TABLE environment_versions;
DROP TABLE environments;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'dropping environments is irreversible (their data lives in the agents now)';
END
$$;
-- +goose StatementEnd
