package types

import (
	"testing"
)

func TestNewAgent_Valid(t *testing.T) {
	input := CreateAgentInput{
		Name:   "Test Agent",
		Prompt: "You are a helpful assistant.",
	}

	agent, err := NewAgent(input)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if agent.Name != input.Name {
		t.Errorf("Name = %q, want %q", agent.Name, input.Name)
	}
}

func TestNewAgent_MissingName(t *testing.T) {
	input := CreateAgentInput{Prompt: "test"}
	_, err := NewAgent(input)
	if err != ErrNameRequired {
		t.Errorf("error = %v, want %v", err, ErrNameRequired)
	}
}

func TestNewAgent_MissingPrompt(t *testing.T) {
	input := CreateAgentInput{Name: "test"}
	_, err := NewAgent(input)
	if err != ErrPromptRequired {
		t.Errorf("error = %v, want %v", err, ErrPromptRequired)
	}
}
