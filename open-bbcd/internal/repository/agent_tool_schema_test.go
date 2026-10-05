package repository

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/lib/pq"
)

// wantPQ asserts err is a pq.Error with the given SQLSTATE and, when non-empty,
// constraint name.
func wantPQ(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pe *pq.Error
	if !errors.As(err, &pe) {
		t.Fatalf("want pq error %s/%s, got %v", code, constraint, err)
	}
	if string(pe.Code) != code {
		t.Fatalf("code = %s (%s), want %s", pe.Code, pe.Message, code)
	}
	if constraint != "" && pe.Constraint != constraint {
		t.Fatalf("constraint = %q, want %q", pe.Constraint, constraint)
	}
}

func seedBOSession(t *testing.T, db *sql.DB, versionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1) RETURNING id::text`, versionID).Scan(&id); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	return id
}

func seedDeployedRoot(t *testing.T, db *sql.DB, agentID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO deployed_sessions (agent_id, user_id) VALUES ($1, 'u1') RETURNING id::text`, agentID).Scan(&id); err != nil {
		t.Fatalf("seed deployed session: %v", err)
	}
	return id
}

func TestAgentToolSchema(t *testing.T) {
	db := openTestDB(t)
	agentID, v1 := seedAgent(t, db)
	_, v2 := seedAgent(t, db)

	bind := func(caller, target, name string) error {
		_, err := db.Exec(`INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name) VALUES ($1,$2,$3)`, caller, target, name)
		return err
	}

	t.Run("defaults", func(t *testing.T) {
		var enabled bool
		if err := db.QueryRow(`SELECT agent_tool_enabled FROM agent_versions WHERE id=$1`, v1).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled {
			t.Fatal("agent_tool_enabled should default false")
		}
		s := seedBOSession(t, db, v1)
		var depth int
		var parent, call sql.NullString
		if err := db.QueryRow(`SELECT depth, parent_session_id::text, parent_tool_call_id FROM chat_sessions WHERE id=$1`, s).Scan(&depth, &parent, &call); err != nil {
			t.Fatal(err)
		}
		if depth != 0 || parent.Valid || call.Valid {
			t.Fatalf("defaults wrong: depth=%d parent=%v call=%v", depth, parent, call)
		}
	})

	t.Run("deployed_sessions agent_version index", func(t *testing.T) {
		var one int
		if err := db.QueryRow(`SELECT 1 FROM pg_indexes WHERE indexname='idx_deployed_sessions_agent_version'`).Scan(&one); err != nil {
			t.Fatalf("index missing: %v", err)
		}
	})

	t.Run("binding name pattern", func(t *testing.T) {
		wantPQ(t, bind(v1, v2, "Bad"), "23514", "")
		wantPQ(t, bind(v1, v2, "a"+strings.Repeat("b", 40)), "23514", "") // 41 chars
	})
	t.Run("self binding", func(t *testing.T) {
		wantPQ(t, bind(v1, v1, "me"), "23514", "")
	})
	t.Run("duplicates", func(t *testing.T) {
		if err := bind(v1, v2, "helper"); err != nil {
			t.Fatal(err)
		}
		_, v3 := seedAgent(t, db)
		wantPQ(t, bind(v1, v3, "helper"), "23505", "") // (caller,name)
		wantPQ(t, bind(v1, v2, "other"), "23505", "")  // (caller,target)
	})

	t.Run("bo parent link", func(t *testing.T) {
		root := seedBOSession(t, db, v1)
		_, err := db.Exec(`INSERT INTO chat_sessions (agent_version_id, parent_session_id, depth) VALUES ($1,$2,1)`, v1, root)
		wantPQ(t, err, "23514", "chat_sessions_parent_link_chk")
	})
	t.Run("bo depth", func(t *testing.T) {
		root := seedBOSession(t, db, v1)
		_, err := db.Exec(`INSERT INTO chat_sessions (agent_version_id, parent_session_id, parent_tool_call_id, depth) VALUES ($1,$2,'c1',0)`, v1, root)
		wantPQ(t, err, "23514", "chat_sessions_depth_chk")
		_, err = db.Exec(`INSERT INTO chat_sessions (agent_version_id, depth) VALUES ($1,1)`, v1)
		wantPQ(t, err, "23514", "chat_sessions_depth_chk")
	})
	t.Run("bo unique tool call", func(t *testing.T) {
		root := seedBOSession(t, db, v1)
		insertChatChild(t, db, root, "call-1", v2)
		_, err := db.Exec(`INSERT INTO chat_sessions (agent_version_id, parent_session_id, parent_tool_call_id, depth) VALUES ($1,$2,'call-1',1)`, v2, root)
		wantPQ(t, err, "23505", "idx_chat_sessions_parent")
	})

	t.Run("deployed child version", func(t *testing.T) {
		root := seedDeployedRoot(t, db, agentID)
		_, err := db.Exec(`INSERT INTO deployed_sessions (agent_id, user_id, parent_session_id, parent_tool_call_id, depth) VALUES ($1,'u1',$2,'c1',1)`, agentID, root)
		wantPQ(t, err, "23514", "deployed_sessions_child_version_chk")
		_, err = db.Exec(`INSERT INTO deployed_sessions (agent_id, user_id, agent_version_id) VALUES ($1,'u1',$2)`, agentID, v1)
		wantPQ(t, err, "23514", "deployed_sessions_child_version_chk")
	})
	t.Run("deployed child ok and unique", func(t *testing.T) {
		root := seedDeployedRoot(t, db, agentID)
		insertDeployedChild(t, db, root, "call-1", v2)
		_, err := db.Exec(`INSERT INTO deployed_sessions (agent_id, user_id, parent_session_id, parent_tool_call_id, depth, agent_version_id) VALUES ($1,'u1',$2,'call-1',1,$3)`, agentID, root, v2)
		wantPQ(t, err, "23505", "idx_deployed_sessions_parent")
	})

	t.Run("bo cascade", func(t *testing.T) {
		root := seedBOSession(t, db, v1)
		child := insertChatChild(t, db, root, "c1", v2)
		grand := insertChatChild(t, db, child, "c2", v1)
		if _, err := db.Exec(`DELETE FROM chat_sessions WHERE id=$1`, root); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM chat_sessions WHERE id IN ($1,$2)`, child, grand).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("children left after root delete: %d", n)
		}
	})
	t.Run("deployed cascade", func(t *testing.T) {
		root := seedDeployedRoot(t, db, agentID)
		child := insertDeployedChild(t, db, root, "c1", v2)
		grand := insertDeployedChild(t, db, child, "c2", v1)
		if _, err := db.Exec(`DELETE FROM deployed_sessions WHERE id=$1`, root); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM deployed_sessions WHERE id IN ($1,$2)`, child, grand).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("children left after root delete: %d", n)
		}
	})
}
