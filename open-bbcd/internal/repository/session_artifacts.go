package repository

import (
	"context"
	"database/sql"
	"errors"

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
	db      *sql.DB
	table   string
	lockKey int32
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
// locked BO session. Only a.SessionID, StoreID, URI, MIME, SizeBytes,
// Sha256 and Filename are read.
func (t sessionArtifacts) CommitUpload(ctx context.Context, a types.SessionArtifact, maxPending int) (*types.SessionArtifact, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int4, hashtext($2::text))`, t.lockKey, a.SessionID); err != nil {
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
		return nil, err
	}
	return row, tx.Commit()
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
// with that id is in this session (including a malformed id).
func (t sessionArtifacts) DeletePendingArtifact(ctx context.Context, sessionID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
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
