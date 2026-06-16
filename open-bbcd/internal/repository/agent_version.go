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

const agentVersionColumns = `id, agent_id, parent_version_id, status, bundle, created_at, updated_at`

func scanAgentVersion(s scanner) (*types.AgentVersion, error) {
	v := &types.AgentVersion{}
	var parent sql.NullString
	var bundle []byte
	if err := s.Scan(&v.ID, &v.AgentID, &parent, &v.Status, &bundle, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if parent.Valid {
		v.ParentVersionID = &parent.String
	}
	v.Bundle = bundle
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
// Used by chat/configurator handlers that need both.
func (r *AgentVersionRepository) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT av.id::text, av.agent_id::text, av.parent_version_id, av.status, av.bundle, av.created_at, av.updated_at,
		       a.id::text, a.name, a.description, a.flow_map_config, a.flow_map_parse_error, a.discovery_file_path, a.created_at
		FROM agent_versions av
		JOIN agents a ON a.id = av.agent_id
		WHERE av.id = $1
	`, versionID)
	v := &types.AgentVersion{}
	a := &types.Agent{}
	var parent sql.NullString
	var bundle []byte
	var aDesc sql.NullString
	var aCfg []byte
	var aParseErr sql.NullString
	var aDisc sql.NullString
	err := row.Scan(&v.ID, &v.AgentID, &parent, &v.Status, &bundle, &v.CreatedAt, &v.UpdatedAt,
		&a.ID, &a.Name, &aDesc, &aCfg, &aParseErr, &aDisc, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, types.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if parent.Valid {
		v.ParentVersionID = &parent.String
	}
	v.Bundle = bundle
	a.Description = aDesc.String
	a.FlowMapConfig = aCfg
	a.FlowMapParseError = aParseErr.String
	a.DiscoveryFilePath = aDisc.String
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

// SetBundle persists the compiled bundle on a version row and transitions it to
// READY. force=false rejects a non-NULL bundle to prevent accidental overwrites.
func (r *AgentVersionRepository) SetBundle(ctx context.Context, versionID string, bundle []byte, force bool) error {
	var q string
	if force {
		q = `UPDATE agent_versions SET bundle = $2::jsonb, status = 'READY', updated_at = now() WHERE id = $1`
	} else {
		q = `UPDATE agent_versions SET bundle = $2::jsonb, status = 'READY', updated_at = now() WHERE id = $1 AND bundle IS NULL`
	}
	res, err := r.db.ExecContext(ctx, q, versionID, bundle)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return types.ErrBundleAlreadySet
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
