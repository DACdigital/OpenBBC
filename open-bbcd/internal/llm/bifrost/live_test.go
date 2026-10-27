package bifrost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// Live smoke (spec acceptance "Live smoke"): runs only with
// OPENBBC_LIVE_LLM_TEST=1, against every allow-listed provider whose
// <PROVIDER>_API_KEY is set. Override a provider's model with
// OPENBBC_LIVE_MODEL_<PROVIDER> (e.g. OPENBBC_LIVE_MODEL_OPENAI=gpt-4o-mini).
// Defaults are small, tool-capable models, one per allow-listed provider
// (config.bifrostProviders); keep the two in step.
var liveModels = map[string]string{
	"openai":     "gpt-4o-mini",
	"anthropic":  "claude-haiku-4-5-20251001",
	"gemini":     "gemini-2.5-flash",
	"mistral":    "mistral-small-latest",
	"groq":       "meta-llama/llama-4-scout-17b-16e-instruct",
	"cohere":     "command-r7b-12-2024",
	"openrouter": "openai/gpt-4o-mini",
	"deepseek":   "deepseek-chat",
	"xai":        "grok-4-fast-non-reasoning",
	"cerebras":   "gpt-oss-120b",
}

// liveTextOnly lists providers whose default live model has no vision: the
// image_tool scenario is skipped for them unless OPENBBC_LIVE_MODEL_<PROVIDER>
// picks another model (a model without vision rejects image input, which the
// spec treats as a deployer model-choice error, not an adapter failure).
var liveTextOnly = map[string]bool{
	"cohere":   true,
	"deepseek": true,
	"cerebras": true,
}

func liveModelOverride(provider string) string {
	return os.Getenv("OPENBBC_LIVE_MODEL_" + strings.ToUpper(provider))
}

const lookupSchema = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`

func liveLLM(t *testing.T, provider string) (*LLM, bool) {
	key := os.Getenv(config.APIKeyEnv(provider))
	if key == "" {
		return nil, false
	}
	model := liveModels[provider]
	if m := liveModelOverride(provider); m != "" {
		model = m
	}
	l, err := New(context.Background(), config.LLMConfig{Adapter: "bifrost", Provider: provider, Model: model, APIKey: key}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("%s: New: %v", provider, err)
	}
	t.Cleanup(l.Shutdown)
	return l, true
}

// turn runs one Generate call and returns text, tool calls and the stop reason.
func turn(t *testing.T, l *LLM, req llm.Request) (string, []llm.ToolUseBlock, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var text strings.Builder
	var calls []llm.ToolUseBlock
	inputs := map[string]*strings.Builder{}
	stop := ""
	for ev, err := range l.Generate(ctx, req) {
		if err != nil {
			t.Fatalf("%s: Generate: %v", l.Name(), err)
		}
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			text.WriteString(e.Delta)
		case llm.ToolUseStartEvent:
			calls = append(calls, llm.ToolUseBlock{ID: e.ID, Name: e.Name})
			inputs[e.ID] = &strings.Builder{}
		case llm.ToolUseInputEvent:
			inputs[e.ID].WriteString(e.JSONFragment)
		case llm.MessageStopEvent:
			stop = e.StopReason
		}
	}
	for i := range calls {
		calls[i].Input = json.RawMessage(inputs[calls[i].ID].String())
	}
	return text.String(), calls, stop
}

func TestLive(t *testing.T) {
	if os.Getenv("OPENBBC_LIVE_LLM_TEST") != "1" {
		t.Skip("set OPENBBC_LIVE_LLM_TEST=1 to run live provider smoke tests")
	}
	png, err := os.ReadFile("testdata/red.png")
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for provider := range liveModels {
		l, ok := liveLLM(t, provider)
		if !ok {
			continue
		}
		ran++
		t.Run(provider+"/tool_round", func(t *testing.T) {
			tools := []llm.ToolDef{{Name: "get_weather", Description: "Current weather for a city", InputSchema: json.RawMessage(lookupSchema)}}
			msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "Use get_weather for Paris, then tell me the result in one sentence."}}}}
			_, calls, stop := turn(t, l, llm.Request{System: "You are terse.", Messages: msgs, Tools: tools, MaxTokens: 512})
			if stop != "tool_use" || len(calls) == 0 {
				t.Fatalf("first round: stop=%q calls=%+v", stop, calls)
			}
			// Answer every call: providers reject a history with an
			// unanswered tool call, and some issue parallel calls here.
			assistant := llm.Message{Role: llm.RoleAssistant}
			results := llm.Message{Role: llm.RoleTool}
			for _, c := range calls {
				if c.Name != "get_weather" || !json.Valid(c.Input) {
					t.Fatalf("first round: bad call %+v", c)
				}
				assistant.Content = append(assistant.Content, c)
				results.Content = append(results.Content, llm.ToolResultBlock{ToolUseID: c.ID, Result: json.RawMessage(`"Sunny, 21C"`)})
			}
			msgs = append(msgs, assistant, results)
			text, _, stop := turn(t, l, llm.Request{System: "You are terse.", Messages: msgs, Tools: tools, MaxTokens: 512})
			if stop != "end_turn" || !strings.Contains(strings.ToLower(text), "sunny") {
				t.Fatalf("second round: stop=%q text=%q", stop, text)
			}
		})
		t.Run(provider+"/image_tool", func(t *testing.T) {
			if liveTextOnly[provider] && liveModelOverride(provider) == "" {
				t.Skipf("default model %q has no vision; set OPENBBC_LIVE_MODEL_%s to a vision model", liveModels[provider], strings.ToUpper(provider))
			}
			tools := []llm.ToolDef{{Name: "get_swatch", Description: "Returns a colour swatch image", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}
			msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "Call get_swatch, look at the image it returns, and answer with just the colour name."}}}}
			_, calls, stop := turn(t, l, llm.Request{Messages: msgs, Tools: tools, MaxTokens: 256})
			if stop != "tool_use" || len(calls) == 0 {
				t.Fatalf("first round: stop=%q calls=%+v", stop, calls)
			}
			msgs = append(msgs,
				llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{calls[0]}},
				llm.Message{Role: llm.RoleTool, Content: []llm.Block{
					llm.ToolResultBlock{ToolUseID: calls[0].ID, Result: json.RawMessage(`"swatch attached"`)},
					llm.InlineMediaBlock{MIME: "image/png", Data: png},
				}},
			)
			text, _, _ := turn(t, l, llm.Request{Messages: msgs, Tools: tools, MaxTokens: 256})
			if !strings.Contains(strings.ToLower(text), "red") {
				t.Fatalf("answer %q does not name the swatch colour (red)", text)
			}
		})
	}
	if ran == 0 {
		t.Skip("no provider API key set")
	}
}
