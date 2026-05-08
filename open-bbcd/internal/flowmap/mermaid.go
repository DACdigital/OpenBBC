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

// validateWorkflowSkillRefs walks every "id[label]" rectangle node in
// the mermaid string and asserts that label is a key in the provided set.
func validateWorkflowSkillRefs(mermaid string, skills map[string]struct{}) error {
	matches := skillNodeRe.FindAllStringSubmatch(mermaid, -1)
	for _, m := range matches {
		label := m[2]
		if _, ok := skills[label]; !ok {
			return fmt.Errorf("%w: workflow node %q references skill %q which is not declared", errUnknownSkill, m[0], label)
		}
	}
	return nil
}
