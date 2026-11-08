-- +goose Up
-- Live crew connections of every process (crew.Presence, crewpg): the
-- crew status, the per-organization connection quota and the reaper read
-- them. A process touches its rows (seen_at) while they live; rows not
-- touched for crewws' stale period belong to a dead process: the reaper
-- releases the ships they claimed (ships.claimed_by) and deletes them. A
-- touch also reports the process's connections whose token was revoked
-- (backstop of NOTIFY seaport_crew), which it then closes.
CREATE TABLE crew_connections (
    session         text PRIMARY KEY,
    organization_id uuid NOT NULL,
    process         text NOT NULL,
    credential      text NOT NULL DEFAULT '',
    ready           boolean NOT NULL DEFAULT false,
    agent           text NOT NULL DEFAULT '',
    concurrency     integer NOT NULL DEFAULT 0,
    steer           boolean NOT NULL DEFAULT false,
    remote          text NOT NULL DEFAULT '',
    deliveries      jsonb NOT NULL DEFAULT '[]',
    connected_at    timestamptz NOT NULL,
    seen_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crew_connections_organization ON crew_connections (organization_id, connected_at);
CREATE INDEX crew_connections_seen ON crew_connections (seen_at);
CREATE INDEX crew_connections_process ON crew_connections (process);

-- The connection (crew_connections.session) holding a claimed ship,
-- recorded by the claim itself, so a claim whose connection is gone (closed,
-- or its process died before recording the delivery) is released by session.
ALTER TABLE ships ADD COLUMN claimed_by text;
UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL WHERE status = 'claimed';
ALTER TABLE ships ADD CONSTRAINT ships_claimed_by CHECK ((status = 'claimed') = (claimed_by IS NOT NULL));
CREATE INDEX ships_claimed_by ON ships (organization_id, claimed_by) WHERE claimed_by IS NOT NULL;

-- +goose Down
DROP INDEX ships_claimed_by;
ALTER TABLE ships DROP CONSTRAINT ships_claimed_by;
ALTER TABLE ships DROP COLUMN claimed_by;
DROP TABLE crew_connections;
