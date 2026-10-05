package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat/chattest"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Integration tests for multi-agent turns (spec acceptance "Root turns from
// both handlers"): the full API wiring and the real repositories, with a
// scripted LLM keyed by system prompt. They skip when DATABASE_URL is unset.

const (
	maRootSys   = "ma-root-sys"
	maWorkerSys = "ma-worker-sys"
)

// newRuntimeAPI builds the full mux (AG-UI transport, artifact registry
// disabled) over db, driven by the scripted model.
func newRuntimeAPI(t *testing.T, db *sql.DB, model llm.LLM) http.Handler {
	t.Helper()
	return newAPI(db, &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096},
		Chat: config.ChatConfig{
			Transport:            "agui",
			MaxToolRounds:        10,
			AgentToolMaxDepth:    3,
			AgentToolMaxParallel: 4,
		},
	}, testLogger(), model)
}

// makeRunnable gives the version a main prompt (its scripted-LLM route key)
// and its agent a tool-less architecture, so Turn can run it.
func makeRunnable(t *testing.T, db *sql.DB, agentID, versionID, system string) {
	t.Helper()
	prompts, _ := json.Marshal(map[string]string{"main_prompt": system})
	if _, err := db.Exec(`UPDATE agent_versions SET prompts = $2::jsonb WHERE id = $1::uuid`, versionID, string(prompts)); err != nil {
		t.Fatalf("seed prompts: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET architecture = '{"tools":[]}'::jsonb WHERE id = $1::uuid`, agentID); err != nil {
		t.Fatalf("seed architecture: %v", err)
	}
}

func setVersionStatus(t *testing.T, db *sql.DB, versionID string, status types.AgentStatus) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agent_versions SET status = $2 WHERE id = $1::uuid`, versionID, string(status)); err != nil {
		t.Fatalf("seed status: %v", err)
	}
}

// bindWorker enables the agent tool on caller and binds "researcher" to target.
func bindWorker(t *testing.T, db *sql.DB, callerID, targetID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agent_versions SET agent_tool_enabled = true WHERE id = $1::uuid`, callerID); err != nil {
		t.Fatalf("enable agent tool: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note)
		VALUES ($1::uuid, $2::uuid, 'researcher', 'finds facts')`, callerID, targetID); err != nil {
		t.Fatalf("bind: %v", err)
	}
}

// seedWorker creates a READY, runnable worker version answering on maWorkerSys.
func seedWorker(t *testing.T, db *sql.DB) string {
	t.Helper()
	agentID, versionID := seedAgentVersion(t, db, false)
	makeRunnable(t, db, agentID, versionID, maWorkerSys)
	setVersionStatus(t, db, versionID, types.AgentStatusReady)
	return versionID
}

// delegatingLLM: the root delegates once to "researcher" then ends with
// "root done"; the worker answers "W says hi".
func delegatingLLM() *chattest.RoutedLLM {
	m := chattest.NewRoutedLLM()
	m.Route(maRootSys, chattest.CallsStep("", chattest.AgentCall("researcher", "look it up", "P")), chattest.TextStep("root done"))
	m.Route(maWorkerSys, chattest.TextStep("W says hi"))
	return m
}

type storedMsg struct {
	Role   string
	Blocks []map[string]any
}

func loadStoredMsgs(t *testing.T, db *sql.DB, table, sessionID string) []storedMsg {
	t.Helper()
	rows, err := db.Query(`SELECT role, content::text FROM `+table+` WHERE session_id = $1::uuid ORDER BY seq`, sessionID)
	if err != nil {
		t.Fatalf("load %s: %v", table, err)
	}
	defer rows.Close()
	var out []storedMsg
	for rows.Next() {
		var m storedMsg
		var content string
		if err := rows.Scan(&m.Role, &content); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(content), &m.Blocks); err != nil {
			t.Fatalf("content %s: %v", content, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// rootAgentToolUseID is the raw id of the single agent tool_use in the
// session's persisted assistant messages.
func rootAgentToolUseID(t *testing.T, msgs []storedMsg) string {
	t.Helper()
	var ids []string
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Blocks {
			if b["type"] == "tool_use" && b["name"] == tools.AgentToolName {
				ids = append(ids, b["id"].(string))
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("agent tool_use ids = %v, want one", ids)
	}
	return ids[0]
}

// assertWorkerTranscript: child messages are user [P], assistant [text].
func assertWorkerTranscript(t *testing.T, msgs []storedMsg) {
	t.Helper()
	if len(msgs) != 2 ||
		msgs[0].Role != "user" || len(msgs[0].Blocks) != 1 || msgs[0].Blocks[0]["type"] != "text" || msgs[0].Blocks[0]["text"] != "P" ||
		msgs[1].Role != "assistant" || len(msgs[1].Blocks) != 1 || msgs[1].Blocks[0]["type"] != "text" || msgs[1].Blocks[0]["text"] != "W says hi" {
		t.Fatalf("child transcript = %+v", msgs)
	}
}

// assertRootToolResult: the root's tool message answers toolUseID with the
// worker text as a JSON string.
func assertRootToolResult(t *testing.T, msgs []storedMsg, toolUseID string) {
	t.Helper()
	var tool []storedMsg
	for _, m := range msgs {
		if m.Role == "tool" {
			tool = append(tool, m)
		}
	}
	if len(tool) != 1 || len(tool[0].Blocks) != 1 {
		t.Fatalf("root tool messages = %+v", tool)
	}
	b := tool[0].Blocks[0]
	if b["type"] != "tool_result" || b["tool_use_id"] != toolUseID || b["is_error"] != false || b["content"] != "W says hi" {
		t.Fatalf("root tool_result = %+v", b)
	}
}

func assertStream(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{`"STEP_STARTED"`, `"STEP_FINISHED"`, "root done", `"RUN_FINISHED"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "RUN_ERROR") {
		t.Fatalf("stream has RUN_ERROR:\n%s", body)
	}
}

func TestMultiAgent_BOTurnDelegatesToWorker(t *testing.T) {
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	worker := seedWorker(t, db)
	rootAgent, root := seedAgentVersion(t, db, false)
	makeRunnable(t, db, rootAgent, root, maRootSys)
	setVersionStatus(t, db, root, types.AgentStatusDraft)
	bindWorker(t, db, root, worker)

	session := uuid.NewString()
	chatRepo := repository.NewChatRepository(db)
	if err := chatRepo.EnsureSession(ctx, session, root); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := chatRepo.SetSessionHeaderOverrides(ctx, session, map[string]map[string]string{"b1": {"X-K": "v"}}); err != nil {
		t.Fatalf("SetSessionHeaderOverrides: %v", err)
	}

	model := delegatingLLM()
	api := newRuntimeAPI(t, db, model)
	rec := serve(api, http.MethodPost, "/agent_versions/"+root+"/chat/"+session+"/turn", "application/json", `{"input":[{"type":"text","text":"go"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d: %s", rec.Code, rec.Body.String())
	}
	assertStream(t, rec.Body.String())

	rootMsgs := loadStoredMsgs(t, db, "chat_messages", session)
	toolUseID := rootAgentToolUseID(t, rootMsgs)

	var (
		childID, childVersion, parentID, parentToolCall, overrides string
		depth                                                      int
	)
	if err := db.QueryRow(`
		SELECT id::text, agent_version_id::text, depth, parent_session_id::text, parent_tool_call_id, backend_header_overrides::text
		FROM chat_sessions WHERE parent_session_id IS NOT NULL`).
		Scan(&childID, &childVersion, &depth, &parentID, &parentToolCall, &overrides); err != nil {
		t.Fatalf("child row: %v", err)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_sessions WHERE parent_session_id IS NOT NULL`); n != 1 {
		t.Fatalf("child sessions = %d, want 1", n)
	}
	if depth != 1 || parentID != session || parentToolCall != toolUseID || childVersion != worker {
		t.Fatalf("child = depth %d parent %s tool %s version %s; want 1 %s %s %s", depth, parentID, parentToolCall, childVersion, session, toolUseID, worker)
	}
	if overrides != `{"b1": {"X-K": "v"}}` {
		t.Fatalf("child overrides = %s, want root's", overrides)
	}

	assertWorkerTranscript(t, loadStoredMsgs(t, db, "chat_messages", childID))
	assertRootToolResult(t, rootMsgs, toolUseID)

	// The worker saw only its prompt.
	wReqs := model.Requests(maWorkerSys)
	if len(wReqs) != 1 || len(wReqs[0].Messages) != 1 || chattest.FirstUserText(wReqs[0]) != "P" {
		t.Fatalf("worker requests = %+v", wReqs)
	}
}

func TestMultiAgent_DeployedTurnDelegatesToWorker(t *testing.T) {
	db := openTestDBForHandlers(t)
	worker := seedWorker(t, db)
	agentID, root := seedAgentVersion(t, db, true)
	makeRunnable(t, db, agentID, root, maRootSys)
	bindWorker(t, db, root, worker)

	model := delegatingLLM()
	api := newRuntimeAPI(t, db, model)
	base := "/deployed/" + agentID + "/sessions"

	rec := serve(api, http.MethodPost, base, "application/json", `{"user_id":"u1","title":"t"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session = %d: %s", rec.Code, rec.Body.String())
	}
	var sess types.DeployedSession
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}

	rec = serve(api, http.MethodPost, base+"/"+sess.ID+"/turn", "application/json", `{"user_id":"u1","input":[{"type":"text","text":"go"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d: %s", rec.Code, rec.Body.String())
	}
	assertStream(t, rec.Body.String())

	rootMsgs := loadStoredMsgs(t, db, "deployed_messages", sess.ID)
	toolUseID := rootAgentToolUseID(t, rootMsgs)

	var (
		childID, childAgent, childUser, childVersion, parentID, parentToolCall string
		depth                                                                  int
	)
	if err := db.QueryRow(`
		SELECT id::text, agent_id::text, user_id, agent_version_id::text, depth, parent_session_id::text, parent_tool_call_id
		FROM deployed_sessions WHERE parent_session_id IS NOT NULL`).
		Scan(&childID, &childAgent, &childUser, &childVersion, &depth, &parentID, &parentToolCall); err != nil {
		t.Fatalf("child row: %v", err)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM deployed_sessions WHERE parent_session_id IS NOT NULL`); n != 1 {
		t.Fatalf("child sessions = %d, want 1", n)
	}
	if childAgent != agentID || childUser != "u1" || childVersion != worker || depth != 1 || parentID != sess.ID || parentToolCall != toolUseID {
		t.Fatalf("child = agent %s user %s version %s depth %d parent %s tool %s", childAgent, childUser, childVersion, depth, parentID, parentToolCall)
	}
	assertWorkerTranscript(t, loadStoredMsgs(t, db, "deployed_messages", childID))
	assertRootToolResult(t, rootMsgs, toolUseID)

	// The list shows only the root; the child is not addressable.
	rec = serve(api, http.MethodGet, base+"?user_id=u1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var list []types.DeployedSession
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != sess.ID {
		t.Fatalf("list = %+v, want only the root", list)
	}
	assertHandler404(t, "GET child", serve(api, http.MethodGet, base+"/"+childID+"?user_id=u1", "", ""))
}

// toolResultSink records the TOOL_CALL_RESULT events of a turn.
type toolResultSink struct {
	mu      sync.Mutex
	results []transport.ToolResultEvent
}

func (s *toolResultSink) Send(_ context.Context, ev transport.Event) error {
	if r, ok := ev.(transport.ToolResultEvent); ok {
		s.mu.Lock()
		s.results = append(s.results, r)
		s.mu.Unlock()
	}
	return nil
}

func (s *toolResultSink) Close() error { return nil }

// The handler refuses a turn on a locked root up front (409), so the lock
// is applied mid-turn: after the root's user message, before its agent call
// spawns a child.
func TestMultiAgent_LockedRootRefusesSpawn(t *testing.T) {
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	worker := seedWorker(t, db)
	rootAgent, root := seedAgentVersion(t, db, false)
	makeRunnable(t, db, rootAgent, root, maRootSys)
	setVersionStatus(t, db, root, types.AgentStatusDraft)
	bindWorker(t, db, root, worker)
	session := uuid.NewString()

	model := chattest.NewRoutedLLM()
	agentStep := chattest.CallsStep("", chattest.AgentCall("researcher", "look it up", "P"))
	model.Route(maRootSys, func(req llm.Request) []llm.Event {
		if _, err := db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, session); err != nil {
			t.Errorf("lock root: %v", err)
		}
		return agentStep(req)
	}, chattest.TextStep("root done"))
	model.Route(maWorkerSys, chattest.TextStep("never"))

	versionRepo := repository.NewAgentVersionRepository(db)
	chatRepo := repository.NewChatRepository(db)
	builder := tools.NewBuilder(&toolBackendStoreAdapter{
		backend:     repository.NewToolBackendRepository(db),
		wiring:      repository.NewVersionWiringRepository(db),
		agentWiring: repository.NewAgentWiringRepository(db),
	})
	o := chat.NewOrchestrator(versionRepo, chatRepo, model, builder, testLogger())

	sink := &toolResultSink{}
	// Only the user-turn claim re-checks the lock; the turn's later writes
	// do not, so the root turn itself completes.
	if stop, err := o.Turn(ctx, root, session, []llm.Block{llm.TextBlock{Text: "go"}}, sink, chat.TurnOpts{}); err != nil || stop != "end_turn" {
		t.Fatalf("Turn = %q, %v", stop, err)
	}

	if len(sink.results) != 1 || !sink.results[0].IsError {
		t.Fatalf("tool results = %+v", sink.results)
	}
	var msg string
	if err := json.Unmarshal(sink.results[0].Result, &msg); err != nil || !strings.HasPrefix(msg, "session_locked: ") {
		t.Fatalf("agent result = %s, want session_locked: …", sink.results[0].Result)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_sessions WHERE parent_session_id IS NOT NULL`); n != 0 {
		t.Fatalf("child sessions = %d, want 0", n)
	}
	if len(model.Requests(maWorkerSys)) != 0 {
		t.Fatal("worker turn ran")
	}
}
