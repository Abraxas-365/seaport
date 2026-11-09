-- +goose Up
-- Dock ownership across processes (dock.Owners, dock.Relay; dockpg). A dock
-- whose channel keeps one live connection per account (WhatsApp) is run by
-- one process at a time: the holder of its lease here, renewed while it
-- lives. A dock whose lease ran out is claimed by another process, which
-- reconnects it.
CREATE TABLE dock_owners (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    process         text NOT NULL,
    expires_at      timestamptz NOT NULL,
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX dock_owners_process ON dock_owners (process);

-- Work another process hands to a dock's owner (link, unlink, send):
-- inserted pending with NOTIFY seaport_docks 'r:<process>', taken (running)
-- and answered (done, reply) by the owner with NOTIFY 'd:<id>'. The caller
-- deletes it once answered; a pending request whose owner lost the dock is
-- withdrawn by the caller and sent to the new owner. Stale rows are swept.
CREATE TABLE dock_requests (
    id              bigserial PRIMARY KEY,
    organization_id uuid NOT NULL,
    dock_id         uuid NOT NULL,
    process         text NOT NULL,
    request         jsonb NOT NULL,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done')),
    reply           jsonb,
    deadline        timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);
CREATE INDEX dock_requests_pending ON dock_requests (process, id) WHERE status = 'pending';
CREATE INDEX dock_requests_deadline ON dock_requests (deadline);

-- The current WhatsApp pairing code of a linking dock, sealed (dockwa), so
-- any process can show it while the dock's owner pairs.
CREATE TABLE wa_pairings (
    dock_id         uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    code            bytea NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE wa_pairings;
DROP TABLE dock_requests;
DROP TABLE dock_owners;
