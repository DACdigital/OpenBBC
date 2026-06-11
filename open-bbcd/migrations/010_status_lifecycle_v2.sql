-- migrations/010_status_lifecycle_v2.sql
--
-- Status lifecycle v2: INITIALIZING -> DRAFT -> TRAINING -> READY -> DEPLOYED.
-- TESTED retired (users may never run an explicit test step). TRAINING is set
-- while the aikdm generation pipeline is iterating; READY when the bundle is
-- finalized for that version and aikdm is done with it.

-- +goose Up
UPDATE agents SET status = 'READY' WHERE status = 'TESTED';
ALTER TABLE agents DROP CONSTRAINT agents_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_status_check
  CHECK (status IN ('INITIALIZING', 'DRAFT', 'TRAINING', 'READY', 'DEPLOYED'));

-- +goose Down
UPDATE agents SET status = 'DRAFT' WHERE status IN ('TRAINING', 'READY');
ALTER TABLE agents DROP CONSTRAINT agents_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_status_check
  CHECK (status IN ('INITIALIZING', 'DRAFT', 'TESTED', 'DEPLOYED'));
