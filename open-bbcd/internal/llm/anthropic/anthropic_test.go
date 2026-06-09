package anthropic

import (
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// mustParseEvent constructs a MessageStreamEventUnion from a raw JSON string.
// The SDK uses unexported raw-JSON fields internally, so the only clean way to
// build a synthetic event for unit-testing is to unmarshal from JSON — matching
// what the real SSE decoder does.
func mustParseEvent(t *testing.T, raw string) sdk.MessageStreamEventUnion {
	t.Helper()
	var ev sdk.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("mustParseEvent: %v", err)
	}
	return ev
}

func TestLLM_Name(t *testing.T) {
	l := New(config.AnthropicConfig{})
	if l.Name() != "anthropic" {
		t.Fatalf("Name() = %q, want %q", l.Name(), "anthropic")
	}
}

func TestBuildMessageNewParams_BasicFields(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		System:    "you are helpful",
		MaxTokens: 1024,
	}
	params := buildMessageNewParams(req)

	// Model is a type alias for string in sdk v1.49.0 — cast directly.
	if string(params.Model) != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q, want claude-sonnet-4-6", params.Model)
	}
	if params.MaxTokens != 1024 {
		t.Fatalf("MaxTokens = %d, want 1024", params.MaxTokens)
	}
	if len(params.System) == 0 {
		t.Fatalf("System should be non-empty when req.System is set")
	}
}

func TestBuildMessageNewParams_EmptySystem(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 512,
	}
	params := buildMessageNewParams(req)
	if len(params.System) != 0 {
		t.Fatalf("System should be empty when req.System is empty, got %d blocks", len(params.System))
	}
}

func TestConvertMessage_UserText(t *testing.T) {
	m := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.Block{llm.TextBlock{Text: "hi"}},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleUser {
		t.Fatalf("role mismatch: got %v", p.Role)
	}
	if len(p.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(p.Content))
	}
	text := p.Content[0].GetText()
	if text == nil || *text != "hi" {
		t.Fatalf("expected text block with 'hi', got %v", text)
	}
}

func TestConvertMessage_AssistantWithToolUse(t *testing.T) {
	m := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.TextBlock{Text: "let me check"},
			llm.ToolUseBlock{ID: "tu_1", Name: "query", Input: []byte(`{"q":"x"}`)},
		},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleAssistant {
		t.Fatalf("role mismatch: got %v", p.Role)
	}
	if len(p.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(p.Content))
	}
}

func TestConvertMessage_ToolRole_BecomesUser(t *testing.T) {
	m := llm.Message{
		Role: llm.RoleTool,
		Content: []llm.Block{
			llm.ToolResultBlock{ToolUseID: "tu_1", Result: []byte(`{"ok":true}`), IsError: false},
		},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleUser {
		t.Fatalf("tool-role messages should map to user role; got %v", p.Role)
	}
	if len(p.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(p.Content))
	}
}

func TestConvertTool(t *testing.T) {
	td := llm.ToolDef{
		Name:        "Skill",
		Description: "Load a skill prompt",
		InputSchema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}
	p := convertTool(td)
	if p.OfTool == nil {
		t.Fatalf("expected OfTool branch to be set")
	}
	if p.OfTool.Name != "Skill" {
		t.Fatalf("name mismatch: got %q", p.OfTool.Name)
	}
	if !p.OfTool.Description.Valid() {
		t.Fatalf("expected Description to be set")
	}
	if p.OfTool.Description.Value != "Load a skill prompt" {
		t.Fatalf("description mismatch: got %q", p.OfTool.Description.Value)
	}
}

func TestBuildMessageNewParams_TemperaturePassthrough(t *testing.T) {
	req := llm.Request{
		Model:       "claude-sonnet-4-6",
		MaxTokens:   100,
		Temperature: 0.7,
	}
	params := buildMessageNewParams(req)
	if !params.Temperature.Valid() {
		t.Fatalf("expected Temperature to be set, but it was omitted")
	}
	if params.Temperature.Value != 0.7 {
		t.Fatalf("Temperature = %v, want 0.7", params.Temperature.Value)
	}
}

func TestBuildMessageNewParams_ZeroTemperatureOmitted(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		// Temperature zero (default) — should be omitted so SDK uses its own default.
	}
	params := buildMessageNewParams(req)
	if params.Temperature.Valid() {
		t.Fatalf("zero Temperature should be omitted, but it was set to %v", params.Temperature.Value)
	}
}

// ---------------------------------------------------------------------------
// chunkTranslator tests
//
// SDK stream-event structs use unexported raw-JSON fields internally (all
// As* accessors call apijson.UnmarshalRoot on the stored raw bytes). The only
// clean way to construct synthetic events is to json.Unmarshal from a JSON
// string — which is exactly what mustParseEvent does above. This matches what
// the real SSE decoder does, so the tests are faithful.
// ---------------------------------------------------------------------------

func TestTranslate_MessageStart_InputTokens(t *testing.T) {
	raw := `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":42,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ue, ok := events[0].(llm.UsageEvent)
	if !ok {
		t.Fatalf("expected UsageEvent, got %T", events[0])
	}
	if ue.InputTokens != 42 {
		t.Fatalf("InputTokens = %d, want 42", ue.InputTokens)
	}
}

func TestTranslate_ContentBlockStart_ToolUse_EmitsToolUseStartEvent(t *testing.T) {
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_abc","name":"search","input":{}}}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	se, ok := events[0].(llm.ToolUseStartEvent)
	if !ok {
		t.Fatalf("expected ToolUseStartEvent, got %T", events[0])
	}
	if se.ID != "tu_abc" {
		t.Fatalf("ID = %q, want tu_abc", se.ID)
	}
	if se.Name != "search" {
		t.Fatalf("Name = %q, want search", se.Name)
	}
	// Index→ID should now be tracked.
	if tr.toolUseIDAtIndex[0] != "tu_abc" {
		t.Fatalf("toolUseIDAtIndex[0] = %q, want tu_abc", tr.toolUseIDAtIndex[0])
	}
}

func TestTranslate_ContentBlockStart_Text_NoEvent(t *testing.T) {
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 0 {
		t.Fatalf("expected no events for text content_block_start, got %d", len(events))
	}
}

func TestTranslate_ContentBlockDelta_Text(t *testing.T) {
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	te, ok := events[0].(llm.TextDeltaEvent)
	if !ok {
		t.Fatalf("expected TextDeltaEvent, got %T", events[0])
	}
	if te.Delta != "hello" {
		t.Fatalf("Delta = %q, want hello", te.Delta)
	}
}

func TestTranslate_ContentBlockDelta_InputJSON_CarriesToolUseID(t *testing.T) {
	// First register the tool_use block at index 1.
	tr := &chunkTranslator{}
	startRaw := `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_xyz","name":"lookup","input":{}}}`
	tr.translate(mustParseEvent(t, startRaw))

	// Now send an input_json_delta at the same index.
	deltaRaw := `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`
	events := tr.translate(mustParseEvent(t, deltaRaw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ie, ok := events[0].(llm.ToolUseInputEvent)
	if !ok {
		t.Fatalf("expected ToolUseInputEvent, got %T", events[0])
	}
	if ie.ID != "tu_xyz" {
		t.Fatalf("ID = %q, want tu_xyz", ie.ID)
	}
	if ie.JSONFragment != `{"q":` {
		t.Fatalf("JSONFragment = %q, want {\"q\":", ie.JSONFragment)
	}
}

func TestTranslate_ContentBlockStop_ToolUse_EmitsToolUseEndEvent(t *testing.T) {
	tr := &chunkTranslator{}
	// Register the block at index 2.
	startRaw := `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_end","name":"finish","input":{}}}`
	tr.translate(mustParseEvent(t, startRaw))

	stopRaw := `{"type":"content_block_stop","index":2}`
	events := tr.translate(mustParseEvent(t, stopRaw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ee, ok := events[0].(llm.ToolUseEndEvent)
	if !ok {
		t.Fatalf("expected ToolUseEndEvent, got %T", events[0])
	}
	if ee.ID != "tu_end" {
		t.Fatalf("ID = %q, want tu_end", ee.ID)
	}
	// Index should be cleaned up.
	if _, ok := tr.toolUseIDAtIndex[2]; ok {
		t.Fatalf("toolUseIDAtIndex[2] should be removed after ContentBlockStop")
	}
}

func TestTranslate_ContentBlockStop_Text_NoEvent(t *testing.T) {
	// A text block stop (no entry in toolUseIDAtIndex) should produce no events.
	tr := &chunkTranslator{}
	stopRaw := `{"type":"content_block_stop","index":0}`
	events := tr.translate(mustParseEvent(t, stopRaw))
	if len(events) != 0 {
		t.Fatalf("expected no events for text content_block_stop, got %d", len(events))
	}
}

func TestTranslate_MessageDelta_StopReasonAndOutputTokens(t *testing.T) {
	raw := `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":17}}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 2 {
		t.Fatalf("expected 2 events (MessageStopEvent + UsageEvent), got %d", len(events))
	}
	se, ok := events[0].(llm.MessageStopEvent)
	if !ok {
		t.Fatalf("expected MessageStopEvent first, got %T", events[0])
	}
	if se.StopReason != "tool_use" {
		t.Fatalf("StopReason = %q, want tool_use", se.StopReason)
	}
	ue, ok := events[1].(llm.UsageEvent)
	if !ok {
		t.Fatalf("expected UsageEvent second, got %T", events[1])
	}
	if ue.OutputTokens != 17 {
		t.Fatalf("OutputTokens = %d, want 17", ue.OutputTokens)
	}
}

func TestTranslate_MessageStop_NoEvent(t *testing.T) {
	raw := `{"type":"message_stop"}`
	tr := &chunkTranslator{}
	events := tr.translate(mustParseEvent(t, raw))
	if len(events) != 0 {
		t.Fatalf("expected no events for message_stop, got %d", len(events))
	}
}

func TestLLM_Generate_NilClient(t *testing.T) {
	// A zero-APIKey LLM should yield exactly one error and then stop.
	l := New(config.AnthropicConfig{})
	var gotErr error
	var eventCount int
	for ev, err := range l.Generate(nil, llm.Request{}) {
		_ = ev
		if err != nil {
			gotErr = err
		}
		eventCount++
	}
	if gotErr == nil {
		t.Fatalf("expected an error from nil-client LLM, got none")
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly 1 yield (the error), got %d", eventCount)
	}
}

func TestBuildMessageNewParams_PromptCachingMarkers(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		System:    "you are X",
		Tools: []llm.ToolDef{
			{Name: "a", Description: "first", InputSchema: []byte(`{"type":"object"}`)},
			{Name: "b", Description: "last", InputSchema: []byte(`{"type":"object"}`)},
		},
	}
	params := buildMessageNewParams(req)

	// System block must have an ephemeral cache-control marker.
	if len(params.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(params.System))
	}
	if params.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system block cache_control.type = %q, want ephemeral", params.System[0].CacheControl.Type)
	}

	// Both tools converted.
	if len(params.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(params.Tools))
	}

	// First tool should NOT have a cache-control marker.
	if params.Tools[0].OfTool != nil && params.Tools[0].OfTool.CacheControl.Type == "ephemeral" {
		t.Fatalf("first tool should not have cache_control marker")
	}

	// Last tool should have an ephemeral cache-control marker.
	if params.Tools[1].OfTool == nil {
		t.Fatalf("last tool OfTool is nil")
	}
	if params.Tools[1].OfTool.CacheControl.Type != "ephemeral" {
		t.Fatalf("last tool cache_control.type = %q, want ephemeral", params.Tools[1].OfTool.CacheControl.Type)
	}
}

func TestBuildMessageNewParams_PromptCachingMarkers_EmptyToolList(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		System:    "you are X",
		// No tools.
	}
	params := buildMessageNewParams(req)

	// System should still be cached even with no tools.
	if len(params.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(params.System))
	}
	if params.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system block cache_control.type = %q, want ephemeral", params.System[0].CacheControl.Type)
	}

	// No tools, no crash.
	if len(params.Tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(params.Tools))
	}
}

func TestBuildMessageNewParams_PromptCachingMarkers_EmptySystem(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		// No system.
		Tools: []llm.ToolDef{
			{Name: "a", Description: "only", InputSchema: []byte(`{"type":"object"}`)},
		},
	}
	params := buildMessageNewParams(req)

	// No system when empty.
	if len(params.System) != 0 {
		t.Fatalf("expected 0 system blocks when System is empty, got %d", len(params.System))
	}

	// Tool should still be cached.
	if len(params.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(params.Tools))
	}
	if params.Tools[0].OfTool == nil {
		t.Fatalf("tool OfTool is nil")
	}
	if params.Tools[0].OfTool.CacheControl.Type != "ephemeral" {
		t.Fatalf("tool cache_control.type = %q, want ephemeral", params.Tools[0].OfTool.CacheControl.Type)
	}
}
