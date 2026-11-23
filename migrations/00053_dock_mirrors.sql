-- +goose Up
-- Dock mirrors (dock.Mirror, shipsvc.Receive): a dock that only triggers (its
-- channel can't reply, or the owner turned its replies off) is given an
-- output: a destination of another dock, where Seaport opens a conversation
-- for each new thread that arrives, linked to the arriving one, so people and
-- the agent talk there. Until now only Datadog docks had one
-- (datadog_docks.mirror_*, migration 00035); it is a property of any dock
-- now, so it lives on docks and the ship service opens it for every channel.
-- Dropping the destination clears the mirror (PG 15+: the column list keeps
-- organization_id, which the docks primary key needs).
ALTER TABLE docks ADD COLUMN mirror_dock uuid;
ALTER TABLE docks ADD COLUMN mirror_destination text;
ALTER TABLE docks ADD CONSTRAINT docks_mirror_pair CHECK ((mirror_dock IS NULL) = (mirror_destination IS NULL));
ALTER TABLE docks ADD CONSTRAINT docks_mirror_not_self CHECK (mirror_dock IS NULL OR mirror_dock <> id);
-- A dock that replies answers in place, so it has no mirror: its replies
-- switch is off, or its channel can't reply (dock.Channel.Replies). docksvc
-- checks it too; this closes the race of turning replies on meanwhile.
ALTER TABLE docks ADD CONSTRAINT docks_mirror_trigger_only
    CHECK (mirror_dock IS NULL OR NOT replies OR channel IN ('webhook', 'datadog'));
ALTER TABLE docks ADD CONSTRAINT docks_mirror_destination
    FOREIGN KEY (organization_id, mirror_dock, mirror_destination)
    REFERENCES dock_destinations (organization_id, dock_id, name) ON DELETE SET NULL (mirror_dock, mirror_destination);

UPDATE docks d SET mirror_dock = dd.mirror_dock, mirror_destination = dd.mirror_destination
  FROM datadog_docks dd WHERE dd.dock_id = d.id AND dd.mirror_dock IS NOT NULL;

-- Dropping the columns drops the pair check and the destination key with them.
ALTER TABLE datadog_docks DROP COLUMN mirror_destination, DROP COLUMN mirror_dock;

-- +goose Down
ALTER TABLE datadog_docks ADD COLUMN mirror_dock uuid;
ALTER TABLE datadog_docks ADD COLUMN mirror_destination text;
UPDATE datadog_docks dd SET mirror_dock = d.mirror_dock, mirror_destination = d.mirror_destination
  FROM docks d WHERE d.id = dd.dock_id AND d.mirror_dock IS NOT NULL;
ALTER TABLE datadog_docks ADD CONSTRAINT datadog_docks_mirror_pair CHECK ((mirror_dock IS NULL) = (mirror_destination IS NULL));
ALTER TABLE datadog_docks ADD CONSTRAINT datadog_docks_mirror_destination
    FOREIGN KEY (organization_id, mirror_dock, mirror_destination)
    REFERENCES dock_destinations (organization_id, dock_id, name) ON DELETE SET NULL (mirror_dock, mirror_destination);
ALTER TABLE docks DROP CONSTRAINT docks_mirror_destination;
ALTER TABLE docks DROP CONSTRAINT docks_mirror_trigger_only;
ALTER TABLE docks DROP CONSTRAINT docks_mirror_not_self;
ALTER TABLE docks DROP CONSTRAINT docks_mirror_pair;
ALTER TABLE docks DROP COLUMN mirror_destination, DROP COLUMN mirror_dock;
