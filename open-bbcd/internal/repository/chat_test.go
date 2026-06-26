package repository

import (
	"context"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestChatRepository_SessionHeaderOverrides_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	versionID := seedAgentVersion(t, db)
	chatRepo := NewChatRepository(db)
	ctx := context.Background()

	// Create a session.
	var sessionID string
	err := db.QueryRowContext(ctx,
		`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`,
		versionID,
	).Scan(&sessionID)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Default should be empty map.
	ovr, err := chatRepo.GetSessionHeaderOverrides(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSessionHeaderOverrides (initial): %v", err)
	}
	if len(ovr) != 0 {
		t.Fatalf("expected empty map initially, got %v", ovr)
	}

	// Set overrides.
	want := map[string]map[string]string{
		"backend-1": {"Authorization": "Bearer test-token", "X-Debug": "1"},
	}
	if err := chatRepo.SetSessionHeaderOverrides(ctx, sessionID, want); err != nil {
		t.Fatalf("SetSessionHeaderOverrides: %v", err)
	}

	// Read back.
	got, err := chatRepo.GetSessionHeaderOverrides(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSessionHeaderOverrides (after set): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 backend entry, got %d", len(got))
	}
	if got["backend-1"]["Authorization"] != "Bearer test-token" {
		t.Errorf("Authorization mismatch: %v", got["backend-1"])
	}
	if got["backend-1"]["X-Debug"] != "1" {
		t.Errorf("X-Debug mismatch: %v", got["backend-1"])
	}

	// Overwrite with empty map.
	if err := chatRepo.SetSessionHeaderOverrides(ctx, sessionID, map[string]map[string]string{}); err != nil {
		t.Fatalf("SetSessionHeaderOverrides (clear): %v", err)
	}
	cleared, err := chatRepo.GetSessionHeaderOverrides(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSessionHeaderOverrides (cleared): %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("expected empty after clear, got %v", cleared)
	}
}

func TestChatRepository_GetSessionHeaderOverrides_NotFound(t *testing.T) {
	db := openTestDB(t)
	chatRepo := NewChatRepository(db)
	ctx := context.Background()

	_, err := chatRepo.GetSessionHeaderOverrides(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil || err != types.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestChatRepository_SetSessionHeaderOverrides_NotFound(t *testing.T) {
	db := openTestDB(t)
	chatRepo := NewChatRepository(db)
	ctx := context.Background()

	err := chatRepo.SetSessionHeaderOverrides(ctx, "00000000-0000-0000-0000-000000000000", map[string]map[string]string{})
	if err == nil || err != types.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
