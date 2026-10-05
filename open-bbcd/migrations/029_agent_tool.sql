-- 029_agent_tool.sql
--
-- multiagent-feature (spec: openbbc-docs docs/superpowers/specs/2026-10-01-multiagent-feature-design.md,
-- § Contracts → Schema). Agent-tool config on the caller version, pinned
-- sub-agent bindings, and parent links for child sessions on both surfaces.
-- Additive; existing sessions become roots, existing versions get the tool off.
-- Rollback must pair the app rollback with goose down (an old binary would
-- treat children as roots).

-- +goose Up
ALTER TABLE agent_versions
  ADD COLUMN agent_tool_enabled BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE agent_version_subagent (
  caller_version_id UUID NOT NULL REFERENCES agent_versions(id) ON DELETE CASCADE,
  target_version_id UUID NOT NULL REFERENCES agent_versions(id) ON DELETE NO ACTION,
  name              TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9_-]{0,39}$'),
  note              TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (caller_version_id, name),
  UNIQUE (caller_version_id, target_version_id),
  CHECK (caller_version_id <> target_version_id)
);
CREATE INDEX idx_avs_target ON agent_version_subagent(target_version_id);

ALTER TABLE chat_sessions
  ADD COLUMN parent_session_id   UUID REFERENCES chat_sessions(id) ON DELETE CASCADE,
  ADD COLUMN parent_tool_call_id TEXT,
  ADD COLUMN depth               INT NOT NULL DEFAULT 0,
  ADD CONSTRAINT chat_sessions_parent_link_chk
    CHECK ((parent_session_id IS NULL) = (parent_tool_call_id IS NULL)),
  ADD CONSTRAINT chat_sessions_depth_chk
    CHECK ((parent_session_id IS NULL) = (depth = 0));
CREATE UNIQUE INDEX idx_chat_sessions_parent ON chat_sessions(parent_session_id, parent_tool_call_id)
  WHERE parent_session_id IS NOT NULL;

ALTER TABLE deployed_sessions
  ADD COLUMN parent_session_id   UUID REFERENCES deployed_sessions(id) ON DELETE CASCADE,
  ADD COLUMN parent_tool_call_id TEXT,
  ADD COLUMN depth               INT NOT NULL DEFAULT 0,
  ADD COLUMN agent_version_id    UUID REFERENCES agent_versions(id) ON DELETE NO ACTION,
  ADD CONSTRAINT deployed_sessions_parent_link_chk
    CHECK ((parent_session_id IS NULL) = (parent_tool_call_id IS NULL)),
  ADD CONSTRAINT deployed_sessions_child_version_chk
    CHECK ((parent_session_id IS NULL) = (agent_version_id IS NULL)),
  ADD CONSTRAINT deployed_sessions_depth_chk
    CHECK ((parent_session_id IS NULL) = (depth = 0));
CREATE UNIQUE INDEX idx_deployed_sessions_parent ON deployed_sessions(parent_session_id, parent_tool_call_id)
  WHERE parent_session_id IS NOT NULL;

-- +goose Down
-- Children first: once the parent link is dropped, a former child would read as a
-- root session of its agent and appear in that agent's session list.
DELETE FROM deployed_sessions WHERE parent_session_id IS NOT NULL;
DELETE FROM chat_sessions     WHERE parent_session_id IS NOT NULL;

DROP INDEX idx_deployed_sessions_parent;
ALTER TABLE deployed_sessions
  DROP CONSTRAINT deployed_sessions_depth_chk,
  DROP CONSTRAINT deployed_sessions_child_version_chk,
  DROP CONSTRAINT deployed_sessions_parent_link_chk,
  DROP COLUMN agent_version_id,
  DROP COLUMN depth,
  DROP COLUMN parent_tool_call_id,
  DROP COLUMN parent_session_id;

DROP INDEX idx_chat_sessions_parent;
ALTER TABLE chat_sessions
  DROP CONSTRAINT chat_sessions_depth_chk,
  DROP CONSTRAINT chat_sessions_parent_link_chk,
  DROP COLUMN depth,
  DROP COLUMN parent_tool_call_id,
  DROP COLUMN parent_session_id;

DROP INDEX idx_avs_target;
DROP TABLE agent_version_subagent;

ALTER TABLE agent_versions DROP COLUMN agent_tool_enabled;
