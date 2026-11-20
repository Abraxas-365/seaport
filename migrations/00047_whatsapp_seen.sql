-- +goose Up
-- WhatsApp dock settings (dock/adapters/dockwa, settings.go): what the
-- dock does to a message once an agent has it (the ship is queued for
-- one; held messages and the self chat are left alone). Both are the
-- owner's choice and off until they turn them on; a dock without a row
-- has the defaults.
--   react      react to the message with `emoji` (GitHub and Slack docks
--              always react with 👀)
--   emoji      the reaction, one emoji
--   mark_read  send the read receipt (blue ticks; WhatsApp sends a
--              private one when the account's read receipts are off)
CREATE TABLE whatsapp_docks (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    react           boolean NOT NULL DEFAULT false,
    emoji           text NOT NULL DEFAULT '👀',
    mark_read       boolean NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE whatsapp_docks;
