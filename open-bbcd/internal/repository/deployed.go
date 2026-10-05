package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type DeployedRepository struct {
	db *sql.DB
	sessionArtifacts
}

func NewDeployedRepository(db *sql.DB) *DeployedRepository {
	return &DeployedRepository{db: db, sessionArtifacts: sessionArtifacts{
		db: db, table: "deployed_session_artifacts", sessionTable: "deployed_sessions", lockKey: deployedSessionArtifactsLockKey,
		recheckSession: recheckDeployedSession,
	}}
}

func scanDeployedSession(s scanner) (*types.DeployedSession, error) {
	sess := &types.DeployedSession{}
	var title, parentID, versionID sql.NullString
	err := s.Scan(&sess.ID, &sess.AgentID, &sess.UserID, &title, &sess.CreatedAt, &sess.UpdatedAt,
		&parentID, &sess.ParentToolCallID, &sess.Depth, &versionID)
	if err != nil {
		return nil, err
	}
	sess.Title = title.String
	if parentID.Valid {
		sess.ParentSessionID = &parentID.String
	}
	if versionID.Valid {
		sess.AgentVersionID = &versionID.String
	}
	return sess, nil
}

const deployedSessionCols = `id::text, agent_id::text, user_id, title, created_at, updated_at, ` +
	`parent_session_id::text, COALESCE(parent_tool_call_id, ''), depth, agent_version_id::text`

// CreateSession inserts a session row. UserID is required (NOT NULL).
func (r *DeployedRepository) CreateSession(ctx context.Context, agentID, userID, title string) (*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO deployed_sessions (agent_id, user_id, title)
		VALUES ($1::uuid, $2, NULLIF($3, ''))
		RETURNING `+deployedSessionCols,
		agentID, userID, title,
	)
	return scanDeployedSession(row)
}

// GetSession returns the root session iff (id, userID) matches a stored row.
// Returns ErrNotFound otherwise — including the case where the session exists
// under a different userID (no existence leak) or is a sub-agent child
// (root-only rule).
func (r *DeployedRepository) GetSession(ctx context.Context, sessionID, userID string) (*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+deployedSessionCols+` FROM deployed_sessions
		WHERE id = $1::uuid AND user_id = $2 AND parent_session_id IS NULL
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
// session belongs to the chain it claims. Exempt from the root-only rule:
// child turns resolve their session through it. No route preamble calls it.
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

// ListSessions returns all root sessions for (agentID, userID), newest first.
func (r *DeployedRepository) ListSessions(ctx context.Context, agentID, userID string) ([]*types.DeployedSession, error) {
	if userID == "" {
		return nil, types.ErrUserIDRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+deployedSessionCols+` FROM deployed_sessions
		WHERE agent_id = $1::uuid AND user_id = $2 AND parent_session_id IS NULL
		ORDER BY created_at DESC
	`, agentID, userID)
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

// UpdateSessionTitle scoped by (agent_id, user_id) on a root session. Returns
// ErrNotFound if no root row matches (a sub-agent child is never renamed).
func (r *DeployedRepository) UpdateSessionTitle(ctx context.Context, agentID, sessionID, userID, title string) error {
	if userID == "" {
		return types.ErrUserIDRequired
	}
	title = strings.TrimSpace(title)
	res, err := r.db.ExecContext(ctx, `
		UPDATE deployed_sessions
		SET title = NULLIF($3, ''), updated_at = now()
		WHERE id = $1::uuid AND user_id = $2 AND agent_id = $4::uuid AND parent_session_id IS NULL
	`, sessionID, userID, title, agentID)
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

// DeleteSession scoped by (agent_id, user_id) on a root session; cascades to
// descendant sessions, messages and artifacts via the FKs. A sub-agent child
// is ErrNotFound and is left in place.
func (r *DeployedRepository) DeleteSession(ctx context.Context, agentID, sessionID, userID string) error {
	if userID == "" {
		return types.ErrUserIDRequired
	}
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM deployed_sessions
		WHERE id = $1::uuid AND user_id = $2 AND agent_id = $3::uuid AND parent_session_id IS NULL
	`, sessionID, userID, agentID)
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
// starting point for a turn). A non-empty ID is persisted as given; an empty
// ID is generated by the database.
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
		INSERT INTO deployed_messages (id, session_id, agent_version_id, role, content, seq)
		VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2::uuid, $3::uuid, $4, $5, $6)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range msgs {
		if _, err := stmt.ExecContext(ctx, m.ID, m.SessionID, m.AgentVersionID, string(m.Role), []byte(m.Content), m.Seq); err != nil {
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

// AppendUserTurn: see ChatRepository.AppendUserTurn. The message id is
// inserted explicitly (m.ID, required) so claimed rows' message_id names the
// real row. Unlike the BO method it takes a DeployedMessage (AgentVersionID on
// the message) instead of an agentVersionID argument. No ON CONFLICT: a
// retried m.ID errors, by design.
func (r *DeployedRepository) AppendUserTurn(ctx context.Context, m types.DeployedMessage) ([]llm.ArtifactRefBlock, error) {
	if m.ID == "" {
		return nil, errors.New("message ID required")
	}
	return r.sessionArtifacts.appendUserTurn(ctx, m.SessionID, m.ID, m.Content, func(tx *sql.Tx, content json.RawMessage) error {
		return insertDeployedMessageTx(ctx, tx, m, content)
	})
}

// AppendToolMessage: see ChatRepository.AppendToolMessage. It takes a
// DeployedMessage (AgentVersionID on the message) instead of an agentVersionID
// argument; m.ID is required.
func (r *DeployedRepository) AppendToolMessage(ctx context.Context, m types.DeployedMessage, refs []llm.ArtifactRefBlock) error {
	if m.ID == "" {
		return errors.New("message ID required")
	}
	return r.sessionArtifacts.appendToolMessage(ctx, m.SessionID, m.ID, refs, func(tx *sql.Tx) error {
		return insertDeployedMessageTx(ctx, tx, m, m.Content)
	})
}

func insertDeployedMessageTx(ctx context.Context, tx *sql.Tx, m types.DeployedMessage, content json.RawMessage) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO deployed_messages (id, session_id, agent_version_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
	`, m.ID, m.SessionID, m.AgentVersionID, string(m.Role), []byte(content), m.Seq)
	if isForeignKeyViolation(err) {
		return types.ErrNotFound
	}
	return err
}

// CreateChildSession inserts a sub-agent child session under parentID in one
// transaction (spec § Repository invariants). It locks the tree's root FOR
// SHARE (deployed sessions have no locked_at, so there is no lock check). The
// child carries the root's agent_id and user_id, is pinned to
// targetVersionID, and has depth = parent.depth + 1. parentToolCallID is the
// raw tool_use id. ErrNotFound when rootID is not a root session, parentID is
// neither rootID nor a descendant of it, or targetVersionID does not exist.
// A duplicate (parentID, parentToolCallID) returns the raw unique-violation
// error. An empty id argument is rejected with a plain error before any SQL
// runs.
func (r *DeployedRepository) CreateChildSession(ctx context.Context, rootID, parentID, parentToolCallID, targetVersionID string) (string, error) {
	if err := validateChildSessionIDs(rootID, parentID, parentToolCallID, targetVersionID); err != nil {
		return "", err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM deployed_sessions WHERE id = $1::uuid AND parent_session_id IS NULL FOR SHARE`,
		rootID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return "", types.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRowContext(ctx, `
		WITH RECURSIVE tree(id) AS (
		    SELECT id FROM deployed_sessions WHERE id = $1::uuid
		    UNION ALL
		    SELECT c.id FROM deployed_sessions c JOIN tree t ON c.parent_session_id = t.id
		)
		INSERT INTO deployed_sessions (agent_id, user_id, parent_session_id, parent_tool_call_id, depth, agent_version_id)
		SELECT root.agent_id, root.user_id, p.id, $3, p.depth + 1, $4::uuid
		FROM deployed_sessions p, deployed_sessions root
		WHERE p.id = $2::uuid AND root.id = $1::uuid AND p.id IN (SELECT id FROM tree)
		RETURNING id::text`, rootID, parentID, parentToolCallID, targetVersionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", types.ErrNotFound
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return "", types.ErrNotFound
		}
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// GetDescendant returns childID iff it is a strict descendant of the root
// session rootID. ErrNotFound when rootID is not a root, when childID is
// rootID itself, or when childID lies outside rootID's tree. No user scope:
// the caller verified rootID's ownership.
func (r *DeployedRepository) GetDescendant(ctx context.Context, rootID, childID string) (*types.DeployedSession, error) {
	row := r.db.QueryRowContext(ctx, `
		WITH RECURSIVE tree(id) AS (
		    SELECT id FROM deployed_sessions WHERE id = $1::uuid AND parent_session_id IS NULL
		    UNION ALL
		    SELECT c.id FROM deployed_sessions c JOIN tree t ON c.parent_session_id = t.id
		)
		SELECT `+deployedSessionCols+` FROM deployed_sessions
		WHERE id = $2::uuid AND id <> $1::uuid AND id IN (SELECT id FROM tree)
	`, rootID, childID)
	sess, err := scanDeployedSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return sess, err
}
