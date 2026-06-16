-- migrations/011_deploy_agent.sql
--
-- Materializes chain identity as a real column on `agents` so chain-root
-- lookups are O(1) instead of recursive walks, and enforces "one DEPLOYED
-- per chain" via a partial unique index. Adds two new tables for deployed
-- agent traffic (sessions + messages), kept fully separate from chat_sessions
-- so BO chat queries can never accidentally surface deployed user data.

-- +goose Up
ALTER TABLE agents ADD COLUMN chain_root_id UUID REFERENCES agents(id);

-- Backfill: walk parent_version_id up from each row to the root and
-- record the root's id. With the current schema (no rows have parents),
-- this resolves to each row's own id, but the recursive form is correct
-- for any future linked-list state.
UPDATE agents AS a SET chain_root_id = (
  WITH RECURSIVE up AS (
    SELECT id, parent_version_id FROM agents WHERE id = a.id
    UNION ALL
    SELECT p.id, p.parent_version_id FROM agents p JOIN up ON p.id = up.parent_version_id
  )
  SELECT id FROM up WHERE parent_version_id IS NULL
);

ALTER TABLE agents ALTER COLUMN chain_root_id SET NOT NULL;

-- Roots must self-reference; non-root rows must point at their root.
ALTER TABLE agents ADD CONSTRAINT agents_root_self_reference
  CHECK (parent_version_id IS NOT NULL OR chain_root_id = id);

-- DB-enforced singleton: at most one DEPLOYED per chain.
CREATE UNIQUE INDEX agents_one_deployed_per_chain
  ON agents (chain_root_id) WHERE status = 'DEPLOYED';

CREATE INDEX idx_agents_chain_root_id ON agents (chain_root_id);

CREATE TABLE deployed_sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain_root_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL,
    title         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_deployed_sessions_chain_user
  ON deployed_sessions (chain_root_id, user_id, created_at DESC);

CREATE TABLE deployed_messages (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id       UUID NOT NULL REFERENCES deployed_sessions(id) ON DELETE CASCADE,
    agent_version_id UUID NOT NULL REFERENCES agents(id),
    role             TEXT NOT NULL CHECK (role IN ('user','assistant','tool')),
    content          JSONB NOT NULL,
    seq              INTEGER NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);
CREATE INDEX idx_deployed_messages_session_seq
  ON deployed_messages (session_id, seq);

-- +goose Down
DROP TABLE deployed_messages;
DROP TABLE deployed_sessions;
DROP INDEX IF EXISTS idx_agents_chain_root_id;
DROP INDEX IF EXISTS agents_one_deployed_per_chain;
ALTER TABLE agents DROP CONSTRAINT agents_root_self_reference;
ALTER TABLE agents DROP COLUMN chain_root_id;
