-- +goose Up
-- Hosted agents' network (phase 3 M4): what the model gateway metered and
-- what the egress proxy refused.

-- An agent's model use per day (UTC) and model, added by the gateway from
-- each response's usage. Kept while the agent exists.
CREATE TABLE model_usage (
    organization_id    uuid NOT NULL,
    agent_id           uuid NOT NULL,
    day                date NOT NULL,
    provider           text NOT NULL,
    model              text NOT NULL,
    requests           bigint NOT NULL DEFAULT 0,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_id, agent_id, day, provider, model),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id) ON DELETE CASCADE
);

-- Hosts the egress proxy refused a slot (counted, throttled per minute);
-- they go with the slot.
CREATE TABLE egress_denials (
    organization_id uuid NOT NULL,
    agent_id        uuid NOT NULL,
    idx             integer NOT NULL,
    host            text NOT NULL,
    count           bigint NOT NULL DEFAULT 1,
    last_seen       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, agent_id, idx, host),
    FOREIGN KEY (organization_id, agent_id, idx) REFERENCES agent_slots (organization_id, agent_id, idx) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE egress_denials;
DROP TABLE model_usage;
