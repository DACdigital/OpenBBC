package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ChatRepository struct {
	db *sql.DB
}

func NewChatRepository(db *sql.DB) *ChatRepository {
	return &ChatRepository{db: db}
}

// EnsureSession inserts a chat_sessions row with the given id if it
// doesn't already exist. If the row exists with a different agent_id,
// returns ErrSessionAgentMismatch. Idempotent: calling twice with the
// same (sessionID, agentID) is a no-op.
//
// The ON CONFLICT … DO UPDATE SET id = chat_sessions.id is a deliberate
// no-op update that lets RETURNING fire on conflict — without it,
// detecting mismatch would require a second round-trip.
func (r *ChatRepository) EnsureSession(ctx context.Context, sessionID, agentID string) error {
	var existingAgentID string
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO chat_sessions (id, agent_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (id) DO UPDATE SET id = chat_sessions.id
		RETURNING agent_id::text
	`, sessionID, agentID).Scan(&existingAgentID)
	if err != nil {
		return err
	}
	if existingAgentID != agentID {
		return types.ErrSessionAgentMismatch
	}
	return nil
}

// GetSession loads a single session and verifies it belongs to agentID.
// Returns ErrNotFound if the session doesn't exist and ErrSessionAgentMismatch
// if it exists but is owned by a different agent.
func (r *ChatRepository) GetSession(ctx context.Context, sessionID, agentID string) (*types.ChatSession, error) {
	s := &types.ChatSession{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, agent_id::text, COALESCE(title, ''), created_at, updated_at
		FROM chat_sessions
		WHERE id = $1::uuid
	`, sessionID).Scan(&s.ID, &s.AgentID, &s.Title, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, err
	}
	if s.AgentID != agentID {
		return nil, types.ErrSessionAgentMismatch
	}
	return s, nil
}

// UpdateSessionTitle sets the title of a session. Verifies the session belongs
// to agentID before updating. An empty title clears the column (NULL in DB).
func (r *ChatRepository) UpdateSessionTitle(ctx context.Context, sessionID, agentID, title string) error {
	var nullable sql.NullString
	if title != "" {
		nullable = sql.NullString{String: title, Valid: true}
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_sessions
		SET title = $3, updated_at = now()
		WHERE id = $1::uuid AND agent_id = $2::uuid
	`, sessionID, agentID, nullable)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the session doesn't exist or it belongs to another agent.
		// Distinguish for the caller.
		var existingAgent string
		err := r.db.QueryRowContext(ctx,
			`SELECT agent_id::text FROM chat_sessions WHERE id = $1::uuid`,
			sessionID,
		).Scan(&existingAgent)
		if errors.Is(err, sql.ErrNoRows) {
			return types.ErrNotFound
		}
		if err != nil {
			return err
		}
		return types.ErrSessionAgentMismatch
	}
	return nil
}

// ListSessions returns all sessions for an agent, newest first.
func (r *ChatRepository) ListSessions(ctx context.Context, agentID string) ([]*types.ChatSession, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, agent_id::text, COALESCE(title, ''), created_at, updated_at
		FROM chat_sessions
		WHERE agent_id = $1::uuid
		ORDER BY created_at DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*types.ChatSession
	for rows.Next() {
		s := &types.ChatSession{}
		if err := rows.Scan(&s.ID, &s.AgentID, &s.Title, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LoadMessages returns all messages for a session ordered by seq ASC.
func (r *ChatRepository) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, session_id::text, role, content, seq, created_at
		FROM chat_messages
		WHERE session_id = $1::uuid
		ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*types.ChatMessage
	for rows.Next() {
		m := &types.ChatMessage{}
		var role string
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &m.Seq, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Role = types.ChatRole(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendMessages inserts a batch of messages in a single transaction.
// Each message must have a unique seq within the session (enforced by
// UNIQUE constraint). Idempotent on (id) — ON CONFLICT DO NOTHING.
// Also bumps the session's updated_at.
func (r *ChatRepository) AppendMessages(ctx context.Context, msgs []types.ChatMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO chat_messages (id, session_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, m.ID, m.SessionID, string(m.Role), []byte(m.Content), m.Seq); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_sessions SET updated_at = now() WHERE id = $1::uuid
	`, msgs[0].SessionID); err != nil {
		return err
	}

	return tx.Commit()
}

// NextSeq returns the next seq value for a session (max+1, or 1 if no
// messages exist yet). Note: there's an inherent race window between
// NextSeq and AppendMessages — callers must serialize per-session writes,
// or accept that two concurrent writers may collide on the UNIQUE
// (session_id, seq) constraint and need to retry. For v1 BO testing this
// is acceptable (one writer per session in practice).
func (r *ChatRepository) NextSeq(ctx context.Context, sessionID string) (int, error) {
	var seq sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT MAX(seq) FROM chat_messages WHERE session_id = $1::uuid
	`, sessionID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 1, nil
	}
	return int(seq.Int64) + 1, nil
}
