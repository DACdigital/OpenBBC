-- migrations/008_add_agent_bundle.sql
-- Add bundle JSONB column and drop the deprecated prompt column.
-- The bundle column holds the aikdm-generated agent bundle (single source of truth).
-- The prompt column has been empty since wizard finalize and is no longer needed.

-- +goose Up
ALTER TABLE agents ADD COLUMN bundle JSONB;
ALTER TABLE agents DROP COLUMN prompt;

-- +goose Down
ALTER TABLE agents ADD COLUMN prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE agents DROP COLUMN bundle;
