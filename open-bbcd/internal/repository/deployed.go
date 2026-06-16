package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type DeployedRepository struct {
	db *sql.DB
}

func NewDeployedRepository(db *sql.DB) *DeployedRepository {
	return &DeployedRepository{db: db}
}

func scanDeployedSession(s scanner) (*types.DeployedSession, error) {
	sess := &types.DeployedSession{}
	var title sql.NullString
	err := s.Scan(&sess.ID, &sess.ChainRootID, &sess.UserID, &title, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sess.Title = title.String
	return sess, nil
}

const deployedSessionCols = `id::text, chain_root_id::text, user_id, title, created_at, updated_at`

// CreateSession inserts a session row. UserID is required (NOT NULL).
func (r *DeployedRepository) CreateSession(ctx context.Context, chainRootID, userID, title string) (*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO deployed_sessions (chain_root_id, user_id, title)
		VALUES ($1::uuid, $2, NULLIF($3, ''))
		RETURNING `+deployedSessionCols,
		chainRootID, userID, title,
	)
	return scanDeployedSession(row)
}

// GetSession returns the session iff (id, userID) matches a stored row.
// Returns ErrNotFound otherwise — including the case where the session exists
// under a different userID (no existence leak).
func (r *DeployedRepository) GetSession(ctx context.Context, sessionID, userID string) (*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+deployedSessionCols+` FROM deployed_sessions
		WHERE id = $1::uuid AND user_id = $2
	`, sessionID, userID)
	sess, err := scanDeployedSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return sess, err
}

// GetSessionByID returns the session row by id alone, ignoring user scope.
// The deployed-runtime handler validates (session_id, user_id) before
// calling into the orchestrator; once in the orchestrator the user scope has
// already been enforced, and the orchestrator only needs to verify the
// session belongs to the chain it claims.
func (r *DeployedRepository) GetSessionByID(ctx context.Context, sessionID string) (*types.DeployedSession, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+deployedSessionCols+` FROM deployed_sessions WHERE id = $1::uuid
	`, sessionID)
	sess, err := scanDeployedSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return sess, err
}

// ListSessions returns all sessions for (chainRootID, userID), newest first.
func (r *DeployedRepository) ListSessions(ctx context.Context, chainRootID, userID string) ([]*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+deployedSessionCols+` FROM deployed_sessions
		WHERE chain_root_id = $1::uuid AND user_id = $2
		ORDER BY created_at DESC
	`, chainRootID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.DeployedSession
	for rows.Next() {
		s, err := scanDeployedSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateSessionTitle scoped by user_id. Returns ErrNotFound if the row doesn't
// match.
func (r *DeployedRepository) UpdateSessionTitle(ctx context.Context, sessionID, userID, title string) error {
	if userID == "" {
		return types.ErrUserIDRequired
	}
	title = strings.TrimSpace(title)
	res, err := r.db.ExecContext(ctx, `
		UPDATE deployed_sessions
		SET title = NULLIF($3, ''), updated_at = now()
		WHERE id = $1::uuid AND user_id = $2
	`, sessionID, userID, title)
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

// DeleteSession scoped by user_id; cascades to messages via the FK.
func (r *DeployedRepository) DeleteSession(ctx context.Context, sessionID, userID string) error {
	if userID == "" {
		return types.ErrUserIDRequired
	}
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM deployed_sessions WHERE id = $1::uuid AND user_id = $2
	`, sessionID, userID)
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

// AppendMessages writes a batch of messages. Caller must supply seq values that
// are contiguous and not already used in the session (NextSeq returns the
// starting point for a turn).
func (r *DeployedRepository) AppendMessages(ctx context.Context, msgs []types.DeployedMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO deployed_messages (session_id, agent_version_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, m.SessionID, m.AgentVersionID, string(m.Role), []byte(m.Content), m.Seq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadMessages returns all messages for a session in seq order.
func (r *DeployedRepository) LoadMessages(ctx context.Context, sessionID string) ([]*types.DeployedMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, session_id::text, agent_version_id::text, role, content, seq, created_at
		FROM deployed_messages WHERE session_id = $1::uuid ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.DeployedMessage
	for rows.Next() {
		m := &types.DeployedMessage{}
		var content []byte
		if err := rows.Scan(&m.ID, &m.SessionID, &m.AgentVersionID, &m.Role, &content, &m.Seq, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Content = content
		out = append(out, m)
	}
	return out, rows.Err()
}

// NextSeq returns the next free sequence number for the session (max+1, or 1).
func (r *DeployedRepository) NextSeq(ctx context.Context, sessionID string) (int, error) {
	var n sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM deployed_messages WHERE session_id = $1::uuid`,
		sessionID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}
