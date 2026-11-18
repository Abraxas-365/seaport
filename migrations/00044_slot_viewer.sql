-- +goose Up
-- A person in a slot's browser viewer keeps the slot busy: the process
-- that relays the viewer beats viewer_at (hosting.ViewerBeat) and the
-- supervisor reads a recent beat (hosting.ViewerStale) as work, so
-- idle_stop doesn't stop the slot and an environment update doesn't
-- restart it under the person.
ALTER TABLE agent_slots ADD COLUMN viewer_at timestamptz;

-- +goose Down
ALTER TABLE agent_slots DROP COLUMN viewer_at;
