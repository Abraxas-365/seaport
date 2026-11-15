-- +goose Up
-- Browser assist (plan browser-assist.md, step 3b): the connector detects
-- pages only a person can pass (a sign-in, a captcha, a 2FA code) and asks
-- for the takeover itself. browser_detect turns that on per profile
-- version; browser_ignore_hosts lists sites (and their subdomains) it
-- never asks for. Both need browser on.
ALTER TABLE agent_profiles ADD COLUMN browser_detect boolean NOT NULL DEFAULT false;
ALTER TABLE agent_profiles ADD COLUMN browser_ignore_hosts jsonb NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE agent_profiles DROP COLUMN browser_ignore_hosts;
ALTER TABLE agent_profiles DROP COLUMN browser_detect;
