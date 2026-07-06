-- migrations/024_training_sessions.sql

-- +goose Up
CREATE TABLE training_sessions (
    id                UUID         NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    source_eval_id    UUID         NOT NULL REFERENCES evals(id)          ON DELETE RESTRICT,
    parent_version_id UUID         NOT NULL REFERENCES agent_versions(id) ON DELETE RESTRICT,
    new_version_id    UUID              NULL REFERENCES agent_versions(id) ON DELETE SET NULL,
    status            TEXT         NOT NULL DEFAULT 'PENDING'
                          CHECK (status IN ('PENDING','IN_PROGRESS','DONE','FAILED')),
    requested_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ       NULL,
    completed_at      TIMESTAMPTZ       NULL,
    error_message     TEXT         NOT NULL DEFAULT '',
    epochs            INTEGER           NULL,
    patience          INTEGER           NULL,
    initial_score     REAL              NULL,
    final_score       REAL              NULL,
    total_epochs_run  INTEGER           NULL,
    stopped_reason    TEXT              NULL,
    training_report   JSONB             NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_ts_source_eval ON training_sessions(source_eval_id);
CREATE INDEX idx_ts_new_version ON training_sessions(new_version_id) WHERE new_version_id IS NOT NULL;
CREATE INDEX idx_ts_status_requested ON training_sessions(status, requested_at DESC);

CREATE UNIQUE INDEX idx_ts_one_active_per_eval
    ON training_sessions(source_eval_id)
    WHERE status IN ('PENDING', 'IN_PROGRESS');

-- +goose Down
DROP INDEX IF EXISTS idx_ts_one_active_per_eval;
DROP INDEX IF EXISTS idx_ts_status_requested;
DROP INDEX IF EXISTS idx_ts_new_version;
DROP INDEX IF EXISTS idx_ts_source_eval;
DROP TABLE IF EXISTS training_sessions;
