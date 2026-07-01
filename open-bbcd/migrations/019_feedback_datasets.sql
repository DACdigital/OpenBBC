-- +goose Up

-- Per-assistant-message feedback. Enforced at the repo layer to attach
-- only to role='assistant' messages (no partial FK support in Postgres).
CREATE TABLE chat_message_feedback (
    message_id       UUID PRIMARY KEY REFERENCES chat_messages(id) ON DELETE CASCADE,
    rating           TEXT NOT NULL CHECK (rating IN ('up','down')),
    comment          TEXT NOT NULL DEFAULT '',
    expected_output  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (rating = 'up' OR length(comment) > 0)
);

CREATE TABLE datasets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE dataset_versions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id   UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK (status IN ('DRAFT','CLOSED')),
    version_num  INTEGER NOT NULL,
    close_note   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at    TIMESTAMPTZ,
    UNIQUE (dataset_id, version_num)
);

-- At most one DRAFT per dataset. Mirrors the 'one DEPLOYED per chain'
-- pattern already in migration 011.
CREATE UNIQUE INDEX datasets_one_draft_per_dataset
    ON dataset_versions (dataset_id) WHERE status = 'DRAFT';

CREATE TABLE dataset_version_sessions (
    dataset_version_id UUID NOT NULL REFERENCES dataset_versions(id) ON DELETE CASCADE,
    session_id         UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE RESTRICT,
    added_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_version_id, session_id),
    UNIQUE (session_id)
);

-- Session-level immutability flag. NULL = mutable; set when the containing
-- dataset version closes.
ALTER TABLE chat_sessions
    ADD COLUMN locked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE chat_sessions DROP COLUMN locked_at;
DROP TABLE dataset_version_sessions;
DROP INDEX IF EXISTS datasets_one_draft_per_dataset;
DROP TABLE dataset_versions;
DROP TABLE datasets;
DROP TABLE chat_message_feedback;
