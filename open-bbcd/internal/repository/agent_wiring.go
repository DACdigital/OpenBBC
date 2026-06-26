package repository

import (
	"context"
	"database/sql"
)

// AgentWiringRepository owns the agent-level endpoint→backend mapping
// (agent_endpoint_backend table, added in migration 017). Endpoints are
// frozen on the agent, so the wiring lives with them — every version of
// the same agent sees the same backend for a given endpoint id.
type AgentWiringRepository struct{ db *sql.DB }

func NewAgentWiringRepository(db *sql.DB) *AgentWiringRepository {
	return &AgentWiringRepository{db: db}
}

// SetEndpointBackend upserts the endpoint→backend mapping for an agent.
func (r *AgentWiringRepository) SetEndpointBackend(ctx context.Context, agentID, endpointID, backendID string) error {
	const q = `INSERT INTO agent_endpoint_backend (agent_id, endpoint_id, backend_id)
		VALUES ($1::uuid, $2, $3::uuid)
		ON CONFLICT (agent_id, endpoint_id)
		DO UPDATE SET backend_id = EXCLUDED.backend_id`
	_, err := r.db.ExecContext(ctx, q, agentID, endpointID, backendID)
	return err
}

// UnsetEndpointBackend removes the mapping. Idempotent.
func (r *AgentWiringRepository) UnsetEndpointBackend(ctx context.Context, agentID, endpointID string) error {
	const q = `DELETE FROM agent_endpoint_backend WHERE agent_id = $1::uuid AND endpoint_id = $2`
	_, err := r.db.ExecContext(ctx, q, agentID, endpointID)
	return err
}

// ListEndpointBackends returns endpoint_id → backend_id for the given agent.
func (r *AgentWiringRepository) ListEndpointBackends(ctx context.Context, agentID string) (map[string]string, error) {
	const q = `SELECT endpoint_id, backend_id::text FROM agent_endpoint_backend WHERE agent_id = $1::uuid`
	rows, err := r.db.QueryContext(ctx, q, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var eid, bid string
		if err := rows.Scan(&eid, &bid); err != nil {
			return nil, err
		}
		m[eid] = bid
	}
	return m, rows.Err()
}
