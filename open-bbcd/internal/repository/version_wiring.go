package repository

import (
	"context"
	"database/sql"
)

type VersionWiringRepository struct{ db *sql.DB }

func NewVersionWiringRepository(db *sql.DB) *VersionWiringRepository {
	return &VersionWiringRepository{db: db}
}

type MCPAttachment struct {
	BackendID string
	Note      string
}

func (r *VersionWiringRepository) SetEndpointBackend(ctx context.Context, versionID, endpointID, backendID string) error {
	const q = `INSERT INTO agent_version_endpoint_backend (agent_version_id, endpoint_id, backend_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (agent_version_id, endpoint_id)
		DO UPDATE SET backend_id = EXCLUDED.backend_id`
	_, err := r.db.ExecContext(ctx, q, versionID, endpointID, backendID)
	return err
}

// UnsetEndpointBackend removes the endpoint→backend mapping. Returns nil if
// no row matched (idempotent).
func (r *VersionWiringRepository) UnsetEndpointBackend(ctx context.Context, versionID, endpointID string) error {
	const q = `DELETE FROM agent_version_endpoint_backend WHERE agent_version_id = $1 AND endpoint_id = $2`
	_, err := r.db.ExecContext(ctx, q, versionID, endpointID)
	return err
}

func (r *VersionWiringRepository) ListEndpointBackends(ctx context.Context, versionID string) (map[string]string, error) {
	const q = `SELECT endpoint_id, backend_id FROM agent_version_endpoint_backend WHERE agent_version_id = $1`
	rows, err := r.db.QueryContext(ctx, q, versionID)
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

func (r *VersionWiringRepository) AttachMCP(ctx context.Context, versionID, backendID, note string) error {
	const q = `INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note)
		VALUES ($1, $2, $3)
		ON CONFLICT (agent_version_id, backend_id)
		DO UPDATE SET note = EXCLUDED.note`
	_, err := r.db.ExecContext(ctx, q, versionID, backendID, note)
	return err
}

// DetachMCP removes the version's attachment to the MCP backend. Returns nil
// if no attachment existed (idempotent).
func (r *VersionWiringRepository) DetachMCP(ctx context.Context, versionID, backendID string) error {
	const q = `DELETE FROM agent_version_mcp_backend WHERE agent_version_id = $1 AND backend_id = $2`
	_, err := r.db.ExecContext(ctx, q, versionID, backendID)
	return err
}

func (r *VersionWiringRepository) ListMCPAttachments(ctx context.Context, versionID string) ([]MCPAttachment, error) {
	const q = `SELECT backend_id, note FROM agent_version_mcp_backend WHERE agent_version_id = $1 ORDER BY backend_id`
	rows, err := r.db.QueryContext(ctx, q, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPAttachment{}
	for rows.Next() {
		a := MCPAttachment{}
		if err := rows.Scan(&a.BackendID, &a.Note); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UsageCounts returns how many distinct agent versions reference each backend id
// across both wiring tables. A version that wires the same backend via both an
// HTTP endpoint AND an MCP attachment is counted once. Used by the MCP list
// page to show "used by N".
func (r *VersionWiringRepository) UsageCounts(ctx context.Context) (map[string]int, error) {
	const q = `
		SELECT backend_id, COUNT(DISTINCT agent_version_id) AS n FROM (
			SELECT agent_version_id, backend_id FROM agent_version_endpoint_backend
			UNION
			SELECT agent_version_id, backend_id FROM agent_version_mcp_backend
		) u
		GROUP BY backend_id`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		m[id] = n
	}
	return m, rows.Err()
}
