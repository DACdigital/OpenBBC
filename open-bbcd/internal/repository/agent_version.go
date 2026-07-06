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

// Delete removes a single version row. Refuses if the version is currently
// DEPLOYED or if a newer version was forked from it (chain integrity — the
// chain is a linked list via parent_version_id and deleting a middle node
// would orphan the child).
func (r *AgentVersionRepository) Delete(ctx context.Context, versionID string) error {
	var status string
	var hasChild bool
	err := r.db.QueryRowContext(ctx, `
		SELECT v.status, EXISTS(SELECT 1 FROM agent_versions c WHERE c.parent_version_id = v.id)
		FROM agent_versions v WHERE v.id = $1::uuid
	`, versionID).Scan(&status, &hasChild)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "DEPLOYED" {
		return types.ErrVersionInUse
	}
	if hasChild {
		return types.ErrVersionHasChildren
	}
	// Sessions pinned inside any dataset (draft or closed) prevent
	// cascading version delete from succeeding. Surface a clean error.
	var pinned bool
	if err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM dataset_version_sessions dvs
		    JOIN chat_sessions s ON s.id = dvs.session_id
		    WHERE s.agent_version_id = $1::uuid
		)
	`, versionID).Scan(&pinned); err != nil {
		return err
	}
	if pinned {
		return types.ErrSessionInDataset
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM agent_versions WHERE id = $1::uuid`, versionID)
	return err
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

// insertVersionFromPromptsTx inserts a new agent_versions row inside an
// existing transaction. Copies MCP attachments forward. Returns the new id.
// Extracted so training-session Complete can bundle version-creation + session
// state update in one transaction. Public callers use CreateVersionFromPrompts.
func (r *AgentVersionRepository) insertVersionFromPromptsTx(ctx context.Context, tx *sql.Tx, parentVersionID string, promptsJSON []byte, status types.AgentStatus) (string, error) {
	var agentID string
	if err := tx.QueryRowContext(ctx,
		`SELECT agent_id::text FROM agent_versions WHERE id = $1`, parentVersionID,
	).Scan(&agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", types.ErrNotFound
		}
		return "", err
	}

	var newID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_versions (agent_id, parent_version_id, status, prompts)
		VALUES ($1::uuid, $2::uuid, $3::text, $4::jsonb)
		RETURNING id::text
	`, agentID, parentVersionID, string(status), promptsJSON).Scan(&newID); err != nil {
		return "", fmt.Errorf("insert new version: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note)
		SELECT $2::uuid, backend_id, note
		FROM agent_version_mcp_backend
		WHERE agent_version_id = $1::uuid
	`, parentVersionID, newID); err != nil {
		return "", fmt.Errorf("copy mcp attachments: %w", err)
	}

	return newID, nil
}

// CreateVersionFromPrompts forks a new agent_versions row from the parent
// version. The new row carries the submitted prompts and is created with the
// caller-supplied status — SavePrompts passes AgentStatusDraft (the user may
// still iterate before deploying) and LandPrompts passes AgentStatusReady
// (training is a supervised, y/N-gated action that already scored the bundle).
// parent_version_id links the version chain; agent_id stays the same
// (architecture is shared agent-wide).
//
// MCP attachments are copied forward in the same transaction so the new
// version inherits its predecessor's per-version wiring without manual
// re-attachment. Endpoint→backend wiring is agent-keyed and doesn't need
// copying.
//
// Returns the new version's id.
func (r *AgentVersionRepository) CreateVersionFromPrompts(ctx context.Context, parentVersionID string, promptsJSON []byte, status types.AgentStatus) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	newID, err := r.insertVersionFromPromptsTx(ctx, tx, parentVersionID, promptsJSON, status)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newID, nil
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

// GetFlowMapConfigForAgent returns the flow_map_config + parse_error of the
// agent's ROOT version (the wizard-created row where parent_version_id IS
// NULL). Used by agent-scoped views (Inputs tab on the agent detail page):
// flow_map_config is effectively per-agent — only one wizard run per agent
// — but it lives on agent_versions because it pre-dates the agent/version
// split. Reading the root row gives the canonical per-agent value.
func (r *AgentVersionRepository) GetFlowMapConfigForAgent(ctx context.Context, agentID string) ([]byte, string, error) {
	var cfg []byte
	var parseErr sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT flow_map_config, flow_map_parse_error
		FROM agent_versions
		WHERE agent_id = $1::uuid AND parent_version_id IS NULL
	`, agentID).Scan(&cfg, &parseErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", types.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return cfg, parseErr.String, nil
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
