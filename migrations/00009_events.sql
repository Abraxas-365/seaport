-- +goose Up
-- The event log (event.Log, eventpg): every process appends here and fans
-- out what any process appended (LISTEN seaport_events), so live clients
-- see every replica's changes and resume by id on any replica. Kept for
-- config.EventRetention, then swept.
CREATE TABLE events (
    id              bigserial PRIMARY KEY,
    organization_id uuid NOT NULL,
    type            text NOT NULL,
    data            json NOT NULL,
    at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_organization ON events (organization_id, id);
CREATE INDEX events_at ON events (at);

-- +goose Down
DROP TABLE events;
