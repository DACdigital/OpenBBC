package types

import (
	"testing"
)

func TestNewResource_Valid(t *testing.T) {
	input := CreateResourceInput{
		AgentID: "agent-123",
		Name:    "get_users",
		Prompt:  "Fetches users from API",
	}

	resource, err := NewResource(input)
	if err != nil {
		t.Fatalf("NewResource() error = %v", err)
	}
	if resource.Name != input.Name {
		t.Errorf("Name = %q, want %q", resource.Name, input.Name)
	}
}

func TestNewResource_MissingAgentID(t *testing.T) {
	input := CreateResourceInput{Name: "test", Prompt: "test"}
	_, err := NewResource(input)
	if err != ErrAgentRequired {
		t.Errorf("error = %v, want %v", err, ErrAgentRequired)
	}
}
