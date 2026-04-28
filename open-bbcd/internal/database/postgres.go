package database

import (
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
)

// Connection pool settings
const (
	MaxOpenConns    = 25
	MaxIdleConns    = 5
	ConnMaxLifetime = 5 * time.Minute
)

func NewPostgres(url string) (*sql.DB, error) {
	if url == "" {
		return nil, errors.New("database URL is required")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	db.SetConnMaxLifetime(ConnMaxLifetime)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
