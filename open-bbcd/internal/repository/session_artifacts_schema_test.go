package repository

import (
	"context"
	"database/sql"
	"testing"

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
