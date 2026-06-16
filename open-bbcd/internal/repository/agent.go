// open-bbcd/internal/repository/agent.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

type AgentRepository struct {
	db *sql.DB
}

func NewAgentRepository(db *sql.DB) *AgentRepository {
	return &AgentRepository{db: db}
}

const agentColumns = `id, name, description, discovery_file_path, created_at`

func scanAgent(s scanner) (*types.Agent, error) {
	a := &types.Agent{}
	var description sql.NullString
	var disc sql.NullString
	if err := s.Scan(&a.ID, &a.Name, &description, &disc, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Description = description.String
	a.DiscoveryFilePath = disc.String
	return a, nil
}

// Create inserts an Agent (REST path).
func (r *AgentRepository) Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
	a, err := types.NewAgent(opts)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (id, name, description)
		VALUES ($1::uuid, $2, $3)
		RETURNING `+agentColumns,
		id, a.Name, a.Description,
	)
	return scanAgent(row)
}

// CreateFromWizard inserts an agents row + an INITIALIZING agent_versions row
// in a single transaction. Returns both. The version's id is auto-generated;
// opts.ID, if set, becomes the agent's id.
func (r *AgentRepository) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, *types.AgentVersion, error) {
	if opts.Name == "" {
		return nil, nil, types.ErrNameRequired
	}
	if r.db == nil {
		return nil, nil, errors.New("repository: no database connection")
	}
	agentID := opts.ID
	if agentID == "" {
		agentID = uuid.NewString()
	}
	versionID := uuid.NewString()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	agentRow := tx.QueryRowContext(ctx, `
		INSERT INTO agents (id, name, discovery_file_path)
		VALUES ($1::uuid, $2, NULLIF($3, ''))
		RETURNING `+agentColumns,
		agentID, opts.Name, opts.DiscoveryFilePath,
	)
	agent, err := scanAgent(agentRow)
	if err != nil {
		return nil, nil, err
	}

	versionRow := tx.QueryRowContext(ctx, `
		INSERT INTO agent_versions (id, agent_id, status, flow_map_config, flow_map_parse_error)
		VALUES ($1::uuid, $2::uuid, 'INITIALIZING', $3, NULLIF($4, ''))
		RETURNING `+agentVersionColumns,
		versionID, agentID, []byte(opts.FlowMapConfig), opts.FlowMapParseError,
	)
	version, err := scanAgentVersion(versionRow)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return agent, version, nil
}

// GetByID returns the Agent (per-agent row).
func (r *AgentRepository) GetByID(ctx context.Context, agentID string) (*types.Agent, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1`, agentID)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return a, err
}

// List returns all Agents (per-agent rows). Used by the JSON GET /agents endpoint.
func (r *AgentRepository) List(ctx context.Context) ([]*types.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListGrouped returns each Agent with its ordered versions. One LEFT JOIN.
func (r *AgentRepository) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id::text, a.name,
		       av.id::text, av.agent_id::text, av.parent_version_id, av.status, av.bundle, av.created_at, av.updated_at
		FROM agents a
		LEFT JOIN agent_versions av ON av.agent_id = a.id
		ORDER BY a.created_at, av.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groupsByID := make(map[string]*types.AgentGroup)
	var order []string
	for rows.Next() {
		var aID, aName string
		var vID sql.NullString
		var vAgentID sql.NullString
		var vParent sql.NullString
		var vStatus sql.NullString
		var vBundle []byte
		var vCreated, vUpdated sql.NullTime
		if err := rows.Scan(&aID, &aName, &vID, &vAgentID, &vParent, &vStatus, &vBundle, &vCreated, &vUpdated); err != nil {
			return nil, err
		}
		g, ok := groupsByID[aID]
		if !ok {
			g = &types.AgentGroup{AgentID: aID, Name: aName}
			groupsByID[aID] = g
			order = append(order, aID)
		}
		if !vID.Valid {
			continue // agent with no versions yet
		}
		v := &types.AgentVersion{
			ID:        vID.String,
			AgentID:   vAgentID.String,
			Status:    vStatus.String,
			Bundle:    vBundle,
			CreatedAt: vCreated.Time,
			UpdatedAt: vUpdated.Time,
		}
		if vParent.Valid {
			v.ParentVersionID = &vParent.String
		}
		g.Versions = append(g.Versions, types.AgentVersionListItem{Version: v, VersionNum: 0})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute version numbers within each group by walking parent_version_id.
	// Sort newest-first for display once numbers are set.
	out := make([]types.AgentGroup, 0, len(order))
	for _, aID := range order {
		g := groupsByID[aID]
		assignVersionNumbers(g)
		sort.Slice(g.Versions, func(i, j int) bool {
			return g.Versions[i].VersionNum > g.Versions[j].VersionNum
		})
		out = append(out, *g)
	}
	return out, nil
}

// assignVersionNumbers walks the parent_version_id chain inside one group and
// assigns 1-based positional numbers (root = 1).
func assignVersionNumbers(g *types.AgentGroup) {
	byID := make(map[string]*types.AgentVersionListItem, len(g.Versions))
	for i := range g.Versions {
		byID[g.Versions[i].Version.ID] = &g.Versions[i]
	}
	for i := range g.Versions {
		v := g.Versions[i].Version
		num := 1
		for cur := v; cur.ParentVersionID != nil; {
			num++
			parent, ok := byID[*cur.ParentVersionID]
			if !ok {
				break
			}
			cur = parent.Version
		}
		g.Versions[i].VersionNum = num
	}
}
