-- +goose Up
-- Datadog docks (dock.ChannelDatadog, dock/adapters/dockdatadog): a dock is
-- one endpoint Datadog's webhook integration calls
-- (POST /datadog/webhook/{dock}) with a secret in a header. Alerts become
-- messages of a conversation per monitor and scope; the dock never posts
-- back. The mirror names a destination of another dock (dock_destinations)
-- where Seaport opens a conversation for each new alert thread, so people
-- and the agent talk there. The secret is sealed (internal/seal, info
-- "seaport datadog v1", purpose naming the dock and the field). The list of
-- channels names the ones of the other dock migrations so they apply in any
-- order (a later migration that adds a channel must list this one too).
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack', 'webhook', 'email', 'datadog'));

CREATE TABLE datadog_docks (
    dock_id            uuid PRIMARY KEY,
    organization_id    uuid NOT NULL,
    secret             bytea NOT NULL,
    mirror_dock        uuid,
    mirror_destination text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CHECK ((mirror_dock IS NULL) = (mirror_destination IS NULL)),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE,
    -- Dropping the destination clears the mirror (PG 15+: the column list
    -- keeps organization_id, which the other key needs).
    FOREIGN KEY (organization_id, mirror_dock, mirror_destination)
        REFERENCES dock_destinations (organization_id, dock_id, name) ON DELETE SET NULL (mirror_dock, mirror_destination)
);

-- +goose Down
DROP TABLE datadog_docks;
DELETE FROM docks WHERE channel = 'datadog';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack', 'webhook', 'email'));
