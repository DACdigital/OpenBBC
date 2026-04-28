-- migrations/002_create_resources.sql

-- +goose Up
CREATE TABLE resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    prompt TEXT NOT NULL,
    mcp_endpoint VARCHAR(512),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_resources_agent_id ON resources(agent_id);
CREATE INDEX idx_resources_name ON resources(name);

-- +goose Down
DROP TABLE IF EXISTS resources;
