// Package bifrosttest is a wire-level fake of an OpenAI-compatible
// chat-completions endpoint, for driving the bifrost adapter (provider
// "openai", base URL = Fake.URL()) in tests without a live provider.
package bifrosttest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is one received chat-completions call.
type Request struct {
	Header http.Header
	Body   map[string]any
}

type reply struct {
	status int
	body   string
}

// Fake answers each request with the next scripted reply for the request's
// system prompt (messages[0] when its role is "system"; "" otherwise) —
// the wire-level twin of chattest.RoutedLLM. Unscripted requests get 500.
type Fake struct {
	mu       sync.Mutex
	replies  map[string][]reply
	requests map[string][]Request
	srv      *httptest.Server
}

func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{replies: map[string][]reply{}, requests: map[string][]Request{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base URL to configure as the provider's BaseURL.
func (f *Fake) URL() string { return f.srv.URL }

// Route queues SSE bodies (see SSE, Text, ToolCall) for a system prompt.
func (f *Fake) Route(system string, sseBodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range sseBodies {
		f.replies[system] = append(f.replies[system], reply{status: http.StatusOK, body: b})
	}
}

// RouteError queues a non-streaming JSON error reply.
func (f *Fake) RouteError(system string, status int, message string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[system] = append(f.replies[system], reply{status: status, body: string(body)})
}

// Requests returns the calls received for a system prompt.
func (f *Fake) Requests(system string) []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests[system]...)
}

// Total is the number of calls received for any system prompt.
func (f *Fake) Total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		n += len(r)
	}
	return n
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	system := ""
	if msgs, _ := body["messages"].([]any); len(msgs) > 0 {
		if m0, _ := msgs[0].(map[string]any); m0["role"] == "system" {
			system, _ = m0["content"].(string)
		}
	}
	f.mu.Lock()
	f.requests[system] = append(f.requests[system], Request{Header: r.Header.Clone(), Body: body})
	queue := f.replies[system]
	if len(queue) == 0 {
		f.mu.Unlock()
		http.Error(w, "bifrosttest: no reply scripted for system "+system, http.StatusInternalServerError)
		return
	}
	next := queue[0]
	f.replies[system] = queue[1:]
	f.mu.Unlock()

	if next.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		_, _ = io.WriteString(w, next.body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, next.body)
}

// Chunk is one chat.completion.chunk payload (id/object/model are filled in).
type Chunk map[string]any

// Delta is a chunk with one choice carrying delta and finish (nil or a reason).
func Delta(delta map[string]any, finish any) Chunk {
	return Chunk{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
}

// Usage is the trailing usage-only chunk.
func Usage(prompt, completion int) Chunk {
	return Chunk{"choices": []any{}, "usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}}
}

// SSE renders chunks as an SSE body terminated by [DONE].
func SSE(chunks ...Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		c["id"], c["object"], c["created"], c["model"] = "chatcmpl-test", "chat.completion.chunk", 1, "gpt-test"
		j, _ := json.Marshal(c)
		b.WriteString("data: ")
		b.Write(j)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// Text is a complete reply: one content delta, finish "stop", usage.
func Text(text string) string {
	return SSE(Delta(map[string]any{"role": "assistant", "content": text}, nil), Delta(map[string]any{}, "stop"), Usage(5, 3))
}

// ToolCall is a complete reply calling one tool: id+name delta, one
// arguments delta, finish "tool_calls", usage.
func ToolCall(id, name, args string) string {
	return SSE(
		Delta(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}}}, nil),
		Delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args}}}}, nil),
		Delta(map[string]any{}, "tool_calls"),
		Usage(5, 3),
	)
}
