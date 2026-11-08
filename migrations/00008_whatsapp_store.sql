-- +goose Up
-- Seaport's own WhatsApp (whatsmeow) store, replacing whatsmeow's sqlstore
-- tables (whatsmeow_*): one device per dock, rows keyed by dock, so deleting
-- a dock deletes its keys. Secret values (private keys, signal sessions,
-- sender keys, app-state keys, message secrets, privacy tokens, buffered
-- plaintexts) are sealed by dockwa (AES-256-GCM, HKDF of SEAPORT_SECRET_KEY,
-- bound to their row). Lookup columns stay clear. Existing whatsmeow_*
-- devices are copied in by dockwa at start; whatsmeow_* is left in place
-- for one release (rollback) and dropped by a later migration.

CREATE TABLE wa_devices (
    dock_id              uuid PRIMARY KEY,
    organization_id      uuid NOT NULL,
    jid                  text NOT NULL UNIQUE,
    lid                  text NOT NULL DEFAULT '',
    registration_id      bigint NOT NULL,
    signed_pre_key_id    integer NOT NULL,
    platform             text NOT NULL DEFAULT '',
    business_name        text NOT NULL DEFAULT '',
    push_name            text NOT NULL DEFAULT '',
    facebook_uuid        uuid,
    lid_migration_ts     bigint NOT NULL DEFAULT 0,
    companion_meta_nonce text NOT NULL DEFAULT '',
    secrets              bytea NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

CREATE TABLE wa_identity_keys (
    dock_id  uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    their_id text NOT NULL,
    identity bytea NOT NULL,
    PRIMARY KEY (dock_id, their_id)
);

CREATE TABLE wa_pre_keys (
    dock_id  uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    key_id   integer NOT NULL CHECK (key_id >= 0 AND key_id < 16777216),
    key      bytea NOT NULL,
    uploaded boolean NOT NULL,
    PRIMARY KEY (dock_id, key_id)
);

CREATE TABLE wa_sessions (
    dock_id  uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    their_id text NOT NULL,
    session  bytea NOT NULL,
    PRIMARY KEY (dock_id, their_id)
);

CREATE TABLE wa_sender_keys (
    dock_id    uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    chat_id    text NOT NULL,
    sender_id  text NOT NULL,
    sender_key bytea NOT NULL,
    PRIMARY KEY (dock_id, chat_id, sender_id)
);

CREATE TABLE wa_app_state_sync_keys (
    dock_id     uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    key_id      bytea NOT NULL,
    key_data    bytea NOT NULL,
    timestamp   bigint NOT NULL,
    fingerprint bytea NOT NULL,
    PRIMARY KEY (dock_id, key_id)
);

CREATE TABLE wa_app_state_versions (
    dock_id uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    name    text NOT NULL,
    version bigint NOT NULL,
    hash    bytea NOT NULL CHECK (length(hash) = 128),
    PRIMARY KEY (dock_id, name)
);

CREATE TABLE wa_app_state_mutation_macs (
    dock_id   uuid NOT NULL,
    name      text NOT NULL,
    version   bigint NOT NULL,
    index_mac bytea NOT NULL CHECK (length(index_mac) = 32),
    value_mac bytea NOT NULL CHECK (length(value_mac) = 32),
    PRIMARY KEY (dock_id, name, version, index_mac),
    FOREIGN KEY (dock_id, name) REFERENCES wa_app_state_versions (dock_id, name) ON DELETE CASCADE
);

CREATE TABLE wa_contacts (
    dock_id        uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    their_jid      text NOT NULL,
    first_name     text,
    full_name      text,
    push_name      text,
    business_name  text,
    redacted_phone text,
    PRIMARY KEY (dock_id, their_jid)
);

CREATE TABLE wa_chat_settings (
    dock_id             uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    chat_jid            text NOT NULL,
    muted_until         bigint NOT NULL DEFAULT 0,
    pinned              boolean NOT NULL DEFAULT false,
    archived            boolean NOT NULL DEFAULT false,
    wasa_root_secret_id text NOT NULL DEFAULT '',
    PRIMARY KEY (dock_id, chat_jid)
);

CREATE TABLE wa_message_secrets (
    dock_id    uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    chat_jid   text NOT NULL,
    sender_jid text NOT NULL,
    message_id text NOT NULL,
    key        bytea NOT NULL,
    PRIMARY KEY (dock_id, chat_jid, sender_jid, message_id)
);

CREATE TABLE wa_privacy_tokens (
    dock_id          uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    their_jid        text NOT NULL,
    token            bytea NOT NULL,
    timestamp        bigint NOT NULL,
    sender_timestamp bigint,
    PRIMARY KEY (dock_id, their_jid)
);
CREATE INDEX wa_privacy_tokens_timestamp ON wa_privacy_tokens (dock_id, timestamp);

CREATE TABLE wa_nct_salts (
    dock_id uuid PRIMARY KEY REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    salt    bytea NOT NULL
);

-- Phone number <-> LID of the users this dock has seen (whatsmeow keeps one
-- global map; here it is per dock, so organizations share nothing).
CREATE TABLE wa_lid_map (
    dock_id uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    lid     text NOT NULL,
    pn      text NOT NULL,
    PRIMARY KEY (dock_id, lid),
    UNIQUE (dock_id, pn)
);

CREATE TABLE wa_event_buffer (
    dock_id          uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    ciphertext_hash  bytea NOT NULL CHECK (length(ciphertext_hash) = 32),
    plaintext        bytea,
    server_timestamp bigint NOT NULL,
    insert_timestamp bigint NOT NULL,
    PRIMARY KEY (dock_id, ciphertext_hash)
);
CREATE INDEX wa_event_buffer_inserted ON wa_event_buffer (dock_id, insert_timestamp);

CREATE TABLE wa_retry_buffer (
    dock_id    uuid NOT NULL REFERENCES wa_devices (dock_id) ON DELETE CASCADE,
    chat_jid   text NOT NULL,
    message_id text NOT NULL,
    format     text NOT NULL,
    plaintext  bytea NOT NULL,
    timestamp  bigint NOT NULL,
    PRIMARY KEY (dock_id, chat_jid, message_id)
);
CREATE INDEX wa_retry_buffer_timestamp ON wa_retry_buffer (dock_id, timestamp);

-- +goose Down
DROP TABLE wa_retry_buffer;
DROP TABLE wa_event_buffer;
DROP TABLE wa_lid_map;
DROP TABLE wa_nct_salts;
DROP TABLE wa_privacy_tokens;
DROP TABLE wa_message_secrets;
DROP TABLE wa_chat_settings;
DROP TABLE wa_contacts;
DROP TABLE wa_app_state_mutation_macs;
DROP TABLE wa_app_state_versions;
DROP TABLE wa_app_state_sync_keys;
DROP TABLE wa_sender_keys;
DROP TABLE wa_sessions;
DROP TABLE wa_pre_keys;
DROP TABLE wa_identity_keys;
DROP TABLE wa_devices;
