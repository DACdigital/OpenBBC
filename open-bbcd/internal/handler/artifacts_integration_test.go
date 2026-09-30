package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// These tests run the real artifact handlers and repositories against
// Postgres. They skip when DATABASE_URL is unset.

// blockingStore's Put signals putStarted, then waits for release.
type blockingStore struct {
	fakeArtifactStore
	putStarted chan struct{}
	release    chan struct{}
}

func (b *blockingStore) Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (artifacts.PutResult, error) {
	close(b.putStarted)
	<-b.release
	return b.fakeArtifactStore.Put(ctx, uri, mime, r, size)
}

func newBlockingStore() *blockingStore {
	return &blockingStore{
		fakeArtifactStore: fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL},
		putStarted:        make(chan struct{}),
		release:           make(chan struct{}),
	}
}

// seedAgentVersion creates an agent with one version and returns both ids.
// With deploy, the version is marked READY and deployed.
func seedAgentVersion(t *testing.T, db *sql.DB, deploy bool) (agentID, versionID string) {
	t.Helper()
	ctx := context.Background()
	agent, version, err := repository.NewAgentRepository(db).CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "it-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if deploy {
		if _, err := db.ExecContext(ctx, `UPDATE agent_versions SET status='READY' WHERE id=$1`, version.ID); err != nil {
			t.Fatalf("seed READY: %v", err)
		}
		if _, err := repository.NewAgentVersionRepository(db).Deploy(ctx, version.ID); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	return agent.ID, version.ID
}

func seedBOSession(t *testing.T, db *sql.DB) (versionID, sessionID string) {
	t.Helper()
	_, versionID = seedAgentVersion(t, db, false)
	sessionID = uuid.NewString()
	if err := repository.NewChatRepository(db).EnsureSession(context.Background(), sessionID, versionID); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	return versionID, sessionID
}

type uploadResult struct {
	code int
	body string
}

// runBlockedUpload serves req through h in a goroutine and returns once the
// store's Put is blocked.
func runBlockedUpload(t *testing.T, h http.HandlerFunc, req *http.Request, store *blockingStore) <-chan uploadResult {
	t.Helper()
	done := make(chan uploadResult, 1)
	go func() {
		rec := httptest.NewRecorder()
		h(rec, req)
		done <- uploadResult{rec.Code, rec.Body.String()}
	}()
	select {
	case <-store.putStarted:
	case res := <-done:
		t.Fatalf("upload finished before Put: %d %s", res.code, res.body)
	case <-time.After(5 * time.Second):
		t.Fatal("upload never reached Put")
	}
	return done
}

func awaitUpload(t *testing.T, done <-chan uploadResult) uploadResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish after release")
		return uploadResult{}
	}
}

func uploadRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	body, ct := newMultipartBody(t, "big.bin", "application/octet-stream", []byte("payload-"+uuid.NewString()))
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", ct)
	return req
}

// startBlockedBOUpload issues an upload through a real ArtifactHandler over
// Postgres and returns once its Put is blocked.
func startBlockedBOUpload(t *testing.T, db *sql.DB, versionID, sessionID string) (*blockingStore, <-chan uploadResult) {
	t.Helper()
	store := newBlockingStore()
	reg, err := loadFakeRegistryStore(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	chatRepo := repository.NewChatRepository(db)
	h := NewArtifactHandler(chatRepo, chatRepo, reg, 5, 10, testLogger())
	req := uploadRequest(t, "/")
	req.SetPathValue("version_id", versionID)
	req.SetPathValue("session_id", sessionID)
	return store, runBlockedUpload(t, h.HandleUpload, req, store)
}

func countRows(t *testing.T, db *sql.DB, table, sessionID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE session_id = $1::uuid`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestIntegration_BlockedUploadHoldsNoConnectionAndDoesNotBlockTurn(t *testing.T) {
	db := openTestDBForHandlers(t)
	vid, sid := seedBOSession(t, db)
	store, done := startBlockedBOUpload(t, db, vid, sid)

	if inUse := db.Stats().InUse; inUse != 0 {
		close(store.release)
		<-done
		t.Fatalf("blocked upload holds %d DB connections, want 0", inUse)
	}
	// A turn on the same session persists user, assistant and tool messages.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chatRepo := repository.NewChatRepository(db)
	if _, err := chatRepo.AppendUserTurn(ctx, vid, types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleUser, Seq: 1, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)}); err != nil {
		t.Fatalf("user turn while upload blocked: %v", err)
	}
	if err := chatRepo.AppendMessages(ctx, vid, []types.ChatMessage{{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleAssistant, Seq: 2, Content: json.RawMessage(`[]`)}}); err != nil {
		t.Fatalf("assistant msg while upload blocked: %v", err)
	}
	if err := chatRepo.AppendToolMessage(ctx, vid, types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleTool, Seq: 3, Content: json.RawMessage(`[]`)}, nil); err != nil {
		t.Fatalf("tool msg while upload blocked: %v", err)
	}
	close(store.release)
	if res := awaitUpload(t, done); res.code != http.StatusCreated {
		t.Fatalf("upload: %d %s", res.code, res.body)
	}
	// The upload committed after the turn's claim, so it is pending for the next turn.
	has, err := chatRepo.HasPendingArtifacts(context.Background(), sid)
	if err != nil || !has {
		t.Fatalf("upload not pending: has=%v err=%v", has, err)
	}
}

func TestIntegration_SessionLockedMidUpload_409NoRow(t *testing.T) {
	db := openTestDBForHandlers(t)
	vid, sid := seedBOSession(t, db)
	store, done := startBlockedBOUpload(t, db, vid, sid)
	if _, err := db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id = $1::uuid`, sid); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	if res := awaitUpload(t, done); res.code != http.StatusConflict {
		t.Fatalf("upload: %d %s", res.code, res.body)
	}
	if n := countRows(t, db, "chat_session_artifacts", sid); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestIntegration_SessionDeletedMidUpload_404(t *testing.T) {
	db := openTestDBForHandlers(t)
	vid, sid := seedBOSession(t, db)
	store, done := startBlockedBOUpload(t, db, vid, sid)
	if _, err := db.Exec(`DELETE FROM chat_sessions WHERE id = $1::uuid`, sid); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	if res := awaitUpload(t, done); res.code != http.StatusNotFound {
		t.Fatalf("upload: %d %s", res.code, res.body)
	}
	if n := countRows(t, db, "chat_session_artifacts", sid); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestIntegration_DeployedSessionDeletedMidUpload_404(t *testing.T) {
	db := openTestDBForHandlers(t)
	agentID, _ := seedAgentVersion(t, db, true)
	deployedRepo := repository.NewDeployedRepository(db)
	sess, err := deployedRepo.CreateSession(context.Background(), agentID, "u1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	store := newBlockingStore()
	reg, err := loadFakeRegistryStore(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	h := NewDeployedArtifactHandler(repository.NewAgentVersionRepository(db), deployedRepo, deployedRepo, reg, 5, 10, testLogger())
	mux := http.NewServeMux()
	h.Register(mux)

	req := uploadRequest(t, "/deployed/"+agentID+"/sessions/"+sess.ID+"/artifacts?user_id=u1")
	done := runBlockedUpload(t, mux.ServeHTTP, req, store)
	if _, err := db.Exec(`DELETE FROM deployed_sessions WHERE id = $1::uuid`, sess.ID); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	if res := awaitUpload(t, done); res.code != http.StatusNotFound {
		t.Fatalf("upload: %d %s", res.code, res.body)
	}
	if n := countRows(t, db, "deployed_session_artifacts", sess.ID); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestIntegration_ParallelUploadsRespectCap(t *testing.T) {
	const maxPending = 3
	db := openTestDBForHandlers(t)
	vid, sid := seedBOSession(t, db)
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)
	chatRepo := repository.NewChatRepository(db)
	h := NewArtifactHandler(chatRepo, chatRepo, reg, 5, maxPending, testLogger())

	// Build the requests up front: t.Fatalf must not run off the test goroutine.
	reqs := make([]*http.Request, maxPending+1)
	for i := range reqs {
		body, ct := newMultipartBody(t, "f.txt", "text/plain", []byte(fmt.Sprintf("distinct-%d", i)))
		req := httptest.NewRequest(http.MethodPost, "/", body)
		req.Header.Set("Content-Type", ct)
		req.SetPathValue("version_id", vid)
		req.SetPathValue("session_id", sid)
		reqs[i] = req
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, len(reqs))
	for i, req := range reqs {
		wg.Add(1)
		go func(i int, req *http.Request) {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			h.HandleUpload(rec, req)
			codes[i] = rec.Code
		}(i, req)
	}
	close(start)
	wg.Wait()
	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	n := countRows(t, db, "chat_session_artifacts", sid)
	if created != maxPending || conflict != 1 || n != maxPending {
		t.Fatalf("created=%d conflict=%d rows=%d codes=%v", created, conflict, n, codes)
	}
}

// TestIntegration_NewAPIRegistersArtifactRoutes builds the full mux with the
// registry enabled: no route-pattern conflict (NewAPI would panic), and each
// new route reaches its handler.
func TestIntegration_NewAPIRegistersArtifactRoutes(t *testing.T) {
	db := openTestDBForHandlers(t)
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	artifacts.RegisterKindForTest("test-fake", func(map[string]string, time.Duration) (artifacts.ArtifactStore, error) {
		return store, nil
	})
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096},
		Chat:      config.ChatConfig{Transport: "jsonl", MaxToolRounds: 10},
		Artifacts: fakeArtifactsConfig(),
	}
	api := NewAPI(db, cfg, testLogger())

	vid, sid := seedBOSession(t, db)
	agentID, _ := seedAgentVersion(t, db, true)
	dsess, err := repository.NewDeployedRepository(db).CreateSession(context.Background(), agentID, "u1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	bo := "/agent_versions/" + vid + "/chat/" + sid
	dep := "/deployed/" + agentID + "/sessions/" + dsess.ID
	unknown := "/deployed/" + uuid.NewString() + "/sessions/" + uuid.NewString()

	cases := []struct {
		method, target string
		want           int
	}{
		// Registered GET patterns answer; POST to the same path is the mux's 405.
		{http.MethodGet, bo + "/pending-artifacts", http.StatusOK},
		{http.MethodPost, bo + "/pending-artifacts", http.StatusMethodNotAllowed},
		{http.MethodGet, bo + "/artifacts/MAIN/sha256/" + strings.Repeat("0", 64), http.StatusNotFound},
		{http.MethodDelete, bo + "/pending-artifacts/" + uuid.NewString(), http.StatusNotFound},
		{http.MethodPost, bo + "/artifacts", http.StatusBadRequest},
		{http.MethodGet, dep + "/pending-artifacts?user_id=u1", http.StatusOK},
		{http.MethodPost, dep + "/pending-artifacts?user_id=u1", http.StatusMethodNotAllowed},
		{http.MethodGet, dep + "/pending-artifacts", http.StatusBadRequest},
		{http.MethodGet, dep + "/artifacts/MAIN/sha256/" + strings.Repeat("0", 64) + "?user_id=u1", http.StatusNotFound},
		{http.MethodDelete, dep + "/pending-artifacts/" + uuid.NewString() + "?user_id=u1", http.StatusNotFound},
		{http.MethodPost, dep + "/artifacts?user_id=u1", http.StatusBadRequest},
		// Not deployed: the deployed check's 404, not the mux's.
		{http.MethodGet, unknown + "/pending-artifacts?user_id=u1", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(c.method, c.target, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d: %s", c.method, c.target, rec.Code, c.want, rec.Body.String())
		}
		if rec.Code == http.StatusNotFound && rec.Body.String() == "404 page not found\n" {
			t.Errorf("%s %s: mux 404, route not registered", c.method, c.target)
		}
	}
}
