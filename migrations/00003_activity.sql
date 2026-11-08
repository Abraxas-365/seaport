-- +goose Up
-- Inbox order: the last message in or out of a conversation.
ALTER TABLE conversations ADD COLUMN last_activity_at timestamptz NOT NULL DEFAULT now();
UPDATE conversations c SET last_activity_at = COALESCE(
    (SELECT max(s.created_at) FROM ships s WHERE s.conversation_id = c.id), c.created_at);
CREATE INDEX conversations_inbox ON conversations (last_activity_at DESC, id DESC);

-- Inbox previews: the latest ship of a conversation, and its held ships.
CREATE INDEX ships_held ON ships (conversation_id) WHERE status = 'held';

-- +goose Down
DROP INDEX ships_held;
DROP INDEX conversations_inbox;
ALTER TABLE conversations DROP COLUMN last_activity_at;
