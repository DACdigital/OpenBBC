-- +goose Up
ALTER TABLE agents
  ADD COLUMN flow_map_config       JSONB,
  ADD COLUMN flow_map_parse_error  TEXT;

ALTER TABLE agents
  DROP COLUMN IF EXISTS wizard_input,
  DROP COLUMN IF EXISTS schema_version;

-- +goose Down
ALTER TABLE agents
  ADD COLUMN wizard_input   JSONB,
  ADD COLUMN schema_version VARCHAR(20);

ALTER TABLE agents
  DROP COLUMN IF EXISTS flow_map_config,
  DROP COLUMN IF EXISTS flow_map_parse_error;
