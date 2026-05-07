-- migrations/005_add_agent_discovery_file_path.sql

-- +goose Up
ALTER TABLE agents
  ADD COLUMN discovery_file_path VARCHAR(255);

-- +goose Down
ALTER TABLE agents
  DROP COLUMN IF EXISTS discovery_file_path;
