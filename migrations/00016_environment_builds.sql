-- +goose Up
-- Environment builds (hosting.Build): the image Seaport derives from an
-- environment version (FROM its image or the harness's base, packages,
-- setup). One row per version; a build is claimed by one process (state
-- building, updated_at kept fresh while it runs) and may be claimed again
-- once failed, forced, or stale (its process died). The log keeps the
-- output's tail; it holds no agent secrets (repos are cloned at start).
CREATE TABLE environment_builds (
    organization_id uuid NOT NULL,
    environment_id  uuid NOT NULL,
    version         integer NOT NULL CHECK (version > 0),
    state           text NOT NULL CHECK (state IN ('building', 'ready', 'failed')),
    base            text NOT NULL,
    image           text NOT NULL DEFAULT '',
    detail          text NOT NULL DEFAULT '',
    log             text NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, environment_id, version),
    FOREIGN KEY (organization_id, environment_id, version)
        REFERENCES environment_versions (organization_id, environment_id, version) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE environment_builds;
