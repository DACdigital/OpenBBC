package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestCanonicalizeClientContext(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"keys sorted at every depth", `{"b":1,"a":{"d":2,"c":[{"z":1,"y":2}]}}`, `{"a":{"c":[{"y":2,"z":1}],"d":2},"b":1}`},
		{"no insignificant whitespace", "{ \"a\" : [ 1 , 2 ] }\n", `{"a":[1,2]}`},
		{"leading whitespace", " \t\r\n{\"a\":1}", `{"a":1}`},
		{"no HTML escaping", `{"h":"<b>&amp;</b>"}`, `{"h":"<b>&amp;</b>"}`},
		{"number literals preserved", `{"a":1.0,"b":1e3,"c":123456789012345678901234567890,"d":-0}`, `{"a":1.0,"b":1e3,"c":123456789012345678901234567890,"d":-0}`},
		{"U+2028 escaped", "{\"s\":\"a b\"}", `{"s":"a\u2028b"}`},
		{"duplicate keys: last wins (top)", `{"a":1,"a":2}`, `{"a":2}`},
		{"duplicate keys: last wins (nested)", `{"o":{"k":"x","k":"y"}}`, `{"o":{"k":"y"}}`},
		{"duplicate object keys: whole last value", `{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`},
		{"lone surrogate becomes U+FFFD", `{"s":"\ud800"}`, "{\"s\":\"�\"}"},
		{"invalid UTF-8 becomes U+FFFD", "{\"s\":\"\xff\"}", "{\"s\":\"�\"}"},
		{"empty object", `{}`, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalizeClientContext(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
			// Idempotent: canonicalizing the canonical form changes nothing.
			again, err := CanonicalizeClientContext(json.RawMessage(got))
			if err != nil || again != got {
				t.Fatalf("not idempotent: %q, %v", again, err)
			}
		})
	}
}

func TestCanonicalizeClientContext_NotObject(t *testing.T) {
	for _, in := range []string{`[]`, `[1]`, `"s"`, `42`, `true`, `null`, ``} {
		_, err := CanonicalizeClientContext(json.RawMessage(in))
		if !errors.Is(err, types.ErrClientContextInvalid) || err.Error() != "invalid client_context: must be a JSON object" {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestCanonicalizeClientContext_NUL(t *testing.T) {
	for _, in := range []string{`{"a\u0000":1}`, `{"a":"x\u0000"}`, `{"a":["\u0000"]}`, `{"a":{"b\u0000":true}}`} {
		_, err := CanonicalizeClientContext(json.RawMessage(in))
		if !errors.Is(err, types.ErrClientContextInvalid) || err.Error() != `invalid client_context: contains \u0000` {
			t.Errorf("%s: err = %v", in, err)
		}
	}
}

func TestRenderClientContext(t *testing.T) {
	if got := RenderClientContext(`{"a":"<x>"}`); got != `<client_context>{"a":"<x>"}</client_context>` {
		t.Fatalf("got %q", got)
	}
}

func TestCodec_ClientContextRoundTrip(t *testing.T) {
	in := []llm.Block{
		llm.ClientContextBlock{Canonical: `{"a":1e3,"h":"<x>"}`},
		llm.TextBlock{Text: "hi"},
		llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "image/png", SizeBytes: 7, Sha256: "abc"},
	}
	raw, err := blocksToJSON(in)
	if err != nil {
		t.Fatalf("blocksToJSON: %v", err)
	}
	if got := blockTypes(t, raw); !reflect.DeepEqual(got, []string{"client_context", "text", "artifact_ref"}) {
		t.Fatalf("types = %v", got)
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		t.Fatal(err)
	}
	if got := parseBlocks(rawBlocks); !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip:\n got %#v\nwant %#v", got, in)
	}
}

func TestCodec_ClientContextJSONShape(t *testing.T) {
	canonical := `{"a":1e3,"h":"<x>"}`
	raw, err := blocksToJSON([]llm.Block{llm.ClientContextBlock{Canonical: canonical}})
	if err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Type      string         `json:"type"`
		Canonical string         `json:"canonical"`
		Context   map[string]any `json:"context"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("%v (%s)", err, raw)
	}
	b := blocks[0]
	if b.Type != "client_context" || b.Canonical != canonical {
		t.Fatalf("block = %+v (%s)", b, raw)
	}
	if b.Context["h"] != "<x>" || b.Context["a"] != float64(1000) {
		t.Fatalf("context = %#v", b.Context)
	}
}

func TestCodec_ClientContextWithoutCanonicalIsSkipped(t *testing.T) {
	got := parseBlocks([]json.RawMessage{
		json.RawMessage(`{"type":"client_context","context":{"a":1}}`),
		json.RawMessage(`{"type":"text","text":"hi"}`),
	})
	if !reflect.DeepEqual(got, []llm.Block{llm.TextBlock{Text: "hi"}}) {
		t.Fatalf("got %#v", got)
	}
}

// ccLLMCases: the plain fake takes renderArtifactsForLLM's early return
// (artifacts.go:92); the multimodal fake with a resolver takes the budgeted
// loop. The block must be rendered on both.
func ccLLMCases(script [][]llm.Event) []struct {
	name     string
	llm      llm.LLM
	requests func() []llm.Request
	resolver ArtifactFetcherResolver
} {
	plain := &fakeLLM{script: script}
	mm := &mmFakeLLM{fakeLLM: &fakeLLM{script: script}}
	return []struct {
		name     string
		llm      llm.LLM
		requests func() []llm.Request
		resolver ArtifactFetcherResolver
	}{
		{"plain", plain, func() []llm.Request { return plain.requests }, nil},
		{"multimodal", mm, func() []llm.Request { return mm.requests },
			func(string) llm.ArtifactFetcher { return stubFetcher{bytes: []byte{0x89}} }},
	}
}

func assertNoClientContextBlock(t *testing.T, reqs []llm.Request) {
	t.Helper()
	for i, r := range reqs {
		for _, m := range r.Messages {
			for _, b := range m.Content {
				if _, ok := b.(llm.ClientContextBlock); ok {
					t.Fatalf("LLM request %d carries a ClientContextBlock", i)
				}
			}
		}
	}
}

func textOf(t *testing.T, b llm.Block) string {
	t.Helper()
	tb, ok := b.(llm.TextBlock)
	if !ok {
		t.Fatalf("block %#v is not a TextBlock", b)
	}
	return tb.Text
}

func TestRenderArtifacts_ClientContextBecomesText(t *testing.T) {
	cc := llm.ClientContextBlock{Canonical: `{"a":1}`}
	in := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{cc, llm.TextBlock{Text: "hi"}}}}
	resolver := func(string) llm.ArtifactFetcher { return stubFetcher{} }
	for _, c := range []struct {
		name     string
		provider llm.LLM
		resolver ArtifactFetcherResolver
	}{{"plain", plainLLM{}, nil}, {"multimodal", mmLLM{}, resolver}} {
		t.Run(c.name, func(t *testing.T) {
			out, err := renderArtifactsForLLM(context.Background(), in, c.provider, c.resolver, newRenderCache(), slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			if got := textOf(t, out[0].Content[0]); got != RenderClientContext(cc.Canonical) {
				t.Fatalf("rendered = %q", got)
			}
			if in[0].Content[0] != llm.Block(cc) {
				t.Fatal("render pass mutated req.Messages; it must keep the block form")
			}
		})
	}
}

func TestOrchestrator_ClientContextOnFirstRound(t *testing.T) {
	for _, c := range ccLLMCases([][]llm.Event{endRound()}) {
		t.Run(c.name, func(t *testing.T) {
			o, chats, _ := newArtifactOrchestrator(t, c.llm, nil)
			if c.resolver != nil {
				o.WithArtifacts(c.resolver)
			}
			cc := llm.ClientContextBlock{Canonical: `{"a":"<x>"}`}
			// Caller order is text-then-context; the block must still come first.
			if _, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}, cc}, &recordingSink{}, TurnOpts{}); err != nil {
				t.Fatal(err)
			}
			user := messagesWithRole(chats, types.ChatRoleUser)[0]
			if got := blockTypes(t, user.Content); len(got) != 2 || got[0] != "client_context" || got[1] != "text" {
				t.Fatalf("persisted user types = %v", got)
			}
			reqs := c.requests()
			last := reqs[0].Messages[len(reqs[0].Messages)-1]
			if last.Role != llm.RoleUser || len(last.Content) != 2 {
				t.Fatalf("LLM user message = %+v", last)
			}
			if got := textOf(t, last.Content[0]); got != `<client_context>{"a":"<x>"}</client_context>` {
				t.Fatalf("first part = %q", got)
			}
			if got := textOf(t, last.Content[1]); got != "hi" {
				t.Fatalf("second part = %q", got)
			}
			assertNoClientContextBlock(t, reqs)
		})
	}
}

func TestOrchestrator_ClientContextReplayOrder(t *testing.T) {
	for _, c := range ccLLMCases([][]llm.Event{endRound(), endRound(), endRound()}) {
		t.Run(c.name, func(t *testing.T) {
			o, _, _ := newArtifactOrchestrator(t, c.llm, nil)
			if c.resolver != nil {
				o.WithArtifacts(c.resolver)
			}
			ctx := context.Background()
			cc1 := llm.ClientContextBlock{Canonical: `{"turn":1}`}
			cc3 := llm.ClientContextBlock{Canonical: `{"turn":3}`}
			for i, in := range [][]llm.Block{
				{cc1, llm.TextBlock{Text: "one"}},
				{llm.TextBlock{Text: "two"}},
				{cc3, llm.TextBlock{Text: "three"}},
			} {
				if _, err := o.Turn(ctx, "v1", "s1", in, &recordingSink{}, TurnOpts{}); err != nil {
					t.Fatalf("turn %d: %v", i+1, err)
				}
			}
			reqs := c.requests()
			assertNoClientContextBlock(t, reqs)
			turn3 := reqs[2]
			var users []llm.Message
			total := 0
			for _, m := range turn3.Messages {
				for _, b := range m.Content {
					if tb, ok := b.(llm.TextBlock); ok && strings.HasPrefix(tb.Text, "<client_context>") {
						total++
					}
				}
				if m.Role == llm.RoleUser {
					users = append(users, m)
				}
			}
			if total != 2 || len(users) != 3 {
				t.Fatalf("contexts = %d (want 2), user messages = %d (want 3)", total, len(users))
			}
			if got := textOf(t, users[0].Content[0]); got != RenderClientContext(cc1.Canonical) {
				t.Fatalf("turn 1 first part = %q", got)
			}
			if len(users[1].Content) != 1 || textOf(t, users[1].Content[0]) != "two" {
				t.Fatalf("turn 2 user message = %+v (must carry no context)", users[1])
			}
			if got := textOf(t, users[2].Content[0]); got != RenderClientContext(cc3.Canonical) {
				t.Fatalf("turn 3 first part = %q", got)
			}
		})
	}
}

func TestOrchestrator_ClientContextOnlyIsEmptyTurn(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	_, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.ClientContextBlock{Canonical: `{"a":1}`}}, &recordingSink{}, TurnOpts{})
	if !errors.Is(err, types.ErrEmptyTurn) {
		t.Fatalf("err = %v, want ErrEmptyTurn", err)
	}
	if n := len(messagesWithRole(chats, types.ChatRoleUser)); n != 0 || flm.calls != 0 {
		t.Fatalf("persisted %d user messages, %d LLM calls", n, flm.calls)
	}
}
