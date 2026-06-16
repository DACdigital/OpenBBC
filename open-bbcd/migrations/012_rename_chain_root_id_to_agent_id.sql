-- migrations/012_rename_chain_root_id_to_agent_id.sql
--
-- Rename the "chain" terminology to "agent" everywhere it appears at the
-- schema level. The column called chain_root_id was always semantically
-- the agent's stable ID; the linked-list-of-versions detail is an
-- implementation concern, not something to leak into column names.

-- +goose Up
ALTER TABLE agents RENAME COLUMN chain_root_id TO agent_id;
ALTER INDEX agents_one_deployed_per_chain RENAME TO agents_one_deployed_per_agent;
ALTER INDEX idx_agents_chain_root_id RENAME TO idx_agents_agent_id;
ALTER TABLE deployed_sessions RENAME COLUMN chain_root_id TO agent_id;
ALTER INDEX idx_deployed_sessions_chain_user RENAME TO idx_deployed_sessions_agent_user;

-- +goose Down
ALTER INDEX idx_deployed_sessions_agent_user RENAME TO idx_deployed_sessions_chain_user;
ALTER TABLE deployed_sessions RENAME COLUMN agent_id TO chain_root_id;
ALTER INDEX idx_agents_agent_id RENAME TO idx_agents_chain_root_id;
ALTER INDEX agents_one_deployed_per_agent RENAME TO agents_one_deployed_per_chain;
ALTER TABLE agents RENAME COLUMN agent_id TO chain_root_id;
