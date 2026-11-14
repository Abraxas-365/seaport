-- +goose Up
-- Schedules (schedule.Schedule): the port itself posts a prompt into a
-- conversation at given times (a cron expression on a time zone's wall
-- clock), as the system sender "system:schedule", so customs, routing,
-- delivery, threads and replies work as for any message. next_at is the
-- next slot, computed on save and advanced by the runner under FOR UPDATE
-- SKIP LOCKED, so one process fires each slot. Deleting the conversation
-- (or its dock) deletes its schedules; an agent is only named, so a
-- deleted (soft) agent leaves the schedule failing loudly at fire time.
CREATE TABLE schedules (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    agent_id        uuid,
    name            text NOT NULL,
    cron            text NOT NULL,
    timezone        text NOT NULL DEFAULT 'UTC',
    text            text NOT NULL,
    enabled         boolean NOT NULL DEFAULT true,
    next_at         timestamptz NOT NULL,
    last_at         timestamptz,
    last_ship_id    uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES conversations (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id)
);
CREATE INDEX schedules_due ON schedules (next_at) WHERE enabled;
CREATE INDEX schedules_conversation ON schedules (organization_id, conversation_id);

-- +goose Down
DROP TABLE schedules;
