-- +goose Up
-- Threads (conversation.Thread): several conversations that are one
-- matter, each a door of it (a pull request, the Slack thread where it is
-- discussed, a WhatsApp group). Every conversation has a thread_id; a
-- conversation alone is a thread of one (thread_id = id). Linking gives
-- the doors of both threads one new id; unlinking a door sets its own id
-- back. No FK: the id is opaque, and a door deleted with its dock leaves
-- the others in their thread. The crew works a thread as one: one agent
-- session, one delivery in flight, routing's previous agent across doors.
ALTER TABLE conversations ADD COLUMN thread_id uuid;
UPDATE conversations SET thread_id = id;
ALTER TABLE conversations ALTER COLUMN thread_id SET NOT NULL;
CREATE INDEX conversations_thread ON conversations (organization_id, thread_id);

-- +goose Down
DROP INDEX conversations_thread;
ALTER TABLE conversations DROP COLUMN thread_id;
