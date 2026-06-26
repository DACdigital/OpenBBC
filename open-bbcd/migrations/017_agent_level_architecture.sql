-- migrations/017_agent_level_architecture.sql
--
-- Move "architecture" (endpoints, flows, skills metadata, external_mcps
-- metadata) from the per-version bundle blob onto the agent itself, where
-- it is frozen on first version creation (finalized_at). Per-version data
-- shrinks to just the editable "prompts" payload (main_prompt + skill
-- prompts). Endpoint→backend wiring moves with the architecture: it's a
-- structural choice keyed by agent_id, not by version. MCP attachments
-- stay version-scoped because their guidance note is editable per version.
--
-- DESTRUCTIVE: drops agent_versions.bundle and agent_version_endpoint_backend
-- with no backfill. The seed script regenerates everything.

-- +goose Up
ALTER TABLE agents ADD COLUMN architecture JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agents ADD COLUMN finalized_at TIMESTAMPTZ;

ALTER TABLE agent_versions ADD COLUMN prompts JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_versions DROP COLUMN bundle;

CREATE TABLE agent_endpoint_backend (
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    endpoint_id TEXT NOT NULL,
    backend_id  UUID NOT NULL REFERENCES tool_backends(id) ON DELETE RESTRICT,
    PRIMARY KEY (agent_id, endpoint_id)
);
CREATE INDEX idx_aeb_backend ON agent_endpoint_backend(backend_id);

DROP TABLE IF EXISTS agent_version_endpoint_backend;

-- +goose Down
CREATE TABLE agent_version_endpoint_backend (
    agent_version_id UUID NOT NULL REFERENCES agent_versions(id) ON DELETE CASCADE,
    endpoint_id      TEXT NOT NULL,
    backend_id       UUID NOT NULL REFERENCES tool_backends(id) ON DELETE RESTRICT,
    PRIMARY KEY (agent_version_id, endpoint_id)
);
CREATE INDEX idx_avep_backend ON agent_version_endpoint_backend(backend_id);

DROP TABLE IF EXISTS agent_endpoint_backend;

ALTER TABLE agent_versions ADD COLUMN bundle JSONB;
ALTER TABLE agent_versions DROP COLUMN prompts;

ALTER TABLE agents DROP COLUMN finalized_at;
ALTER TABLE agents DROP COLUMN architecture;
