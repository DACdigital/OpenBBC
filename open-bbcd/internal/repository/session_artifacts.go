package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// First keys of the per-session upload advisory lock, one per table, so a
// BO and a deployed session with colliding hashtext() never serialise each
// other.
const (
	chatSessionArtifactsLockKey     int32 = 27
	deployedSessionArtifactsLockKey int32 = 28
)

// sqlQueryer is the subset of *sql.DB and *sql.Tx used by the helpers, so
// one query runs either standalone or inside a transaction.
type sqlQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sessionArtifacts implements the session-artifact table operations shared by
// chat_session_artifacts (BO) and deployed_session_artifacts (deployed).
// Embedded by ChatRepository and DeployedRepository, so its exported methods
// are theirs.
//
// table is a trusted constant set by the constructors, never user input.
type sessionArtifacts struct {
	db    *sql.DB
	table string
	// sessionTable is the owning session table (trusted constant). The turn
	// writes lock its row FOR KEY SHARE first, so lock order is always
	// session row, then artifact rows (matching a cascading session delete).
	sessionTable string
	lockKey      int32
	// recheckSession re-reads the owning session inside the upload commit
	// transaction: ErrNotFound if it is gone, ErrSessionLocked if a BO
	// session was locked by a dataset close during a slow upload.
	recheckSession func(ctx context.Context, tx *sql.Tx, sessionID string) error
}

const sessionArtifactCols = `id::text, session_id::text, origin, store_id, uri, mime, size_bytes, sha256,
	COALESCE(filename, ''), COALESCE(message_id::text, ''), created_at`

func scanSessionArtifact(s scanner) (*types.SessionArtifact, error) {
	a := &types.SessionArtifact{}
	if err := s.Scan(&a.ID, &a.SessionID, &a.Origin, &a.StoreID, &a.URI, &a.MIME,
		&a.SizeBytes, &a.Sha256, &a.Filename, &a.MessageID, &a.CreatedAt); err != nil {
		return nil, err
	}
	return a, nil
}

func (t sessionArtifacts) findPending(ctx context.Context, q sqlQueryer, sessionID, storeID, uri string) (*types.SessionArtifact, error) {
	a, err := scanSessionArtifact(q.QueryRowContext(ctx, `
		SELECT `+sessionArtifactCols+` FROM `+t.table+`
		WHERE session_id = $1::uuid AND store_id = $2 AND uri = $3
		  AND origin = 'upload' AND message_id IS NULL`, sessionID, storeID, uri))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (t sessionArtifacts) countPending(ctx context.Context, q sqlQueryer, sessionID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM `+t.table+`
		WHERE session_id = $1::uuid AND origin = 'upload' AND message_id IS NULL`, sessionID).Scan(&n)
	return n, err
}

// PrecheckUpload is the non-locking, non-authoritative upload pre-check
// (spec upload step 2): it returns the existing pending row for the blob if
// any, else ErrPendingArtifactCap if the session is at maxPending, else
// (nil, nil). Lets the handler skip a Put that would be refused anyway.
func (t sessionArtifacts) PrecheckUpload(ctx context.Context, sessionID, storeID, uri string, maxPending int) (*types.SessionArtifact, error) {
	existing, err := t.findPending(ctx, t.db, sessionID, storeID, uri)
	if err != nil || existing != nil {
		return existing, err
	}
	n, err := t.countPending(ctx, t.db, sessionID)
	if err != nil {
		return nil, err
	}
	if n >= maxPending {
		return nil, types.ErrPendingArtifactCap
	}
	return nil, nil
}

// CommitUpload is the authoritative upload commit (spec upload steps 4-6),
// in one short transaction serialised per session by an advisory lock that
// turns never take. Returns the existing pending row on a dedup hit (the cap
// does not apply to it), ErrPendingArtifactCap at the cap, ErrNotFound if
// the session is gone (re-read or FK violation), and ErrSessionLocked for a
// locked BO session. A dedup hit may return a row that an in-flight
// (uncommitted) turn is about to claim; the file still reaches that turn.
// Only a.SessionID, StoreID, URI, MIME, SizeBytes,
// Sha256 and Filename are read.
func (t sessionArtifacts) CommitUpload(ctx context.Context, a types.SessionArtifact, maxPending int) (*types.SessionArtifact, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int4, hashtext($2::uuid::text))`, t.lockKey, a.SessionID); err != nil {
		return nil, err
	}
	if err := t.recheckSession(ctx, tx, a.SessionID); err != nil {
		return nil, err
	}
	existing, err := t.findPending(ctx, tx, a.SessionID, a.StoreID, a.URI)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	n, err := t.countPending(ctx, tx, a.SessionID)
	if err != nil {
		return nil, err
	}
	if n >= maxPending {
		return nil, types.ErrPendingArtifactCap
	}
	row, err := scanSessionArtifact(tx.QueryRowContext(ctx, `
		INSERT INTO `+t.table+` (session_id, origin, store_id, uri, mime, size_bytes, sha256, filename)
		VALUES ($1::uuid, 'upload', $2, $3, $4, $5, $6, NULLIF($7, ''))
		RETURNING `+sessionArtifactCols,
		a.SessionID, a.StoreID, a.URI, a.MIME, a.SizeBytes, a.Sha256, a.Filename))
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, types.ErrNotFound
		}
		if isUniqueViolation(err) {
			// Backstop: a concurrent commit inserted the same pending blob.
			// The tx is aborted, so re-read outside it and treat as a dedup hit.
			if dup, ferr := t.findPending(ctx, t.db, a.SessionID, a.StoreID, a.URI); ferr == nil && dup != nil {
				return dup, nil
			}
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return row, nil
}

// ListPendingArtifacts returns the session's pending uploads in (created_at, id)
// order, the order the next turn appends them. Never nil.
func (t sessionArtifacts) ListPendingArtifacts(ctx context.Context, sessionID string) ([]*types.SessionArtifact, error) {
	rows, err := t.db.QueryContext(ctx, `
		SELECT `+sessionArtifactCols+` FROM `+t.table+`
		WHERE session_id = $1::uuid AND origin = 'upload' AND message_id IS NULL
		ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*types.SessionArtifact{}
	for rows.Next() {
		a, err := scanSessionArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HasPendingArtifacts reports whether the session has at least one pending upload.
func (t sessionArtifacts) HasPendingArtifacts(ctx context.Context, sessionID string) (bool, error) {
	var has bool
	err := t.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM `+t.table+`
		WHERE session_id = $1::uuid AND origin = 'upload' AND message_id IS NULL)`, sessionID).Scan(&has)
	return has, err
}

// DeletePendingArtifact removes one pending upload. ErrArtifactConsumed if
// the row is in this session but already claimed; ErrNotFound if no row
// with that id is in this session (including a malformed or non-canonical
// id: uuid.Parse also accepts urn:uuid:, braced and 32-hex spellings, which
// Postgres ::uuid rejects or which no handler ever returns).
func (t sessionArtifacts) DeletePendingArtifact(ctx context.Context, sessionID, id string) error {
	if _, err := uuid.Parse(id); err != nil || len(id) != 36 {
		return types.ErrNotFound
	}
	res, err := t.db.ExecContext(ctx, `
		DELETE FROM `+t.table+`
		WHERE id = $1::uuid AND session_id = $2::uuid AND origin = 'upload' AND message_id IS NULL`, id, sessionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	var exists bool
	if err := t.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM `+t.table+` WHERE id = $1::uuid AND session_id = $2::uuid)`,
		id, sessionID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return types.ErrArtifactConsumed
	}
	return types.ErrNotFound
}

// LookupSessionArtifact is the retrieval allow-list check: the newest row of
// any origin, pending or consumed, for (sessionID, storeID, uri). ErrNotFound
// if none.
func (t sessionArtifacts) LookupSessionArtifact(ctx context.Context, sessionID, storeID, uri string) (*types.SessionArtifact, error) {
	a, err := scanSessionArtifact(t.db.QueryRowContext(ctx, `
		SELECT `+sessionArtifactCols+` FROM `+t.table+`
		WHERE session_id = $1::uuid AND store_id = $2 AND uri = $3
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, sessionID, storeID, uri))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return a, err
}

func recheckChatSession(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var lockedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT locked_at FROM chat_sessions WHERE id = $1::uuid`, sessionID).Scan(&lockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if lockedAt.Valid {
		return types.ErrSessionLocked
	}
	return nil
}

func recheckDeployedSession(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM deployed_sessions WHERE id = $1::uuid`, sessionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	return err
}

// claimPending assigns every pending upload on the session to messageID
// inside tx and returns their refs in (created_at, id) order. Concurrent
// claims over existing pending rows serialise on the row locks; the loser
// re-evaluates message_id IS NULL and matches nothing, so each row is claimed
// once. With none pending both turns proceed and UNIQUE(session_id, seq) is
// the backstop.
func (t sessionArtifacts) claimPending(ctx context.Context, tx *sql.Tx, sessionID, messageID string) ([]llm.ArtifactRefBlock, error) {
	rows, err := tx.QueryContext(ctx, `
		UPDATE `+t.table+` SET message_id = $2::uuid, updated_at = now()
		WHERE session_id = $1::uuid AND origin = 'upload' AND message_id IS NULL
		RETURNING id::text, store_id, uri, mime, size_bytes, sha256, COALESCE(filename, ''), created_at`,
		sessionID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type claimed struct {
		id  string
		at  time.Time
		ref llm.ArtifactRefBlock
	}
	var cs []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.id, &c.ref.StoreID, &c.ref.URI, &c.ref.MIME, &c.ref.SizeBytes,
			&c.ref.Sha256, &c.ref.Filename, &c.at); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(cs, func(i, j int) bool {
		if !cs[i].at.Equal(cs[j].at) {
			return cs[i].at.Before(cs[j].at)
		}
		return cs[i].id < cs[j].id // canonical uuid text sorts like Postgres uuid
	})
	out := make([]llm.ArtifactRefBlock, len(cs))
	for i, c := range cs {
		out[i] = c.ref
	}
	return out, nil
}

// insertToolResultRows records one origin='tool_result' row per ref for the
// tool-role message messageID, inside tx.
func (t sessionArtifacts) insertToolResultRows(ctx context.Context, tx *sql.Tx, sessionID, messageID string, refs []llm.ArtifactRefBlock) error {
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+t.table+` (session_id, origin, store_id, uri, mime, size_bytes, sha256, filename, message_id)
			VALUES ($1::uuid, 'tool_result', $2, $3, $4, $5, $6, NULLIF($7, ''), $8::uuid)`,
			sessionID, r.StoreID, r.URI, r.MIME, r.SizeBytes, r.Sha256, r.Filename, messageID); err != nil {
			return err
		}
	}
	return nil
}

// lockSession takes FOR KEY SHARE on the session row as the first statement of
// a turn write, so it cannot deadlock with a cascading session delete.
// ErrNotFound if the session is gone.
func (t sessionArtifacts) lockSession(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+t.sessionTable+` WHERE id = $1::uuid FOR KEY SHARE`, sessionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	return err
}

// appendUserTurn is the shared body of AppendUserTurn: claim, empty-turn
// check, content assembly, then insertMsg(tx, content), all in one tx.
func (t sessionArtifacts) appendUserTurn(ctx context.Context, sessionID, messageID string, content json.RawMessage,
	insertMsg func(tx *sql.Tx, content json.RawMessage) error) ([]llm.ArtifactRefBlock, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.lockSession(ctx, tx, sessionID); err != nil {
		return nil, err
	}
	// Reject bad content before taking artifact row locks.
	blocks, hasText, err := decodeUserContent(content)
	if err != nil {
		return nil, err
	}
	refs, err := t.claimPending(ctx, tx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 && !hasText {
		return nil, types.ErrEmptyTurn
	}
	for _, r := range refs {
		b, err := json.Marshal(types.ArtifactRefContent{
			Type: "artifact_ref", StoreID: r.StoreID, URI: r.URI, MIME: r.MIME,
			SizeBytes: r.SizeBytes, Sha256: r.Sha256, Filename: r.Filename,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	final, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	if err := insertMsg(tx, final); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return refs, nil
}

// appendToolMessage is the shared body of AppendToolMessage: message, then
// its tool_result rows, in one tx.
func (t sessionArtifacts) appendToolMessage(ctx context.Context, sessionID, messageID string, refs []llm.ArtifactRefBlock,
	insertMsg func(tx *sql.Tx) error) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.lockSession(ctx, tx, sessionID); err != nil {
		return err
	}
	if err := insertMsg(tx); err != nil {
		return err
	}
	if err := t.insertToolResultRows(ctx, tx, sessionID, messageID, refs); err != nil {
		return err
	}
	return tx.Commit()
}

// decodeUserContent splits a user message's block array and reports
// whether it holds a non-empty text block. Empty/absent content is [].
func decodeUserContent(content json.RawMessage) ([]json.RawMessage, bool, error) {
	blocks := []json.RawMessage{}
	if len(content) > 0 {
		if err := json.Unmarshal(content, &blocks); err != nil {
			return nil, false, err
		}
	}
	hasText := false
	for _, b := range blocks {
		var head struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(b, &head) == nil && head.Type == "text" && head.Text != "" {
			hasText = true
		}
	}
	return blocks, hasText, nil
}
