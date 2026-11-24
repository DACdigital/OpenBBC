package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/lib/pq"
)

type ToolBackendRepository struct{ db *sql.DB }

func NewToolBackendRepository(db *sql.DB) *ToolBackendRepository {
	return &ToolBackendRepository{db: db}
}

func (r *ToolBackendRepository) Create(ctx context.Context, be *types.ToolBackend) error {
	const q = `INSERT INTO tool_backends (name, kind, config) VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at`
	err := r.db.QueryRowContext(ctx, q, be.Name, string(be.Kind), be.Config).
		Scan(&be.ID, &be.CreatedAt, &be.UpdatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return types.ErrToolBackendNameTaken
		}
	}
	return err
}

const toolBackendColumns = `id, name, kind, config, managed_by_seed, created_at, updated_at`

func (r *ToolBackendRepository) Get(ctx context.Context, id string) (*types.ToolBackend, error) {
	const q = `SELECT ` + toolBackendColumns + ` FROM tool_backends WHERE id = $1`
	be := &types.ToolBackend{}
	var kind string
	err := r.db.QueryRowContext(ctx, q, id).
		Scan(&be.ID, &be.Name, &kind, &be.Config, &be.ManagedBySeed, &be.CreatedAt, &be.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	be.Kind = types.ToolBackendKind(kind)
	return be, nil
}

// GetByName returns the backend with the given (unique) name, or ErrNotFound.
func (r *ToolBackendRepository) GetByName(ctx context.Context, name string) (*types.ToolBackend, error) {
	const q = `SELECT ` + toolBackendColumns + ` FROM tool_backends WHERE name = $1`
	be := &types.ToolBackend{}
	var kind string
	err := r.db.QueryRowContext(ctx, q, name).
		Scan(&be.ID, &be.Name, &kind, &be.Config, &be.ManagedBySeed, &be.CreatedAt, &be.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	be.Kind = types.ToolBackendKind(kind)
	return be, nil
}

func (r *ToolBackendRepository) List(ctx context.Context) ([]*types.ToolBackend, error) {
	const q = `SELECT ` + toolBackendColumns + ` FROM tool_backends ORDER BY name`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*types.ToolBackend{}
	for rows.Next() {
		be := &types.ToolBackend{}
		var kind string
		if err := rows.Scan(&be.ID, &be.Name, &kind, &be.Config, &be.ManagedBySeed, &be.CreatedAt, &be.UpdatedAt); err != nil {
			return nil, err
		}
		be.Kind = types.ToolBackendKind(kind)
		out = append(out, be)
	}
	return out, rows.Err()
}

// lockManaged selects the row FOR UPDATE inside tx and returns ErrNotFound if
// it is missing or ErrBackendManaged if it is owned by the seed file.
func lockManaged(ctx context.Context, tx *sql.Tx, id string) error {
	var managed bool
	err := tx.QueryRowContext(ctx,
		`SELECT managed_by_seed FROM tool_backends WHERE id = $1 FOR UPDATE`, id).Scan(&managed)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if managed {
		return types.ErrBackendManaged
	}
	return nil
}

// Update updates the name and config of an existing tool backend.
// Kind is immutable after creation — changing kind would require a new backend
// row, since wired endpoints/MCPs are kind-specific.
// Backends managed by the seed file are read-only: ErrBackendManaged.
func (r *ToolBackendRepository) Update(ctx context.Context, be *types.ToolBackend) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockManaged(ctx, tx, be.ID); err != nil {
		return err
	}
	const q = `UPDATE tool_backends SET name = $1, config = $2, updated_at = now() WHERE id = $3
		RETURNING updated_at`
	err = tx.QueryRowContext(ctx, q, be.Name, be.Config, be.ID).Scan(&be.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return types.ErrToolBackendNameTaken
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes a backend. Backends managed by the seed file are read-only:
// ErrBackendManaged.
func (r *ToolBackendRepository) Delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockManaged(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tool_backends WHERE id = $1`, id); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23503" {
			return types.ErrToolBackendInUse
		}
		return err
	}
	return tx.Commit()
}
