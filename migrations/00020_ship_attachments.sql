-- +goose Up
-- Media of inbound ships (images first). Rows are the index; the bytes
-- live sealed in blob storage (SEAPORT_BLOB_URL) under blob_key.
ALTER TABLE ships ADD CONSTRAINT ships_organization_id_id_key UNIQUE (organization_id, id);

CREATE TABLE ship_attachments (
    organization_id uuid NOT NULL,
    id              uuid NOT NULL,
    ship_id         uuid NOT NULL,
    position        integer NOT NULL CHECK (position >= 0),
    kind            text NOT NULL CHECK (kind IN ('image')),
    mime            text NOT NULL,
    size            bigint NOT NULL CHECK (size > 0),
    name            text NOT NULL DEFAULT '',
    sha256          text NOT NULL,
    blob_key        text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (ship_id, position),
    FOREIGN KEY (organization_id, ship_id) REFERENCES ships (organization_id, id) ON DELETE CASCADE
);

-- Blobs to delete once due: staged before a Put (deleted when the row
-- that uses the blob commits; a blob whose row never came is swept after
-- ship.TrashGrace) and every blob whose row is gone (a dock, conversation
-- or ship deleted: the cascade fires the trigger).
CREATE TABLE blob_trash (
    blob_key        text PRIMARY KEY,
    organization_id uuid NOT NULL,
    due_at          timestamptz NOT NULL
);
CREATE INDEX blob_trash_due ON blob_trash (due_at);

-- +goose StatementBegin
CREATE FUNCTION ship_attachment_trash() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO blob_trash (blob_key, organization_id, due_at)
    VALUES (OLD.blob_key, OLD.organization_id, now())
    ON CONFLICT (blob_key) DO UPDATE SET due_at = now();
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER ship_attachments_trash AFTER DELETE ON ship_attachments
    FOR EACH ROW EXECUTE FUNCTION ship_attachment_trash();

-- +goose Down
DROP TRIGGER ship_attachments_trash ON ship_attachments;
DROP FUNCTION ship_attachment_trash();
DROP TABLE blob_trash;
DROP TABLE ship_attachments;
ALTER TABLE ships DROP CONSTRAINT ships_organization_id_id_key;
