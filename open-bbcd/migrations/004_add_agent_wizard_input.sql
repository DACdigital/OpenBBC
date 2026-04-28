-- migrations/004_add_agent_wizard_input.sql

-- +goose Up
ALTER TABLE agents
  ADD COLUMN wizard_input   JSONB,
  ADD COLUMN schema_version VARCHAR(20);

-- +goose Down
ALTER TABLE agents
  DROP COLUMN IF EXISTS wizard_input,
  DROP COLUMN IF EXISTS schema_version;
