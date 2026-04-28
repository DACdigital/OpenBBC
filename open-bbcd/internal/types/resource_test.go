package types

import (
	"testing"
)

func TestNewResource_Valid(t *testing.T) {
	opts := CreateResourceOpts{
		AgentID: "agent-123",
		Name:    "get_users",
		Prompt:  "Fetches users from API",
	}

	resource, err := NewResource(opts)
	if err != nil {
		t.Fatalf("NewResource() error = %v", err)
	}
	if resource.Name != opts.Name {
		t.Errorf("Name = %q, want %q", resource.Name, opts.Name)
	}
}

func TestNewResource_MissingAgentID(t *testing.T) {
	opts := CreateResourceOpts{Name: "test", Prompt: "test"}
	_, err := NewResource(opts)
	if err != ErrAgentRequired {
		t.Errorf("error = %v, want %v", err, ErrAgentRequired)
	}
}
