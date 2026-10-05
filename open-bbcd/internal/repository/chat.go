package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ChatRepository struct {
	db *sql.DB
	sessionArtifacts
}

func NewChatRepository(db *sql.DB) *ChatRepository {
	return &ChatRepository{db: db, sessionArtifacts: sessionArtifacts{
		db: db, table: "chat_session_artifacts", sessionTable: "chat_sessions", lockKey: chatSessionArtifactsLockKey,
		recheckSession: recheckChatSession, lockTurnSession: lockChatSessionForTurn,
	}}
}

// EnsureSession inserts a chat_sessions row with the given id if it
// doesn't already exist. If the row exists with a different agent_version_id,
// returns ErrSessionAgentMismatch. Idempotent: calling twice with the
// same (sessionID, versionID) is a no-op.
//
// The ON CONFLICT … DO UPDATE SET id = chat_sessions.id is a deliberate
// no-op update that lets RETURNING fire on conflict — without it,
// detecting mismatch would require a second round-trip.
func (r *ChatRepository) EnsureSession(ctx context.Context, sessionID, versionID string) error {
	var existingVersionID string
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO chat_sessions (id, agent_version_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (id) DO UPDATE SET id = chat_sessions.id
		RETURNING agent_version_id::text
	`, sessionID, versionID).Scan(&existingVersionID)
	if err != nil {
		return err
	}
	if existingVersionID != versionID {
		return types.ErrSessionAgentMismatch
	}
	return nil
}

// GetSession loads a single root session and verifies it belongs to versionID.
// Returns ErrNotFound if the session doesn't exist or is a sub-agent child
// (root-only rule) and ErrSessionAgentMismatch if it exists but is owned by a
// different agent version.
func (r *ChatRepository) GetSession(ctx context.Context, sessionID, versionID string) (*types.ChatSession, error) {
	s := &types.ChatSession{}
	var lockedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, agent_version_id::text, COALESCE(title, ''), created_at, updated_at, locked_at, depth
		FROM chat_sessions
		WHERE id = $1::uuid AND parent_session_id IS NULL
	`, sessionID).Scan(&s.ID, &s.AgentVersionID, &s.Title, &s.CreatedAt, &s.UpdatedAt, &lockedAt, &s.Depth)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, err
	}
	if s.AgentVersionID != versionID {
		return nil, types.ErrSessionAgentMismatch
	}
	if lockedAt.Valid {
		t := lockedAt.Time
		s.LockedAt = &t
	}
	return s, nil
}

// IsChildSession reports whether sessionID names a sub-agent child session.
// BO preambles that treat GetSession's ErrNotFound as "not created yet" call
// it first so a child id is a 404, never a lazily adopted session (spec §
// root-only rule). Unknown ids are not children.
func (r *ChatRepository) IsChildSession(ctx context.Context, sessionID string) (bool, error) {
	var child bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM chat_sessions WHERE id = $1::uuid AND parent_session_id IS NOT NULL)`,
		sessionID).Scan(&child)
	return child, err
}

// UpdateSessionTitle sets the title of a root session. Verifies the session
// belongs to versionID before updating; a child session is ErrNotFound. An empty title clears the column (NULL in DB).
func (r *ChatRepository) UpdateSessionTitle(ctx context.Context, sessionID, versionID, title string) error {
	var nullable sql.NullString
	if title != "" {
		nullable = sql.NullString{String: title, Valid: true}
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_sessions
		SET title = $3, updated_at = now()
		WHERE id = $1::uuid AND agent_version_id = $2::uuid AND parent_session_id IS NULL
	`, sessionID, versionID, nullable)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the session doesn't exist or it belongs to another agent version.
		// Distinguish for the caller.
		var existingVersion string
		err := r.db.QueryRowContext(ctx,
			`SELECT agent_version_id::text FROM chat_sessions WHERE id = $1::uuid AND parent_session_id IS NULL`,
			sessionID,
		).Scan(&existingVersion)
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

// ListSessions returns one page of root sessions for an agent version, newest
// first, plus the total row count (sub-agent children excluded from both). limit<=0 fetches everything.
func (r *ChatRepository) ListSessions(ctx context.Context, versionID string, limit, offset int) ([]*types.ChatSession, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_sessions WHERE agent_version_id = $1::uuid AND parent_session_id IS NULL`,
		versionID,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `
		SELECT id::text, agent_version_id::text, COALESCE(title, ''), created_at, updated_at, locked_at
		FROM chat_sessions
		WHERE agent_version_id = $1::uuid AND parent_session_id IS NULL
		ORDER BY created_at DESC`
	args := []any{versionID}
	if limit > 0 {
		q += " LIMIT $2 OFFSET $3"
		args = append(args, limit, offset)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*types.ChatSession
	for rows.Next() {
		s := &types.ChatSession{}
		var lockedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.AgentVersionID, &s.Title, &s.CreatedAt, &s.UpdatedAt, &lockedAt); err != nil {
			return nil, 0, err
		}
		if lockedAt.Valid {
			t := lockedAt.Time
			s.LockedAt = &t
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
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
//
// agentVersionID is accepted for interface conformance with the deployed-store
// sibling; BO chat sessions are already version-pinned via
// chat_sessions.agent_version_id, so this parameter is ignored here.
func (r *ChatRepository) AppendMessages(ctx context.Context, agentVersionID string, msgs []types.ChatMessage) error {
	_ = agentVersionID
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

// GetSessionHeaderOverrides returns the per-backend header override map for a
// session. The map is keyed by backend_id; values are header-name → value.
// Returns ErrNotFound if the session doesn't exist.
func (r *ChatRepository) GetSessionHeaderOverrides(ctx context.Context, sessionID string) (map[string]map[string]string, error) {
	const q = `SELECT backend_header_overrides FROM chat_sessions WHERE id = $1::uuid`
	var raw json.RawMessage
	err := r.db.QueryRowContext(ctx, q, sessionID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, err
	}
	var out map[string]map[string]string
	if len(raw) == 0 {
		return map[string]map[string]string{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetSessionHeaderOverrides replaces the per-backend header override map for a
// root session. Returns ErrNotFound if the session doesn't exist or is a
// sub-agent child.
func (r *ChatRepository) SetSessionHeaderOverrides(ctx context.Context, sessionID string, ovr map[string]map[string]string) error {
	raw, err := json.Marshal(ovr)
	if err != nil {
		return err
	}
	const q = `UPDATE chat_sessions SET backend_header_overrides = $1 WHERE id = $2::uuid AND parent_session_id IS NULL`
	res, err := r.db.ExecContext(ctx, q, raw, sessionID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}

// AppendUserTurn persists the user message and claims every pending artifact
// on the session in one transaction. Claimed refs are appended to
// msg.Content after its existing blocks, in (created_at, id) order, and
// returned. Returns types.ErrEmptyTurn (persisting nothing) if msg has no
// non-empty text block and nothing was claimed. agentVersionID is ignored
// (BO sessions are version-pinned), as in AppendMessages. There is no
// ON CONFLICT: a retried msg.ID errors, by design. msg.ID is required.
func (r *ChatRepository) AppendUserTurn(ctx context.Context, agentVersionID string, msg types.ChatMessage) ([]llm.ArtifactRefBlock, error) {
	_ = agentVersionID
	if msg.ID == "" {
		return nil, errors.New("message ID required")
	}
	return r.sessionArtifacts.appendUserTurn(ctx, msg.SessionID, msg.ID, msg.Content, func(tx *sql.Tx, content json.RawMessage) error {
		return r.insertChatMessageTx(ctx, tx, msg, content)
	})
}

// AppendToolMessage persists a tool-role message and one origin='tool_result'
// row per ref (message_id = msg.ID) in one transaction. refs may be empty.
// No ON CONFLICT: a retried msg.ID errors, by design. msg.ID is required.
func (r *ChatRepository) AppendToolMessage(ctx context.Context, agentVersionID string, msg types.ChatMessage, refs []llm.ArtifactRefBlock) error {
	_ = agentVersionID
	if msg.ID == "" {
		return errors.New("message ID required")
	}
	return r.sessionArtifacts.appendToolMessage(ctx, msg.SessionID, msg.ID, refs, func(tx *sql.Tx) error {
		return r.insertChatMessageTx(ctx, tx, msg, msg.Content)
	})
}

// insertChatMessageTx inserts one message and bumps the session's
// updated_at, as AppendMessages does.
func (r *ChatRepository) insertChatMessageTx(ctx context.Context, tx *sql.Tx, m types.ChatMessage, content json.RawMessage) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_messages (id, session_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)
	`, m.ID, m.SessionID, string(m.Role), []byte(content), m.Seq); err != nil {
		if isForeignKeyViolation(err) {
			return types.ErrNotFound
		}
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE chat_sessions SET updated_at = now() WHERE id = $1::uuid`, m.SessionID)
	return err
}
