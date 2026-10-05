package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// --- buildMessageViews: sub-agent card pairing (no DB) ---

func agentUse(id, name, desc string) string {
	in, _ := json.Marshal(map[string]string{"subagent": name, "description": desc, "prompt": "do " + name})
	b, _ := json.Marshal(map[string]any{"type": "tool_use", "id": id, "name": "agent", "input": json.RawMessage(in)})
	return string(b)
}

func agentResult(id, text string, isErr bool) string {
	c, _ := json.Marshal(text)
	b, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": id, "content": json.RawMessage(c), "is_error": isErr})
	return string(b)
}

func msg(role types.ChatRole, blocks ...string) *types.ChatMessage {
	return &types.ChatMessage{ID: uuid.NewString(), Role: role, Content: []byte("[" + strings.Join(blocks, ",") + "]")}
}

func TestBuildMessageViews_SubagentCards(t *testing.T) {
	msgs := []*types.ChatMessage{
		msg(types.ChatRoleUser, `{"type":"text","text":"hi"}`),
		msg(types.ChatRoleAssistant,
			`{"type":"text","text":"delegating"}`,
			agentUse("tu_ok", "researcher", "find facts"),
			agentUse("tu_err", "ghost", "never spawned"),
			agentUse("tu_int", "slow", "cut off"),
			`{"type":"tool_use","id":"tu_http","name":"lookup","input":{}}`,
		),
		msg(types.ChatRoleTool,
			agentResult("tu_ok", "the facts", false),
			agentResult("tu_err", "max_depth_exceeded: too deep", true),
			`{"type":"tool_result","tool_use_id":"tu_http","content":{"ok":true}}`,
		),
		// A later row's result for tu_int is not "immediately following".
		msg(types.ChatRoleAssistant, `{"type":"text","text":"done"}`),
		msg(types.ChatRoleTool, agentResult("tu_int", "late", false)),
	}
	links := map[string]string{"tu_ok": "/c/ok", "tu_int": "/c/int"}
	views := buildMessageViews(msgs, "/b/", func(id string) string { return links[id] })
	if len(views) != 2 {
		t.Fatalf("bubbles = %d, want 2", len(views))
	}
	var cards []blockView
	var kinds []string
	for _, b := range views[1].Blocks {
		kinds = append(kinds, b.Kind)
		if b.Kind == "subagent" {
			cards = append(cards, b)
		}
	}
	// text, 3 cards, http tool_call, http tool_result, text, late tool_result.
	want := []string{"text", "subagent", "subagent", "subagent", "tool_call", "tool_result", "text", "tool_result"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	check := func(c blockView, name, desc, state, result, href string) {
		t.Helper()
		if c.SubagentName != name || c.SubagentDescription != desc || c.SubagentState != state ||
			c.SubagentResult != result || c.SubagentHref != href || c.SubagentPrompt != "do "+name {
			t.Errorf("card = %+v, want %s/%s/%s/%q/%q", c, name, desc, state, result, href)
		}
	}
	check(cards[0], "researcher", "find facts", "done", "the facts", "/c/ok")
	check(cards[1], "ghost", "never spawned", "error", "max_depth_exceeded: too deep", "")
	check(cards[2], "slow", "cut off", "interrupted", "", "/c/int")

	// nil linker: no hrefs.
	views = buildMessageViews(msgs, "/b/", nil)
	if views[1].Blocks[1].SubagentHref != "" {
		t.Errorf("nil linker produced href %q", views[1].Blocks[1].SubagentHref)
	}
}

func TestBuildMessageViews_EmptyArtifactBaseIsLabel(t *testing.T) {
	v := buildMessageViews([]*types.ChatMessage{
		msg(types.ChatRoleTool, `{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/ab","mime":"image/png","size_bytes":3,"filename":"a.png"}`),
	}, "", nil)
	if b := v[0].Blocks[0]; b.Kind != "artifact_ref" || b.ArtifactHref != "" || b.ArtifactLabel == "" {
		t.Fatalf("block = %+v, want label without href", b)
	}
}

// --- DB-backed: history view + child transcript route ---

type childFixture struct {
	db                       *sql.DB
	h                        *ChatHandler
	boss, helper, sub        string // version ids
	root, child, late, grand string // session ids
	otherRoot, otherChild    string
	rootPath                 string
}

func seedChildVersion(t *testing.T, db *sql.DB, name, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		WITH a AS (INSERT INTO agents (name) VALUES ($1) RETURNING id)
		INSERT INTO agent_versions (agent_id, status, flow_map_config)
		SELECT id, $2, '{}'::jsonb FROM a RETURNING id::text`, name, status).Scan(&id); err != nil {
		t.Fatalf("seed version %s: %v", name, err)
	}
	return id
}

func appendMsgs(t *testing.T, repo *repository.ChatRepository, sessionID string, msgs ...*types.ChatMessage) {
	t.Helper()
	rows := make([]types.ChatMessage, len(msgs))
	for i, m := range msgs {
		rows[i] = *m
		rows[i].SessionID = sessionID
		rows[i].Seq = i + 1
	}
	if err := repo.AppendMessages(context.Background(), "", rows); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func newChildFixture(t *testing.T) *childFixture {
	t.Helper()
	db := openTestDBForHandlers(t)
	ctx := context.Background()
	chats := repository.NewChatRepository(db)
	f := &childFixture{db: db}
	f.boss = seedChildVersion(t, db, "boss", "DRAFT")
	f.helper = seedChildVersion(t, db, "helper", "READY")
	f.sub = seedChildVersion(t, db, "deepagent", "READY")

	f.root = uuid.NewString()
	if err := chats.EnsureSession(ctx, f.root, f.boss); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.child, err = chats.CreateChildSession(ctx, f.root, f.root, "tu_a", f.helper); err != nil {
		t.Fatal(err)
	}
	if f.late, err = chats.CreateChildSession(ctx, f.root, f.root, "tu_c", f.helper); err != nil {
		t.Fatal(err)
	}
	if f.grand, err = chats.CreateChildSession(ctx, f.root, f.child, "tu_x", f.sub); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, chats, f.root,
		msg(types.ChatRoleUser, `{"type":"text","text":"hi"}`),
		msg(types.ChatRoleAssistant,
			agentUse("tu_a", "researcher", "Look up facts"),
			agentUse("tu_b", "ghost", "Never spawned"),
			agentUse("tu_c", "slowpoke", "Interrupted with child"),
			agentUse("tu_d", "nochild", "Interrupted without child"),
		),
		msg(types.ChatRoleTool,
			agentResult("tu_a", "facts found", false),
			agentResult("tu_b", "max_depth_exceeded: depth limit reached", true),
		),
	)
	appendMsgs(t, chats, f.child,
		msg(types.ChatRoleUser, `{"type":"text","text":"Look up facts"}`),
		msg(types.ChatRoleAssistant, agentUse("tu_x", "deeper", "Go deeper")),
		msg(types.ChatRoleTool, agentResult("tu_x", "deep result", false),
			`{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/ab","mime":"image/png","size_bytes":3,"filename":"chart.png"}`),
	)
	appendMsgs(t, chats, f.grand,
		msg(types.ChatRoleUser, `{"type":"text","text":"Go deeper"}`),
		msg(types.ChatRoleAssistant, `{"type":"text","text":"grandchild answer"}`),
	)

	// An unrelated tree under another root of the same version.
	f.otherRoot = uuid.NewString()
	if err := chats.EnsureSession(ctx, f.otherRoot, f.boss); err != nil {
		t.Fatal(err)
	}
	if f.otherChild, err = chats.CreateChildSession(ctx, f.otherRoot, f.otherRoot, "tu_o", f.helper); err != nil {
		t.Fatal(err)
	}

	h, err := NewChatHandler(repository.NewAgentVersionRepository(db), chats, nil, nil, &stubTurnRunner{},
		jsonl.NewFactory(), nil, nil, web.Assets, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	f.rootPath = "/agent_versions/" + f.boss + "/chat/" + f.root
	return f
}

func (f *childFixture) getChild(version, root, child string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/agent_versions/"+version+"/chat/"+root+"/children/"+child, nil)
	r.SetPathValue("version_id", version)
	r.SetPathValue("session_id", root)
	r.SetPathValue("child_id", child)
	w := httptest.NewRecorder()
	f.h.ChildTranscript(w, r)
	return w
}

// cardFor extracts one card's opening tag and head (state, name,
// description, link) by binding name.
func cardFor(t *testing.T, body, name string) string {
	t.Helper()
	for _, chunk := range strings.Split(body, `<div class="subagent-card`)[1:] {
		if strings.Contains(chunk, `<code class="subagent-name">`+name+`</code>`) {
			if i := strings.Index(chunk, "</div>"); i >= 0 {
				chunk = chunk[:i]
			}
			return `<div class="subagent-card` + chunk
		}
	}
	t.Fatalf("no card for %q in:\n%s", name, body)
	return ""
}

func TestChatView_SubagentHistoryCards_DB(t *testing.T) {
	f := newChildFixture(t)
	r := httptest.NewRequest(http.MethodGet, f.rootPath, nil)
	r.SetPathValue("version_id", f.boss)
	r.SetPathValue("session_id", f.root)
	w := httptest.NewRecorder()
	f.h.ChatView(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	c := cardFor(t, body, "researcher")
	if !strings.Contains(c, `data-state="done"`) || !strings.Contains(c, "Look up facts") ||
		!strings.Contains(c, `href="`+f.rootPath+`/children/`+f.child+`"`) {
		t.Errorf("done card: %s", c)
	}
	c = cardFor(t, body, "ghost")
	if !strings.Contains(c, `data-state="error"`) || strings.Contains(c, "href=") {
		t.Errorf("error card (max_depth_exceeded, no child): %s", c)
	}
	c = cardFor(t, body, "slowpoke")
	if !strings.Contains(c, `data-state="interrupted"`) || !strings.Contains(c, `href="`+f.rootPath+`/children/`+f.late+`"`) {
		t.Errorf("interrupted card with child: %s", c)
	}
	c = cardFor(t, body, "nochild")
	if !strings.Contains(c, `data-state="interrupted"`) || strings.Contains(c, "href=") {
		t.Errorf("interrupted card without child: %s", c)
	}
	// Paired results are inside the cards, not separate tool-result blocks.
	if strings.Contains(body, `<details class="tool-result`) || strings.Contains(body, `▸ agent(`) {
		t.Errorf("agent tool_use/tool_result rendered separately:\n%s", body)
	}
	if !strings.Contains(body, "max_depth_exceeded: depth limit reached") {
		t.Error("error card lacks its result text")
	}
}

func TestChildTranscript_DepthOneAndTwo_DB(t *testing.T) {
	f := newChildFixture(t)

	w := f.getChild(f.boss, f.root, f.child)
	if w.Code != http.StatusOK {
		t.Fatalf("child: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"helper · v1", "depth 1",
		`href="` + f.rootPath + `"`, // back to the root chat
		`<span class="artifact-link artifact-link-disabled" title="chart.png (image/png, 3 B)">`,
		"deep result",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("child transcript lacks %q", want)
		}
	}
	// Nested card links via the ROOT, not via the child.
	c := cardFor(t, body, "deeper")
	if !strings.Contains(c, `href="`+f.rootPath+`/children/`+f.grand+`"`) || !strings.Contains(c, `data-state="done"`) {
		t.Errorf("grandchild card: %s", c)
	}
	for _, banned := range []string{`<a class="artifact-link"`, `id="chat-form"`, `id="chat-input"`, "feedback-footer", "assign-dataset", "data-version-id"} {
		if strings.Contains(body, banned) {
			t.Errorf("read-only child transcript contains %q", banned)
		}
	}

	w = f.getChild(f.boss, f.root, f.grand)
	if w.Code != http.StatusOK {
		t.Fatalf("grandchild: %d %s", w.Code, w.Body.String())
	}
	body = w.Body.String()
	if !strings.Contains(body, "deepagent · v1") || !strings.Contains(body, "depth 2") || !strings.Contains(body, "grandchild answer") {
		t.Errorf("grandchild transcript:\n%s", body)
	}
}

func TestChildTranscript_404s_DB(t *testing.T) {
	f := newChildFixture(t)
	unknown := uuid.NewString()
	cases := []struct{ name, version, root, child string }{
		{"wrong version", f.helper, f.root, f.child},
		{"unknown version", unknown, f.root, f.child},
		{"root id is a child", f.boss, f.child, f.grand},
		{"child of another root", f.boss, f.root, f.otherChild},
		{"child is the root", f.boss, f.root, f.root},
		{"unknown root", f.boss, unknown, f.child},
		{"unknown child", f.boss, f.root, unknown},
		{"malformed child", f.boss, f.root, "nope"},
		{"malformed root", f.boss, "nope", f.child},
		{"malformed version", "nope", f.root, f.child},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := f.getChild(c.version, c.root, c.child); w.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
			}
		})
	}
}
