-- Adds per-eval config: whether to mock MCP tool calls (default true) and
-- any header overrides applied when running against real MCPs.
-- header_overrides is a flat map[string]string (simpler than chat's
-- per-backend layout — the eval runs one target agent so a flat set of
-- overrides is enough).

-- +goose Up
ALTER TABLE evals
    ADD COLUMN mock_mcp_tools  BOOL   NOT NULL DEFAULT TRUE,
    ADD COLUMN header_overrides JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(header_overrides) = 'object');

-- +goose Down
ALTER TABLE evals
    DROP COLUMN header_overrides,
    DROP COLUMN mock_mcp_tools;
