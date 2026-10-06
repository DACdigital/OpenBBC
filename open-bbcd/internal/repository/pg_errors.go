package repository

import (
	"errors"

	"github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505). Uses errors.As so wrapped errors are handled correctly.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

// isForeignKeyViolation reports whether err is a Postgres foreign_key_violation
// (SQLSTATE 23503).
func isForeignKeyViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23503"
	}
	return false
}

// isCheckViolationOn reports whether err is a Postgres check_violation
// (SQLSTATE 23514) raised by the named constraint.
func isCheckViolationOn(err error, constraint string) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23514" && pqErr.Constraint == constraint
	}
	return false
}

// versionReferenceFKs are the NO ACTION FKs into agent_versions added by
// migration 029. A violation means a delete pre-check missed a reference.
// The RESTRICT FKs from training_sessions and evals are deliberately absent.
var versionReferenceFKs = map[string]bool{
	"agent_version_subagent_target_version_id_fkey": true,
	"deployed_sessions_agent_version_id_fkey":       true,
}

// translateVersionFK maps a 23503 raised by a migration-029 FK to
// types.ErrVersionReferenced so a missed pre-check is a 409, not a 500.
func translateVersionFK(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23503" && versionReferenceFKs[pqErr.Constraint] {
		return types.ErrVersionReferenced
	}
	return err
}
