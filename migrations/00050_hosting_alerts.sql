-- +goose Up
-- Alerts (hostingsvc/alerts.go): the owner of a hosted agent names a dock
-- destination (a WhatsApp chat, a Slack channel...) and the supervisor
-- tells it when a slot fails, the agent's image fails or takes too long to
-- build, or a conversation waits longer than alert_wait_seconds; and again
-- when that clears. alert_dock NULL: alerts off.
ALTER TABLE agent_hosting
    ADD COLUMN alert_dock uuid,
    ADD COLUMN alert_destination text NOT NULL DEFAULT '',
    ADD COLUMN alert_wait_seconds integer NOT NULL DEFAULT 600,
    -- alert_error: why the last notice couldn't be sent ('' when it went).
    ADD COLUMN alert_error text NOT NULL DEFAULT '',
    ADD CONSTRAINT agent_hosting_alert_dock FOREIGN KEY (organization_id, alert_dock)
        REFERENCES docks (organization_id, id) ON DELETE SET NULL (alert_dock),
    ADD CONSTRAINT agent_hosting_alert_destination CHECK (alert_dock IS NULL OR alert_destination <> '');

-- hosting_alerts: one row per condition raised (kind + key: the slot's
-- index, the build's digest, '' for waiting conversations), open until it
-- clears. sent_at: when its notice went out; done_at: nothing more to send
-- (its "resolved" notice went, or it cleared before its notice went, or
-- the agent's alerts went off or elsewhere). send_after: not before (a
-- send in progress or a failed one waits hosting.AlertRetry).
-- conversation_id: where the notice went, so the resolved one answers
-- there (a Slack thread).
CREATE TABLE hosting_alerts (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id  uuid NOT NULL,
    agent_id         uuid NOT NULL,
    kind             text NOT NULL,
    key              text NOT NULL DEFAULT '',
    detail           text NOT NULL DEFAULT '',
    waiting          integer NOT NULL DEFAULT 0,
    since            timestamptz NOT NULL,
    raised_at        timestamptz NOT NULL DEFAULT now(),
    resolved_at      timestamptz,
    sent_at          timestamptz,
    done_at          timestamptz,
    send_after       timestamptz,
    conversation_id  uuid,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agent_hosting (organization_id, agent_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX hosting_alerts_open ON hosting_alerts (organization_id, agent_id, kind, key) WHERE resolved_at IS NULL;
CREATE INDEX hosting_alerts_due ON hosting_alerts (organization_id, agent_id) WHERE done_at IS NULL;

-- +goose Down
DROP TABLE hosting_alerts;
ALTER TABLE agent_hosting
    DROP CONSTRAINT agent_hosting_alert_destination,
    DROP CONSTRAINT agent_hosting_alert_dock,
    DROP COLUMN alert_error,
    DROP COLUMN alert_wait_seconds,
    DROP COLUMN alert_destination,
    DROP COLUMN alert_dock;
