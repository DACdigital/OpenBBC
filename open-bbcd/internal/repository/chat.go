package repository

import (
	"context"
	"database/sql"

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
