-- +goose Up
-- Browser sessions of iam mode (auth.Session): the cookie holds a random id,
-- only its SHA-256 is stored; IAMKit tokens are sealed (AES-GCM, key from
-- SEAPORT_SECRET_KEY).
CREATE TABLE web_sessions (
    id_hash         text PRIMARY KEY,
    organization_id uuid NOT NULL,
    subject         text NOT NULL,
    profile         jsonb NOT NULL DEFAULT '{}',
    tokens          text NOT NULL,
    access_expires  timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX web_sessions_expiry ON web_sessions (expires_at);

-- +goose Down
DROP TABLE web_sessions;
