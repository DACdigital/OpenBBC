-- 027_chat_session_artifacts.sql
--
-- Session artifacts for BO test chat (spec: deployed-runtime-artifacts,
-- § Data — new tables). One row = one artifact in a session's read scope.
-- message_id NULL = pending upload (claimed by the next turn); otherwise the
-- message carrying the ref. message_id is deliberately not an FK: messages
-- and rows are removed together by the session cascade.
--
-- Additive, plus a backfill: PR #54 (Plan 1) already persisted artifact_ref
-- blocks on BO tool-role chat_messages, and retrieval is now a row lookup, so
-- without rows those refs would 404 after upgrade. Each such block becomes
-- one origin='tool_result' row bound to its message. User-role refs were
-- never persisted before this migration, so there is nothing else to fill.

-- +goose Up
CREATE TABLE chat_session_artifacts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
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
CREATE UNIQUE INDEX uq_chat_session_artifacts_pending_blob
    ON chat_session_artifacts (session_id, store_id, uri)
    WHERE origin = 'upload' AND message_id IS NULL;

-- Claim path.
CREATE INDEX idx_chat_session_artifacts_pending
    ON chat_session_artifacts (session_id)
    WHERE message_id IS NULL;

-- Retrieval allow-list lookup.
CREATE INDEX idx_chat_session_artifacts_lookup
    ON chat_session_artifacts (session_id, store_id, uri);

-- Backfill (see header). Blocks without store_id/uri cannot be looked up and
-- are skipped; missing metadata gets the same defaults a fresh row could
-- carry. The markers let the repository test run this exact statement.
-- backfill:begin
INSERT INTO chat_session_artifacts
    (session_id, origin, store_id, uri, mime, size_bytes, sha256, filename, message_id, created_at, updated_at)
SELECT m.session_id, 'tool_result', b->>'store_id', b->>'uri',
       COALESCE(b->>'mime', 'application/octet-stream'),
       CASE WHEN b->>'size_bytes' ~ '^[0-9]{1,18}$' THEN (b->>'size_bytes')::bigint ELSE 0 END,
       COALESCE(b->>'sha256', ''),
       NULLIF(b->>'filename', ''),
       m.id, m.created_at, m.created_at
FROM chat_messages m
CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(m.content) = 'array' THEN m.content ELSE '[]'::jsonb END) AS b
WHERE m.role = 'tool'
  AND b->>'type' = 'artifact_ref'
  AND COALESCE(b->>'store_id', '') <> ''
  AND COALESCE(b->>'uri', '') <> '';
-- backfill:end

-- +goose Down
DROP TABLE chat_session_artifacts;
