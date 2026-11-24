package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Forks append at the agent's tip (spec 2026-11-24-declarative-seed-design.md
// §4): parent_version_id is the current tip, content comes from the source.

func readParent(t *testing.T, db sqlQueryer, versionID string) string {
	t.Helper()
	var parent *string
	if err := db.QueryRowContext(context.Background(), `SELECT parent_version_id::text FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent == nil {
		return ""
	}
	return *parent
}

func readMCPSet(t *testing.T, db sqlQueryer, versionID string) []MCPAttachment {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT backend_id::text, note FROM agent_version_mcp_backend WHERE agent_version_id = $1::uuid ORDER BY backend_id`, versionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []MCPAttachment
	for rows.Next() {
		var a MCPAttachment
		if err := rows.Scan(&a.BackendID, &a.Note); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func mustVersionNum(t *testing.T, repo *AgentVersionRepository, versionID string) int {
	t.Helper()
	n, err := repo.GetVersionNum(context.Background(), versionID)
	if err != nil {
		t.Fatalf("GetVersionNum: %v", err)
	}
	return n
}

func TestFork_FromNonTipAppendsAtTip(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentVersionRepository(db)
	ctx := context.Background()

	_, v1 := seedVersionWithStatus(t, db, types.AgentStatusReady)
	be := seedMCPBackend(t, db, "slack")
	if _, err := db.Exec(`INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note) VALUES ($1::uuid, $2::uuid, 'from v1')`, v1, be); err != nil {
		t.Fatal(err)
	}
	wantBindings := seedForkParent(t, db, v1)
	wantMCP := readMCPSet(t, db, v1)

	v2, err := repo.CreateVersionFromPrompts(ctx, v1, []byte(`{"main_prompt":"v2"}`), types.AgentStatusDraft)
	if err != nil {
		t.Fatalf("fork v2: %v", err)
	}
	v3, err := repo.CreateVersionFromPrompts(ctx, v2, []byte(`{"main_prompt":"v3"}`), types.AgentStatusDraft)
	if err != nil {
		t.Fatalf("fork v3: %v", err)
	}
	// Make v3's wiring differ from v1's so the copy source is observable.
	if _, err := db.Exec(`DELETE FROM agent_version_mcp_backend WHERE agent_version_id = $1::uuid`, v3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM agent_version_subagent WHERE caller_version_id = $1::uuid`, v3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_versions SET agent_tool_enabled = false WHERE id = $1::uuid`, v3); err != nil {
		t.Fatal(err)
	}

	submitted, _ := json.Marshal(types.Prompts{MainPrompt: "edited from v1", SkillPrompts: map[string]string{"s": "p"}})
	v4, err := repo.CreateVersionFromPrompts(ctx, v1, submitted, types.AgentStatusDraft)
	if err != nil {
		t.Fatalf("fork from non-tip v1: %v", err)
	}

	if got := readParent(t, db, v4); got != v3 {
		t.Fatalf("parent_version_id = %s, want tip v3 %s", got, v3)
	}
	if n := mustVersionNum(t, repo, v4); n != 4 {
		t.Fatalf("version num = %d, want 4", n)
	}
	got, err := repo.GetByID(ctx, v4)
	if err != nil {
		t.Fatal(err)
	}
	var gotPrompts, wantPrompts types.Prompts
	_ = json.Unmarshal(got.Prompts, &gotPrompts)
	_ = json.Unmarshal(submitted, &wantPrompts)
	if gotPrompts.MainPrompt != wantPrompts.MainPrompt || gotPrompts.SkillPrompts["s"] != "p" {
		t.Fatalf("prompts = %s, want %s", got.Prompts, submitted)
	}
	gotMCP := readMCPSet(t, db, v4)
	if len(gotMCP) != len(wantMCP) || gotMCP[0] != wantMCP[0] {
		t.Fatalf("MCP set = %+v, want %+v (from v1)", gotMCP, wantMCP)
	}
	assertForkCopied(t, db, v1, v4, true, wantBindings)
}

func TestFork_ConcurrentBothSucceed(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentVersionRepository(db)
	ctx := context.Background()

	_, v1 := seedVersionWithStatus(t, db, types.AgentStatusReady)
	n := mustVersionNum(t, repo, v1)

	const forks = 2
	ids := make([]string, forks)
	errs := make([]error, forks)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < forks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = repo.CreateVersionFromPrompts(ctx, v1, []byte(`{}`), types.AgentStatusDraft)
		}(i)
	}
	close(start)
	wg.Wait()

	var nums []int
	for i := 0; i < forks; i++ {
		if errs[i] != nil {
			t.Fatalf("fork %d: %v", i, errs[i])
		}
		nums = append(nums, mustVersionNum(t, repo, ids[i]))
	}
	sort.Ints(nums)
	if nums[0] != n+1 || nums[1] != n+2 {
		t.Fatalf("version nums = %v, want [%d %d]", nums, n+1, n+2)
	}
}

func TestTrainingComplete_AppendsAtTip(t *testing.T) {
	db := openTestDB(t)
	trepo := NewTrainingSessionRepository(db)
	vrepo := NewAgentVersionRepository(db)
	ctx := context.Background()

	evalID, v1 := seedEvalForTraining(t, db)
	v2, err := vrepo.CreateVersionFromPrompts(ctx, v1, []byte(`{}`), types.AgentStatusDraft)
	if err != nil {
		t.Fatal(err)
	}
	var sessionID string
	if err := db.QueryRow(`
		INSERT INTO training_sessions (source_eval_id, parent_version_id, status, started_at, epochs, patience)
		VALUES ($1::uuid, $2::uuid, 'IN_PROGRESS', now(), 5, 3) RETURNING id::text`, evalID, v1).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	prompts, _ := json.Marshal(types.Prompts{MainPrompt: "trained", SkillPrompts: map[string]string{}})
	newID, err := trepo.Complete(ctx, vrepo, sessionID, prompts, json.RawMessage(`{}`), types.CompleteSummary{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := readParent(t, db, newID); got != v2 {
		t.Fatalf("parent_version_id = %s, want tip %s", got, v2)
	}
	if n := mustVersionNum(t, vrepo, newID); n != 3 {
		t.Fatalf("version num = %d, want 3", n)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agent_versions WHERE id = $1::uuid`, newID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "READY" {
		t.Fatalf("status = %s, want READY", status)
	}
	// training_sessions.parent_version_id keeps meaning "trained from".
	var trainedFrom string
	if err := db.QueryRow(`SELECT parent_version_id::text FROM training_sessions WHERE id = $1::uuid`, sessionID).Scan(&trainedFrom); err != nil {
		t.Fatal(err)
	}
	if trainedFrom != v1 {
		t.Fatalf("training parent = %s, want %s", trainedFrom, v1)
	}
}

func TestLockAgentTip(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentVersionRepository(db)
	ctx := context.Background()

	agentID, v1 := seedAgent(t, db)
	v2, err := repo.CreateVersionFromPrompts(ctx, v1, []byte(`{}`), types.AgentStatusDraft)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	tip, err := lockAgentTip(ctx, tx, agentID)
	if err != nil || tip != v2 {
		t.Fatalf("lockAgentTip = %q, %v; want %q", tip, err, v2)
	}

	var empty string
	if err := db.QueryRow(`INSERT INTO agents (name) VALUES ('no-versions-' || gen_random_uuid()) RETURNING id::text`).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	if tip, err := lockAgentTip(ctx, db, empty); err != nil || tip != "" {
		t.Fatalf("lockAgentTip(no versions) = %q, %v; want \"\", nil", tip, err)
	}
	if _, err := lockAgentTip(ctx, db, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("lockAgentTip(missing) = %v, want ErrNotFound", err)
	}
}
