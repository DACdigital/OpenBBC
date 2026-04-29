-- migrations/003_add_agent_versioning.sql

-- +goose Up
ALTER TABLE agents
  ADD COLUMN parent_version_id UUID REFERENCES agents(id) ON DELETE NO ACTION,
  -- Status lifecycle: INITIALIZING -> DRAFT -> TESTED -> DEPLOYED
  ADD COLUMN status VARCHAR(50) NOT NULL DEFAULT 'DRAFT'
    CHECK (status IN ('INITIALIZING', 'DRAFT', 'TESTED', 'DEPLOYED'));

CREATE INDEX idx_agents_parent_version_id ON agents(parent_version_id);

-- +goose Down
DROP INDEX IF EXISTS idx_agents_parent_version_id;
ALTER TABLE agents
  DROP COLUMN IF EXISTS parent_version_id,
  DROP COLUMN IF EXISTS status;
