// open-bbcd/internal/repository/subagent.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// SubAgentRepository owns agent-tool config on a caller version: the
// agent_tool_enabled flag and agent_version_subagent bindings (spec §
// Repository invariants). Every write runs in one transaction that first
// locks the caller row (and, for an add, the target row) FOR UPDATE in
// ascending id order, which serialises the status checks against Finalize,
// Land, deploy and undeploy, and against eval/training create (FOR SHARE).
type SubAgentRepository struct{ db *sql.DB }

func NewSubAgentRepository(db *sql.DB) *SubAgentRepository { return &SubAgentRepository{db: db} }

// bindingNameRe mirrors the CHECK on agent_version_subagent.name. Validating
// in Go first gives a clean error before any lock is taken; the CHECK stays
// the source of truth (a violation of that CHECK is translated to the same
// error).
var bindingNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// errInvalidBindingName wraps ErrNameRequired so handler.statusFor maps it to
// 400 without a new public sentinel.
var errInvalidBindingName = fmt.Errorf("%w: sub-agent name must match ^[a-z][a-z0-9_-]{0,39}$", types.ErrNameRequired)

// lockVersions locks the given agent_versions rows FOR UPDATE in ascending id
// order (LockRows runs above the sort, so rows are locked in that order — two
// concurrent adds over the same pair cannot deadlock) and returns id→status.
// Any id without a row yields ErrNotFound.
func lockVersions(ctx context.Context, tx *sql.Tx, ids ...string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, status FROM agent_versions
		WHERE id = ANY($1::uuid[])
		ORDER BY id
		FOR UPDATE
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := make(map[string]string, len(ids))
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		statuses[id] = status
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := statuses[id]; !ok {
			return nil, types.ErrNotFound
		}
	}
	return statuses, nil
}

// checkCallerWritable enforces the first two invariant rows: config is
// editable only while the caller is INITIALIZING or DRAFT, and only while no
// eval or training session on it is pending or running (a running eval would
// otherwise score a config different from the one it was started on).
func checkCallerWritable(ctx context.Context, q sqlQueryer, callerID, status string) error {
	if status != string(types.AgentStatusInitializing) && status != string(types.AgentStatusDraft) {
		return types.ErrVersionLocked
	}
	var active bool
	if err := q.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM evals
		              WHERE agent_version_id = $1::uuid AND status IN ('PENDING','IN_PROGRESS'))
		    OR EXISTS(SELECT 1 FROM training_sessions
		              WHERE parent_version_id = $1::uuid AND status IN ('PENDING','IN_PROGRESS'))
	`, callerID).Scan(&active); err != nil {
		return err
	}
	if active {
		return types.ErrEvalOrTrainingActive
	}
	return nil
}

// checkNoCollision refuses enabling the agent tool when the caller's agent
// architecture already exposes an endpoint tool with the same LLM-visible
// name — the orchestrator would otherwise emit two tools named "agent".
func checkNoCollision(ctx context.Context, tx *sql.Tx, callerID string) error {
	var arch []byte
	err := tx.QueryRowContext(ctx, `
		SELECT a.architecture FROM agents a
		JOIN agent_versions v ON v.agent_id = a.id
		WHERE v.id = $1::uuid
	`, callerID).Scan(&arch)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if tools.ArchitectureHasEndpointTool(arch, tools.AgentToolName) {
		return types.ErrToolNameCollision
	}
	return nil
}

// beginCallerWrite opens a transaction, locks the caller (plus extra ids) and
// runs the writable check. The caller must defer Rollback on the returned tx.
func (r *SubAgentRepository) beginCallerWrite(ctx context.Context, callerID string, extra ...string) (*sql.Tx, map[string]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	statuses, err := lockVersions(ctx, tx, append([]string{callerID}, extra...)...)
	if err == nil {
		err = checkCallerWritable(ctx, tx, callerID, statuses[callerID])
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	return tx, statuses, nil
}

// SetAgentToolEnabled toggles agent_tool_enabled on the caller version.
// Disabling is always allowed past the collision check so a colliding agent
// can still turn the tool off.
func (r *SubAgentRepository) SetAgentToolEnabled(ctx context.Context, callerID string, enabled bool) error {
	tx, _, err := r.beginCallerWrite(ctx, callerID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if enabled {
		if err := checkNoCollision(ctx, tx, callerID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_versions SET agent_tool_enabled = $2, updated_at = now() WHERE id = $1::uuid
	`, callerID, enabled); err != nil {
		return err
	}
	return tx.Commit()
}

// AddBinding pins targetID under name on the caller version.
func (r *SubAgentRepository) AddBinding(ctx context.Context, callerID, name, targetID, note string) error {
	if name == "" {
		return types.ErrNameRequired
	}
	if !bindingNameRe.MatchString(name) {
		return errInvalidBindingName
	}
	// A self-binding is the trivial cycle; refuse it before locking (the
	// table CHECK would reject it anyway).
	if callerID == targetID {
		return types.ErrTopologyCycle
	}

	tx, statuses, err := r.beginCallerWrite(ctx, callerID, targetID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := checkNoCollision(ctx, tx, callerID); err != nil {
		return err
	}
	if st := statuses[targetID]; st != string(types.AgentStatusReady) && st != string(types.AgentStatusDeployed) {
		return types.ErrTargetNotRunnable
	}

	// Defence in depth: under the status rules a writable caller has no
	// incoming edges (only READY/DEPLOYED versions can be targets), so the
	// caller can never be reachable from the target. UNION (not UNION ALL)
	// makes the walk terminate even over raw cyclic fixtures.
	var cycle bool
	if err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE reach(id) AS (
		    SELECT target_version_id FROM agent_version_subagent WHERE caller_version_id = $1::uuid
		    UNION
		    SELECT s.target_version_id FROM agent_version_subagent s JOIN reach r ON s.caller_version_id = r.id
		)
		SELECT EXISTS(SELECT 1 FROM reach WHERE id = $2::uuid)
	`, targetID, callerID).Scan(&cycle); err != nil {
		return err
	}
	if cycle {
		return types.ErrTopologyCycle
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note)
		VALUES ($1::uuid, $2::uuid, $3, $4)
	`, callerID, targetID, name, note); err != nil {
		return translateBindingInsertErr(err)
	}
	return tx.Commit()
}

// bindingNameCheck is the auto-generated name of the CHECK on
// agent_version_subagent.name (migration 029).
const bindingNameCheck = "agent_version_subagent_name_check"

// translateBindingInsertErr maps an INSERT INTO agent_version_subagent error:
// a unique violation is a duplicate name/target, and only the name CHECK is an
// invalid name — any other check violation is returned unchanged.
func translateBindingInsertErr(err error) error {
	switch {
	case isUniqueViolation(err):
		return types.ErrBindingConflict
	case isCheckViolationOn(err, bindingNameCheck):
		return errInvalidBindingName
	}
	return err
}

// UpdateNotes sets the note of each named binding on the caller. Names with
// no binding are ignored: the form posts every row it rendered, and a row
// deleted meanwhile is not an error worth surfacing.
func (r *SubAgentRepository) UpdateNotes(ctx context.Context, callerID string, notes map[string]string) error {
	tx, _, err := r.beginCallerWrite(ctx, callerID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for name, note := range notes {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_version_subagent SET note = $3, updated_at = now()
			WHERE caller_version_id = $1::uuid AND name = $2
		`, callerID, name, note); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteBinding removes the named binding. ErrNotFound when it does not exist.
func (r *SubAgentRepository) DeleteBinding(ctx context.Context, callerID, name string) error {
	tx, _, err := r.beginCallerWrite(ctx, callerID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		DELETE FROM agent_version_subagent WHERE caller_version_id = $1::uuid AND name = $2
	`, callerID, name)
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
	return tx.Commit()
}

// ListBindings returns the caller's bindings ordered by name; an empty,
// non-nil slice when there are none (JSON renders [] rather than null).
// No lock: a read for display or for building one turn's tool list.
func (r *SubAgentRepository) ListBindings(ctx context.Context, callerID string) ([]types.SubAgentBinding, error) {
	return listBindings(ctx, r.db, callerID)
}

func listBindings(ctx context.Context, q sqlQueryer, callerID string) ([]types.SubAgentBinding, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT caller_version_id::text, target_version_id::text, name, note, created_at, updated_at
		FROM agent_version_subagent
		WHERE caller_version_id = $1::uuid
		ORDER BY name
	`, callerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []types.SubAgentBinding{}
	for rows.Next() {
		var b types.SubAgentBinding
		if err := rows.Scan(&b.CallerVersionID, &b.TargetVersionID, &b.Name, &b.Note, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetAgentToolConfig returns the version's agent_tool_enabled flag and its
// bindings from one snapshot. ErrNotFound when the version does not exist.
func (r *SubAgentRepository) GetAgentToolConfig(ctx context.Context, versionID string) (bool, []types.SubAgentBinding, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT agent_tool_enabled FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, types.ErrNotFound
	}
	if err != nil {
		return false, nil, err
	}
	bindings, err := listBindings(ctx, tx, versionID)
	if err != nil {
		return false, nil, err
	}
	return enabled, bindings, tx.Commit()
}

// ConfigWriteBlock reports why config writes on versionID would be refused
// (ErrVersionLocked, ErrEvalOrTrainingActive) or nil when they would be
// allowed. Lock-free and advisory — for rendering the tab read-only; the
// writes re-check under the row lock. ErrNotFound when the version is missing.
func (r *SubAgentRepository) ConfigWriteBlock(ctx context.Context, versionID string) error {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	return checkCallerWritable(ctx, r.db, versionID, status)
}
