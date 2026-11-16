-- +goose Up
-- Server tool calls the provider runs and bills per call on top of tokens
-- (Anthropic's usage.server_tool_use: web search, web fetch), metered by
-- the gateway with the rest of a response's usage.
ALTER TABLE model_usage
    ADD COLUMN web_search_requests bigint NOT NULL DEFAULT 0,
    ADD COLUMN web_fetch_requests bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE model_usage
    DROP COLUMN web_search_requests,
    DROP COLUMN web_fetch_requests;
