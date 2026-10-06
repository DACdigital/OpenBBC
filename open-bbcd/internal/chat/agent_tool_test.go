package chat

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestAgentToolDef(t *testing.T) {
	def := agentToolDef([]types.SubAgentBinding{
		{Name: "researcher", Note: "finds facts"},
		{Name: "writer", Note: "writes"},
	})
	if def.Name != "agent" {
		t.Fatalf("name = %q, want agent", def.Name)
	}
	wantDesc := "Delegate a self-contained task to a specialised sub-agent. The sub-agent starts with NO access to this conversation or its files — put everything it needs in `prompt`. It returns its final answer as text.\n\nAvailable sub-agents:\n- researcher: finds facts\n- writer: writes"
	if def.Description != wantDesc {
		t.Fatalf("description mismatch:\n got %q\nwant %q", def.Description, wantDesc)
	}
	wantSchema := `{"type":"object","required":["subagent","description","prompt"],"additionalProperties":false,"properties":{"subagent":{"type":"string","enum":["researcher","writer"]},"description":{"type":"string","description":"3-10 word summary of the task, shown to the user as progress"},"prompt":{"type":"string","description":"Full task instructions for the sub-agent"}}}`
	var got, want map[string]any
	if err := json.Unmarshal(def.InputSchema, &got); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(wantSchema), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema mismatch:\n got %s\nwant %s", def.InputSchema, wantSchema)
	}
	if _, ok := got["properties"].(map[string]any)["artifacts"]; ok {
		t.Fatal("schema must not have an artifacts property")
	}
}

func TestAgentToolDef_EmptyNote(t *testing.T) {
	def := agentToolDef([]types.SubAgentBinding{{Name: "a", Note: ""}, {Name: "b", Note: "  "}, {Name: "c", Note: "x"}})
	want := agentToolDescriptionHead + "- a\n- b\n- c: x"
	if def.Description != want {
		t.Fatalf("description = %q, want %q", def.Description, want)
	}
}

func TestAgentToolParseInput(t *testing.T) {
	in, err := parseAgentInput(json.RawMessage(`{"subagent":"researcher","description":"find facts","prompt":"Find X"}`))
	if err != nil {
		t.Fatalf("valid input: %v", err)
	}
	if in != (agentInput{Subagent: "researcher", Description: "find facts", Prompt: "Find X"}) {
		t.Fatalf("parsed = %+v", in)
	}

	bad := map[string]string{
		"unparsable":          `{"subagent":`,
		"empty raw":           ``,
		"not an object":       `"x"`,
		"null":                `null`,
		"missing subagent":    `{"description":"d","prompt":"p"}`,
		"missing description": `{"subagent":"s","prompt":"p"}`,
		"missing prompt":      `{"subagent":"s","description":"d"}`,
		"empty subagent":      `{"subagent":"","description":"d","prompt":"p"}`,
		"blank description":   `{"subagent":"s","description":"   ","prompt":"p"}`,
		"blank prompt":        `{"subagent":"s","description":"d","prompt":"\n\t"}`,
		"wrong type":          `{"subagent":1,"description":"d","prompt":"p"}`,
		"wrong type prompt":   `{"subagent":"s","description":"d","prompt":["p"]}`,
		"unknown property":    `{"subagent":"s","description":"d","prompt":"p","artifacts":[]}`,
		"trailing data":       `{"subagent":"s","description":"d","prompt":"p"} {}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := parseAgentInput(json.RawMessage(raw))
			var ate *agentToolError
			if !errors.As(err, &ate) {
				t.Fatalf("err = %v, want *agentToolError", err)
			}
			if ate.Code != "invalid_input" {
				t.Fatalf("code = %q, want invalid_input", ate.Code)
			}
		})
	}
}

func TestAgentToolErrorString(t *testing.T) {
	e := &agentToolError{Code: "unknown_subagent", Details: "no binding named x"}
	if e.Error() != "unknown_subagent: no binding named x" {
		t.Fatalf("Error() = %q", e.Error())
	}
}
