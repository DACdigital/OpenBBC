package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost/bifrosttest"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Integration tests for the bifrost adapter (spec acceptance "End to end"):
// real API wiring and repositories, the real Bifrost client, and a fake
// OpenAI endpoint. They skip when DATABASE_URL is unset.

func newBifrostAPI(t *testing.T, db *sql.DB, fake *bifrosttest.Fake, key string) http.Handler {
	t.Helper()
	return newBifrostAPIWithStore(t, db, fake, key, nil)
}

// newBifrostAPIWithStore is newBifrostAPI with the artifact registry enabled
// over store (one "MAIN" store of kind "test-fake"); a nil store leaves the
// registry disabled.
func newBifrostAPIWithStore(t *testing.T, db *sql.DB, fake *bifrosttest.Fake, key string, store artifacts.ArtifactStore) http.Handler {
	t.Helper()
	var artCfg config.ArtifactsConfig
	if store != nil {
		artifacts.RegisterKindForTest("test-fake", func(map[string]string, time.Duration) (artifacts.ArtifactStore, error) {
			return store, nil
		})
		artCfg = fakeArtifactsConfig()
	}
	llmCfg := config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "gpt-test", APIKey: key, BaseURL: fake.URL()}
	client, err := bifrost.New(context.Background(), llmCfg, testLogger())
	if err != nil {
		t.Fatalf("bifrost.New: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return newAPI(db, &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{MaxTokens: 4096},
		LLM:       llmCfg,
		Artifacts: artCfg,
		Chat: config.ChatConfig{
			Transport:            "agui",
			MaxToolRounds:        10,
			AgentToolMaxDepth:    3,
			AgentToolMaxParallel: 4,
		},
	}, testLogger(), client)
}

const agentArgs = `{"subagent":"researcher","description":"look it up","prompt":"P"}`

// routeDelegation scripts the root to call the worker once then answer
// "root done", and the worker to answer "W says hi".
func routeDelegation(fake *bifrosttest.Fake) {
	fake.Route(maRootSys, bifrosttest.ToolCall("call_root", "agent", agentArgs), bifrosttest.Text("root done"))
	fake.Route(maWorkerSys, bifrosttest.Text("W says hi"))
}

// assertWireRoundTrip: the root's second call replays the agent tool_use as
// an assistant tool_call and answers it with a tool message; the worker saw
// only its system prompt and "P".
func assertWireRoundTrip(t *testing.T, fake *bifrosttest.Fake) {
	t.Helper()
	root := fake.Requests(maRootSys)
	if len(root) != 2 {
		t.Fatalf("root calls = %d, want 2", len(root))
	}
	msgs := root[1].Body["messages"].([]any)
	var sawCall, sawResult bool
	for _, m := range msgs {
		mm := m.(map[string]any)
		if calls, ok := mm["tool_calls"].([]any); ok && mm["role"] == "assistant" {
			c := calls[0].(map[string]any)
			sawCall = c["id"] == "call_root" && c["function"].(map[string]any)["arguments"] == agentArgs
		}
		if mm["role"] == "tool" {
			sawResult = mm["tool_call_id"] == "call_root" && mm["content"] == "W says hi"
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("root replay messages = %v", msgs)
	}
	worker := fake.Requests(maWorkerSys)
	if len(worker) != 1 {
		t.Fatalf("worker calls = %d", len(worker))
	}
	wm := worker[0].Body["messages"].([]any)
	if len(wm) != 2 || wm[1].(map[string]any)["role"] != "user" {
		t.Fatalf("worker messages = %v", wm)
	}
	if tools, _ := root[0].Body["tools"].([]any); len(tools) == 0 {
		t.Fatal("root request carried no tools (agent tool missing)")
	}
}

func TestBifrost_BOTurnDelegatesToWorker(t *testing.T) {
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	worker := seedWorker(t, db)
	rootAgent, root := seedAgentVersion(t, db, false)
	makeRunnable(t, db, rootAgent, root, maRootSys)
	setVersionStatus(t, db, root, types.AgentStatusDraft)
	bindWorker(t, db, root, worker)

	session := uuid.NewString()
	if err := repository.NewChatRepository(db).EnsureSession(ctx, session, root); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	fake := bifrosttest.New(t)
	routeDelegation(fake)
	api := newBifrostAPI(t, db, fake, "sk-test")
	rec := serve(api, http.MethodPost, "/agent_versions/"+root+"/chat/"+session+"/turn", "application/json", `{"input":[{"type":"text","text":"go"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d: %s", rec.Code, rec.Body.String())
	}
	assertStream(t, rec.Body.String())

	rootMsgs := loadStoredMsgs(t, db, "chat_messages", session)
	toolUseID := rootAgentToolUseID(t, rootMsgs)
	if toolUseID != "call_root" {
		t.Fatalf("tool_use id = %q, want provider id call_root", toolUseID)
	}
	var childID string
	if err := db.QueryRow(`SELECT id::text FROM chat_sessions WHERE parent_session_id = $1::uuid`, session).Scan(&childID); err != nil {
		t.Fatalf("child row: %v", err)
	}
	assertWorkerTranscript(t, loadStoredMsgs(t, db, "chat_messages", childID))
	assertRootToolResult(t, rootMsgs, toolUseID)
	assertWireRoundTrip(t, fake)
}

func TestBifrost_DeployedTurnDelegatesToWorker(t *testing.T) {
	db := openTestDBForHandlers(t)
	worker := seedWorker(t, db)
	agentID, root := seedAgentVersion(t, db, true)
	makeRunnable(t, db, agentID, root, maRootSys)
	bindWorker(t, db, root, worker)

	fake := bifrosttest.New(t)
	routeDelegation(fake)
	api := newBifrostAPI(t, db, fake, "sk-test")
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
	body := rec.Body.String()
	assertStream(t, body)
	for _, want := range []string{`"TOOL_CALL_START"`, `"TOOL_CALL_END"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s:\n%s", want, body)
		}
	}

	rootMsgs := loadStoredMsgs(t, db, "deployed_messages", sess.ID)
	toolUseID := rootAgentToolUseID(t, rootMsgs)
	var childID string
	if err := db.QueryRow(`SELECT id::text FROM deployed_sessions WHERE parent_session_id = $1::uuid`, sess.ID).Scan(&childID); err != nil {
		t.Fatalf("child row: %v", err)
	}
	assertWorkerTranscript(t, loadStoredMsgs(t, db, "deployed_messages", childID))
	assertRootToolResult(t, rootMsgs, toolUseID)
	assertWireRoundTrip(t, fake)
}

// A token-capped round cut inside a tool call's arguments ends the turn as
// max_tokens: the truncated call is dropped, nothing is dispatched, and the
// provider is called exactly once.
func TestBifrost_LengthMidToolCallEndsTurn(t *testing.T) {
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	const sys = "bf-trunc-sys"
	agentID, version := seedAgentVersion(t, db, false)
	makeRunnable(t, db, agentID, version, sys)
	setVersionStatus(t, db, version, types.AgentStatusDraft)
	session := uuid.NewString()
	if err := repository.NewChatRepository(db).EnsureSession(ctx, session, version); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	fake := bifrosttest.New(t)
	fake.Route(sys, bifrosttest.SSE(
		bifrosttest.Delta(map[string]any{"role": "assistant", "content": "partial"}, nil),
		bifrosttest.Delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_x", "type": "function", "function": map[string]any{"name": "lookup", "arguments": ""}}}}, nil),
		bifrosttest.Delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `{"q":"ab`}}}}, nil),
		bifrosttest.Delta(map[string]any{}, "length"),
	))
	api := newBifrostAPI(t, db, fake, "sk-test")
	rec := serve(api, http.MethodPost, "/agent_versions/"+version+"/chat/"+session+"/turn", "application/json", `{"input":[{"type":"text","text":"go"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "RUN_ERROR") {
		t.Fatalf("stream has RUN_ERROR:\n%s", rec.Body.String())
	}
	if n := fake.Total(); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
	msgs := loadStoredMsgs(t, db, "chat_messages", session)
	var assistant []storedMsg
	for _, m := range msgs {
		if m.Role == "tool" {
			t.Fatalf("tool message persisted: %+v", m)
		}
		if m.Role == "assistant" {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 1 || len(assistant[0].Blocks) != 1 || assistant[0].Blocks[0]["type"] != "text" || assistant[0].Blocks[0]["text"] != "partial" {
		t.Fatalf("assistant messages = %+v", assistant)
	}
}

func TestBifrost_MissingKeyFailsTurn(t *testing.T) {
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	const sys = "bf-nokey-sys"
	agentID, version := seedAgentVersion(t, db, false)
	makeRunnable(t, db, agentID, version, sys)
	setVersionStatus(t, db, version, types.AgentStatusDraft)
	session := uuid.NewString()
	if err := repository.NewChatRepository(db).EnsureSession(ctx, session, version); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	fake := bifrosttest.New(t)
	api := newBifrostAPI(t, db, fake, "")
	rec := serve(api, http.MethodPost, "/agent_versions/"+version+"/chat/"+session+"/turn", "application/json", `{"input":[{"type":"text","text":"go"}]}`)
	body := rec.Body.String()
	if !strings.Contains(body, "RUN_ERROR") || !strings.Contains(body, "bifrost: OPENAI_API_KEY not configured") {
		t.Fatalf("turn = %d, body:\n%s", rec.Code, body)
	}
	if fake.Total() != 0 {
		t.Fatalf("provider calls = %d, want 0", fake.Total())
	}
}

// A BO turn with an attached artifact: a PNG reaches the provider as a native
// image_url data URI, a PDF as the "[Attachment: …]" text surrogate.
func TestBifrost_BOTurnRendersAttachments(t *testing.T) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n")

	cases := []struct {
		name, filename, mime string
		data                 []byte
		check                func(t *testing.T, parts []map[string]any)
	}{
		{"png", "pic.png", "image/png", pngBuf.Bytes(), func(t *testing.T, parts []map[string]any) {
			for _, p := range parts {
				if p["type"] != "image_url" {
					continue
				}
				url, _ := p["image_url"].(map[string]any)["url"].(string)
				if strings.HasPrefix(url, "data:image/png;base64,") {
					return
				}
			}
			t.Fatalf("no image_url data:image/png part in %v", parts)
		}},
		{"pdf", "doc.pdf", "application/pdf", pdf, func(t *testing.T, parts []map[string]any) {
			for _, p := range parts {
				if p["type"] == "image_url" {
					t.Fatalf("PDF sent as image_url: %v", parts)
				}
			}
			for _, p := range parts {
				if txt, _ := p["text"].(string); p["type"] == "text" && strings.Contains(txt, "[Attachment:") {
					return
				}
			}
			t.Fatalf("no [Attachment: …] text part in %v", parts)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openTestDBForHandlers(t)
			sys := "bf-attach-" + c.name
			agentID, version := seedAgentVersion(t, db, false)
			makeRunnable(t, db, agentID, version, sys)
			setVersionStatus(t, db, version, types.AgentStatusDraft)
			session := uuid.NewString()
			if err := repository.NewChatRepository(db).EnsureSession(context.Background(), session, version); err != nil {
				t.Fatalf("EnsureSession: %v", err)
			}

			store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliveryBytes, getData: c.data}
			fake := bifrosttest.New(t)
			fake.Route(sys, bifrosttest.Text("seen"))
			api := newBifrostAPIWithStore(t, db, fake, "sk-test", store)

			base := "/agent_versions/" + version + "/chat/" + session
			body, ct := newMultipartBody(t, c.filename, c.mime, c.data)
			req := httptest.NewRequest(http.MethodPost, base+"/artifacts", body)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
			}

			rec = serve(api, http.MethodPost, base+"/turn", "application/json", `{"input":[{"type":"text","text":"look"}]}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("turn = %d: %s", rec.Code, rec.Body.String())
			}
			if out := rec.Body.String(); !strings.Contains(out, "RUN_FINISHED") || strings.Contains(out, "RUN_ERROR") {
				t.Fatalf("turn stream:\n%s", out)
			}

			reqs := fake.Requests(sys)
			if len(reqs) != 1 {
				t.Fatalf("provider calls = %d, want 1", len(reqs))
			}
			var parts []map[string]any
			for _, m := range reqs[0].Body["messages"].([]any) {
				mm := m.(map[string]any)
				if mm["role"] != "user" {
					continue
				}
				content, ok := mm["content"].([]any)
				if !ok {
					continue
				}
				for _, p := range content {
					parts = append(parts, p.(map[string]any))
				}
			}
			c.check(t, parts)
		})
	}
}
