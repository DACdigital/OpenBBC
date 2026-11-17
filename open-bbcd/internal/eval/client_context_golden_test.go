package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

var update = flag.Bool("update", false, "rewrite the aikdm client_context golden fixtures")

// goldenDir is aikdm's fixture directory, relative to this package.
const goldenDir = "../../../aikdm/tests/fixtures"

// Two contexts: canonicalization edge cases (sorting, number literals,
// <>&, lone surrogate, U+2028, duplicate keys), and characters YAML treats
// specially but Go's JSON encoder leaves raw (U+0085, U+FEFF, a long
// foldable string). All escapes are JSON escapes, so the source stays ASCII.
var goldenContexts = []string{
	`{"z":{"b":1,"a":[1.0,1e3,-0]},"big":123456789012345678901234567890,"html":"<b>&</b>","sur":"\ud800","ls":"a\u2028b","dup":1,"dup":2}`,
	`{"nel":"x\u0085y","bom":"\ufeffhead","long":"` + strings.Repeat("word ", 40) + `end"}`,
}

func goldenUserContent(t *testing.T, rawCtx, text string) json.RawMessage {
	t.Helper()
	var blocks []any
	if rawCtx != "" {
		canonical, err := chat.CanonicalizeClientContext(json.RawMessage(rawCtx))
		if err != nil {
			t.Fatalf("canonicalize %s: %v", rawCtx, err)
		}
		// Same keys as chat.blocksToJSON writes.
		blocks = append(blocks, map[string]any{
			"type": "client_context", "canonical": canonical, "context": json.RawMessage(canonical),
		})
	}
	blocks = append(blocks, map[string]any{"type": "text", "text": text})
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// aikdmUserTurn is aikdm's render_user_turn contract: every client_context
// rendered string first, in order, then the text blocks, joined by "\n\n".
func aikdmUserTurn(t *testing.T, content any) string {
	t.Helper()
	var ctxs, texts []string
	for _, b := range content.([]any) {
		m := b.(map[string]any)
		switch m["type"] {
		case "client_context":
			r := m["rendered"].(string)
			if r != chat.RenderClientContext(m["canonical"].(string)) {
				t.Fatalf("rendered %q is not RenderClientContext(canonical)", r)
			}
			ctxs = append(ctxs, r)
		case "text":
			if s := m["text"].(string); s != "" {
				texts = append(texts, s)
			}
		}
	}
	return strings.Join(append(ctxs, texts...), "\n\n")
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(goldenDir, name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/eval -run TestClientContextGoldenExport -update)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with: go test ./internal/eval -run TestClientContextGoldenExport -update\n--- got ---\n%s", path, got)
	}
}

func TestClientContextGoldenExport(t *testing.T) {
	fs := &fakeStore{
		eval:   &types.Eval{ID: "e-golden", AgentVersionID: "av-golden", DatasetVersionID: "dv-golden", MockMCPTools: true},
		bundle: []byte(`{"main_prompt":"sys","tools":[]}`),
		refs:   []*types.DatasetSessionRef{{SessionID: "s-golden", SessionTitle: "client context golden"}},
		msgs: map[string][]*types.ChatMessage{"s-golden": {
			{ID: "u1", Role: types.ChatRoleUser, Content: goldenUserContent(t, goldenContexts[0], "first")},
			{ID: "a1", Role: types.ChatRoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"one"}]`)},
			{ID: "u2", Role: types.ChatRoleUser, Content: goldenUserContent(t, "", "second")},
			{ID: "a2", Role: types.ChatRoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"two"}]`)},
			{ID: "u3", Role: types.ChatRoleUser, Content: goldenUserContent(t, goldenContexts[1], "third")},
			{ID: "a3", Role: types.ChatRoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"three"}]`)},
		}},
		feedback: map[string]map[string]*types.ChatMessageFeedback{"s-golden": {
			"a3": {MessageID: "a3", Rating: types.FeedbackRatingUp, JudgeCriteria: []string{"uses the context"}},
		}},
	}
	payload, err := Build(context.Background(), fs, "e-golden")
	if err != nil {
		t.Fatal(err)
	}

	var y bytes.Buffer
	if err := WriteYAML(&y, payload); err != nil {
		t.Fatal(err)
	}

	expected := map[string]string{}
	for _, m := range payload.DatasetVersion.Sessions[0].Transcript {
		if m.Role == string(types.ChatRoleUser) {
			expected[m.MessageID] = aikdmUserTurn(t, m.Content)
		}
	}
	var j bytes.Buffer
	enc := json.NewEncoder(&j)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(expected); err != nil {
		t.Fatal(err)
	}

	checkGolden(t, "client_context_export.yaml", y.Bytes())
	checkGolden(t, "client_context_expected.json", j.Bytes())
}
