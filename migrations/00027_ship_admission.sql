-- +goose Up
-- What customs needs to admit a message later: whether it addressed the
-- account (mentions mode) and its sender's handles (deny rules). With them
-- a group's messages held while it was pending are delivered once it is
-- allowed, as a direct chat's are (conversation.Gate). The GitHub, Slack
-- and Jira docks forward only messages that address the account, so their
-- messages waiting now are marked addressed.
ALTER TABLE ships ADD COLUMN addressed boolean NOT NULL DEFAULT false;
ALTER TABLE ships ADD COLUMN sender_handles jsonb NOT NULL DEFAULT '[]';
UPDATE ships s SET addressed = true
FROM conversations c, docks d
WHERE s.organization_id = c.organization_id AND s.conversation_id = c.id
  AND d.organization_id = c.organization_id AND d.id = c.dock_id
  AND d.channel IN ('github', 'slack', 'jira')
  AND s.direction = 'in' AND s.status = 'held' AND s.hold_reason = 'conversation pending';

-- +goose Down
ALTER TABLE ships DROP COLUMN sender_handles;
ALTER TABLE ships DROP COLUMN addressed;
