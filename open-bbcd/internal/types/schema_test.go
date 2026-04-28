package types

import (
	"testing"

	"gopkg.in/yaml.v3"
)

const testSchema = `
version: v1
wizard:
  name:
    label: "Agent name"
    type: text
    required: true
    order: 1
  scope:
    label: "Describe the scope"
    type: textarea
    required: true
    order: 2
  discovery_file:
    label: "Upload file"
    type: file
    required: false
    order: 3
`

func TestWizardSchema_OrderedFields(t *testing.T) {
	var s WizardSchema
	if err := yaml.Unmarshal([]byte(testSchema), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fields := s.OrderedFields()
	if len(fields) != 3 {
		t.Fatalf("len = %d, want 3", len(fields))
	}
	if fields[0].Key != "name" {
		t.Errorf("fields[0].Key = %q, want %q", fields[0].Key, "name")
	}
	if fields[1].Key != "scope" {
		t.Errorf("fields[1].Key = %q, want %q", fields[1].Key, "scope")
	}
	if fields[2].Key != "discovery_file" {
		t.Errorf("fields[2].Key = %q, want %q", fields[2].Key, "discovery_file")
	}
	if fields[0].Field.Type != "text" {
		t.Errorf("fields[0].Field.Type = %q, want \"text\"", fields[0].Field.Type)
	}
}
