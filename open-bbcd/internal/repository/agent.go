package repository

import (
	"context"
	"database/sql"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentRepository struct {
	db *sql.DB
}

func NewAgentRepository(db *sql.DB) *AgentRepository {
	return &AgentRepository{db: db}
}

func (r *AgentRepository) Create(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error) {
	agent, err := types.NewAgent(input)
	if err != nil {
		return nil, err
	}

	err = r.db.QueryRowContext(ctx, `
		INSERT INTO agents (name, description, prompt)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, prompt, created_at, updated_at
	`, agent.Name, agent.Description, agent.Prompt).Scan(
		&agent.ID,
		&agent.Name,
		&agent.Description,
		&agent.Prompt,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func (r *AgentRepository) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	agent := &types.Agent{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, prompt, created_at, updated_at
		FROM agents WHERE id = $1
	`, id).Scan(
		&agent.ID,
		&agent.Name,
		&agent.Description,
		&agent.Prompt,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func (r *AgentRepository) List(ctx context.Context) ([]*types.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, prompt, created_at, updated_at
		FROM agents ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*types.Agent
	for rows.Next() {
		agent := &types.Agent{}
		if err := rows.Scan(
			&agent.ID,
			&agent.Name,
			&agent.Description,
			&agent.Prompt,
			&agent.CreatedAt,
			&agent.UpdatedAt,
		); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}
