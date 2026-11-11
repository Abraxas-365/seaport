-- +goose Up
-- GitHub docks (dock.ChannelGitHub, dock/adapters/dockgithub): a dock is
-- one GitHub App, registered from a manifest; its private key, webhook
-- secret and client secret are sealed (internal/seal, info
-- "seaport github v1", purpose naming the dock and the field). An App may
-- be installed on several accounts (users or organizations).
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github'));

CREATE TABLE github_apps (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    app_id          bigint NOT NULL,
    slug            text NOT NULL,
    name            text NOT NULL,
    owner           text NOT NULL DEFAULT '',
    owner_type      text NOT NULL DEFAULT '',
    html_url        text NOT NULL,
    client_id       text NOT NULL DEFAULT '',
    private_key     bytea NOT NULL,
    webhook_secret  bytea NOT NULL,
    client_secret   bytea NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

CREATE TABLE github_installations (
    dock_id         uuid NOT NULL REFERENCES github_apps (dock_id) ON DELETE CASCADE,
    organization_id uuid NOT NULL,
    installation_id bigint NOT NULL,
    account         text NOT NULL,
    account_type    text NOT NULL DEFAULT '',
    suspended       boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dock_id, installation_id),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE github_installations;
DROP TABLE github_apps;
DELETE FROM docks WHERE channel = 'github';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api'));
