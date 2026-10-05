package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// Integration tests for the multiagent-feature root-only rule and the
// temporary eval/training gate (spec § REST — root-only rule and child
// transcripts, § REST — eval and training gate). Children are raw-inserted:
// nothing creates them at runtime yet. They skip when DATABASE_URL is unset.

// newMultiAgentAPI builds the full mux (artifact registry enabled, so the
// artifact routes are registered) over db.
func newMultiAgentAPI(t *testing.T, db *sql.DB) http.Handler {
	t.Helper()
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	artifacts.RegisterKindForTest("test-fake", func(map[string]string, time.Duration) (artifacts.ArtifactStore, error) {
		return store, nil
	})
	return NewAPI(db, &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096},
		Chat:      config.ChatConfig{Transport: "jsonl", MaxToolRounds: 10},
		Artifacts: fakeArtifactsConfig(),
	}, testLogger())
}

// rawChatChild inserts a BO child session under parentID, pinned to versionID.
func rawChatChild(t *testing.T, db *sql.DB, parentID, versionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO chat_sessions (agent_version_id, parent_session_id, parent_tool_call_id, depth, backend_header_overrides)
		SELECT $2::uuid, p.id, 'toolu_' || substr(md5(random()::text), 1, 8), p.depth + 1, '{"b1":{"X-K":"v"}}'::jsonb
		FROM chat_sessions p WHERE p.id = $1::uuid
		RETURNING id::text`, parentID, versionID).Scan(&id); err != nil {
		t.Fatalf("rawChatChild: %v", err)
	}
	return id
}

// rawDeployedChild inserts a deployed child under parentID (copying the
// parent's agent_id and user_id), pinned to versionID.
func rawDeployedChild(t *testing.T, db *sql.DB, parentID, versionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO deployed_sessions (agent_id, user_id, title, parent_session_id, parent_tool_call_id, depth, agent_version_id)
		SELECT p.agent_id, p.user_id, 'child-title', p.id, 'toolu_' || substr(md5(random()::text), 1, 8), p.depth + 1, $2::uuid
		FROM deployed_sessions p WHERE p.id = $1::uuid
		RETURNING id::text`, parentID, versionID).Scan(&id); err != nil {
		t.Fatalf("rawDeployedChild: %v", err)
	}
	return id
}

func rawChatMessage(t *testing.T, db *sql.DB, sessionID string, seq int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'assistant', '[{"type":"text","text":"hi"}]'::jsonb, $2)
		RETURNING id::text`, sessionID, seq).Scan(&id); err != nil {
		t.Fatalf("rawChatMessage: %v", err)
	}
	return id
}

func rawDeployedMessage(t *testing.T, db *sql.DB, sessionID, versionID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO deployed_messages (session_id, agent_version_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, 'assistant', '[]'::jsonb, 1)`, sessionID, versionID); err != nil {
		t.Fatalf("rawDeployedMessage: %v", err)
	}
}

func countWhere(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func serve(api http.Handler, method, target, contentType, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec
}

const formCT = "application/x-www-form-urlencoded"

// assertHandler404 wants a 404 written by the handler, not the mux (route not registered)
// fallback.
func assertHandler404(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s = %d, want 404: %s", what, rec.Code, rec.Body.String())
		return
	}
	if rec.Body.String() == "404 page not found\n" {
		t.Errorf("%s: mux 404, route not registered", what)
	}
}

func TestMultiAgent_BORootOnlyRoutes(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newMultiAgentAPI(t, db)

	vid, root := seedBOSession(t, db)
	_, targetVID := seedAgentVersion(t, db, false)
	child := rawChatChild(t, db, root, targetVID)
	rootMsg := rawChatMessage(t, db, root, 1)
	childMsg := rawChatMessage(t, db, child, 1)
	_, otherRoot := seedBOSession(t, db)
	otherMsg := rawChatMessage(t, db, otherRoot, 1)

	var datasetID string
	if err := db.QueryRow(`INSERT INTO datasets (name) VALUES ('ma-ds') RETURNING id::text`).Scan(&datasetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_versions (dataset_id, status, version_num) VALUES ($1::uuid, 'DRAFT', 1)`, datasetID); err != nil {
		t.Fatal(err)
	}
	// The child carries feedback, so only the root-only rule can refuse the assign.
	if _, err := db.Exec(`INSERT INTO chat_message_feedback (message_id, rating) VALUES ($1::uuid, 'up')`, childMsg); err != nil {
		t.Fatal(err)
	}

	bo := "/agent_versions/" + vid + "/chat/"
	sessionsBefore := countWhere(t, db, `SELECT count(*) FROM chat_sessions`)
	messagesBefore := countWhere(t, db, `SELECT count(*) FROM chat_messages`)

	cases := []struct{ method, target, ct, body string }{
		{http.MethodGet, bo + child, "", ""},
		{http.MethodPatch, bo + child + "/title", "application/json", `{"title":"hijack"}`},
		{http.MethodPost, bo + child + "/turn", "application/json", `{"input":[{"type":"text","text":"hi"}]}`},
		{http.MethodGet, bo + child + "/headers", "", ""},
		{http.MethodPost, bo + child + "/headers", formCT, "header_backend[]=b1&header_key[]=X-K&header_val[]=evil"},
		{http.MethodGet, bo + child + "/assign-dataset", "", ""},
		{http.MethodPost, bo + child + "/assign-dataset", formCT, "dataset_id=" + datasetID},
		{http.MethodDelete, bo + child + "/assign-dataset", "", ""},
		// Feedback for the child's message: under the child's path and under the root's.
		{http.MethodGet, bo + child + "/messages/" + childMsg + "/feedback", "", ""},
		{http.MethodPost, bo + child + "/messages/" + childMsg + "/feedback", formCT, "rating=down&comment=x"},
		{http.MethodDelete, bo + child + "/messages/" + childMsg + "/feedback", "", ""},
		{http.MethodGet, bo + root + "/messages/" + childMsg + "/feedback", "", ""},
		{http.MethodPost, bo + root + "/messages/" + childMsg + "/feedback", formCT, "rating=down&comment=x"},
		{http.MethodDelete, bo + root + "/messages/" + childMsg + "/feedback", "", ""},
		// Another root's message under this root's path.
		{http.MethodPost, bo + root + "/messages/" + otherMsg + "/feedback", formCT, "rating=up"},
		// Artifact routes (registry enabled).
		{http.MethodGet, bo + child + "/pending-artifacts", "", ""},
		{http.MethodDelete, bo + child + "/pending-artifacts/" + uuid.NewString(), "", ""},
		{http.MethodGet, bo + child + "/artifacts/MAIN/sha256/" + strings.Repeat("0", 64), "", ""},
	}
	for _, c := range cases {
		assertHandler404(t, c.method+" "+c.target, serve(api, c.method, c.target, c.ct, c.body))
	}
	// Upload needs a multipart body.
	up := uploadRequest(t, bo+child+"/artifacts")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, up)
	assertHandler404(t, "POST child artifacts", rec)

	// No side effects.
	if n := countWhere(t, db, `SELECT count(*) FROM chat_sessions`); n != sessionsBefore {
		t.Errorf("chat_sessions = %d, want %d", n, sessionsBefore)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_messages`); n != messagesBefore {
		t.Errorf("chat_messages = %d, want %d", n, messagesBefore)
	}
	var title sql.NullString
	var ovr string
	if err := db.QueryRow(`SELECT title, backend_header_overrides::text FROM chat_sessions WHERE id = $1::uuid`, child).Scan(&title, &ovr); err != nil {
		t.Fatal(err)
	}
	if title.Valid || ovr != `{"b1": {"X-K": "v"}}` {
		t.Errorf("child changed: title=%v overrides=%s", title, ovr)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM dataset_version_sessions`); n != 0 {
		t.Errorf("dataset_version_sessions = %d, want 0", n)
	}
	var rating, comment string
	if err := db.QueryRow(`SELECT rating, comment FROM chat_message_feedback WHERE message_id = $1::uuid`, childMsg).Scan(&rating, &comment); err != nil || rating != "up" || comment != "" {
		t.Errorf("child feedback changed: %s %q %v", rating, comment, err)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_message_feedback WHERE message_id = $1::uuid`, otherMsg); n != 0 {
		t.Errorf("other root's feedback rows = %d, want 0", n)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_session_artifacts WHERE session_id = $1::uuid`, child); n != 0 {
		t.Errorf("child artifact rows = %d, want 0", n)
	}

	// The same routes still work for the root.
	if rec := serve(api, http.MethodPost, bo+root+"/messages/"+rootMsg+"/feedback", formCT, "rating=up&judge_criteria_json=%5B%22ok%22%5D"); rec.Code != http.StatusOK {
		t.Errorf("root feedback POST = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := serve(api, http.MethodGet, bo+root+"/messages/"+rootMsg+"/feedback", "", ""); rec.Code != http.StatusOK {
		t.Errorf("root feedback GET = %d", rec.Code)
	}
	if rec := serve(api, http.MethodGet, bo+root, "", ""); rec.Code != http.StatusOK {
		t.Errorf("root chat view = %d", rec.Code)
	}
	if rec := serve(api, http.MethodGet, bo+root+"/pending-artifacts", "", ""); rec.Code != http.StatusOK {
		t.Errorf("root pending-artifacts = %d", rec.Code)
	}
	if rec := serve(api, http.MethodGet, bo+root+"/headers", "", ""); rec.Code != http.StatusOK {
		t.Errorf("root headers = %d", rec.Code)
	}

	// Session lists show only roots — also for the version the child is pinned to.
	list := serve(api, http.MethodGet, "/agent_versions/"+vid+"/chat", "", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), root) || strings.Contains(list.Body.String(), child) {
		t.Errorf("caller list: code %d, has root=%v has child=%v", list.Code, strings.Contains(list.Body.String(), root), strings.Contains(list.Body.String(), child))
	}
	list = serve(api, http.MethodGet, "/agent_versions/"+targetVID+"/chat", "", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), child) {
		t.Errorf("target list: code %d, has child=%v", list.Code, strings.Contains(list.Body.String(), child))
	}
}

// TestMultiAgent_BOChildTurnRunsNothing drives Turn through a real
// ChatRepository with a stub orchestrator: a child id must neither run the
// orchestrator nor create any session or message.
func TestMultiAgent_BOChildTurnRunsNothing(t *testing.T) {
	db := openTestDBForHandlers(t)
	vid, root := seedBOSession(t, db)
	child := rawChatChild(t, db, root, vid)
	chatRepo := repository.NewChatRepository(db)
	runner := &stubTurnRunner{}
	h, err := NewChatHandler(repository.NewAgentVersionRepository(db), chatRepo, chatRepo, nil, runner,
		jsonl.NewFactory(), repository.NewFeedbackRepository(db), repository.NewDatasetRepository(db), web.Assets, testLogger())
	if err != nil {
		t.Fatalf("NewChatHandler: %v", err)
	}
	sessions := countWhere(t, db, `SELECT count(*) FROM chat_sessions`)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"input":[{"type":"text","text":"hi"}]}`))
	req.SetPathValue("version_id", vid)
	req.SetPathValue("session_id", child)
	rec := httptest.NewRecorder()
	h.Turn(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("orchestrator calls = %d, want 0", runner.calls)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_sessions`); n != sessions {
		t.Fatalf("chat_sessions = %d, want %d", n, sessions)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM chat_messages`); n != 0 {
		t.Fatalf("chat_messages = %d, want 0", n)
	}
}

func TestMultiAgent_DeployedRootOnlyRoutes(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newMultiAgentAPI(t, db)
	ctx := context.Background()

	agentID, depVID := seedAgentVersion(t, db, true)
	otherAgentID, _ := seedAgentVersion(t, db, true)
	deployed := repository.NewDeployedRepository(db)
	root, err := deployed.CreateSession(ctx, agentID, "u1", "root-title")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	child := rawDeployedChild(t, db, root.ID, depVID)
	grandchild := rawDeployedChild(t, db, child, depVID)
	rawDeployedMessage(t, db, root.ID, depVID)
	rawDeployedMessage(t, db, child, depVID)
	rawDeployedMessage(t, db, grandchild, depVID)

	dep := "/deployed/" + agentID + "/sessions/"
	q := "?user_id=u1"
	cases := []struct{ method, target, ct, body string }{
		{http.MethodGet, dep + child + q, "", ""},
		{http.MethodPatch, dep + child + "/title" + q, "application/json", `{"user_id":"u1","title":"hijack"}`},
		{http.MethodDelete, dep + child + q, "", ""},
		{http.MethodPost, dep + child + "/turn" + q, "application/json", `{"user_id":"u1","input":[{"type":"text","text":"hi"}]}`},
		{http.MethodGet, dep + child + "/pending-artifacts" + q, "", ""},
		{http.MethodDelete, dep + child + "/pending-artifacts/" + uuid.NewString() + q, "", ""},
		{http.MethodGet, dep + child + "/artifacts/MAIN/sha256/" + strings.Repeat("0", 64) + q, "", ""},
		// Another agent's id with a valid root.
		{http.MethodDelete, "/deployed/" + otherAgentID + "/sessions/" + root.ID + q, "", ""},
		{http.MethodPatch, "/deployed/" + otherAgentID + "/sessions/" + root.ID + "/title" + q, "application/json", `{"user_id":"u1","title":"hijack"}`},
	}
	for _, c := range cases {
		assertHandler404(t, c.method+" "+c.target, serve(api, c.method, c.target, c.ct, c.body))
	}
	up := uploadRequest(t, dep+child+"/artifacts"+q)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, up)
	assertHandler404(t, "POST child artifacts", rec)

	var childTitle, rootTitle string
	if err := db.QueryRow(`SELECT title FROM deployed_sessions WHERE id = $1::uuid`, child).Scan(&childTitle); err != nil {
		t.Fatalf("child gone: %v", err)
	}
	if err := db.QueryRow(`SELECT title FROM deployed_sessions WHERE id = $1::uuid`, root.ID).Scan(&rootTitle); err != nil {
		t.Fatalf("root gone: %v", err)
	}
	if childTitle != "child-title" || rootTitle != "root-title" {
		t.Errorf("titles changed: child=%q root=%q", childTitle, rootTitle)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM deployed_messages`); n != 3 {
		t.Errorf("deployed_messages = %d, want 3", n)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM deployed_session_artifacts WHERE session_id = $1::uuid`, child); n != 0 {
		t.Errorf("child artifact rows = %d, want 0", n)
	}

	// The list returns only the root.
	list := serve(api, http.MethodGet, dep[:len(dep)-1]+q, "", "")
	var sessions []types.DeployedSession
	if err := json.Unmarshal(list.Body.Bytes(), &sessions); err != nil || list.Code != http.StatusOK {
		t.Fatalf("list: %d %s (%v)", list.Code, list.Body.String(), err)
	}
	if len(sessions) != 1 || sessions[0].ID != root.ID {
		t.Errorf("list = %+v, want only the root", sessions)
	}
	if rec := serve(api, http.MethodGet, dep+root.ID+q, "", ""); rec.Code != http.StatusOK {
		t.Errorf("root GET = %d", rec.Code)
	}

	// Deleting the root removes every descendant and their messages.
	if rec := serve(api, http.MethodDelete, dep+root.ID+q, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("root DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if n := countWhere(t, db, `SELECT count(*) FROM deployed_sessions`); n != 0 {
		t.Errorf("deployed_sessions = %d, want 0", n)
	}
	if n := countWhere(t, db, `SELECT count(*) FROM deployed_messages`); n != 0 {
		t.Errorf("deployed_messages = %d, want 0", n)
	}
}

// seedGatedVersion returns a READY version with agent_tool_enabled set
// (raw: bypasses the config-write invariants) and a CLOSED dataset version.
func seedGatedVersion(t *testing.T, db *sql.DB) (versionID, dvID string) {
	t.Helper()
	_, versionID = seedAgentVersion(t, db, false)
	if _, err := db.Exec(`UPDATE agent_versions SET status = 'READY', agent_tool_enabled = true WHERE id = $1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	var datasetID string
	if err := db.QueryRow(`INSERT INTO datasets (name) VALUES ($1) RETURNING id::text`, "gate-"+uuid.NewString()[:8]).Scan(&datasetID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID); err != nil {
		t.Fatal(err)
	}
	return versionID, dvID
}

func rawGateEval(t *testing.T, db *sql.DB, versionID, dvID, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO evals (agent_version_id, dataset_version_id, status, score)
		VALUES ($1::uuid, $2::uuid, $3, CASE WHEN $3 = 'DONE' THEN 0.5 END)
		RETURNING id::text`, versionID, dvID, status).Scan(&id); err != nil {
		t.Fatalf("rawGateEval: %v", err)
	}
	return id
}

func assertSentinel409(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Errorf("%s = %d, want 409: %s", what, rec.Code, rec.Body.String())
		return
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != types.ErrMultiAgentEvalUnsupported.Error() {
		t.Errorf("%s body = %s (%v), want sentinel JSON", what, rec.Body.String(), err)
	}
}

func assertGateFailed(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var status, msg string
	if err := db.QueryRow(`SELECT status, COALESCE(error_message, '') FROM `+table+` WHERE id = $1::uuid`, id).Scan(&status, &msg); err != nil {
		t.Fatalf("%s %s: %v", table, id, err)
	}
	if status != "FAILED" || msg != types.ErrMultiAgentEvalUnsupported.Error() {
		t.Errorf("%s %s = %s %q, want FAILED with sentinel text", table, id, status, msg)
	}
}

func TestMultiAgent_EvalGate(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newMultiAgentAPI(t, db)
	v, dv := seedGatedVersion(t, db)

	// Create is refused for JSON and form requests, with no row.
	assertSentinel409(t, "create JSON", serve(api, http.MethodPost, "/agent_versions/"+v+"/evals", "application/json", `{"dataset_version_id":"`+dv+`"}`))
	assertSentinel409(t, "create form", serve(api, http.MethodPost, "/agent_versions/"+v+"/evals", formCT, url.Values{"dataset_version_id": {dv}}.Encode()))
	if n := countWhere(t, db, `SELECT count(*) FROM evals`); n != 0 {
		t.Fatalf("evals = %d, want 0", n)
	}

	// Drainer flow on a raw PENDING eval: export fails it (and its PENDING
	// training) forward, and the next PENDING poll no longer lists it.
	pending := rawGateEval(t, db, v, dv, "PENDING")
	var ts string
	if err := db.QueryRow(`INSERT INTO training_sessions (source_eval_id, parent_version_id) VALUES ($1::uuid, $2::uuid) RETURNING id::text`, pending, v).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	assertSentinel409(t, "export", serve(api, http.MethodGet, "/evals/"+pending+"/export.yaml", "", ""))
	assertGateFailed(t, db, "evals", pending)
	assertGateFailed(t, db, "training_sessions", ts)
	poll := serve(api, http.MethodGet, "/evals.json?status=PENDING", "", "")
	if poll.Code != http.StatusOK || strings.Contains(poll.Body.String(), pending) {
		t.Errorf("PENDING poll = %d, lists failed eval=%v", poll.Code, strings.Contains(poll.Body.String(), pending))
	}
	assertSentinel409(t, "start after export", serve(api, http.MethodPost, "/evals/"+pending+"/start", "", ""))

	// Start alone also fails a PENDING eval forward.
	pending2 := rawGateEval(t, db, v, dv, "PENDING")
	assertSentinel409(t, "start", serve(api, http.MethodPost, "/evals/"+pending2+"/start", "", ""))
	assertGateFailed(t, db, "evals", pending2)
}

func TestMultiAgent_TrainingGate(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newMultiAgentAPI(t, db)
	v, dv := seedGatedVersion(t, db)
	done := rawGateEval(t, db, v, dv, "DONE")

	assertSentinel409(t, "training create", serve(api, http.MethodPost, "/training-sessions", formCT, url.Values{"source_eval_id": {done}}.Encode()))
	if n := countWhere(t, db, `SELECT count(*) FROM training_sessions`); n != 0 {
		t.Fatalf("training_sessions = %d, want 0", n)
	}

	var ts string
	if err := db.QueryRow(`INSERT INTO training_sessions (source_eval_id, parent_version_id) VALUES ($1::uuid, $2::uuid) RETURNING id::text`, done, v).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	assertSentinel409(t, "training start", serve(api, http.MethodPost, "/training-sessions/"+ts+"/start", "application/json", `{"epochs":1,"patience":1}`))
	assertGateFailed(t, db, "training_sessions", ts)
}
