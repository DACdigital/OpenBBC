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

func (r *ToolBackendRepository) Get(ctx context.Context, id string) (*types.ToolBackend, error) {
	const q = `SELECT id, name, kind, config, created_at, updated_at FROM tool_backends WHERE id = $1`
	be := &types.ToolBackend{}
	var kind string
	err := r.db.QueryRowContext(ctx, q, id).
		Scan(&be.ID, &be.Name, &kind, &be.Config, &be.CreatedAt, &be.UpdatedAt)
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
	const q = `SELECT id, name, kind, config, created_at, updated_at FROM tool_backends ORDER BY name`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*types.ToolBackend{}
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

func (r *ToolBackendRepository) Update(ctx context.Context, be *types.ToolBackend) error {
	const q = `UPDATE tool_backends SET name = $1, config = $2, updated_at = now() WHERE id = $3
		RETURNING updated_at`
	err := r.db.QueryRowContext(ctx, q, be.Name, be.Config, be.ID).Scan(&be.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return types.ErrToolBackendNameTaken
	}
	return err
}

func (r *ToolBackendRepository) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM tool_backends WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23503" {
			return types.ErrToolBackendInUse
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}
