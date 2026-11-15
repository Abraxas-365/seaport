-- +goose Up
-- Vaults (vault.Vault): an organization's shared memory. A vault is a named
-- set of markdown entries; it is granted to agents (seen everywhere) and to
-- docks (seen while an agent works a conversation of that dock), each grant
-- with a mode, read or write. Every save of an entry appends a version
-- (who wrote it, from which conversation), so a bad write can be reverted;
-- deleting an entry keeps its versions until the vault goes. Entries are
-- searched with full text ('simple' configuration: names and bodies are in
-- any language). Deleting a vault or a dock drops its grants; an agent is
-- soft deleted, so its grants stay (the queries skip deleted agents).
CREATE TABLE vaults (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    name            text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    title           text NOT NULL DEFAULT '',
    description     text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name)
);

CREATE TABLE vault_entries (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    vault_id        uuid NOT NULL,
    name            text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    body            text NOT NULL,
    version         integer NOT NULL CHECK (version >= 1),
    written_by_kind text NOT NULL CHECK (written_by_kind IN ('agent', 'user')),
    written_by      text NOT NULL DEFAULT '',
    conversation_id uuid,
    dock_id         uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    search          tsvector GENERATED ALWAYS AS (to_tsvector('simple', name || ' ' || body)) STORED,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, vault_id, name),
    FOREIGN KEY (organization_id, vault_id) REFERENCES vaults (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX vault_entries_search ON vault_entries USING gin (search);
CREATE INDEX vault_entries_recent ON vault_entries (organization_id, vault_id, updated_at DESC);

-- The history of an entry. No FK to vault_entries: the versions outlive a
-- deleted entry (until the vault goes).
CREATE TABLE vault_entry_versions (
    organization_id uuid NOT NULL,
    vault_id        uuid NOT NULL,
    entry_id        uuid NOT NULL,
    version         integer NOT NULL CHECK (version >= 1),
    name            text NOT NULL,
    body            text NOT NULL,
    written_by_kind text NOT NULL CHECK (written_by_kind IN ('agent', 'user')),
    written_by      text NOT NULL DEFAULT '',
    conversation_id uuid,
    dock_id         uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, vault_id, entry_id, version),
    FOREIGN KEY (organization_id, vault_id) REFERENCES vaults (organization_id, id) ON DELETE CASCADE
);

CREATE TABLE vault_agents (
    organization_id uuid NOT NULL,
    vault_id        uuid NOT NULL,
    agent_id        uuid NOT NULL,
    mode            text NOT NULL CHECK (mode IN ('read', 'write')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, vault_id, agent_id),
    FOREIGN KEY (organization_id, vault_id) REFERENCES vaults (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX vault_agents_agent ON vault_agents (organization_id, agent_id);

CREATE TABLE vault_docks (
    organization_id uuid NOT NULL,
    vault_id        uuid NOT NULL,
    dock_id         uuid NOT NULL,
    mode            text NOT NULL CHECK (mode IN ('read', 'write')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, vault_id, dock_id),
    FOREIGN KEY (organization_id, vault_id) REFERENCES vaults (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX vault_docks_dock ON vault_docks (organization_id, dock_id);

-- +goose Down
DROP TABLE vault_docks;
DROP TABLE vault_agents;
DROP TABLE vault_entry_versions;
DROP TABLE vault_entries;
DROP TABLE vaults;
