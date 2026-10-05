package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// addVersion raw-inserts another version of agentID with the given status.
func addVersion(t *testing.T, db *sql.DB, agentID string, parentID *string, status types.AgentStatus) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO agent_versions (agent_id, parent_version_id, status)
		VALUES ($1::uuid, $2::uuid, $3) RETURNING id::text`, agentID, parentID, string(status)).Scan(&id); err != nil {
		t.Fatalf("addVersion: %v", err)
	}
	return id
}

// boRoot raw-inserts a root BO chat session on versionID.
func boRoot(t *testing.T, db *sql.DB, versionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&id); err != nil {
		t.Fatalf("boRoot: %v", err)
	}
	return id
}

// deployedRoot raw-inserts a root deployed session of agentID.
func deployedRoot(t *testing.T, db *sql.DB, agentID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO deployed_sessions (agent_id, user_id) VALUES ($1::uuid, 'u1') RETURNING id::text`, agentID).Scan(&id); err != nil {
		t.Fatalf("deployedRoot: %v", err)
	}
	return id
}

func lockChat(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("lockChat: %v", err)
		}
	}
}

// rowExists reports whether table has a row with the given id.
func rowExists(t *testing.T, db *sql.DB, table, id string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id = $1::uuid)`, id).Scan(&ok); err != nil {
		t.Fatalf("rowExists %s: %v", table, err)
	}
	return ok
}

// referenceSetups puts V (a READY version of its own agent) into one of the
// three referenced states because of another agent Y.
var referenceSetups = map[string]func(t *testing.T, db *sql.DB, v string){
	"binding target": func(t *testing.T, db *sql.DB, v string) {
		y := seedVersionStatusOnly(t, db, types.AgentStatusDraft)
		rawBind(t, db, y, "worker", v)
	},
	"deployed child": func(t *testing.T, db *sql.DB, v string) {
		yAgent, _ := seedVersionWithStatus(t, db, types.AgentStatusDeployed)
		insertDeployedChild(t, db, deployedRoot(t, db, yAgent), "call-1", v)
	},
	"locked BO child after binding removed": func(t *testing.T, db *sql.DB, v string) {
		y := seedVersionStatusOnly(t, db, types.AgentStatusDraft)
		rawBind(t, db, y, "worker", v)
		root := boRoot(t, db, y)
		child := insertChatChild(t, db, root, "call-1", v)
		lockChat(t, db, root, child)
		if _, err := db.Exec(`DELETE FROM agent_version_subagent WHERE caller_version_id = $1::uuid`, y); err != nil {
			t.Fatalf("delete binding: %v", err)
		}
	},
}

func TestVersionDelete_Referenced(t *testing.T) {
	for name, setup := range referenceSetups {
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			vrepo := NewAgentVersionRepository(db)
			_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
			setup(t, db, v)
			if err := vrepo.Delete(context.Background(), v); !errors.Is(err, types.ErrVersionReferenced) {
				t.Fatalf("Delete = %v, want ErrVersionReferenced", err)
			}
			if !rowExists(t, db, "agent_versions", v) {
				t.Fatal("version was deleted")
			}
		})
	}
}

func TestVersionDelete_UnlockedBOChildCascades(t *testing.T) {
	db := openTestDB(t)
	vrepo := NewAgentVersionRepository(db)
	_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
	y := seedVersionStatusOnly(t, db, types.AgentStatusDraft)
	root := boRoot(t, db, y)
	child := insertChatChild(t, db, root, "call-1", v)
	grandchild := insertChatChild(t, db, child, "call-2", y)

	if err := vrepo.Delete(context.Background(), v); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rowExists(t, db, "agent_versions", v) {
		t.Fatal("version still exists")
	}
	if rowExists(t, db, "chat_sessions", child) || rowExists(t, db, "chat_sessions", grandchild) {
		t.Fatal("child subtree survived")
	}
	if !rowExists(t, db, "chat_sessions", root) {
		t.Fatal("root was deleted")
	}
}

func TestAgentDelete_ReferencedByAnotherAgent(t *testing.T) {
	for name, setup := range referenceSetups {
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			arepo := NewAgentRepository(db)
			a, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
			setup(t, db, v)
			if err := arepo.Delete(context.Background(), a); !errors.Is(err, types.ErrVersionReferenced) {
				t.Fatalf("Delete = %v, want ErrVersionReferenced", err)
			}
			if !rowExists(t, db, "agents", a) || !rowExists(t, db, "agent_versions", v) {
				t.Fatal("agent or version was deleted")
			}
		})
	}
}

func TestAgentDelete_SelfBinding(t *testing.T) {
	db := openTestDB(t)
	arepo := NewAgentRepository(db)
	a, v1 := seedVersionWithStatus(t, db, types.AgentStatusReady)
	v2 := addVersion(t, db, a, &v1, types.AgentStatusDraft)
	rawBind(t, db, v2, "older", v1)

	// Deployed children from an earlier deployment, pinned to v1.
	dRoot := deployedRoot(t, db, a)
	dChild := insertDeployedChild(t, db, dRoot, "call-1", v1)
	// An unlocked BO tree and a locked one, both rooted on v2 with children on v1.
	bRoot := boRoot(t, db, v2)
	bChild := insertChatChild(t, db, bRoot, "call-1", v1)
	lRoot := boRoot(t, db, v2)
	lChild := insertChatChild(t, db, lRoot, "call-1", v1)
	lockChat(t, db, lRoot, lChild)

	if err := arepo.Delete(context.Background(), a); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for table, ids := range map[string][]string{
		"agents":            {a},
		"agent_versions":    {v1, v2},
		"deployed_sessions": {dRoot, dChild},
		"chat_sessions":     {bRoot, bChild, lRoot, lChild},
	} {
		for _, id := range ids {
			if rowExists(t, db, table, id) {
				t.Fatalf("%s %s survived", table, id)
			}
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM agent_version_subagent`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bindings = %d (%v), want 0", n, err)
	}
}

func TestAgentDelete_CascadesDeployedTree(t *testing.T) {
	db := openTestDB(t)
	arepo := NewAgentRepository(db)
	vrepo := NewAgentVersionRepository(db)
	r, _ := seedVersionWithStatus(t, db, types.AgentStatusReady)
	bAgent, b := seedVersionWithStatus(t, db, types.AgentStatusReady)
	root := deployedRoot(t, db, r)
	child := insertDeployedChild(t, db, root, "call-1", b)
	grandchild := insertDeployedChild(t, db, child, "call-2", b)

	// While the tree exists, B's version is referenced.
	if err := vrepo.Delete(context.Background(), b); !errors.Is(err, types.ErrVersionReferenced) {
		t.Fatalf("Delete(B) before = %v, want ErrVersionReferenced", err)
	}
	if err := arepo.Delete(context.Background(), r); err != nil {
		t.Fatalf("Delete(R): %v", err)
	}
	for _, id := range []string{root, child, grandchild} {
		if rowExists(t, db, "deployed_sessions", id) {
			t.Fatalf("deployed session %s survived", id)
		}
	}
	if !rowExists(t, db, "agents", bAgent) {
		t.Fatal("B's agent was deleted")
	}
	if err := vrepo.Delete(context.Background(), b); err != nil {
		t.Fatalf("Delete(B) after: %v", err)
	}
}
