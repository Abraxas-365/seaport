-- +goose Up
-- replies (dock.Dock.Replies): the owner's switch of whether the crew may
-- answer on this dock. Off, the dock is a trigger: its messages still wake
-- the agent (customs, routing and history as before), but nothing is
-- posted there; the agent answers on another door of the thread or stays
-- silent. The channel's own capability (dock.Channel.Replies) is code.
ALTER TABLE docks ADD COLUMN replies boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE docks DROP COLUMN replies;
