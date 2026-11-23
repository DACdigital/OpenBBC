package bifrost

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost/bifrosttest"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llmextras"
)

var update = flag.Bool("update", false, "rewrite golden files")

func wireRequest() llm.Request {
	return llm.Request{
		System:      "sys",
		MaxTokens:   256,
		Temperature: 0.5,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}},
		Tools:       []llm.ToolDef{{Name: "lookup", Description: "Look up", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
	}
}

// stableHeaders drops headers that vary by run (the fake's random port).
func stableHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if k != "Host" {
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

// testdata/unchanged_request.json was recorded before llm-request-extras
// existed: with no matching rule the adapter must still send exactly this.
func TestWire_NoRuleRequestUnchanged(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.Text("hello"))
	if _, err := collect(t, newTestLLM(t, fake.URL(), "sk-test"), wireRequest()); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests("sys")
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	compareGolden(t, "testdata/unchanged_request.json", map[string]any{"headers": stableHeaders(reqs[0].Header), "body": reqs[0].Body})
}

const glmRules = `[{"match":"*/glm-*","headers":{"x-bf-passthrough-extra-params":"true"},"body":{"chat_template_kwargs":{"reasoning_effort":"low"}}}]`

// extrasLLM builds the adapter as boot does: rules parsed from raw and
// resolved once against "<provider>/<model>".
func extrasLLM(t *testing.T, baseURL, raw, model string) *LLM {
	t.Helper()
	rules, err := llmextras.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	provider, bare, _ := strings.Cut(model, "/")
	cfg := config.LLMConfig{Adapter: "bifrost", Provider: provider, Model: bare, APIKey: "sk-test", BaseURL: baseURL,
		Extras: llmextras.Resolve(rules, model)}
	l, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Shutdown)
	return l
}

func record(t *testing.T, l *LLM, fake *bifrosttest.Fake) bifrosttest.Request {
	t.Helper()
	fake.Route("sys", bifrosttest.Text("hello"))
	if _, err := collect(t, l, wireRequest()); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests("sys")
	if len(reqs) == 0 {
		t.Fatal("no request recorded")
	}
	return reqs[len(reqs)-1]
}

// Acceptance 2: the GLM rule's header and body field reach the wire; the
// body field only does when the passthrough flag is set, so this also proves
// the flag.
func TestWire_GLMRuleAddsHeaderAndBody(t *testing.T) {
	fake := bifrosttest.New(t)
	r := record(t, extrasLLM(t, fake.URL(), glmRules, "openai/vllm/glm-5.3-flash"), fake)
	if got := r.Header.Get("x-bf-passthrough-extra-params"); got != "true" {
		t.Fatalf("header = %q; headers = %v", got, r.Header)
	}
	kw, _ := r.Body["chat_template_kwargs"].(map[string]any)
	if kw["reasoning_effort"] != "low" {
		t.Fatalf("body = %v", r.Body)
	}
	if r.Body["model"] != "vllm/glm-5.3-flash" || r.Body["max_completion_tokens"] != float64(256) || r.Header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("adapter fields lost: header %v body %v", r.Header, r.Body)
	}
}

// Acceptance 1 and 3: rules present but none matching sends exactly the
// pre-change request (same golden as with no rules at all).
func TestWire_NonMatchingRuleRequestUnchanged(t *testing.T) {
	fake := bifrosttest.New(t)
	// Model "gpt-test" so the golden's body matches; the rule targets GLM.
	r := record(t, extrasLLM(t, fake.URL(), glmRules, "openai/gpt-test"), fake)
	compareGolden(t, "testdata/unchanged_request.json", map[string]any{"headers": stableHeaders(r.Header), "body": r.Body})
}

// Acceptance 3: with two matching rules only the first applies.
func TestWire_FirstMatchingRuleOnly(t *testing.T) {
	fake := bifrosttest.New(t)
	rules := `[{"match":"*glm*","headers":{"x-first":"1"},"body":{"first":true}},{"match":"*/glm-*","headers":{"x-second":"2"},"body":{"second":true}}]`
	r := record(t, extrasLLM(t, fake.URL(), rules, "openai/vllm/glm-5.3-flash"), fake)
	if r.Header.Get("x-first") != "1" || r.Header.Get("x-second") != "" || r.Body["first"] != true || r.Body["second"] != nil {
		t.Fatalf("header %v body %v", r.Header, r.Body)
	}
}

// Headers alone and body alone each work; a non-reserved field the adapter
// sets (temperature) is overwritten by the rule, as documented.
func TestWire_HeadersOnlyAndBodyOnly(t *testing.T) {
	fake := bifrosttest.New(t)
	r := record(t, extrasLLM(t, fake.URL(), `[{"match":"*","headers":{"x-only":"h"}}]`, "openai/m"), fake)
	if r.Header.Get("x-only") != "h" || r.Body["temperature"] != 0.5 {
		t.Fatalf("header %v body %v", r.Header, r.Body)
	}
	fake2 := bifrosttest.New(t)
	r = record(t, extrasLLM(t, fake2.URL(), `[{"match":"*","body":{"temperature":0.1,"top_k":7}}]`, "openai/m"), fake2)
	if r.Body["temperature"] != 0.1 || r.Body["top_k"] != float64(7) {
		t.Fatalf("body %v", r.Body)
	}
}

// Every request carries the rule, not just the first.
func TestWire_RuleOnEveryRequest(t *testing.T) {
	fake := bifrosttest.New(t)
	l := extrasLLM(t, fake.URL(), glmRules, "openai/vllm/glm-5.3-flash")
	record(t, l, fake)
	r := record(t, l, fake)
	if r.Header.Get("x-bf-passthrough-extra-params") != "true" || r.Body["chat_template_kwargs"] == nil {
		t.Fatalf("second request header %v body %v", r.Header, r.Body)
	}
}
