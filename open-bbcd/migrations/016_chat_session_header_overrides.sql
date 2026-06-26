-- migrations/016_chat_session_header_overrides.sql

-- +goose Up
ALTER TABLE chat_sessions
  ADD COLUMN backend_header_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE chat_sessions
  DROP COLUMN IF EXISTS backend_header_overrides;
