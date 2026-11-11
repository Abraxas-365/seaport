-- +goose Up
-- What an agent did on each delivery (ship.Turn): the tools it called,
-- title/kind/status/timing only (titles redacted), stored by the crew hub
-- when the delivery ends. Kept as long as the conversation.
CREATE TABLE ship_turns (
    organization_id uuid NOT NULL,
    id              uuid NOT NULL, -- the delivery id
    conversation_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    agent_name      text NOT NULL,
    outcome         text NOT NULL CHECK (outcome IN ('acked', 'released', 'dropped')),
    started_at      timestamptz NOT NULL,
    ended_at        timestamptz NOT NULL,
    tools           jsonb NOT NULL,
    omitted         integer NOT NULL DEFAULT 0 CHECK (omitted >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES conversations (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id)
);
CREATE INDEX ship_turns_conversation ON ship_turns (organization_id, conversation_id, ended_at DESC);

-- +goose Down
DROP TABLE ship_turns;
