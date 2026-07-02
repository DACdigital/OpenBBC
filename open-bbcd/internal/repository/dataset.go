package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DatasetRepository owns datasets, dataset_versions, and dataset_version_sessions.
// One repo for the whole aggregate — assignment methods stay here rather
// than a separate wiring repo (matches AgentWiringRepository's shape).
type DatasetRepository struct{ db *sql.DB }

func NewDatasetRepository(db *sql.DB) *DatasetRepository {
	return &DatasetRepository{db: db}
}

const datasetColumns = `id::text, name, description, created_at`

func (r *DatasetRepository) Create(ctx context.Context, name, description string) (*types.Dataset, error) {
	if name == "" {
		return nil, types.ErrDatasetNameRequired
	}
	d := &types.Dataset{}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO datasets (name, description) VALUES ($1, $2)
		RETURNING `+datasetColumns,
		name, description,
	).Scan(&d.ID, &d.Name, &d.Description, &d.CreatedAt)
	return d, err
}

func (r *DatasetRepository) GetByID(ctx context.Context, id string) (*types.Dataset, error) {
	d := &types.Dataset{}
	err := r.db.QueryRowContext(ctx,
		`SELECT `+datasetColumns+` FROM datasets WHERE id = $1::uuid`, id,
	).Scan(&d.ID, &d.Name, &d.Description, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return d, err
}

func (r *DatasetRepository) List(ctx context.Context) ([]*types.Dataset, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+datasetColumns+` FROM datasets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.Dataset
	for rows.Next() {
		d := &types.Dataset{}
		if err := rows.Scan(&d.ID, &d.Name, &d.Description, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const versionColumns = `id::text, dataset_id::text, status, version_num, close_note, created_at, closed_at`

func scanVersion(row interface{ Scan(...interface{}) error }) (*types.DatasetVersion, error) {
	v := &types.DatasetVersion{}
	var status string
	var closed sql.NullTime
	if err := row.Scan(&v.ID, &v.DatasetID, &status, &v.VersionNum, &v.CloseNote, &v.CreatedAt, &closed); err != nil {
		return nil, err
	}
	v.Status = types.DatasetVersionStatus(status)
	if closed.Valid {
		t := closed.Time
		v.ClosedAt = &t
	}
	return v, nil
}

// EnsureDraft returns the current DRAFT for the dataset, creating one with
// version_num = MAX(version_num)+1 if none exists. Single-tx.
func (r *DatasetRepository) EnsureDraft(ctx context.Context, datasetID string) (*types.DatasetVersion, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
		SELECT `+versionColumns+`
		FROM dataset_versions
		WHERE dataset_id = $1::uuid AND status = 'DRAFT'
	`, datasetID)
	v, err := scanVersion(row)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	row = tx.QueryRowContext(ctx, `
		INSERT INTO dataset_versions (dataset_id, status, version_num)
		VALUES ($1::uuid, 'DRAFT',
		        COALESCE((SELECT MAX(version_num) FROM dataset_versions WHERE dataset_id = $1::uuid), 0) + 1)
		RETURNING `+versionColumns,
		datasetID,
	)
	v, err = scanVersion(row)
	if err != nil {
		return nil, err
	}
	// Inherit sessions from the most recent CLOSED version so users see
	// cumulative dataset content instead of an empty next version. Sessions
	// in the previous closed version are locked, so their feedback is
	// frozen — they behave as immutable members that carry forward.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dataset_version_sessions (dataset_version_id, session_id)
		SELECT $1::uuid, dvs.session_id
		FROM dataset_version_sessions dvs
		JOIN dataset_versions dv ON dv.id = dvs.dataset_version_id
		WHERE dv.dataset_id = $2::uuid AND dv.status = 'CLOSED'
		  AND dv.version_num = (
		      SELECT MAX(version_num) FROM dataset_versions
		      WHERE dataset_id = $2::uuid AND status = 'CLOSED'
		  )
	`, v.ID, datasetID); err != nil {
		return nil, fmt.Errorf("inherit previous version sessions: %w", err)
	}
	return v, tx.Commit()
}

// CloseDraft flips the given DRAFT to CLOSED (with optional note) and sets
// chat_sessions.locked_at on every session in that version. One tx.
func (r *DatasetRepository) CloseDraft(ctx context.Context, versionID, note string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM dataset_versions WHERE id = $1::uuid`, versionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != string(types.DatasetVersionDraft) {
		return types.ErrDatasetVersionClosed
	}
	// Refuse close if any feedback row in this version's sessions still has
	// an empty judge_criteria list. Empty list is the DB default, so this
	// keeps freeze honest without forcing criteria at capture time.
	var missing bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1
		    FROM chat_message_feedback f
		    JOIN chat_messages m ON m.id = f.message_id
		    WHERE m.session_id IN (
		        SELECT session_id FROM dataset_version_sessions WHERE dataset_version_id = $1::uuid
		    )
		    AND jsonb_array_length(f.judge_criteria) = 0
		)
	`, versionID).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return types.ErrDatasetMissingCriteria
	}
	// Purge sessions that lost all their feedback between assignment and
	// close. A session with no thumbs is dataset dead weight and would
	// corrupt the frozen snapshot with a locked-but-signalless row.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM dataset_version_sessions dvs
		WHERE dvs.dataset_version_id = $1::uuid
		  AND NOT EXISTS (
		      SELECT 1 FROM chat_message_feedback f
		      JOIN chat_messages m ON m.id = f.message_id
		      WHERE m.session_id = dvs.session_id
		  )
	`, versionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE dataset_versions
		SET status='CLOSED', closed_at=now(), close_note=$2
		WHERE id = $1::uuid
	`, versionID, note); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_sessions
		SET locked_at = now()
		WHERE locked_at IS NULL
		  AND id IN (SELECT session_id FROM dataset_version_sessions WHERE dataset_version_id = $1::uuid)
	`, versionID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListVersions returns all versions for a dataset, newest first (DRAFT
// comes first if it exists).
func (r *DatasetRepository) ListVersions(ctx context.Context, datasetID string) ([]*types.DatasetVersion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+versionColumns+`
		FROM dataset_versions
		WHERE dataset_id = $1::uuid
		ORDER BY (status = 'DRAFT') DESC, version_num DESC
	`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.DatasetVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *DatasetRepository) GetVersion(ctx context.Context, versionID string) (*types.DatasetVersion, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+versionColumns+` FROM dataset_versions WHERE id = $1::uuid`, versionID)
	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return v, err
}

// GetVersionSessions returns rows enriched with source agent name +
// version_num + thumbs counts, sorted by added_at DESC. version_num is
// computed via a recursive walk of parent_version_id (root = 1).
func (r *DatasetRepository) GetVersionSessions(ctx context.Context, versionID string) ([]*types.DatasetSessionRef, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH RECURSIVE chain AS (
		    SELECT id, parent_version_id, 1 AS num
		    FROM agent_versions WHERE parent_version_id IS NULL
		    UNION ALL
		    SELECT av.id, av.parent_version_id, c.num + 1
		    FROM agent_versions av JOIN chain c ON av.parent_version_id = c.id
		)
		SELECT
		    s.id::text,
		    COALESCE(s.title, ''),
		    a.id::text,
		    a.name,
		    av.id::text,
		    COALESCE(c.num, 1),
		    (SELECT COUNT(*) FROM chat_message_feedback f
		       JOIN chat_messages m ON m.id = f.message_id
		       WHERE m.session_id = s.id AND f.rating = 'up'),
		    (SELECT COUNT(*) FROM chat_message_feedback f
		       JOIN chat_messages m ON m.id = f.message_id
		       WHERE m.session_id = s.id AND f.rating = 'down'),
		    dvs.added_at
		FROM dataset_version_sessions dvs
		JOIN chat_sessions s   ON s.id = dvs.session_id
		JOIN agent_versions av ON av.id = s.agent_version_id
		JOIN agents a          ON a.id = av.agent_id
		LEFT JOIN chain c      ON c.id = av.id
		WHERE dvs.dataset_version_id = $1::uuid
		ORDER BY dvs.added_at DESC
	`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.DatasetSessionRef
	for rows.Next() {
		ref := &types.DatasetSessionRef{}
		if err := rows.Scan(
			&ref.SessionID, &ref.SessionTitle,
			&ref.AgentID, &ref.AgentName,
			&ref.AgentVersionID, &ref.AgentVersionNum,
			&ref.ThumbsUpCount, &ref.ThumbsDownCount,
			&ref.AddedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// AssignSessionToDraft adds a session to the dataset's current draft
// (creating one if none). Refuses if:
//   - the session has no feedback rows (ErrSessionNoFeedback)
//   - the session is locked (ErrSessionLocked)
//   - the session already belongs to a different dataset (ErrSessionAlreadyInDataset)
//
// Assigning a session that's already in the SAME dataset (e.g. inherited
// from a previous CLOSED version) is a no-op at the row level — the draft
// already has the row from EnsureDraft's inheritance step.
func (r *DatasetRepository) AssignSessionToDraft(ctx context.Context, datasetID, sessionID string) (*types.DatasetVersion, error) {
	var hasFeedback bool
	if err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM chat_message_feedback f
		    JOIN chat_messages m ON m.id = f.message_id
		    WHERE m.session_id = $1::uuid
		)
	`, sessionID).Scan(&hasFeedback); err != nil {
		return nil, err
	}
	if !hasFeedback {
		return nil, types.ErrSessionNoFeedback
	}
	var locked sql.NullTime
	if err := r.db.QueryRowContext(ctx,
		`SELECT locked_at FROM chat_sessions WHERE id = $1::uuid`, sessionID,
	).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, err
	}
	if locked.Valid {
		return nil, types.ErrSessionLocked
	}

	// Since migration 020 dropped the global UNIQUE(session_id) constraint,
	// we now explicitly check that this session isn't a member of a
	// DIFFERENT dataset via any version.
	var otherDataset bool
	if err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM dataset_version_sessions dvs
		    JOIN dataset_versions dv ON dv.id = dvs.dataset_version_id
		    WHERE dvs.session_id = $1::uuid AND dv.dataset_id != $2::uuid
		)
	`, sessionID, datasetID).Scan(&otherDataset); err != nil {
		return nil, err
	}
	if otherDataset {
		return nil, types.ErrSessionAlreadyInDataset
	}

	draft, err := r.EnsureDraft(ctx, datasetID)
	if err != nil {
		return nil, err
	}
	// Insert into the draft, tolerating duplicate row if the session was
	// already in the draft (either via a re-click or via inheritance).
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO dataset_version_sessions (dataset_version_id, session_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (dataset_version_id, session_id) DO NOTHING
	`, draft.ID, sessionID)
	if err != nil {
		return nil, err
	}
	return draft, nil
}

// AssignmentView is a session's dataset membership for chat-view rendering.
type AssignmentView struct {
	DatasetID     string
	DatasetName   string
	VersionID     string
	VersionNum    int
	VersionStatus types.DatasetVersionStatus
}

// GetSessionAssignment returns the session's dataset membership or nil if
// unassigned. When a session appears in multiple versions of the same
// dataset (draft + inherited from prior closed), the DRAFT row wins so
// the chat UI shows the mutable badge.
func (r *DatasetRepository) GetSessionAssignment(ctx context.Context, sessionID string) (*AssignmentView, error) {
	av := &AssignmentView{}
	var status string
	err := r.db.QueryRowContext(ctx, `
		SELECT d.id::text, d.name, dv.id::text, dv.version_num, dv.status
		FROM dataset_version_sessions dvs
		JOIN dataset_versions dv ON dv.id = dvs.dataset_version_id
		JOIN datasets d           ON d.id = dv.dataset_id
		WHERE dvs.session_id = $1::uuid
		ORDER BY (dv.status = 'DRAFT') DESC, dv.version_num DESC
		LIMIT 1
	`, sessionID).Scan(&av.DatasetID, &av.DatasetName, &av.VersionID, &av.VersionNum, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	av.VersionStatus = types.DatasetVersionStatus(status)
	return av, nil
}

// UnassignSession removes the session from the current DRAFT of its
// dataset. Refuses if the session is locked (part of a CLOSED version;
// removing would break the immutable snapshot). Rows in CLOSED versions
// stay put — only the DRAFT row is deleted.
func (r *DatasetRepository) UnassignSession(ctx context.Context, sessionID string) error {
	var locked sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT locked_at FROM chat_sessions WHERE id = $1::uuid`, sessionID,
	).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if locked.Valid {
		return types.ErrSessionLocked
	}
	// Delete only the DRAFT membership row. CLOSED versions (if any —
	// possible if this session was in a prior CLOSED and then never got
	// locked… actually locked_at rules that out, so this scope is only
	// pruning the pending DRAFT) keep their rows for snapshot integrity.
	_, err = r.db.ExecContext(ctx, `
		DELETE FROM dataset_version_sessions dvs
		USING dataset_versions dv
		WHERE dv.id = dvs.dataset_version_id
		  AND dvs.session_id = $1::uuid
		  AND dv.status = 'DRAFT'
	`, sessionID)
	return err
}
