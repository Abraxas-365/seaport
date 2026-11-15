-- +goose Up
-- Browser assist (plan browser-assist.md, step 2): a profile version says
-- whom to tell when the agent asks for a takeover (agent.Profile.BrowserAssist:
-- '' nobody, 'conversation' the one the agent is working, else a
-- destination name of one of its docks). Each takeover in agents.takeovers
-- records where its notice went (notified_at, notified_in), so the agent is
-- woken there when a person ends it.
ALTER TABLE agent_profiles ADD COLUMN browser_assist text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_profiles DROP COLUMN browser_assist;
