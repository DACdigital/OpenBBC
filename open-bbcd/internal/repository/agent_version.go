// open-bbcd/internal/repository/agent_version.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentVersionRepository struct {
	db *sql.DB
}

func NewAgentVersionRepository(db *sql.DB) *AgentVersionRepository {
	return &AgentVersionRepository{db: db}
}

const agentVersionColumns = `id, agent_id, parent_version_id, status, prompts, flow_map_config, flow_map_parse_error, created_at, updated_at`

func scanAgentVersion(s scanner) (*types.AgentVersion, error) {
	v := &types.AgentVersion{}
	var parent sql.NullString
	var prompts []byte
	var cfg []byte
	var parseErr sql.NullString
	if err := s.Scan(&v.ID, &v.AgentID, &parent, &v.Status, &prompts, &cfg, &parseErr, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if parent.Valid {
		v.ParentVersionID = &parent.String
	}
	v.Prompts = prompts
	v.FlowMapConfig = cfg
	v.FlowMapParseError = parseErr.String
	return v, nil
}

// GetByID returns the AgentVersion row by id.
func (r *AgentVersionRepository) GetByID(ctx context.Context, versionID string) (*types.AgentVersion, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+agentVersionColumns+` FROM agent_versions WHERE id = $1`, versionID)
	v, err := scanAgentVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return v, err
}

// GetWithAgent returns both the AgentVersion and its owning Agent via JOIN.
// Used by chat/configurator handlers that need both — the agent carries
// the frozen architecture, the version carries the editable prompts.
func (r *AgentVersionRepository) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT av.id::text, av.agent_id::text, av.parent_version_id, av.status, av.prompts, av.flow_map_config, av.flow_map_parse_error, av.created_at, av.updated_at,
		       a.id::text, a.name, a.description, a.discovery_file_path, a.architecture, a.finalized_at, a.created_at
		FROM agent_versions av
		JOIN agents a ON a.id = av.agent_id
		WHERE av.id = $1
	`, versionID)
	v := &types.AgentVersion{}
	a := &types.Agent{}
	var parent sql.NullString
	var prompts []byte
	var vCfg []byte
	var vParseErr sql.NullString
	var aDesc sql.NullString
	var aDisc sql.NullString
	var arch []byte
	var aFinal sql.NullTime
	err := row.Scan(&v.ID, &v.AgentID, &parent, &v.Status, &prompts, &vCfg, &vParseErr, &v.CreatedAt, &v.UpdatedAt,
		&a.ID, &a.Name, &aDesc, &aDisc, &arch, &aFinal, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, types.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if parent.Valid {
		v.ParentVersionID = &parent.String
	}
	v.Prompts = prompts
	v.FlowMapConfig = vCfg
	v.FlowMapParseError = vParseErr.String
	a.Description = aDesc.String
	a.DiscoveryFilePath = aDisc.String
	a.Architecture = arch
	if aFinal.Valid {
		t := aFinal.Time
		a.FinalizedAt = &t
	}
	return v, a, nil
}

// Deploy promotes versionID to DEPLOYED, demoting any other DEPLOYED version of
// the same agent. Returns the previously-deployed version ID (or nil).
func (r *AgentVersionRepository) Deploy(ctx context.Context, versionID string) (*string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var status, agentID string
	err = tx.QueryRowContext(ctx,
		`SELECT status, agent_id::text FROM agent_versions WHERE id = $1`, versionID,
	).Scan(&status, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == string(types.AgentStatusDeployed) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if status != string(types.AgentStatusReady) {
		return nil, types.ErrAgentNotDeployable
	}
	var prevID sql.NullString
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_versions SET status = 'READY', updated_at = now()
		WHERE agent_id = $1 AND status = 'DEPLOYED' AND id != $2
		RETURNING id::text
	`, agentID, versionID).Scan(&prevID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("demote previous: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_versions SET status='DEPLOYED', updated_at = now() WHERE id = $1`, versionID,
	); err != nil {
		return nil, fmt.Errorf("promote target: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if prevID.Valid {
		return &prevID.String, nil
	}
	return nil, nil
}

// Undeploy demotes the version to READY.
func (r *AgentVersionRepository) Undeploy(ctx context.Context, versionID string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE agent_versions SET status='READY', updated_at = now() WHERE id = $1 AND status = 'DEPLOYED'`,
		versionID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var dummy string
		err := r.db.QueryRowContext(ctx, `SELECT id::text FROM agent_versions WHERE id = $1`, versionID).Scan(&dummy)
		if errors.Is(err, sql.ErrNoRows) {
			return types.ErrNotFound
		}
		if err != nil {
			return err
		}
		return types.ErrAgentNotDeployed
	}
	return nil
}

// CurrentDeployedID returns the version ID currently DEPLOYED for the given
// agent, or "" if none.
func (r *AgentVersionRepository) CurrentDeployedID(ctx context.Context, agentID string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id::text FROM agent_versions WHERE agent_id = $1 AND status = 'DEPLOYED'`,
		agentID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// LandBundle splits an aikdm bundle into the agent-level architecture
// (frozen on first land via finalized_at) and the version-level prompts,
// in a single transaction. Sets the version status to READY.
//
// force=false rejects a re-land when prompts are already non-default,
// preserving the "bundle already set" contract that callers depend on.
// force=true overwrites prompts unconditionally and re-stamps architecture
// on the parent agent (dev seed escape hatch).
func (r *AgentVersionRepository) LandBundle(ctx context.Context, versionID string, bundleJSON []byte, force bool) error {
	archJSON, promptsJSON, err := types.SplitBundle(bundleJSON)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var agentID string
	var promptsEmpty bool
	err = tx.QueryRowContext(ctx, `
		SELECT agent_id::text, (prompts IS NULL OR prompts::text = '{}'::jsonb::text)
		FROM agent_versions WHERE id = $1
	`, versionID).Scan(&agentID, &promptsEmpty)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !force && !promptsEmpty {
		return types.ErrBundleAlreadySet
	}

	// Architecture is frozen on first land; finalized_at gets stamped iff null.
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET architecture = $2::jsonb,
		    finalized_at = COALESCE(finalized_at, now())
		WHERE id = $1
	`, agentID, archJSON); err != nil {
		return fmt.Errorf("write architecture: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_versions
		SET prompts = $2::jsonb, status = 'READY', updated_at = now()
		WHERE id = $1
	`, versionID, promptsJSON); err != nil {
		return fmt.Errorf("write prompts: %w", err)
	}

	return tx.Commit()
}

// SetPrompts overwrites the version's prompts row (no agent-level effect).
// Used by the prompts editor's "save creates new version" flow — the new
// version is inserted with the edited prompts in one step rather than via
// this method, but exposing it keeps the data layer symmetrical.
func (r *AgentVersionRepository) SetPrompts(ctx context.Context, versionID string, promptsJSON []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agent_versions SET prompts = $2::jsonb, updated_at = now() WHERE id = $1
	`, versionID, promptsJSON)
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

// UpdateStatus performs a guarded status transition on a version row.
func (r *AgentVersionRepository) UpdateStatus(ctx context.Context, versionID, expectedFrom, to string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE agent_versions SET status = $3, updated_at = now() WHERE id = $1 AND status = $2`,
		versionID, expectedFrom, to,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return types.ErrInvalidAgentStatus
	}
	return nil
}

// GetFlowMapConfig returns the version's flow_map_config + parse_error.
// Returns ErrNotFound if the version does not exist.
func (r *AgentVersionRepository) GetFlowMapConfig(ctx context.Context, versionID string) ([]byte, string, error) {
	var cfg []byte
	var parseErr sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT flow_map_config, flow_map_parse_error FROM agent_versions WHERE id = $1`,
		versionID,
	).Scan(&cfg, &parseErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", types.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return cfg, parseErr.String, nil
}

// UpdateFlowMapConfig overwrites this version's config in place. (Future:
// when the FE enables edits, this should spawn a new version. Out of scope
// for now.)
func (r *AgentVersionRepository) UpdateFlowMapConfig(ctx context.Context, versionID string, cfg []byte) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE agent_versions SET flow_map_config = $2, flow_map_parse_error = NULL, updated_at = now() WHERE id = $1`,
		versionID, cfg,
	)
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
