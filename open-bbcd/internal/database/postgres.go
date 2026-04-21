package database

import (
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
)

func NewPostgres(url string) (*sql.DB, error) {
	if url == "" {
		return nil, errors.New("database URL is required")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
