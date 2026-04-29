-- migrations/005_unique_parent_version_id.sql
-- Enforce linear version chains: each agent can be the parent of at most one child.

-- +goose Up
ALTER TABLE agents
  ADD CONSTRAINT agents_parent_version_id_unique UNIQUE (parent_version_id);

-- +goose Down
ALTER TABLE agents
  DROP CONSTRAINT IF EXISTS agents_parent_version_id_unique;
