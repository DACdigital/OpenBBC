-- migrations/009_create_chat_sessions.sql
-- Chat sessions per agent version + per-session message log.
-- chat_sessions: many per agent_id; cascade delete with parent agent.
-- chat_messages: ordered by `seq` per session (race-tolerant vs timestamps);
-- content holds an array of Anthropic-style content blocks (text/tool_use/tool_result).

-- +goose Up
CREATE TABLE chat_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id     UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    title        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_sessions_agent_id_created
    ON chat_sessions (agent_id, created_at DESC);

CREATE TABLE chat_messages (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id   UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content      JSONB NOT NULL,
    seq          INTEGER NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

CREATE INDEX idx_chat_messages_session_id_seq
    ON chat_messages (session_id, seq);

-- +goose Down
DROP TABLE chat_messages;
DROP TABLE chat_sessions;
