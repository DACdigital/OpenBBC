package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// Child sessions (spec § Repository invariants → CreateChildSession,
// § Datasets close-draft cascade, acceptance "Locked root" + "Close-draft
// cascade").

func chatRow(t *testing.T, db *sql.DB, id string) (agentVersionID string, parentID sql.NullString, toolCallID sql.NullString, depth int, lockedAt sql.NullTime, overrides string) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT agent_version_id::text, parent_session_id::text, parent_tool_call_id, depth, locked_at, backend_header_overrides::text
		FROM chat_sessions WHERE id = $1::uuid`, id,
	).Scan(&agentVersionID, &parentID, &toolCallID, &depth, &lockedAt, &overrides); err != nil {
		t.Fatalf("chatRow(%s): %v", id, err)
	}
	return
}

func countChatChildren(t *testing.T, db *sql.DB, parentID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_sessions WHERE parent_session_id = $1::uuid`, parentID).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	return n
}

func TestChatRepository_CreateChildSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	target := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	repo := NewChatRepository(db)
	if err := repo.SetSessionHeaderOverrides(ctx, r, map[string]map[string]string{"b1": {"X": "1"}}); err != nil {
		t.Fatalf("SetSessionHeaderOverrides: %v", err)
	}

	c, err := repo.CreateChildSession(ctx, r, r, "tu_1", target)
	if err != nil {
		t.Fatalf("CreateChildSession: %v", err)
	}
	av, parent, tool, depth, locked, ovr := chatRow(t, db, c)
	if av != target || parent.String != r || tool.String != "tu_1" || depth != 1 || locked.Valid {
		t.Fatalf("child row = av %s parent %v tool %v depth %d locked %v", av, parent, tool, depth, locked)
	}
	if ovr != `{"b1": {"X": "1"}}` {
		t.Fatalf("child overrides = %s, want root's", ovr)
	}

	// The root's overrides change after C spawned; a grandchild copies the
	// root's current overrides, never C's snapshot.
	if err := repo.SetSessionHeaderOverrides(ctx, r, map[string]map[string]string{"b1": {"X": "2"}}); err != nil {
		t.Fatalf("SetSessionHeaderOverrides: %v", err)
	}
	g, err := repo.CreateChildSession(ctx, r, c, "tu_2", v)
	if err != nil {
		t.Fatalf("CreateChildSession(grandchild): %v", err)
	}
	av, parent, tool, depth, _, ovr = chatRow(t, db, g)
	if av != v || parent.String != c || tool.String != "tu_2" || depth != 2 {
		t.Fatalf("grandchild row = av %s parent %v tool %v depth %d", av, parent, tool, depth)
	}
	if ovr != `{"b1": {"X": "2"}}` {
		t.Fatalf("grandchild overrides = %s, want root's", ovr)
	}

	// Duplicate (parent, tool_call) surfaces as a non-nil error.
	if _, err := repo.CreateChildSession(ctx, r, r, "tu_1", target); err == nil {
		t.Fatal("duplicate (parent, tool_call): want error")
	}
	// Missing parent / missing root → ErrNotFound.
	if _, err := repo.CreateChildSession(ctx, r, uuid.NewString(), "tu_9", v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing parent err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateChildSession(ctx, uuid.NewString(), r, "tu_9", v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing root err = %v, want ErrNotFound", err)
	}
	// A child passed as root is not a root → ErrNotFound.
	if _, err := repo.CreateChildSession(ctx, c, c, "tu_9", v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("child-as-root err = %v, want ErrNotFound", err)
	}
	// Missing target version → ErrNotFound (FK violation).
	if _, err := repo.CreateChildSession(ctx, r, r, "tu_9", uuid.NewString()); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing target err = %v, want ErrNotFound", err)
	}
	if n := countChatChildren(t, db, r); n != 1 {
		t.Fatalf("children of root = %d, want 1", n)
	}
}

func TestChatRepository_CreateChildSession_LockedRoot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	if _, err := db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, r); err != nil {
		t.Fatal(err)
	}
	repo := NewChatRepository(db)
	if _, err := repo.CreateChildSession(ctx, r, r, "tu_1", v); !errors.Is(err, types.ErrSessionLocked) {
		t.Fatalf("err = %v, want ErrSessionLocked", err)
	}
	if n := countChatChildren(t, db, r); n != 0 {
		t.Fatalf("children = %d, want 0", n)
	}
}

func TestDeployedRepository_CreateChildSession(t *testing.T) {
	repo, _, agentID := newDeployedRepoTest(t)
	ctx := context.Background()
	target := seedAgentVersion(t, repo.db)
	root, err := repo.CreateSession(ctx, agentID, "user-A", "root")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	c, err := repo.CreateChildSession(ctx, root.ID, root.ID, "tu_1", target)
	if err != nil {
		t.Fatalf("CreateChildSession: %v", err)
	}
	got, err := repo.GetSessionByID(ctx, c)
	if err != nil {
		t.Fatalf("GetSessionByID: %v", err)
	}
	if got.AgentID != agentID || got.UserID != "user-A" || got.Depth != 1 ||
		got.ParentSessionID == nil || *got.ParentSessionID != root.ID || got.ParentToolCallID != "tu_1" ||
		got.AgentVersionID == nil || *got.AgentVersionID != target {
		t.Fatalf("child = %+v", got)
	}
	g, err := repo.CreateChildSession(ctx, root.ID, c, "tu_2", target)
	if err != nil {
		t.Fatalf("CreateChildSession(grandchild): %v", err)
	}
	got, err = repo.GetSessionByID(ctx, g)
	if err != nil {
		t.Fatalf("GetSessionByID(grandchild): %v", err)
	}
	if got.Depth != 2 || got.UserID != "user-A" || got.AgentID != agentID || *got.ParentSessionID != c {
		t.Fatalf("grandchild = %+v", got)
	}

	if _, err := repo.CreateChildSession(ctx, root.ID, uuid.NewString(), "tu_9", target); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing parent err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateChildSession(ctx, uuid.NewString(), root.ID, "tu_9", target); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing root err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateChildSession(ctx, c, c, "tu_9", target); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("child-as-root err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateChildSession(ctx, root.ID, root.ID, "tu_1", target); err == nil {
		t.Fatal("duplicate (parent, tool_call): want error")
	}
}

func TestChatRepository_GetDescendant(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	r2 := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "tu_1", v)
	g := insertChatChild(t, db, c, "tu_2", v)
	repo := NewChatRepository(db)

	got, err := repo.GetDescendant(ctx, r, c)
	if err != nil {
		t.Fatalf("GetDescendant(R, C): %v", err)
	}
	if got.ID != c || got.AgentVersionID != v || got.Depth != 1 || got.ParentSessionID == nil ||
		*got.ParentSessionID != r || got.ParentToolCallID != "tu_1" || got.LockedAt != nil {
		t.Fatalf("C = %+v", got)
	}
	got, err = repo.GetDescendant(ctx, r, g)
	if err != nil {
		t.Fatalf("GetDescendant(R, G): %v", err)
	}
	if got.ID != g || got.Depth != 2 || *got.ParentSessionID != c || got.ParentToolCallID != "tu_2" {
		t.Fatalf("G = %+v", got)
	}
	if _, err := db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, g); err != nil {
		t.Fatal(err)
	}
	if got, err = repo.GetDescendant(ctx, r, g); err != nil || got.LockedAt == nil {
		t.Fatalf("locked G = %+v, %v", got, err)
	}
	for _, tc := range []struct{ name, root, child string }{
		{"root itself", r, r},
		{"other root", r2, c},
		{"child as root", c, g},
		{"unknown", r, uuid.NewString()},
	} {
		if _, err := repo.GetDescendant(ctx, tc.root, tc.child); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", tc.name, err)
		}
	}
}

func TestDeployedRepository_GetDescendant(t *testing.T) {
	tr := newDeployedTree(t)
	ctx := context.Background()
	r2, err := tr.repo.CreateSession(ctx, tr.agentID, "user-A", "r2")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := tr.repo.GetDescendant(ctx, tr.root, tr.child)
	if err != nil {
		t.Fatalf("GetDescendant(R, C): %v", err)
	}
	if got.ID != tr.child || got.Depth != 1 || got.ParentSessionID == nil || *got.ParentSessionID != tr.root ||
		got.ParentToolCallID != "call-1" || got.AgentVersionID == nil || *got.AgentVersionID != tr.versionID {
		t.Fatalf("C = %+v", got)
	}
	got, err = tr.repo.GetDescendant(ctx, tr.root, tr.grandkid)
	if err != nil {
		t.Fatalf("GetDescendant(R, G): %v", err)
	}
	if got.ID != tr.grandkid || got.Depth != 2 || *got.ParentSessionID != tr.child {
		t.Fatalf("G = %+v", got)
	}
	for _, tc := range []struct{ name, root, child string }{
		{"root itself", tr.root, tr.root},
		{"other root", r2.ID, tr.child},
		{"child as root", tr.child, tr.grandkid},
		{"unknown", tr.root, uuid.NewString()},
	} {
		if _, err := tr.repo.GetDescendant(ctx, tc.root, tc.child); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", tc.name, err)
		}
	}
}

func TestChatRepository_ChildByParentToolCall(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	r2 := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "tu_1", v)
	repo := NewChatRepository(db)

	got, err := repo.ChildByParentToolCall(ctx, r, "tu_1")
	if err != nil || got != c {
		t.Fatalf("ChildByParentToolCall(R, tu_1) = %q, %v; want %s", got, err, c)
	}
	for _, tc := range []struct{ name, parent, tool string }{
		{"other tool call", r, "tu_2"},
		{"other parent", r2, "tu_1"},
		{"child as parent", c, "tu_1"},
	} {
		if _, err := repo.ChildByParentToolCall(ctx, tc.parent, tc.tool); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", tc.name, err)
		}
	}
}

// draftWithRoots gives each root a rated assistant message (with criteria)
// and assigns it to a fresh dataset's draft; returns the draft version id.
func draftWithRoots(t *testing.T, db *sql.DB, roots ...string) string {
	t.Helper()
	ctx := context.Background()
	ds := NewDatasetRepository(db)
	d, err := ds.Create(ctx, "ds-"+uuid.NewString()[:8], "")
	if err != nil {
		t.Fatalf("Create dataset: %v", err)
	}
	var draftID string
	for _, r := range roots {
		m := insertChatMessage(t, db, r, 1)
		if _, err := db.Exec(`INSERT INTO chat_message_feedback (message_id, rating, judge_criteria)
			VALUES ($1::uuid, 'up', '["c"]'::jsonb)`, m); err != nil {
			t.Fatalf("seed feedback: %v", err)
		}
		draft, err := ds.AssignSessionToDraft(ctx, d.ID, r)
		if err != nil {
			t.Fatalf("AssignSessionToDraft: %v", err)
		}
		draftID = draft.ID
	}
	return draftID
}

func TestDataset_CloseDraft_CascadesToDescendants(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c1 := insertChatChild(t, db, r, "tu_1", v)
	c2 := insertChatChild(t, db, r, "tu_2", v)
	g := insertChatChild(t, db, c1, "tu_3", v)
	// A tree whose root is not in the dataset stays unlocked.
	other := insertChatRoot(t, db, v)
	otherChild := insertChatChild(t, db, other, "tu_1", v)

	draft := draftWithRoots(t, db, r)
	if err := NewDatasetRepository(db).CloseDraft(ctx, draft, ""); err != nil {
		t.Fatalf("CloseDraft: %v", err)
	}
	for _, id := range []string{r, c1, c2, g} {
		if _, _, _, _, locked, _ := chatRow(t, db, id); !locked.Valid {
			t.Fatalf("session %s not locked", id)
		}
	}
	for _, id := range []string{other, otherChild} {
		if _, _, _, _, locked, _ := chatRow(t, db, id); locked.Valid {
			t.Fatalf("session %s outside the dataset got locked", id)
		}
	}
}

// TestChatRepository_CreateChildSession_CloseDraftRace runs CreateChildSession
// (connection A) against CloseDraft (connection B) on the same root. Either A
// commits first and B's statement 2 locks the child, or B locks the root first
// and A returns ErrSessionLocked with no child. Never an unlocked session
// under a locked root.
func TestChatRepository_CreateChildSession_CloseDraftRace(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	db.SetMaxOpenConns(4)
	v := seedAgentVersion(t, db)
	repo := NewChatRepository(db)
	ds := NewDatasetRepository(db)

	var childFirst, lockFirst int
	for i := 0; i < 20; i++ {
		r := insertChatRoot(t, db, v)
		draft := draftWithRoots(t, db, r)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var childID string
		var errA, errB error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			childID, errA = repo.CreateChildSession(ctx, r, r, "tu_x", v)
		}()
		go func() {
			defer wg.Done()
			<-start
			errB = ds.CloseDraft(ctx, draft, "")
		}()
		close(start)
		wg.Wait()

		if errB != nil {
			t.Fatalf("iter %d: CloseDraft: %v", i, errB)
		}
		children := countChatChildren(t, db, r)
		switch {
		case errA == nil:
			childFirst++
			t.Logf("iter %d: child committed first (child %s)", i, childID)
			if children != 1 {
				t.Fatalf("iter %d: A succeeded but children = %d", i, children)
			}
			if _, _, _, _, locked, _ := chatRow(t, db, childID); !locked.Valid {
				t.Fatalf("iter %d: child not locked", i)
			}
		case errors.Is(errA, types.ErrSessionLocked):
			lockFirst++
			t.Logf("iter %d: close-draft locked the root first", i)
			if children != 0 {
				t.Fatalf("iter %d: A refused but children = %d", i, children)
			}
		default:
			t.Fatalf("iter %d: CreateChildSession unexpected err: %v", i, errA)
		}
		var unlocked int
		if err := db.QueryRow(`SELECT COUNT(*) FROM chat_sessions
			WHERE locked_at IS NULL AND (id = $1::uuid OR parent_session_id = $1::uuid)`, r).Scan(&unlocked); err != nil {
			t.Fatalf("iter %d: count unlocked: %v", i, err)
		}
		if unlocked != 0 {
			t.Fatalf("iter %d: %d unlocked sessions under locked root", i, unlocked)
		}
		if _, err := repo.CreateChildSession(ctx, r, r, "tu_y", v); !errors.Is(err, types.ErrSessionLocked) {
			t.Fatalf("iter %d: follow-up err = %v, want ErrSessionLocked", i, err)
		}
	}
	t.Logf("orderings: child-first=%d lock-first=%d", childFirst, lockFirst)
}

// waitForLockWaiter polls until some backend is blocked on a heavyweight
// lock, i.e. the other side has reached the conflicting row lock.
func waitForLockWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no backend blocked on a lock within 5s")
}

// TestCloseDraft_ChildSpawnInterleavings forces both orderings of the
// locked-root race deterministically by holding the conflicting row lock in
// an explicit transaction until the other side is seen waiting on it.
func TestCloseDraft_ChildSpawnInterleavings(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	repo := NewChatRepository(db)
	ds := NewDatasetRepository(db)

	t.Run("spawn holds FOR SHARE, close-draft waits then locks the child", func(t *testing.T) {
		r := insertChatRoot(t, db, v)
		draft := draftWithRoots(t, db, r)
		// Same statements as CreateChildSession, with a pause between the
		// root lock and the commit.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		var lockedAt sql.NullTime
		if err := tx.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id = $1::uuid FOR SHARE`, r).Scan(&lockedAt); err != nil {
			t.Fatal(err)
		}
		if lockedAt.Valid {
			t.Fatal("root unexpectedly locked")
		}
		done := make(chan error, 1)
		go func() { done <- ds.CloseDraft(ctx, draft, "") }()
		waitForLockWaiter(t, db)
		var child string
		if err := tx.QueryRow(`
			INSERT INTO chat_sessions (agent_version_id, parent_session_id, parent_tool_call_id, depth)
			VALUES ($1::uuid, $2::uuid, 'tu_x', 1) RETURNING id::text`, v, r).Scan(&child); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("CloseDraft: %v", err)
		}
		if _, _, _, _, locked, _ := chatRow(t, db, child); !locked.Valid {
			t.Fatal("child committed while close-draft waited is unlocked")
		}
	})

	t.Run("close-draft locks root first, spawn waits then refuses", func(t *testing.T) {
		r := insertChatRoot(t, db, v)
		// Same statement as CloseDraft's statement 1, held open.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, r); err != nil {
			t.Fatal(err)
		}
		type result struct {
			id  string
			err error
		}
		done := make(chan result, 1)
		go func() {
			id, err := repo.CreateChildSession(ctx, r, r, "tu_x", v)
			done <- result{id, err}
		}()
		waitForLockWaiter(t, db)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		res := <-done
		if !errors.Is(res.err, types.ErrSessionLocked) {
			t.Fatalf("CreateChildSession = %q, %v; want ErrSessionLocked", res.id, res.err)
		}
		if n := countChatChildren(t, db, r); n != 0 {
			t.Fatalf("children = %d, want 0", n)
		}
	})
}

// TestChatRepository_CreateChildSession_ParentOutsideRootTree: the parent
// must be rootID itself or one of its descendants; a parent in another tree
// (child or root) is ErrNotFound and writes nothing.
func TestChatRepository_CreateChildSession_ParentOutsideRootTree(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	repo := NewChatRepository(db)
	r1 := insertChatRoot(t, db, v)
	r2 := insertChatRoot(t, db, v)
	c2, err := repo.CreateChildSession(ctx, r2, r2, "tu_1", v)
	if err != nil {
		t.Fatalf("CreateChildSession(R2): %v", err)
	}
	if _, err := repo.CreateChildSession(ctx, r1, c2, "tu_x", v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("parent in other tree err = %v, want ErrNotFound", err)
	}
	if n := countChatChildren(t, db, c2); n != 0 {
		t.Fatalf("children of C2 = %d, want 0", n)
	}
	if _, err := repo.CreateChildSession(ctx, r1, r2, "tu_y", v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("other root as parent err = %v, want ErrNotFound", err)
	}
	if n := countChatChildren(t, db, r2); n != 1 {
		t.Fatalf("children of R2 = %d, want 1", n)
	}
}

func TestDeployedRepository_CreateChildSession_ParentOutsideRootTree(t *testing.T) {
	repo, _, agentID := newDeployedRepoTest(t)
	ctx := context.Background()
	target := seedAgentVersion(t, repo.db)
	r1, err := repo.CreateSession(ctx, agentID, "user-A", "r1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r2, err := repo.CreateSession(ctx, agentID, "user-B", "r2")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	c2, err := repo.CreateChildSession(ctx, r2.ID, r2.ID, "tu_1", target)
	if err != nil {
		t.Fatalf("CreateChildSession(R2): %v", err)
	}
	if _, err := repo.CreateChildSession(ctx, r1.ID, c2, "tu_x", target); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("parent in other tree err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateChildSession(ctx, r1.ID, r2.ID, "tu_y", target); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("other root as parent err = %v, want ErrNotFound", err)
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM deployed_sessions WHERE parent_session_id IN ($1::uuid, $2::uuid)`, c2, r2.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("children under R2's tree = %d, want 1", n)
	}
}

// TestCreateChildSession_RejectsEmptyIDs: both repositories refuse an empty
// root/parent/tool-call/target id before touching the database (nil *sql.DB —
// reaching BeginTx would panic). Runs without DATABASE_URL.
func TestCreateChildSession_RejectsEmptyIDs(t *testing.T) {
	ctx := context.Background()
	const id = "00000000-0000-0000-0000-000000000001"
	cases := []struct {
		name                       string
		root, parent, call, target string
		want                       string
	}{
		{"root", "", id, "tu_1", id, "root id is required"},
		{"parent", id, "", "tu_1", id, "parent id is required"},
		{"tool call", id, id, "", id, "parent tool call id is required"},
		{"target", id, id, "tu_1", "", "target version id is required"},
	}
	repos := map[string]func(ctx context.Context, rootID, parentID, callID, targetID string) (string, error){
		"chat":     NewChatRepository(nil).CreateChildSession,
		"deployed": NewDeployedRepository(nil).CreateChildSession,
	}
	for repoName, create := range repos {
		for _, tc := range cases {
			t.Run(repoName+"/"+tc.name, func(t *testing.T) {
				got, err := create(ctx, tc.root, tc.parent, tc.call, tc.target)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("CreateChildSession = (%q, %v), want error containing %q", got, err, tc.want)
				}
				if errors.Is(err, types.ErrNotFound) {
					t.Fatalf("empty id must be a plain error, got sentinel %v", err)
				}
			})
		}
	}
}
