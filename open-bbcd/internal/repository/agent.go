// open-bbcd/internal/repository/agent.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(s scanner) (*types.Agent, error) {
	agent := &types.Agent{}
	var description sql.NullString
	var bundle []byte
	var parentVersionID sql.NullString
	var flowMapConfig []byte
	var flowMapParseError sql.NullString
	var discoveryFilePath sql.NullString
	err := s.Scan(
		&agent.ID,
		&agent.ChainRootID,
		&agent.Name,
		&description,
		&bundle,
		&agent.Status,
		&parentVersionID,
		&flowMapConfig,
		&flowMapParseError,
		&discoveryFilePath,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	agent.Description = description.String
	agent.Bundle = bundle
	if parentVersionID.Valid {
		agent.ParentVersionID = &parentVersionID.String
	}
	agent.FlowMapConfig = flowMapConfig
	if flowMapParseError.Valid {
		agent.FlowMapParseError = flowMapParseError.String
	}
	if discoveryFilePath.Valid {
		agent.DiscoveryFilePath = discoveryFilePath.String
	}
	return agent, nil
}

// agentColumns lists the SELECT/RETURNING columns. Must stay in sync with
// scanAgent's positional scan destinations.
const agentColumns = `id, chain_root_id, name, description, bundle, status, parent_version_id, flow_map_config, flow_map_parse_error, discovery_file_path, created_at, updated_at`

func (r *AgentRepository) Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
	agent, err := types.NewAgent(opts)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (id, chain_root_id, name, description)
		VALUES ($1::uuid, $1::uuid, $2, $3)
		RETURNING `+agentColumns,
		id, agent.Name, agent.Description,
	)
	return scanAgent(row)
}

func (r *AgentRepository) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+agentColumns+` FROM agents WHERE id = $1
	`, id)
	agent, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return agent, err
}

func (r *AgentRepository) List(ctx context.Context) ([]*types.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+agentColumns+` FROM agents ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*types.Agent
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

// ListGrouped fetches all agents and groups them into named version chains.
// Within each chain, versions are ordered newest first. Version numbers are
// computed from position in the parent_version_id linked list.
func (r *AgentRepository) ListGrouped(ctx context.Context) ([]types.AgentChain, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+agentColumns+` FROM agents ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []*types.Agent
	byID := make(map[string]*types.Agent)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, agent)
		byID[agent.ID] = agent
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Walk each agent up to its chain root.
	rootOf := func(cur *types.Agent) string {
		for cur.ParentVersionID != nil {
			parent, ok := byID[*cur.ParentVersionID]
			if !ok {
				break
			}
			cur = parent
		}
		return cur.ID
	}

	type accumulator struct {
		name     string
		versions []*types.Agent // oldest first (creation order from query)
	}
	chainMap := make(map[string]*accumulator)
	var rootOrder []string // preserves first-seen order for stable output

	for _, a := range all {
		rootID := rootOf(a)
		if _, exists := chainMap[rootID]; !exists {
			// Chain name is always the root agent's name; agent names are
			// immutable per chain so this is stable across all versions.
			chainMap[rootID] = &accumulator{name: byID[rootID].Name}
			rootOrder = append(rootOrder, rootID)
		}
		chainMap[rootID].versions = append(chainMap[rootID].versions, a)
	}

	chains := make([]types.AgentChain, 0, len(rootOrder))
	for _, rootID := range rootOrder {
		acc := chainMap[rootID]
		versions := make([]types.AgentVersion, len(acc.versions))
		for i, a := range acc.versions {
			versions[i] = types.AgentVersion{Agent: a, VersionNum: i + 1}
		}
		// Reverse so newest version is first in the slice (for display).
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].VersionNum > versions[j].VersionNum
		})
		chains = append(chains, types.AgentChain{RootID: rootID, Name: acc.name, Versions: versions})
	}
	return chains, nil
}

// ChainRootID walks parent_version_id up from agentID to the chain root and
// returns its ID. Returns ErrNotFound if agentID does not exist.
func (r *AgentRepository) ChainRootID(ctx context.Context, agentID string) (string, error) {
	curID := agentID
	for {
		var parent sql.NullString
		err := r.db.QueryRowContext(ctx,
			`SELECT parent_version_id FROM agents WHERE id = $1`,
			curID,
		).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return "", types.ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if !parent.Valid {
			return curID, nil
		}
		curID = parent.String
	}
}

// CreateFromWizard inserts an agent in INITIALIZING status from wizard form
// answers. The agent's UUID and discovery-file key are pre-generated by the
// caller so the file write happens before the row insert. opts.ID, when
// non-empty, must be a valid UUID string (Postgres rejects malformed input).
func (r *AgentRepository) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
	if opts.Name == "" {
		return nil, types.ErrNameRequired
	}
	if r.db == nil {
		return nil, errors.New("repository: no database connection")
	}
	id := opts.ID
	if id == "" {
		id = uuid.NewString()
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (id, chain_root_id, name, status, flow_map_config, flow_map_parse_error, discovery_file_path)
		VALUES ($1::uuid, $1::uuid, $2, 'INITIALIZING', $3, NULLIF($4, ''), NULLIF($5, ''))
		RETURNING `+agentColumns,
		id, opts.Name, []byte(opts.FlowMapConfig), opts.FlowMapParseError, opts.DiscoveryFilePath,
	)
	return scanAgent(row)
}

// GetFlowMapConfig returns the agent's parsed config (or nil bytes if absent).
// Returns ErrNotFound if no agent has the given id.
func (r *AgentRepository) GetFlowMapConfig(ctx context.Context, agentID string) ([]byte, string, error) {
	var cfg []byte
	var parseErr sql.NullString
	row := r.db.QueryRowContext(ctx, `
		SELECT flow_map_config, flow_map_parse_error FROM agents WHERE id = $1
	`, agentID)
	if err := row.Scan(&cfg, &parseErr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", types.ErrNotFound
		}
		return nil, "", err
	}
	return cfg, parseErr.String, nil
}

// UpdateFlowMapConfig overwrites the agent's flow_map_config and clears any
// prior parse error. Used by configurator edit endpoints (PR2/PR3).
func (r *AgentRepository) UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents
		SET flow_map_config = $2, flow_map_parse_error = NULL, updated_at = now()
		WHERE id = $1
	`, agentID, cfg)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}

// SetBundle writes the agent's bundle JSONB. Without force, refuses to
// overwrite a non-NULL bundle (write-once enforcement). With force, overrides.
// Returns ErrNotFound if the agent doesn't exist; ErrBundleAlreadySet if a
// bundle exists and force is false.
func (r *AgentRepository) SetBundle(ctx context.Context, agentID string, bundle []byte, force bool) error {
	if force {
		res, err := r.db.ExecContext(ctx, `
			UPDATE agents SET bundle = $2, updated_at = now() WHERE id = $1
		`, agentID, bundle)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return types.ErrNotFound
		}
		return nil
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents SET bundle = $2, updated_at = now()
		WHERE id = $1 AND bundle IS NULL
	`, agentID, bundle)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either not found, or bundle is already set.
		var exists bool
		if err := r.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, agentID,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return types.ErrNotFound
		}
		return types.ErrBundleAlreadySet
	}
	return nil
}

// UpdateStatus transitions the agent's status. Used by Finalize
// (INITIALIZING → DRAFT). Returns ErrInvalidAgentStatus if the current
// status doesn't match expectedFrom — preventing accidental re-finalize.
func (r *AgentRepository) UpdateStatus(ctx context.Context, agentID, expectedFrom, to string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents
		SET status = $3, updated_at = now()
		WHERE id = $1 AND status = $2
	`, agentID, expectedFrom, to)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the agent doesn't exist, or its status isn't expectedFrom.
		var cur string
		row := r.db.QueryRowContext(ctx, `SELECT status FROM agents WHERE id = $1`, agentID)
		if err := row.Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return types.ErrNotFound
			}
			return err
		}
		return fmt.Errorf("%w: have %q, want %q", types.ErrInvalidAgentStatus, cur, expectedFrom)
	}
	return nil
}
