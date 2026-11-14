-- +goose Up
-- Email docks (dock.ChannelEmail, dock/adapters/dockemail): a dock is one
-- mailbox read over IMAP and answered over SMTP with a password (kind
-- 'password'; a later kind may add OAuth). The password is sealed
-- (internal/seal, info "seaport email v1", purpose naming the dock and the
-- field). The list of channels names the ones of the other dock migrations
-- so they apply in any order.
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack', 'webhook', 'email'));

CREATE TABLE email_accounts (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    kind            text NOT NULL DEFAULT 'password' CHECK (kind IN ('password')),
    address         text NOT NULL,
    display_name    text NOT NULL DEFAULT '',
    imap_host       text NOT NULL,
    imap_port       integer NOT NULL CHECK (imap_port BETWEEN 1 AND 65535),
    imap_tls        text NOT NULL CHECK (imap_tls IN ('tls', 'starttls')),
    smtp_host       text NOT NULL,
    smtp_port       integer NOT NULL CHECK (smtp_port BETWEEN 1 AND 65535),
    smtp_tls        text NOT NULL CHECK (smtp_tls IN ('tls', 'starttls')),
    username        text NOT NULL,
    password        bytea NOT NULL,
    folder          text NOT NULL DEFAULT 'INBOX',
    -- Where the session is in the folder: the highest UID taken in (a new
    -- link starts at the folder's current end) and the UIDVALIDITY it
    -- belongs to (0: not known yet).
    last_uid        bigint NOT NULL DEFAULT 0,
    uid_validity    bigint NOT NULL DEFAULT 0,
    -- The owner's last word on the connection, for the admin API.
    connected       boolean NOT NULL DEFAULT false,
    last_error      text NOT NULL DEFAULT '',
    seen_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- One row per email thread of a dock: what a reply needs (who to write to,
-- the subject, the chain of message ids). chat_id is the conversation's
-- external id.
CREATE TABLE email_threads (
    dock_id         uuid NOT NULL,
    organization_id uuid NOT NULL,
    chat_id         text NOT NULL,
    subject         text NOT NULL DEFAULT '',
    subject_key     text NOT NULL DEFAULT '',
    participants    jsonb NOT NULL DEFAULT '[]',
    last_message_id text NOT NULL DEFAULT '',
    refs            jsonb NOT NULL DEFAULT '[]',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dock_id, chat_id),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX email_threads_subject ON email_threads (dock_id, subject_key, updated_at DESC);

-- Message-ID → thread: replies name the message they answer (In-Reply-To,
-- References), inbound and sent alike.
CREATE TABLE email_messages (
    dock_id         uuid NOT NULL,
    organization_id uuid NOT NULL,
    message_id      text NOT NULL,
    chat_id         text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dock_id, message_id),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE email_messages;
DROP TABLE email_threads;
DROP TABLE email_accounts;
DELETE FROM docks WHERE channel = 'email';
ALTER TABLE docks DROP CONSTRAINT docks_channel_check;
ALTER TABLE docks ADD CONSTRAINT docks_channel_check CHECK (channel IN ('whatsapp', 'api', 'github', 'jira', 'slack', 'webhook'));
