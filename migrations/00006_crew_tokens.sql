-- +goose Up
-- Crew tokens: agent credentials of an organization (crew.Token). Only the
-- SHA-256 of the value is stored; revoked rows stay for the record.
CREATE TABLE crew_tokens (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name            text NOT NULL,
    token_hash      text NOT NULL UNIQUE,
    hint            text NOT NULL,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz,
    UNIQUE (organization_id, id)
);
CREATE INDEX crew_tokens_active ON crew_tokens (organization_id, created_at, id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE crew_tokens;
