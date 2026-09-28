-- migrations/026_agent_discovery_zip.sql
--
-- Moves the discovery zip blob from local disk into Postgres so open-bbcd
-- becomes stateless (only the DB is stateful) and can be deployed without a
-- PVC. The old discovery_file_path column stays for now — it's ignored by
-- new writes and reads, but retained so this migration is trivially
-- reversible. A follow-up migration can drop it once we're sure nothing
-- references it.

-- +goose Up
ALTER TABLE agents ADD COLUMN discovery_zip BYTEA;

-- +goose Down
ALTER TABLE agents DROP COLUMN discovery_zip;
