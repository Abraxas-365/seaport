-- +goose Up
-- Subscription sign-in of hosted slots (phase 3 M6, experimental): a
-- slot's latest sign-in, relayed by the process running the harness's
-- login in the slot's container. The harness keeps its tokens in the
-- slot's state volume; nothing here signs anyone in.
CREATE TABLE slot_logins (
    organization_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    idx             integer NOT NULL,
    -- id names one attempt: the relay's writes match it.
    id              text NOT NULL,
    -- run is the slot run it signs in (agent_slots.run).
    run             text NOT NULL,
    state           text NOT NULL CHECK (state IN ('starting', 'waiting', 'verifying', 'done', 'failed')),
    url             text NOT NULL DEFAULT '',
    -- code: the pasted authorization code, from its submission (any
    -- process) until the relay takes it (within a second). Single-use and
    -- bound to the login's PKCE verifier, which never leaves the container.
    code            text NOT NULL DEFAULT '',
    detail          text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id, idx),
    FOREIGN KEY (organization_id, agent_id, idx) REFERENCES agent_slots (organization_id, agent_id, idx) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE slot_logins;
