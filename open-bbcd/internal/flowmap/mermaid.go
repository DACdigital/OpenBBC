package flowmap

import (
	"errors"
	"fmt"
	"regexp"
)

var errUnknownSkill = errors.New("unknown skill")

// skillNodeRe matches mermaid flowchart skill nodes of the form
// "<nodeId>[<skill-id>]". The captured group is the skill-id (the label
// between `[` and `]`). Skill nodes are rectangles by mermaid convention;
// `id([...])` (start/end stadium) and `id{...}` (decision diamond) are
// other shapes that are NOT skill references.
var skillNodeRe = regexp.MustCompile(`(?m)([A-Za-z_][A-Za-z0-9_]*)\[([^\]\[]+)\]`)

// ValidateWorkflowSkillRefs walks every "id[label]" rectangle node in
// the mermaid string and asserts that label is a key in the provided set.
func ValidateWorkflowSkillRefs(mermaid string, skills map[string]struct{}) error {
	matches := skillNodeRe.FindAllStringSubmatch(mermaid, -1)
	for _, m := range matches {
		label := m[2]
		if _, ok := skills[label]; !ok {
			return fmt.Errorf("%w: workflow node %q references skill %q which is not declared", errUnknownSkill, m[0], label)
		}
	}
	return nil
}

// WorkflowReferencesSkill returns true if the mermaid string declares any
// id[<skill-id>] rectangle node whose label equals the given skill id.
func WorkflowReferencesSkill(mermaid, skillID string) bool {
	for _, m := range skillNodeRe.FindAllStringSubmatch(mermaid, -1) {
		if m[2] == skillID {
			return true
		}
	}
	return false
}

// NodeKind enumerates the shapes the editor produces.
type NodeKind string

const (
	NodeStart    NodeKind = "start"
	NodeEnd      NodeKind = "end"
	NodeSkill    NodeKind = "skill"
	NodeDecision NodeKind = "decision"
)

// ParsedNode is one node in a parsed mermaid flowchart.
type ParsedNode struct {
	ID    string   // mermaid node id (e.g. "s_place_order")
	Kind  NodeKind // start | end | skill | decision
	Label string   // for skill: skill-id; for decision: question text; for start/end: literal "start"/"end"
}

// ParsedEdge is one edge in a parsed mermaid flowchart.
type ParsedEdge struct {
	From  string // source node id
	To    string // target node id
	Label string // empty for `-->`; non-empty for `-- yes -->` / `-- no -->`
}

// ParsedWorkflow is the structured form of a mermaid flowchart TD block.
// Round-trip property: serialize(parse(s)) preserves node ids, kinds, labels,
// edges (set-equal), and edge labels — though edge ORDER may differ since
// the serializer emits a deterministic order.
type ParsedWorkflow struct {
	Nodes []ParsedNode
	Edges []ParsedEdge
}
