package repository

import (
	"errors"

	"github.com/lib/pq"
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
