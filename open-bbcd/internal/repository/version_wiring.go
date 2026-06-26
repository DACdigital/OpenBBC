package repository

import (
	"context"
	"database/sql"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type VersionWiringRepository struct{ db *sql.DB }

func NewVersionWiringRepository(db *sql.DB) *VersionWiringRepository {
	return &VersionWiringRepository{db: db}
}

type MCPAttachment struct {
	BackendID string
	Note      string
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

// ListBackendsForVersion returns all distinct tool backends wired to a
// version — HTTP backends via the agent-level endpoint_backend mapping
// (resolved through the version's agent_id), MCP backends via the version's
// own mcp_backend attachments. Used by the header-overrides modal in the
// BO chat UI.
func (r *VersionWiringRepository) ListBackendsForVersion(ctx context.Context, versionID string) ([]*types.ToolBackend, error) {
	const q = `
		SELECT DISTINCT tb.id, tb.name, tb.kind, tb.config, tb.created_at, tb.updated_at
		FROM tool_backends tb
		WHERE tb.id IN (
			SELECT backend_id FROM agent_endpoint_backend
			WHERE agent_id = (SELECT agent_id FROM agent_versions WHERE id = $1)
			UNION
			SELECT backend_id FROM agent_version_mcp_backend WHERE agent_version_id = $1
		)
		ORDER BY tb.name`
	rows, err := r.db.QueryContext(ctx, q, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.ToolBackend
	for rows.Next() {
		be := &types.ToolBackend{}
		var kind string
		if err := rows.Scan(&be.ID, &be.Name, &kind, &be.Config, &be.CreatedAt, &be.UpdatedAt); err != nil {
			return nil, err
		}
		be.Kind = types.ToolBackendKind(kind)
		out = append(out, be)
	}
	return out, rows.Err()
}

// UsageCounts returns how many distinct entities reference each backend id.
// For HTTP backends: count of distinct agents wiring it via
// agent_endpoint_backend. For MCP backends: count of distinct versions
// attaching it via agent_version_mcp_backend. Used by the MCP list page
// to show "used by N".
//
// The grain change from the pre-017 shape (which counted versions for both)
// is deliberate: HTTP wiring is now per-agent, MCP attachments stay per
// version. A backend used as both HTTP and MCP backend is still counted
// once via the DISTINCT in the union.
func (r *VersionWiringRepository) UsageCounts(ctx context.Context) (map[string]int, error) {
	const q = `
		SELECT backend_id, COUNT(DISTINCT scope_id) AS n FROM (
			SELECT agent_id::text         AS scope_id, backend_id FROM agent_endpoint_backend
			UNION
			SELECT agent_version_id::text AS scope_id, backend_id FROM agent_version_mcp_backend
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
