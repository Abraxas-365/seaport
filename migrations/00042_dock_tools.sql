-- +goose Up
-- Agent tools per dock (dock.Tools): the owner's switches of what agents
-- working the dock may read besides the messages they are handed. All on
-- by default, as before. They bind agents only; people read what their
-- role allows.
--   tool_history        earlier messages of the dock's conversations
--   tool_conversations  the conversations tool lists the dock's chats
--   tool_attachments    the files people send (images in prompts, the
--                       attachment tool)
ALTER TABLE docks
    ADD COLUMN tool_history boolean NOT NULL DEFAULT true,
    ADD COLUMN tool_conversations boolean NOT NULL DEFAULT true,
    ADD COLUMN tool_attachments boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE docks
    DROP COLUMN tool_history,
    DROP COLUMN tool_conversations,
    DROP COLUMN tool_attachments;
