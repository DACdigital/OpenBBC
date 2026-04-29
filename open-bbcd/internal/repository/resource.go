package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ResourceRepository struct {
	db *sql.DB
}

func NewResourceRepository(db *sql.DB) *ResourceRepository {
	return &ResourceRepository{db: db}
}

func (r *ResourceRepository) Create(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error) {
	resource, err := types.NewResource(opts)
	if err != nil {
		return nil, err
	}

	err = r.db.QueryRowContext(ctx, `
		INSERT INTO resources (agent_id, name, description, prompt, mcp_endpoint)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
	`, resource.AgentID, resource.Name, resource.Description, resource.Prompt, resource.MCPEndpoint).Scan(
		&resource.ID,
		&resource.AgentID,
		&resource.Name,
		&resource.Description,
		&resource.Prompt,
		&resource.MCPEndpoint,
		&resource.CreatedAt,
		&resource.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *ResourceRepository) GetByID(ctx context.Context, id string) (*types.Resource, error) {
	resource := &types.Resource{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
		FROM resources WHERE id = $1
	`, id).Scan(
		&resource.ID,
		&resource.AgentID,
		&resource.Name,
		&resource.Description,
		&resource.Prompt,
		&resource.MCPEndpoint,
		&resource.CreatedAt,
		&resource.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *ResourceRepository) ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
		FROM resources WHERE agent_id = $1 ORDER BY created_at DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var resources []*types.Resource
	for rows.Next() {
		resource := &types.Resource{}
		if err := rows.Scan(
			&resource.ID,
			&resource.AgentID,
			&resource.Name,
			&resource.Description,
			&resource.Prompt,
			&resource.MCPEndpoint,
			&resource.CreatedAt,
			&resource.UpdatedAt,
		); err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}
