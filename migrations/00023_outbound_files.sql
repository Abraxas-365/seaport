-- +goose Up
-- Agents send files with their replies: outbound ships carry attachments
-- too, images or any other file (shown as a download, never rendered).
ALTER TABLE ship_attachments DROP CONSTRAINT ship_attachments_kind_check;
ALTER TABLE ship_attachments ADD CONSTRAINT ship_attachments_kind_check CHECK (kind IN ('image', 'document'));

-- +goose Down
DELETE FROM ship_attachments WHERE kind = 'document';
ALTER TABLE ship_attachments DROP CONSTRAINT ship_attachments_kind_check;
ALTER TABLE ship_attachments ADD CONSTRAINT ship_attachments_kind_check CHECK (kind IN ('image'));
