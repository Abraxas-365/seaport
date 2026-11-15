-- +goose Up
-- Browser takeovers (plan browser-assist.md): login_requests becomes
-- takeovers. One row per host the agent asked a person to take over its
-- browser at: {host, kind (login|captcha|2fa|other), note, conversation_id,
-- at, notified_at, done_at}, newest first, at most 8. Existing requests
-- keep their host and time as kind login; reason becomes note.
ALTER TABLE agents RENAME COLUMN login_requests TO takeovers;
UPDATE agents SET takeovers = (
  SELECT COALESCE(jsonb_agg(jsonb_build_object(
    'host', r->>'host', 'kind', 'login', 'note', COALESCE(r->>'reason', ''), 'at', r->'at')), '[]'::jsonb)
  FROM jsonb_array_elements(takeovers) AS r)
WHERE takeovers <> '[]'::jsonb;

-- +goose Down
UPDATE agents SET takeovers = (
  SELECT COALESCE(jsonb_agg(jsonb_build_object(
    'host', r->>'host', 'reason', COALESCE(r->>'note', ''), 'at', r->'at')), '[]'::jsonb)
  FROM jsonb_array_elements(takeovers) AS r)
WHERE takeovers <> '[]'::jsonb;
ALTER TABLE agents RENAME COLUMN takeovers TO login_requests;
