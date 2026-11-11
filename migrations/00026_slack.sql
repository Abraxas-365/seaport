-- +goose Up
-- Slack docks (dock.ChannelSlack, dock/adapters/dockslack): a dock is one
-- Slack App, made from a manifest (or by hand); its client secret, signing
-- secret and bot token are sealed (internal/seal, info "seaport slack v1",
-- purpose naming the dock and the field). slack_threads are the threads
-- Seaport answers in: follow-ups there reach the crew without a mention.
-- The list of channels includes jira on purpose (migration 00025), so the
-- two migrations work in either order.
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack'));

CREATE TABLE slack_apps (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    app_id          text NOT NULL DEFAULT '',
    name            text NOT NULL DEFAULT '',
    team_id         text NOT NULL DEFAULT '',
    team_name       text NOT NULL DEFAULT '',
    team_url        text NOT NULL DEFAULT '',
    bot_user_id     text NOT NULL DEFAULT '',
    bot_id          text NOT NULL DEFAULT '',
    client_id       text NOT NULL DEFAULT '',
    -- NULL: the app was made by hand (no OAuth client known) or not yet.
    client_secret   bytea,
    signing_secret  bytea NOT NULL,
    -- NULL until the app is installed (OAuth callback) or pasted.
    bot_token       bytea,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

CREATE TABLE slack_threads (
    dock_id         uuid NOT NULL REFERENCES slack_apps (dock_id) ON DELETE CASCADE,
    organization_id uuid NOT NULL,
    channel         text NOT NULL,
    thread_ts       text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dock_id, channel, thread_ts),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE slack_threads;
DROP TABLE slack_apps;
DELETE FROM docks WHERE channel = 'slack';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github'));
