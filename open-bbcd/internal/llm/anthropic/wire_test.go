package anthropic

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

var update = flag.Bool("update", false, "rewrite golden files")

// recorded is one request the fake Messages endpoint received.
type recorded struct {
	Header http.Header
	Body   map[string]any
}

// fakeMessages is an httptest server answering /v1/messages with a short
// text stream, recording every request.
type fakeMessages struct {
	mu   sync.Mutex
	reqs []recorded
	srv  *httptest.Server
}

const textStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func newFakeMessages(t *testing.T) *fakeMessages {
	t.Helper()
	f := &fakeMessages{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{Header: r.Header.Clone(), Body: body})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, textStream)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMessages) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// testClientOptions points the SDK at the fake and disables retries.
func (f *fakeMessages) clientOptions() []option.RequestOption {
	return []option.RequestOption{option.WithBaseURL(f.srv.URL), option.WithMaxRetries(0)}
}

func run(t *testing.T, l *LLM, req llm.Request) []llm.Event {
	t.Helper()
	var evs []llm.Event
	for ev, err := range l.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func wireRequest() llm.Request {
	return llm.Request{
		Model:     "claude-test",
		System:    "sys",
		MaxTokens: 256,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}},
		Tools:     []llm.ToolDef{{Name: "lookup", Description: "Look up", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
	}
}

// volatileHeaders vary by machine or run and are left out of the golden.
var volatileHeaders = map[string]bool{
	"Host": true, "X-Stainless-Os": true, "X-Stainless-Arch": true,
	"X-Stainless-Runtime-Version": true, "Accept-Encoding": true,
}

func stableHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if !volatileHeaders[k] {
			out[k] = v
		}
	}
	return out
}

func compareGolden(t *testing.T, path string, got any) {
	t.Helper()
	js, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	js = append(js, '\n')
	if *update {
		if err := os.WriteFile(path, js, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(want) != string(js) {
		t.Fatalf("request differs from %s:\n got: %s\nwant: %s", path, js, want)
	}
}

func headerNames(h map[string][]string) []string {
	var names []string
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// testdata/unchanged_request.json was recorded before llm-request-extras
// existed: with no matching rule the adapter must still send exactly this.
func TestWire_NoRuleRequestUnchanged(t *testing.T) {
	f := newFakeMessages(t)
	c := sdk.NewClient(append([]option.RequestOption{option.WithAPIKey("sk-ant-test")}, f.clientOptions()...)...)
	l := &LLM{cfg: config.AnthropicConfig{APIKey: "sk-ant-test"}, client: &c}
	run(t, l, wireRequest())
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	compareGolden(t, "testdata/unchanged_request.json", map[string]any{"headers": stableHeaders(reqs[0].Header), "body": reqs[0].Body})
}
