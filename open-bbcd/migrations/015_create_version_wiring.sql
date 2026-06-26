-- migrations/015_create_version_wiring.sql

-- +goose Up
CREATE TABLE agent_version_endpoint_backend (
    agent_version_id UUID NOT NULL REFERENCES agent_versions(id) ON DELETE CASCADE,
    endpoint_id      TEXT NOT NULL,
    backend_id       UUID NOT NULL REFERENCES tool_backends(id) ON DELETE RESTRICT,
    PRIMARY KEY (agent_version_id, endpoint_id)
);
CREATE INDEX idx_avep_backend ON agent_version_endpoint_backend(backend_id);

CREATE TABLE agent_version_mcp_backend (
    agent_version_id UUID NOT NULL REFERENCES agent_versions(id) ON DELETE CASCADE,
    backend_id       UUID NOT NULL REFERENCES tool_backends(id) ON DELETE RESTRICT,
    note             TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_version_id, backend_id)
);
CREATE INDEX idx_avmb_backend ON agent_version_mcp_backend(backend_id);

-- +goose Down
DROP TABLE IF EXISTS agent_version_mcp_backend;
DROP TABLE IF EXISTS agent_version_endpoint_backend;
