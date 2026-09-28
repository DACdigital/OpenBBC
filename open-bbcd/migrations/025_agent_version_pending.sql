-- migrations/025_agent_version_pending.sql
--
-- Adds PENDING to agent_versions.status. PENDING is the state between
-- INITIALIZING (wizard/architecture editing) and READY (bundle landed):
-- the root version has been finalized by the user and is waiting for the
-- aikdm alpha-generation drainer to produce and land its bundle. Mirrors
-- the PENDING → IN_PROGRESS → DONE/FAILED pattern used by evals and
-- training_sessions, minus the IN_PROGRESS step (alpha-gen is short and
-- externally driven by scripts/process_pending_alphas.sh).

-- +goose Up
ALTER TABLE agent_versions DROP CONSTRAINT agent_versions_status_check;
ALTER TABLE agent_versions ADD CONSTRAINT agent_versions_status_check
    CHECK (status IN ('INITIALIZING','PENDING','DRAFT','TRAINING','READY','DEPLOYED'));

-- +goose Down
UPDATE agent_versions SET status = 'DRAFT' WHERE status = 'PENDING';
ALTER TABLE agent_versions DROP CONSTRAINT agent_versions_status_check;
ALTER TABLE agent_versions ADD CONSTRAINT agent_versions_status_check
    CHECK (status IN ('INITIALIZING','DRAFT','TRAINING','READY','DEPLOYED'));
