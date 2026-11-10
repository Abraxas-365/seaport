-- +goose Up
-- Harness homes (hosting.Home): the ~/.claude or ~/.rness config an
-- environment bakes into its image. Content-addressed per organization
-- (digest = SHA-256 of the normalized tar); the bytes live sealed in blob
-- storage under blob_key. A version names its home (environment_versions.
-- home, also in the spec), so a named home can't be deleted; one no
-- version names is swept after hosting.HomeGrace (the trigger hands its
-- blob to blob_trash, which the media sweeper empties).
CREATE TABLE hosting_homes (
    organization_id uuid NOT NULL,
    digest          text NOT NULL CHECK (digest ~ '^sha256:[a-f0-9]{64}$'),
    harness         text NOT NULL CHECK (harness IN ('rness', 'claude-code')),
    files           jsonb NOT NULL,
    size            bigint NOT NULL CHECK (size >= 0),
    blob_key        text NOT NULL UNIQUE,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- touched_at: last uploaded or named (the sweep's grace counts from it)
    touched_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, digest)
);

ALTER TABLE environment_versions ADD COLUMN home text;
ALTER TABLE environment_versions ADD CONSTRAINT environment_versions_home_fkey
    FOREIGN KEY (organization_id, home) REFERENCES hosting_homes (organization_id, digest);
CREATE INDEX environment_versions_home ON environment_versions (organization_id, home) WHERE home IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION hosting_home_trash() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO blob_trash (blob_key, organization_id, due_at)
    VALUES (OLD.blob_key, OLD.organization_id, now())
    ON CONFLICT (blob_key) DO UPDATE SET due_at = now();
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER hosting_homes_trash AFTER DELETE ON hosting_homes
    FOR EACH ROW EXECUTE FUNCTION hosting_home_trash();

-- +goose Down
DROP TRIGGER hosting_homes_trash ON hosting_homes;
DROP FUNCTION hosting_home_trash();
ALTER TABLE environment_versions DROP CONSTRAINT environment_versions_home_fkey;
ALTER TABLE environment_versions DROP COLUMN home;
DROP TABLE hosting_homes;
