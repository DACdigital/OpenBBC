package types

import (
	"testing"
)

func TestNewAgent_Valid(t *testing.T) {
	opts := CreateAgentOpts{
		Name: "Test Agent",
	}

	agent, err := NewAgent(opts)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if agent.Name != opts.Name {
		t.Errorf("Name = %q, want %q", agent.Name, opts.Name)
	}
	if agent.Status != string(AgentStatusDraft) {
		t.Errorf("Status = %q, want %q", agent.Status, AgentStatusDraft)
	}
}

func TestNewAgent_MissingName(t *testing.T) {
	opts := CreateAgentOpts{}
	_, err := NewAgent(opts)
	if err != ErrNameRequired {
		t.Errorf("error = %v, want %v", err, ErrNameRequired)
	}
}
