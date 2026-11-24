-- 031_static_ids.sql
--
-- declarative-seed (spec: openbbc-docs docs/superpowers/specs/2026-11-24-declarative-seed-design.md §1).
-- Operator-chosen static IDs on agents and agent versions, and a
-- managed_by_seed flag on tool backends. Additive; existing rows get
-- NULL static_id / managed_by_seed=false.

-- +goose Up
ALTER TABLE agents
  ADD COLUMN static_id TEXT NULL UNIQUE,
  ADD CONSTRAINT agents_static_id_format
    CHECK (static_id ~ '^[a-z0-9][a-z0-9._-]{0,99}$'
       AND static_id !~ '^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$');

ALTER TABLE agent_versions
  ADD COLUMN static_id TEXT NULL UNIQUE,
  ADD CONSTRAINT agent_versions_static_id_format
    CHECK (static_id ~ '^[a-z0-9][a-z0-9._-]{0,99}$'
       AND static_id !~ '^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$');

ALTER TABLE tool_backends
  ADD COLUMN managed_by_seed BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tool_backends DROP COLUMN managed_by_seed;
ALTER TABLE agent_versions DROP CONSTRAINT agent_versions_static_id_format, DROP COLUMN static_id;
ALTER TABLE agents DROP CONSTRAINT agents_static_id_format, DROP COLUMN static_id;
