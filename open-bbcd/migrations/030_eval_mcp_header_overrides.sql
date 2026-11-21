-- 030_eval_mcp_header_overrides.sql
--
-- eval-mcp-tools (spec: openbbc-docs docs/superpowers/specs/2026-11-21-eval-mcp-tools-design.md,
-- § Contracts → 1). Per-backend MCP headers on evals:
-- {"<backend name>": {"<Header>": "<value>"}}. Applied on export-time
-- discovery and on aikdm's real tools/call. Distinct from the flat
-- header_overrides (migration 023), which keeps applying to HTTP endpoint
-- tools only. Additive; existing rows get {}.

-- +goose Up
ALTER TABLE evals
  ADD COLUMN mcp_header_overrides JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD CONSTRAINT evals_mcp_header_overrides_object
    CHECK (jsonb_typeof(mcp_header_overrides) = 'object');

-- +goose Down
ALTER TABLE evals
  DROP CONSTRAINT evals_mcp_header_overrides_object,
  DROP COLUMN mcp_header_overrides;
