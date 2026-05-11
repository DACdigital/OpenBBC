package flowmap

import (
	"fmt"
	"strings"
)

// SerializeWorkflow emits a `flowchart TD` mermaid block for the given nodes
// and edges. Output is deterministic:
//   - one node-declaration line per node in input order, with shape syntax
//     based on Kind (stadium for start/end, brackets for skill, braces for
//     decision)
//   - one edge line per edge, edges in input order
//
// Round-trip property with ParseWorkflow holds for canonical fixtures.
func SerializeWorkflow(wf ParsedWorkflow) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, n := range wf.Nodes {
		b.WriteString("  ")
		b.WriteString(formatNode(n))
		b.WriteByte('\n')
	}
	for _, e := range wf.Edges {
		b.WriteString("  ")
		if e.Label == "" {
			fmt.Fprintf(&b, "%s --> %s", e.From, e.To)
		} else {
			fmt.Fprintf(&b, "%s -- %s --> %s", e.From, e.Label, e.To)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func formatNode(n ParsedNode) string {
	switch n.Kind {
	case NodeStart, NodeEnd:
		return fmt.Sprintf("%s([%s])", n.ID, n.Label)
	case NodeDecision:
		return fmt.Sprintf("%s{%s}", n.ID, n.Label)
	case NodeSkill:
		return fmt.Sprintf("%s[%s]", n.ID, n.Label)
	default:
		// Defensive fallback: render as skill rectangle.
		return fmt.Sprintf("%s[%s]", n.ID, n.Label)
	}
}
