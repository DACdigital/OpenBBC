package database

import (
	"testing"
)

func TestNewPostgres_InvalidURL(t *testing.T) {
	_, err := NewPostgres("")
	if err == nil {
		t.Error("NewPostgres(\"\") should return error for empty URL")
	}
}
