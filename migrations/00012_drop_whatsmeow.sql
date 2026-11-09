-- +goose Up
-- whatsmeow's own sqlstore tables (whatsmeow_*, used before 00008) held the
-- WhatsApp keys in clear. The previous release copied every linked device
-- into the sealed store (wa_*) at start and kept these one release for a
-- rollback; this drops them all. A linked dock whose device is still only
-- here (the previous release never started on this database) stops the
-- migration instead of losing its link.
-- +goose StatementBegin
DO $$
DECLARE
    stranded int;
    t text;
BEGIN
    IF to_regclass('whatsmeow_device') IS NOT NULL THEN
        EXECUTE $q$
            SELECT count(*) FROM docks d
            WHERE d.channel = 'whatsapp' AND d.status = 'linked'
              AND EXISTS (SELECT 1 FROM whatsmeow_device w WHERE w.jid = d.address)
              AND NOT EXISTS (SELECT 1 FROM wa_devices x WHERE x.dock_id = d.id)
        $q$ INTO stranded;
        IF stranded > 0 THEN
            RAISE EXCEPTION '% linked WhatsApp dock(s) are still only in the old whatsmeow_* tables: start the previous Seaport release once (it moves them to the sealed store), or unlink them there, then upgrade', stranded;
        END IF;
    END IF;
    FOR t IN SELECT tablename FROM pg_tables
             WHERE schemaname = current_schema() AND tablename LIKE 'whatsmeow\_%' LOOP
        EXECUTE format('DROP TABLE IF EXISTS %I CASCADE', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- The dropped tables are not restored (their keys live sealed in wa_*).
SELECT 1;
