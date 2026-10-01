package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/migrations"
	"github.com/google/uuid"
)

// checkSessionArtifactConstraints pins the spec § Data — new tables
// constraints on one session-artifact table.
func checkSessionArtifactConstraints(t *testing.T, db *sql.DB, table, sessionID string, deleteSession func()) {
	t.Helper()
	ctx := context.Background()
	insert := func(origin, uri string, messageID any) error {
		_, err := db.ExecContext(ctx, `INSERT INTO `+table+`
			(session_id, origin, store_id, uri, mime, size_bytes, sha256, message_id)
			VALUES ($1::uuid, $2, 'MAIN', $3, 'image/png', 1, 'x', $4::uuid)`,
			sessionID, origin, uri, messageID)
		return err
	}
	if err := insert("bogus", "sha256/a", nil); err == nil {
		t.Fatal("origin CHECK not enforced")
	}
	if err := insert("upload", "sha256/a", nil); err != nil {
		t.Fatalf("first pending row: %v", err)
	}
	if err := insert("upload", "sha256/a", nil); !isUniqueViolation(err) {
		t.Fatalf("second pending row for same blob: err=%v, want unique violation", err)
	}
	// A consumed row and a new pending row for one blob may coexist, and
	// tool_result rows are unconstrained.
	if err := insert("upload", "sha256/a", uuid.NewString()); err != nil {
		t.Fatalf("consumed row beside pending: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := insert("tool_result", "sha256/a", uuid.NewString()); err != nil {
			t.Fatalf("tool_result row %d: %v", i, err)
		}
	}
	if err := insert("tool_result", "sha256/b", nil); err == nil {
		t.Fatal("tool_result row without message_id accepted")
	}
	deleteSession()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE session_id = $1::uuid`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("session delete left %d rows (want cascade)", n)
	}
}

func TestChatSessionArtifacts_Constraints(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	vid := seedAgentVersion(t, db)
	sid := uuid.NewString()
	if err := NewChatRepository(db).EnsureSession(ctx, sid, vid); err != nil {
		t.Fatal(err)
	}
	checkSessionArtifactConstraints(t, db, "chat_session_artifacts", sid, func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM chat_sessions WHERE id = $1::uuid`, sid); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeployedSessionArtifacts_Constraints(t *testing.T) {
	repo, _, agentID := newDeployedRepoTest(t)
	ctx := context.Background()
	sess, err := repo.CreateSession(ctx, agentID, "user-A", "")
	if err != nil {
		t.Fatal(err)
	}
	checkSessionArtifactConstraints(t, repo.db, "deployed_session_artifacts", sess.ID, func() {
		if err := repo.DeleteSession(ctx, sess.ID, "user-A"); err != nil {
			t.Fatal(err)
		}
	})
}

// chatArtifactsBackfillSQL extracts the backfill statement from the embedded
// migration 027 (between the backfill:begin/end markers), so the test runs
// exactly the SQL goose applied, against seeded data, without re-running
// goose on a shared DB.
func chatArtifactsBackfillSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile("027_chat_session_artifacts.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	const begin, end = "-- backfill:begin", "-- backfill:end"
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatal("027 has no backfill:begin/end markers")
	}
	return s[i+len(begin) : j]
}

func TestChatSessionArtifacts_Backfill(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewChatRepository(db)
	sid := uuid.NewString()
	if err := repo.EnsureSession(ctx, sid, seedAgentVersion(t, db)); err != nil {
		t.Fatal(err)
	}
	toolMsg, userMsg := uuid.NewString(), uuid.NewString()
	// A Plan-1 (PR #54) tool-role message: tool_result first, then refs. One
	// full ref, one with only store_id/uri (defaults apply), one lacking uri
	// (skipped). The user message's ref is not a tool result and is skipped.
	toolContent := `[
		{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":false},
		{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/aa","mime":"image/png","size_bytes":3,"sha256":"aa","filename":"shot.png"},
		{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/bb","filename":""},
		{"type":"artifact_ref","store_id":"MAIN"},
		{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/dd","size_bytes":"12.0"},
		{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/ee","size_bytes":"abc"}
	]`
	userContent := `[{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/cc","mime":"image/png","size_bytes":1,"sha256":"cc"}]`
	for _, m := range []struct {
		id, role, content string
		seq               int
	}{
		{userMsg, "user", userContent, 1}, {toolMsg, "tool", toolContent, 2},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO chat_messages (id, session_id, role, content, seq)
			VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5)`, m.id, sid, m.role, m.content, m.seq); err != nil {
			t.Fatal(err)
		}
	}
	// A non-array content row must not break the backfill.
	if _, err := db.ExecContext(ctx, `INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'tool', '{"legacy":true}'::jsonb, 3)`, sid); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, chatArtifactsBackfillSQL(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	a, err := repo.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/aa")
	if err != nil {
		t.Fatalf("lookup backfilled ref: %v", err)
	}
	if a.Origin != types.ArtifactOriginToolResult || a.MessageID != toolMsg || a.MIME != "image/png" ||
		a.SizeBytes != 3 || a.Sha256 != "aa" || a.Filename != "shot.png" {
		t.Fatalf("backfilled row = %+v", a)
	}
	b, err := repo.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/bb")
	if err != nil {
		t.Fatalf("lookup defaulted ref: %v", err)
	}
	if b.MIME != "application/octet-stream" || b.SizeBytes != 0 || b.Sha256 != "" || b.Filename != "" {
		t.Fatalf("defaulted row = %+v", b)
	}
	var nullFilename bool
	if err := db.QueryRowContext(ctx, `SELECT filename IS NULL FROM chat_session_artifacts WHERE id = $1::uuid`, b.ID).Scan(&nullFilename); err != nil || !nullFilename {
		t.Fatalf("empty filename not NULLed: null=%v err=%v", nullFilename, err)
	}
	if _, err := repo.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/cc"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("user-role ref backfilled: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_session_artifacts WHERE session_id = $1::uuid`, sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{"sha256/dd", "sha256/ee"} {
		bad, err := repo.LookupSessionArtifact(ctx, sid, "MAIN", uri)
		if err != nil || bad.SizeBytes != 0 {
			t.Fatalf("non-integer size_bytes ref %s = %+v, err %v; want backfilled with 0", uri, bad, err)
		}
	}
	if n != 4 {
		t.Fatalf("backfilled %d rows, want 4", n)
	}
}
