// open-bbcd/internal/repository/agent.go
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
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
	var parentVersionID sql.NullString
	var wizardInput []byte
	var schemaVersion sql.NullString
	err := s.Scan(
		&agent.ID,
		&agent.Name,
		&description,
		&agent.Prompt,
		&agent.Status,
		&parentVersionID,
		&wizardInput,
		&schemaVersion,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	agent.Description = description.String
	if parentVersionID.Valid {
		agent.ParentVersionID = &parentVersionID.String
	}
	agent.WizardInput = wizardInput
	if schemaVersion.Valid {
		agent.SchemaVersion = schemaVersion.String
	}
	return agent, nil
}

const agentColumns = `id, name, description, prompt, status, parent_version_id, wizard_input, schema_version, created_at, updated_at`

func (r *AgentRepository) Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
	agent, err := types.NewAgent(opts)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (name, description, prompt)
		VALUES ($1, $2, $3)
		RETURNING `+agentColumns,
		agent.Name, agent.Description, agent.Prompt,
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

// CreateVersion inserts a new DRAFT agent whose parent_version_id points to parentID.
// Name and description are inherited from the parent; only prompt is editable.
func (r *AgentRepository) CreateVersion(ctx context.Context, parentID string, opts types.CreateVersionOpts) (*types.Agent, error) {
	if opts.Prompt == "" {
		return nil, types.ErrPromptRequired
	}
	parent, err := r.GetByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	desc := sql.NullString{String: parent.Description, Valid: parent.Description != ""}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (name, description, prompt, parent_version_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+agentColumns,
		parent.Name, desc, opts.Prompt, parentID,
	)
	return scanAgent(row)
}

// GetVersionChain returns the full chain that contains agentID (matched as root or any version).
func (r *AgentRepository) GetVersionChain(ctx context.Context, agentID string) (types.AgentChain, error) {
	chains, err := r.ListGrouped(ctx)
	if err != nil {
		return types.AgentChain{}, err
	}
	for _, chain := range chains {
		if chain.RootID == agentID {
			return chain, nil
		}
		for _, v := range chain.Versions {
			if v.Agent.ID == agentID {
				return chain, nil
			}
		}
	}
	return types.AgentChain{}, types.ErrNotFound
}

// CreateFromWizard inserts an agent in INITIALIZING status from wizard form answers.
func (r *AgentRepository) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
	if opts.Name == "" {
		return nil, types.ErrNameRequired
	}
	if r.db == nil {
		return nil, errors.New("repository: no database connection")
	}
	wizardJSON, err := json.Marshal(opts.WizardInput)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO agents (name, prompt, status, wizard_input, schema_version)
		VALUES ($1, '', $2, $3, $4)
		RETURNING `+agentColumns,
		opts.Name, string(types.AgentStatusInitializing), wizardJSON, opts.SchemaVersion,
	)
	return scanAgent(row)
}
