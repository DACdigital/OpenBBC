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
//
// Temporary multi-agent gate: the version row is read FOR SHARE in the same
// transaction as the insert; when agent_tool_enabled is set nothing is
// inserted and ErrMultiAgentEvalUnsupported is returned. ErrNotFound when the
// version does not exist.
func (r *EvalRepository) Create(ctx context.Context, agentVersionID, datasetVersionID string, mockMCP bool, headerOverrides map[string]string) (*types.Eval, error) {
	if headerOverrides == nil {
		headerOverrides = map[string]string{}
	}
	headers, err := json.Marshal(headerOverrides)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	enabled, err := versionAgentToolEnabledTx(ctx, tx, agentVersionID)
	if err != nil {
		return nil, err
	}
	if enabled {
		return nil, types.ErrMultiAgentEvalUnsupported
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO evals (agent_version_id, dataset_version_id, status, mock_mcp_tools, header_overrides)
		VALUES ($1::uuid, $2::uuid, 'PENDING', $3, $4::jsonb)
		RETURNING `+evalColumns,
		agentVersionID, datasetVersionID, mockMCP, headers,
	)
	e, err := scanEval(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return e, nil
}

// versionAgentToolEnabledTx reads agent_tool_enabled for versionID under
// FOR SHARE, so a concurrent agent-tool config write (FOR NO KEY UPDATE on
// the same row) serialises with the caller's insert (spec § REST — eval and
// training gate). Returns ErrNotFound when the version does not exist.
func versionAgentToolEnabledTx(ctx context.Context, tx *sql.Tx, versionID string) (bool, error) {
	var enabled bool
	err := tx.QueryRowContext(ctx,
		`SELECT agent_tool_enabled FROM agent_versions WHERE id = $1::uuid FOR SHARE`, versionID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, types.ErrNotFound
	}
	return enabled, err
}

// lockEvalGateTx locks the eval row FOR NO KEY UPDATE (enough to serialise
// status transitions without blocking FK inserts that reference the eval) and
// returns its status plus its version's current agent_tool_enabled.
// ErrNotFound when the eval is missing.
func lockEvalGateTx(ctx context.Context, tx *sql.Tx, evalID string) (status string, enabled bool, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT e.status, v.agent_tool_enabled
		FROM evals e JOIN agent_versions v ON v.id = e.agent_version_id
		WHERE e.id = $1::uuid
		FOR NO KEY UPDATE OF e
	`, evalID).Scan(&status, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, types.ErrNotFound
	}
	return status, enabled, err
}

// failEvalMultiAgentTx moves a PENDING eval to FAILED with the gate sentinel
// text, setting exactly the columns Fail sets.
func failEvalMultiAgentTx(ctx context.Context, tx *sql.Tx, evalID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE evals SET status='FAILED', error_message=$2, completed_at=now()
		WHERE id = $1::uuid AND status = 'PENDING'
	`, evalID, types.ErrMultiAgentEvalUnsupported.Error())
	return err
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

// List returns evals newest-first, optionally filtered by status.
// status="" means no filter. limit is clamped to [1, 500]; 0 or negative defaults to 100.
func (r *EvalRepository) List(ctx context.Context, status string, limit int) ([]*types.Eval, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if status == "" {
		return r.query(ctx,
			`SELECT `+evalColumns+` FROM evals ORDER BY created_at DESC LIMIT $1`,
			limit,
		)
	}
	return r.query(ctx,
		`SELECT `+evalColumns+` FROM evals WHERE status = $1 ORDER BY created_at DESC LIMIT $2`,
		status, limit,
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
// ErrEvalNotPending if the current status is anything else, and ErrNotFound
// for an unknown id.
//
// Temporary multi-agent gate: when the eval's version currently has
// agent_tool_enabled, a PENDING eval is failed forward (FAILED with the
// sentinel text, so a drainer never retries it) and ErrMultiAgentEvalUnsupported
// is returned; a non-PENDING eval is left untouched and gets the same error.
func (r *EvalRepository) Start(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	status, enabled, err := lockEvalGateTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if enabled {
		if status == string(types.EvalStatusPending) {
			if err := failEvalMultiAgentTx(ctx, tx, id); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		}
		return types.ErrMultiAgentEvalUnsupported
	}
	if status != string(types.EvalStatusPending) {
		return types.ErrEvalNotPending
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE evals SET status='IN_PROGRESS', started_at=now()
		WHERE id = $1::uuid AND status = 'PENDING'
	`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// FailIfMultiAgent is the export-route backstop of the temporary multi-agent
// gate. When the eval's version currently has agent_tool_enabled it, in one
// transaction, fails a PENDING eval forward and fails every PENDING training
// session sourced from it (both with the sentinel text), then returns
// ErrMultiAgentEvalUnsupported — also when nothing was PENDING. Returns nil
// without writing when the version is not gated, ErrNotFound for an unknown
// eval.
func (r *EvalRepository) FailIfMultiAgent(ctx context.Context, evalID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	status, enabled, err := lockEvalGateTx(ctx, tx, evalID)
	if err != nil {
		return err
	}
	if !enabled {
		return tx.Commit()
	}
	if status == string(types.EvalStatusPending) {
		if err := failEvalMultiAgentTx(ctx, tx, evalID); err != nil {
			return err
		}
	}
	if err := failTrainingsFromEvalMultiAgentTx(ctx, tx, evalID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return types.ErrMultiAgentEvalUnsupported
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
