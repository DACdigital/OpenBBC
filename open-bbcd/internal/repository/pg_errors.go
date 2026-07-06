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
