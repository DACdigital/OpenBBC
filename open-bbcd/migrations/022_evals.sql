-- migrations/022_evals.sql
-- Eval aggregate: one row per (agent_version, dataset_version) run, plus
-- one child row per simulated session. Score fields fill in on completion.

-- +goose Up
CREATE TABLE evals (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_version_id     UUID NOT NULL REFERENCES agent_versions(id) ON DELETE CASCADE,
    dataset_version_id   UUID NOT NULL REFERENCES dataset_versions(id) ON DELETE RESTRICT,
    status               TEXT NOT NULL CHECK (status IN ('PENDING','IN_PROGRESS','DONE','FAILED')),
    score                DOUBLE PRECISION,
    total_criteria       INTEGER,
    passed_criteria      INTEGER,
    error_message        TEXT NOT NULL DEFAULT '',
    aikdm_meta           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at           TIMESTAMPTZ,
    completed_at         TIMESTAMPTZ
);
CREATE INDEX evals_by_agent_version   ON evals (agent_version_id,   created_at DESC);
CREATE INDEX evals_by_dataset_version ON evals (dataset_version_id, created_at DESC);

CREATE TABLE eval_sessions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    eval_id              UUID NOT NULL REFERENCES evals(id) ON DELETE CASCADE,
    session_id           UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE RESTRICT,
    score                DOUBLE PRECISION NOT NULL,
    total_criteria       INTEGER NOT NULL,
    passed_criteria      INTEGER NOT NULL,
    transcript           JSONB NOT NULL,
    judgments            JSONB NOT NULL,
    UNIQUE (eval_id, session_id)
);

-- +goose Down
DROP TABLE eval_sessions;
DROP TABLE evals;
