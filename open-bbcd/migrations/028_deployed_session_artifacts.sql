-- 028_deployed_session_artifacts.sql
--
-- Session artifacts for deployed chat (spec: deployed-runtime-artifacts,
-- § Data — new tables). One row = one artifact in a session's read scope.
-- message_id NULL = pending upload (claimed by the next turn); otherwise the
-- message carrying the ref. message_id is deliberately not an FK: messages
-- and rows are removed together by the session cascade.
--
-- Additive only; no backfill. Unlike BO (027), Plan 1 (PR #54) never wired
-- the deployed orchestrator to the artifact registry, so no deployed_messages
-- row can carry an artifact_ref block.

-- +goose Up
CREATE TABLE deployed_session_artifacts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES deployed_sessions(id) ON DELETE CASCADE,
    origin      TEXT NOT NULL CHECK (origin IN ('upload','tool_result')),
    store_id    TEXT NOT NULL,
    uri         TEXT NOT NULL,
    mime        TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    sha256      TEXT NOT NULL,
    filename    TEXT,
    message_id  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Only uploads can be pending.
    CHECK (origin = 'upload' OR message_id IS NOT NULL)
);

-- At most one pending row per blob per session (upload dedup backstop).
CREATE UNIQUE INDEX uq_deployed_session_artifacts_pending_blob
    ON deployed_session_artifacts (session_id, store_id, uri)
    WHERE origin = 'upload' AND message_id IS NULL;

-- Claim path.
CREATE INDEX idx_deployed_session_artifacts_pending
    ON deployed_session_artifacts (session_id)
    WHERE message_id IS NULL;

-- Retrieval allow-list lookup.
CREATE INDEX idx_deployed_session_artifacts_lookup
    ON deployed_session_artifacts (session_id, store_id, uri);

-- +goose Down
DROP TABLE deployed_session_artifacts;
