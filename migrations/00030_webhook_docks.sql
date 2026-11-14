-- +goose Up
-- Webhook docks (dock.ChannelWebhook, dock/adapters/dockwebhook): a dock is
-- one inbound endpoint (POST /hooks/{dock}) that any system able to POST
-- JSON can call. Its secret is sealed (internal/seal, info "seaport webhook
-- v1", purpose naming the dock and the field); the template maps a body to
-- the message's fields. The list of channels names the ones of the other
-- dock migrations so they apply in any order (a later migration that adds
-- a channel must list this one too).
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack', 'webhook'));

CREATE TABLE webhook_docks (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    secret          bytea NOT NULL,
    template        jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE webhook_docks;
DELETE FROM docks WHERE channel = 'webhook';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack'));
