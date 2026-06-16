// open-bbcd/internal/repository/scanner.go
package repository

// scanner is the common subset of *sql.Row and *sql.Rows that the repo
// scan helpers depend on.
type scanner interface {
	Scan(dest ...any) error
}
