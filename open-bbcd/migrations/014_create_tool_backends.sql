-- migrations/014_create_tool_backends.sql

-- +goose Up
CREATE TABLE tool_backends (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    kind        TEXT NOT NULL CHECK (kind IN ('http_endpoint', 'mcp_client')),
    config      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tool_backends_kind ON tool_backends(kind);

-- +goose Down
DROP TABLE IF EXISTS tool_backends;
