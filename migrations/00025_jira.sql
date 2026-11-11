-- +goose Up
-- Jira docks (dock.ChannelJira, dock/adapters/dockjira): a dock is one Jira
-- Cloud site reached as a service account (its email and API token) with
-- basic auth; the token and the webhook secret are sealed (internal/seal,
-- info "seaport jira v1", purpose naming the dock and the field).
-- The channel list names slack too so this and the Slack migration apply
-- in either order.
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack'));

CREATE TABLE jira_sites (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    site            text NOT NULL,
    account_id      text NOT NULL,
    display_name    text NOT NULL DEFAULT '',
    email           text NOT NULL,
    api_token       bytea NOT NULL,
    webhook_secret  bytea NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE jira_sites;
DELETE FROM docks WHERE channel = 'jira';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github'));
