-- +goose Up
-- dock_destinations: the places the owner lets an agent open a new
-- conversation at (a Slack channel, a WhatsApp chat...), by name. An agent
-- "opens" one through the MCP tool: Seaport posts the first message there,
-- creates the conversation allowed (the owner chose the place) and links it
-- to the thread the agent is working on.
CREATE TABLE dock_destinations (
    organization_id uuid NOT NULL,
    dock_id         uuid NOT NULL,
    name            text NOT NULL,
    target          text NOT NULL,
    title           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, dock_id, name),
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE dock_destinations;
