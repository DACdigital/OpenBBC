package bifrost

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost/bifrosttest"
)

func newTestLLM(t *testing.T, baseURL, key string) *LLM {
	t.Helper()
	l, err := New(context.Background(), config.LLMConfig{Adapter: "bifrost", Provider: "openai", Model: "gpt-test", APIKey: key, BaseURL: baseURL}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(l.Shutdown)
	return l
}

func collect(t *testing.T, l *LLM, req llm.Request) ([]llm.Event, error) {
	t.Helper()
	var evs []llm.Event
	for ev, err := range l.Generate(context.Background(), req) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func userReq(system, text string) llm.Request {
	return llm.Request{System: system, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: text}}}}}
}

func TestLLM_Name(t *testing.T) {
	if got := newTestLLM(t, "http://127.0.0.1:1", "k").Name(); got != "bifrost:openai" {
		t.Fatalf("Name() = %q", got)
	}
}

func TestGenerate_TextStream(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.Text("hello"))
	evs, err := collect(t, newTestLLM(t, fake.URL(), "sk-test"), userReq("sys", "hi"))
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{llm.TextDeltaEvent{Delta: "hello"}, llm.MessageStopEvent{StopReason: "end_turn"}, llm.UsageEvent{InputTokens: 5, OutputTokens: 3}}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
	reqs := fake.Requests("sys")
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Bearer sk-test" || reqs[0].Body["model"] != "gpt-test" {
		t.Fatalf("request = %+v", reqs)
	}
}

func TestGenerate_ToolCall(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.ToolCall("call_1", "lookup", `{"q":"x"}`))
	evs, err := collect(t, newTestLLM(t, fake.URL(), "sk"), userReq("sys", "hi"))
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ToolUseStartEvent{ID: "call_1", Name: "lookup"},
		llm.ToolUseInputEvent{ID: "call_1", JSONFragment: `{"q":"x"}`},
		llm.ToolUseEndEvent{ID: "call_1"},
		llm.MessageStopEvent{StopReason: "tool_use"},
		llm.UsageEvent{InputTokens: 5, OutputTokens: 3},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestGenerate_MissingKeyFailsWithoutRequest(t *testing.T) {
	fake := bifrosttest.New(t)
	_, err := collect(t, newTestLLM(t, fake.URL(), ""), userReq("sys", "hi"))
	if err == nil || err.Error() != "bifrost: OPENAI_API_KEY not configured" {
		t.Fatalf("err = %v", err)
	}
	if fake.Total() != 0 {
		t.Fatalf("requests = %d, want 0", fake.Total())
	}
}

func TestGenerate_HTTPError(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.RouteError("sys", http.StatusUnauthorized, "bad key")
	_, err := collect(t, newTestLLM(t, fake.URL(), "sk"), userReq("sys", "hi"))
	if err == nil || !strings.HasPrefix(err.Error(), "bifrost: openai: bad key") || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerate_StreamWithoutFinishIsError(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.SSE(bifrosttest.Delta(map[string]any{"content": "par"}, nil)))
	evs, err := collect(t, newTestLLM(t, fake.URL(), "sk"), userReq("sys", "hi"))
	if err == nil || err.Error() != "bifrost: openai: stream ended without finish_reason" {
		t.Fatalf("err = %v", err)
	}
	if len(evs) != 1 || evs[0] != (llm.TextDeltaEvent{Delta: "par"}) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestGenerate_CancelReturnsCtxErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	l := newTestLLM(t, srv.URL, "sk")
	// Snapshot after Init so Bifrost's long-lived worker pool counts as
	// "current"; anything Generate starts (the drain goroutine, the
	// provider's stream reader) must be gone once the call has returned.
	// fasthttp's connection-pool cleaners are started lazily on the first
	// request and live as long as the client, so they are ignored too.
	// goleak retries until its deadline, so the drain gets time to finish.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.(*Client).mCleaner"),
		goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.(*HostClient).connsCleaner"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotErr error
	start := time.Now()
	for ev, err := range l.Generate(ctx, userReq("sys", "hi")) {
		if err != nil {
			gotErr = err
			break
		}
		if _, ok := ev.(llm.TextDeltaEvent); ok {
			cancel()
		}
	}
	if gotErr != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", gotErr)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Generate did not return promptly after cancel")
	}
}

func TestGenerate_CancelledBeforeStreamReturnsCtxErr(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.Text("hello"))
	l := newTestLLM(t, fake.URL(), "sk")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var errs []error
	for _, err := range l.Generate(ctx, userReq("sys", "hi")) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 1 || errs[0] != context.Canceled {
		t.Fatalf("errs = %v, want [context.Canceled]", errs)
	}
}
