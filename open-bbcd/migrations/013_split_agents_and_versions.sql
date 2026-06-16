-- migrations/013_split_agents_and_versions.sql
--
-- Split the monolithic agents table into:
--   - agents (per logical agent: name, description, discovery_file_path)
--   - agent_versions (per version: status, bundle, flow_map_config,
--     flow_map_parse_error, parent_version_id, timestamps)
--
-- flow_map_config + flow_map_parse_error live on agent_versions from the
-- start: a version is a frozen "spec + bundle" snapshot, and editing the
-- spec spawns a new version (future workflow). The agents row holds only
-- the immutable starting point (name, description, discovery_file_path).
--
-- FK redirects:
--   - agent_versions.agent_id -> agents.id (was self-FK to the old agents table)
--   - chat_sessions: column rename agent_id -> agent_version_id, FK target agent_versions
--   - deployed_sessions.agent_id stays pointing at the per-agent ID, FK redirected to the new agents
--   - deployed_messages.agent_version_id keeps its meaning; FK target is the renamed agent_versions
-- The CHECK constraint agents_root_self_reference goes away (the real FK replaces it).

-- +goose Up
-- Step 1: rename the existing table out of the way.
ALTER TABLE agents RENAME TO agent_versions;
ALTER INDEX agents_pkey RENAME TO agent_versions_pkey;
ALTER INDEX agents_one_deployed_per_agent RENAME TO agent_versions_one_deployed_per_agent;
ALTER INDEX idx_agents_agent_id RENAME TO idx_agent_versions_agent_id;
ALTER INDEX idx_agents_parent_version_id RENAME TO idx_agent_versions_parent_version_id;
ALTER TABLE agent_versions DROP CONSTRAINT agents_root_self_reference;
ALTER TABLE agent_versions DROP CONSTRAINT agents_status_check;
ALTER TABLE agent_versions ADD CONSTRAINT agent_versions_status_check
  CHECK (status IN ('INITIALIZING','DRAFT','TRAINING','READY','DEPLOYED'));

-- Step 2: create the new agents table with the per-agent fields.
-- flow_map_config + flow_map_parse_error are NOT here — they stay on agent_versions.
CREATE TABLE agents (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 TEXT NOT NULL,
    description          TEXT,
    discovery_file_path  TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Step 3: backfill agents from the root version of each existing agent.
-- The root version row is the one where agent_id = id (post-PR#27 invariant).
INSERT INTO agents (id, name, description, discovery_file_path, created_at)
SELECT id, name, description, discovery_file_path, created_at
FROM agent_versions
WHERE id = agent_id;

-- Step 4: drop now-redundant per-agent columns from agent_versions.
-- flow_map_config + flow_map_parse_error stay — they're per-version.
ALTER TABLE agent_versions DROP COLUMN name;
ALTER TABLE agent_versions DROP COLUMN description;
ALTER TABLE agent_versions DROP COLUMN discovery_file_path;

-- Step 5: agent_versions.agent_id was a self-FK to the old agents table (renamed).
-- Repoint it at the new agents table.
ALTER TABLE agent_versions DROP CONSTRAINT agents_chain_root_id_fkey;
ALTER TABLE agent_versions ADD CONSTRAINT agent_versions_agent_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

-- Step 6: parent_version_id was a self-FK on agents (renamed). Keep it pointing at agent_versions.
-- Postgres tracks the rename automatically; just rename the constraint for clarity.
ALTER TABLE agent_versions RENAME CONSTRAINT agents_parent_version_id_fkey TO agent_versions_parent_version_id_fkey;

-- Step 7: chat_sessions column rename.
ALTER TABLE chat_sessions RENAME COLUMN agent_id TO agent_version_id;
ALTER TABLE chat_sessions DROP CONSTRAINT chat_sessions_agent_id_fkey;
ALTER TABLE chat_sessions ADD CONSTRAINT chat_sessions_agent_version_id_fkey
  FOREIGN KEY (agent_version_id) REFERENCES agent_versions(id) ON DELETE CASCADE;
ALTER INDEX idx_chat_sessions_agent_id_created RENAME TO idx_chat_sessions_agent_version_id_created;

-- Step 8: deployed_sessions.agent_id FK was pointing at the old agents (now agent_versions).
-- The DATA in that column is the agent_id (which is what we want); only the FK target needs to change.
ALTER TABLE deployed_sessions DROP CONSTRAINT deployed_sessions_chain_root_id_fkey;
ALTER TABLE deployed_sessions ADD CONSTRAINT deployed_sessions_agent_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

-- Step 8b: resources.agent_id was pointing at the old agents table (now agent_versions).
-- Redirect to the new per-agent agents table. Resources are conceptually per-agent
-- (the route /agents/{agent_id}/resources implies this), but pre-split they were
-- stored against version-row IDs. Dev DB has 0 resources today, so no data migration.
ALTER TABLE resources DROP CONSTRAINT resources_agent_id_fkey;
ALTER TABLE resources ADD CONSTRAINT resources_agent_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

-- Step 9: deployed_messages.agent_version_id FK is fine — it always referenced the old agents.id
-- which is now agent_versions.id (Postgres tracks the rename). The constraint is already named
-- deployed_messages_agent_version_id_fkey from migration 011 onward, so no rename needed.

-- +goose Down
-- Reverse order. Best-effort restoration; production rollback path is "goose down on a fresh DB".
-- Restores the pre-split shape where the (singular) agents table held name, description,
-- flow_map_config, flow_map_parse_error, discovery_file_path alongside the per-version fields.

ALTER TABLE chat_sessions RENAME COLUMN agent_version_id TO agent_id;
ALTER TABLE chat_sessions DROP CONSTRAINT chat_sessions_agent_version_id_fkey;
ALTER INDEX idx_chat_sessions_agent_version_id_created RENAME TO idx_chat_sessions_agent_id_created;

ALTER TABLE deployed_sessions DROP CONSTRAINT deployed_sessions_agent_id_fkey;

ALTER TABLE agent_versions DROP CONSTRAINT agent_versions_agent_id_fkey;

-- Drop resources FK now too — it references the new agents table which we're about to DROP.
-- We'll re-add it after the rename so it points at the renamed-back agents table.
ALTER TABLE resources DROP CONSTRAINT resources_agent_id_fkey;

-- Restore the per-agent columns that were dropped on the Up. flow_map_config /
-- flow_map_parse_error already exist on agent_versions (kept from the start), so
-- we backfill the agents table from those during the rename step below.
ALTER TABLE agent_versions ADD COLUMN name TEXT;
ALTER TABLE agent_versions ADD COLUMN description TEXT;
ALTER TABLE agent_versions ADD COLUMN discovery_file_path TEXT;

UPDATE agent_versions av SET
  name = a.name,
  description = a.description,
  discovery_file_path = a.discovery_file_path
FROM agents a
WHERE av.agent_id = a.id;

ALTER TABLE agent_versions ALTER COLUMN name SET NOT NULL;

DROP TABLE agents;

ALTER TABLE agent_versions RENAME TO agents;
ALTER INDEX agent_versions_pkey RENAME TO agents_pkey;
ALTER INDEX agent_versions_one_deployed_per_agent RENAME TO agents_one_deployed_per_agent;
ALTER INDEX idx_agent_versions_agent_id RENAME TO idx_agents_agent_id;
ALTER INDEX idx_agent_versions_parent_version_id RENAME TO idx_agents_parent_version_id;

ALTER TABLE chat_sessions ADD CONSTRAINT chat_sessions_agent_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE deployed_sessions ADD CONSTRAINT deployed_sessions_chain_root_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE agents ADD CONSTRAINT agents_chain_root_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

-- Restore resources FK to the renamed-back agents table (post-DROP-TABLE + post-RENAME,
-- this is the former agent_versions). The constraint was dropped above before DROP TABLE.
ALTER TABLE resources ADD CONSTRAINT resources_agent_id_fkey
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE;

-- Restore the parent_version_id FK constraint name so Up's rename in step 6 finds it.
ALTER TABLE agents RENAME CONSTRAINT agent_versions_parent_version_id_fkey TO agents_parent_version_id_fkey;

ALTER TABLE agents DROP CONSTRAINT agent_versions_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_status_check
  CHECK (status IN ('INITIALIZING','DRAFT','TRAINING','READY','DEPLOYED'));
ALTER TABLE agents ADD CONSTRAINT agents_root_self_reference
  CHECK (parent_version_id IS NOT NULL OR agent_id = id);
