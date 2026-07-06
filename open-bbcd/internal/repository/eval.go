package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/lib/pq"
)

// EvalRepository owns evals + eval_sessions.
type EvalRepository struct{ db *sql.DB }

func NewEvalRepository(db *sql.DB) *EvalRepository { return &EvalRepository{db: db} }

const evalColumns = `id::text, agent_version_id::text, dataset_version_id::text, status,
	mock_mcp_tools, header_overrides,
	score, total_criteria, passed_criteria, error_message, aikdm_meta,
	created_at, started_at, completed_at`

func scanEval(s scanner) (*types.Eval, error) {
	e := &types.Eval{}
	var status string
	var mockMCP bool
	var headerRaw []byte
	var score sql.NullFloat64
	var total, passed sql.NullInt64
	var startedAt, completedAt sql.NullTime
	var meta []byte
	if err := s.Scan(
		&e.ID, &e.AgentVersionID, &e.DatasetVersionID, &status,
		&mockMCP, &headerRaw,
		&score, &total, &passed, &e.ErrorMessage, &meta,
		&e.CreatedAt, &startedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	e.Status = types.EvalStatus(status)
	e.MockMCPTools = mockMCP
	if len(headerRaw) > 0 {
		_ = json.Unmarshal(headerRaw, &e.HeaderOverrides)
	}
	if e.HeaderOverrides == nil {
		e.HeaderOverrides = map[string]string{}
	}
	if score.Valid {
		v := score.Float64
		e.Score = &v
	}
	if total.Valid {
		v := int(total.Int64)
		e.TotalCriteria = &v
	}
	if passed.Valid {
		v := int(passed.Int64)
		e.PassedCriteria = &v
	}
	if startedAt.Valid {
		t := startedAt.Time
		e.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		e.CompletedAt = &t
	}
	e.AikdmMeta = meta
	return e, nil
}

// Create inserts a PENDING eval with the given config. Callers are responsible
// for validating dataset-version-closed / criteria-complete before calling.
func (r *EvalRepository) Create(ctx context.Context, agentVersionID, datasetVersionID string, mockMCP bool, headerOverrides map[string]string) (*types.Eval, error) {
	if headerOverrides == nil {
		headerOverrides = map[string]string{}
	}
	headers, err := json.Marshal(headerOverrides)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO evals (agent_version_id, dataset_version_id, status, mock_mcp_tools, header_overrides)
		VALUES ($1::uuid, $2::uuid, 'PENDING', $3, $4::jsonb)
		RETURNING `+evalColumns,
		agentVersionID, datasetVersionID, mockMCP, headers,
	)
	return scanEval(row)
}

func (r *EvalRepository) GetByID(ctx context.Context, id string) (*types.Eval, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+evalColumns+` FROM evals WHERE id = $1::uuid`, id)
	e, err := scanEval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return e, err
}

func (r *EvalRepository) ListAll(ctx context.Context) ([]*types.Eval, error) {
	return r.query(ctx, `SELECT `+evalColumns+` FROM evals ORDER BY created_at DESC`)
}

func (r *EvalRepository) ListByAgentVersion(ctx context.Context, agentVersionID string) ([]*types.Eval, error) {
	return r.query(ctx,
		`SELECT `+evalColumns+` FROM evals WHERE agent_version_id = $1::uuid ORDER BY created_at DESC`,
		agentVersionID,
	)
}

func (r *EvalRepository) ListPendingOrInProgressForPair(ctx context.Context, agentVersionID, datasetVersionID string) ([]*types.Eval, error) {
	return r.query(ctx,
		`SELECT `+evalColumns+` FROM evals
		 WHERE agent_version_id = $1::uuid AND dataset_version_id = $2::uuid
		   AND status IN ('PENDING','IN_PROGRESS')
		 ORDER BY created_at DESC`,
		agentVersionID, datasetVersionID,
	)
}

func (r *EvalRepository) query(ctx context.Context, sqlStr string, args ...any) ([]*types.Eval, error) {
	rows, err := r.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.Eval
	for rows.Next() {
		e, err := scanEval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Start transitions PENDING → IN_PROGRESS and stamps started_at. Returns
// ErrEvalNotPending if the current status is anything else.
func (r *EvalRepository) Start(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE evals SET status='IN_PROGRESS', started_at=now()
		WHERE id = $1::uuid AND status = 'PENDING'
	`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return r.classifyMissingOrConflict(ctx, id, types.ErrEvalNotPending)
	}
	return nil
}

// Submit persists the terminal result. On DONE, inserts every session row
// and stamps score+counts. On FAILED, only stamps status + error_message.
// Refuses if the eval is already DONE or FAILED.
func (r *EvalRepository) Submit(ctx context.Context, id string, result *types.EvalResult) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == string(types.EvalStatusDone) || status == string(types.EvalStatusFailed) {
		return types.ErrEvalAlreadyFinal
	}

	if result.Status == types.EvalStatusFailed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE evals
			SET status='FAILED', error_message=$2, aikdm_meta=$3::jsonb, completed_at=now()
			WHERE id = $1::uuid
		`, id, result.ErrorMessage, marshalOrEmpty(result.AikdmMeta)); err != nil {
			return err
		}
		return tx.Commit()
	}

	for _, s := range result.Sessions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO eval_sessions
				(eval_id, session_id, score, total_criteria, passed_criteria, transcript, judgments)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7::jsonb)
		`,
			id, s.SessionID, s.Score, s.TotalCriteria, s.PassedCriteria,
			marshalOrEmpty(s.Transcript), marshalOrEmpty(s.Judgments),
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE evals
		SET status='DONE', score=$2, total_criteria=$3, passed_criteria=$4,
		    aikdm_meta=$5::jsonb, completed_at=now()
		WHERE id = $1::uuid
	`, id, result.Score, result.TotalCriteria, result.PassedCriteria, marshalOrEmpty(result.AikdmMeta)); err != nil {
		return err
	}
	return tx.Commit()
}

// Fail is a script-friendly shortcut for the error-only terminal state.
func (r *EvalRepository) Fail(ctx context.Context, id, errMsg string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == string(types.EvalStatusDone) || status == string(types.EvalStatusFailed) {
		return types.ErrEvalAlreadyFinal
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE evals SET status='FAILED', error_message=$2, completed_at=now() WHERE id = $1::uuid
	`, id, errMsg); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *EvalRepository) ListSessions(ctx context.Context, evalID string) ([]*types.EvalSession, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
		    es.id::text, es.eval_id::text, es.session_id::text,
		    COALESCE(s.title, ''), s.agent_version_id::text,
		    es.score, es.total_criteria, es.passed_criteria,
		    es.transcript, es.judgments
		FROM eval_sessions es
		JOIN chat_sessions s ON s.id = es.session_id
		WHERE es.eval_id = $1::uuid
		ORDER BY es.score ASC
	`, evalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.EvalSession
	for rows.Next() {
		s := &types.EvalSession{}
		var transcript, judgments []byte
		if err := rows.Scan(&s.ID, &s.EvalID, &s.SessionID,
			&s.SessionTitle, &s.AgentVersionID,
			&s.Score, &s.TotalCriteria, &s.PassedCriteria,
			&transcript, &judgments); err != nil {
			return nil, err
		}
		s.Transcript = transcript
		s.Judgments = judgments
		out = append(out, s)
	}
	return out, rows.Err()
}

// AverageScoreByAgentVersion returns the plain mean of DONE eval scores
// for the given agent version. When no DONE evals exist, avg=0.0 and
// count=0 — templates should check count > 0 before rendering avg
// (otherwise render "—").
func (r *EvalRepository) AverageScoreByAgentVersion(ctx context.Context, agentVersionID string) (avg float64, count int, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(AVG(score), 0), COUNT(*)
		FROM evals
		WHERE agent_version_id = $1::uuid AND status = 'DONE' AND score IS NOT NULL
	`, agentVersionID).Scan(&avg, &count)
	return
}

// LastScoreByAgentVersion returns the score of the most recent DONE eval
// for this version and whether one exists. Ignores PENDING / IN_PROGRESS /
// FAILED evals.
func (r *EvalRepository) LastScoreByAgentVersion(ctx context.Context, agentVersionID string) (score float64, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT score FROM evals
		WHERE agent_version_id = $1::uuid AND status = 'DONE' AND score IS NOT NULL
		ORDER BY completed_at DESC NULLS LAST, created_at DESC
		LIMIT 1
	`, agentVersionID).Scan(&score)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return score, true, nil
}

func (r *EvalRepository) classifyMissingOrConflict(ctx context.Context, id string, conflictErr error) error {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	return conflictErr
}

func marshalOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return []byte(raw)
}

// EvalRowView pairs an eval with human-readable labels (agent name, dataset name,
// version numbers). Kept here to avoid a hot join loop in the handler.
type EvalRowView struct {
	Eval              *types.Eval
	AgentName         string
	AgentVersionNum   int
	DatasetName       string
	DatasetVersionNum int
}

// EnrichRows returns one EvalRowView per eval, resolving agent/dataset labels
// in a single query. Preserves input order via the passed slice of ids.
func (r *EvalRepository) EnrichRows(ctx context.Context, evals []*types.Eval) ([]EvalRowView, error) {
	if len(evals) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(evals))
	for _, e := range evals {
		ids = append(ids, e.ID)
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH RECURSIVE chain AS (
		    SELECT id, parent_version_id, 1 AS num
		    FROM agent_versions WHERE parent_version_id IS NULL
		    UNION ALL
		    SELECT av.id, av.parent_version_id, c.num + 1
		    FROM agent_versions av JOIN chain c ON av.parent_version_id = c.id
		)
		SELECT
		    e.id::text,
		    a.name,
		    COALESCE(c.num, 1),
		    d.name,
		    dv.version_num
		FROM evals e
		JOIN agent_versions av ON av.id = e.agent_version_id
		JOIN agents a ON a.id = av.agent_id
		LEFT JOIN chain c ON c.id = av.id
		JOIN dataset_versions dv ON dv.id = e.dataset_version_id
		JOIN datasets d ON d.id = dv.dataset_id
		WHERE e.id::text = ANY($1)
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[string]EvalRowView{}
	for rows.Next() {
		var evalID string
		var v EvalRowView
		if err := rows.Scan(&evalID, &v.AgentName, &v.AgentVersionNum, &v.DatasetName, &v.DatasetVersionNum); err != nil {
			return nil, err
		}
		labels[evalID] = v
	}
	out := make([]EvalRowView, 0, len(evals))
	for _, e := range evals {
		lbl := labels[e.ID]
		lbl.Eval = e
		out = append(out, lbl)
	}
	return out, rows.Err()
}
