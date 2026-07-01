-- migrations/020_dataset_inheritance.sql
-- Sessions can now appear in multiple versions OF THE SAME dataset — a new
-- DRAFT copies the previous CLOSED version's sessions on creation so users
-- see cumulative dataset content instead of an empty next version.
-- Cross-dataset uniqueness (session belongs to at most one dataset) is now
-- enforced at the repo layer (DatasetRepository.AssignSessionToDraft).

-- +goose Up
ALTER TABLE dataset_version_sessions
    DROP CONSTRAINT dataset_version_sessions_session_id_key;

-- +goose Down
-- Note: only reversible when there are no duplicate session_ids. Dev-only rollback.
ALTER TABLE dataset_version_sessions
    ADD CONSTRAINT dataset_version_sessions_session_id_key UNIQUE (session_id);
