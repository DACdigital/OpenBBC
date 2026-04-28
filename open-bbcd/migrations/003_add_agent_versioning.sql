-- migrations/003_add_agent_versioning.sql

-- +goose Up
ALTER TABLE agents
  ADD COLUMN parent_version_id UUID REFERENCES agents(id),
  ADD COLUMN status VARCHAR(50) NOT NULL DEFAULT 'DRAFT';

CREATE INDEX idx_agents_parent_version_id ON agents(parent_version_id);

-- +goose Down
DROP INDEX IF EXISTS idx_agents_parent_version_id;
ALTER TABLE agents
  DROP COLUMN IF EXISTS parent_version_id,
  DROP COLUMN IF EXISTS status;
