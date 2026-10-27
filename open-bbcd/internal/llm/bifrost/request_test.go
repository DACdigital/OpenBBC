package bifrost

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// wire marshals the request's messages the way Bifrost sends them, so the
// assertions read like the OpenAI JSON body.
func wire(t *testing.T, r *schemas.BifrostChatRequest) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(r.Input)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildChatRequest_SystemFirstAndParams(t *testing.T) {
	r := buildChatRequest(llm.Request{
		System:      "be nice",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}},
		MaxTokens:   512,
		Temperature: 0.3,
	}, schemas.OpenAI, "gpt-4o")
	if r.Provider != schemas.OpenAI || r.Model != "gpt-4o" {
		t.Fatalf("provider/model = %s/%s", r.Provider, r.Model)
	}
	msgs := wire(t, r)
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[0]["content"] != "be nice" || msgs[1]["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	if *r.Params.MaxCompletionTokens != 512 || *r.Params.Temperature != 0.3 {
		t.Fatalf("params = %+v", r.Params)
	}
}

func TestBuildChatRequest_NoSystemZeroParams(t *testing.T) {
	r := buildChatRequest(llm.Request{Model: "override", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}}}, schemas.OpenAI, "gpt-4o")
	if r.Model != "override" {
		t.Fatalf("model = %s, want req.Model to win", r.Model)
	}
	if msgs := wire(t, r); len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	if r.Params.MaxCompletionTokens != nil || r.Params.Temperature != nil {
		t.Fatalf("zero params must be omitted: %+v", r.Params)
	}
}

func TestBuildChatRequest_UserImageDataURI(t *testing.T) {
	r := buildChatRequest(llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		llm.TextBlock{Text: "look"},
		llm.InlineMediaBlock{MIME: "image/png", Data: []byte{0x89, 0x50}},
	}}}}, schemas.OpenAI, "m")
	parts := wire(t, r)[0]["content"].([]any)
	img := parts[1].(map[string]any)
	if img["type"] != "image_url" || img["image_url"].(map[string]any)["url"] != "data:image/png;base64,iVA=" {
		t.Fatalf("image part = %v", img)
	}
}

func TestBuildChatRequest_AssistantToolUseRoundTrip(t *testing.T) {
	r := buildChatRequest(llm.Request{Messages: []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{
		llm.TextBlock{Text: "checking"},
		llm.ToolUseBlock{ID: "call_1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`)},
		llm.ToolUseBlock{ID: "call_2", Name: "ping"},
	}}}}, schemas.OpenAI, "m")
	m := wire(t, r)[0]
	if m["role"] != "assistant" || m["content"] != "checking" {
		t.Fatalf("assistant = %v", m)
	}
	calls := m["tool_calls"].([]any)
	c0 := calls[0].(map[string]any)
	c1 := calls[1].(map[string]any)
	if c0["id"] != "call_1" || c0["type"] != "function" || c0["function"].(map[string]any)["name"] != "lookup" || c0["function"].(map[string]any)["arguments"] != `{"q":"x"}` {
		t.Fatalf("call 0 = %v", c0)
	}
	if c1["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("empty input must become {}: %v", c1)
	}
}

func TestBuildChatRequest_ToolResults(t *testing.T) {
	r := buildChatRequest(llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.Block{
		llm.ToolResultBlock{ToolUseID: "call_1", Result: json.RawMessage(`"W says hi"`)},
		llm.ToolResultBlock{ToolUseID: "call_2", Result: json.RawMessage(`{"n":1}`), IsError: true},
	}}}}, schemas.OpenAI, "m")
	msgs := wire(t, r)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	if msgs[0]["role"] != "tool" || msgs[0]["tool_call_id"] != "call_1" || msgs[0]["content"] != "W says hi" {
		t.Fatalf("tool 0 = %v", msgs[0])
	}
	if msgs[1]["tool_call_id"] != "call_2" || msgs[1]["content"] != `Error: {"n":1}` || msgs[1]["is_error"] != true {
		t.Fatalf("tool 1 = %v", msgs[1])
	}
}

func TestBuildChatRequest_ToolAttachmentsGoToSyntheticUserMessage(t *testing.T) {
	r := buildChatRequest(llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.Block{
		llm.ToolResultBlock{ToolUseID: "call_1", Result: json.RawMessage(`"ok"`)},
		llm.ToolResultBlock{ToolUseID: "call_2", Result: json.RawMessage(`"ok"`)},
		llm.InlineMediaBlock{MIME: "image/png", Data: []byte("x")},
		llm.TextBlock{Text: "[Attachment: r.pdf (application/pdf, 1 KB)]"},
	}}}}, schemas.OpenAI, "m")
	msgs := wire(t, r)
	if len(msgs) != 3 || msgs[0]["role"] != "tool" || msgs[1]["role"] != "tool" || msgs[2]["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
	parts := msgs[2]["content"].([]any)
	if len(parts) != 3 ||
		parts[0].(map[string]any)["text"] != "Attachments returned by tool call(s) call_1, call_2:" ||
		parts[1].(map[string]any)["type"] != "image_url" ||
		parts[2].(map[string]any)["text"] != "[Attachment: r.pdf (application/pdf, 1 KB)]" {
		t.Fatalf("synthetic user parts = %v", parts)
	}
}

func TestBuildChatRequest_NoAttachmentsNoSyntheticMessage(t *testing.T) {
	r := buildChatRequest(llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.Block{
		llm.ToolResultBlock{ToolUseID: "call_1", Result: json.RawMessage(`"ok"`)},
	}}}}, schemas.OpenAI, "m")
	if msgs := wire(t, r); len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestBuildChatRequest_Tools(t *testing.T) {
	r := buildChatRequest(llm.Request{Tools: []llm.ToolDef{
		{Name: "lookup", Description: "find", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)},
		{Name: "ping"},
	}}, schemas.OpenAI, "m")
	raw, _ := json.Marshal(r.Params.Tools)
	want := `[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}},{"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{}}}}]`
	if string(raw) != want {
		t.Fatalf("tools =\n%s\nwant\n%s", raw, want)
	}
}
