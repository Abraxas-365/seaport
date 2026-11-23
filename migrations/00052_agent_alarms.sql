-- +goose Up
-- Agent alarms (plan agent-alarms.md): an alarm is a schedule that an agent
-- sets itself to wake up later in the conversation it is working. It reuses
-- the schedules table with three additions: run_at (a one-shot time instead
-- of a cron expression; cron stays NOT NULL text and is '' for a one-shot),
-- created_by (the agent that set it; NULL for a person's schedule) and
-- done_at (a one-shot that fired or was missed; kept visible for a while,
-- then purged by the runner).
ALTER TABLE schedules ADD COLUMN run_at timestamptz;
ALTER TABLE schedules ADD COLUMN created_by uuid;
ALTER TABLE schedules ADD COLUMN done_at timestamptz;
ALTER TABLE schedules ADD CONSTRAINT schedules_when CHECK ((run_at IS NULL) <> (cron = ''));
ALTER TABLE schedules ADD CONSTRAINT schedules_creator
    FOREIGN KEY (organization_id, created_by) REFERENCES agents (organization_id, id);
CREATE INDEX schedules_alarms ON schedules (organization_id, created_by, conversation_id)
    WHERE created_by IS NOT NULL AND done_at IS NULL;
CREATE INDEX schedules_done ON schedules (done_at) WHERE done_at IS NOT NULL;
-- A name is unique among the schedules that can still run: a done one-shot
-- (kept for DoneKeep) frees its name, so an agent can set "deploy" again
-- after its "deploy" rang. Re-arming a done one-shot whose name was taken
-- meanwhile is a Conflict.
ALTER TABLE schedules DROP CONSTRAINT schedules_organization_id_name_key;
CREATE UNIQUE INDEX schedules_live_name ON schedules (organization_id, name) WHERE done_at IS NULL;
-- The owner turns the ability on per agent profile (like browser).
ALTER TABLE agent_profiles ADD COLUMN alarms boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent_profiles DROP COLUMN alarms;
DROP INDEX schedules_done;
DROP INDEX schedules_alarms;
-- One-shots have no cron: drop them so the constraint can go.
DELETE FROM schedules WHERE cron = '';
DROP INDEX schedules_live_name;
ALTER TABLE schedules ADD CONSTRAINT schedules_organization_id_name_key UNIQUE (organization_id, name);
ALTER TABLE schedules DROP CONSTRAINT schedules_creator;
ALTER TABLE schedules DROP CONSTRAINT schedules_when;
ALTER TABLE schedules DROP COLUMN done_at;
ALTER TABLE schedules DROP COLUMN created_by;
ALTER TABLE schedules DROP COLUMN run_at;
