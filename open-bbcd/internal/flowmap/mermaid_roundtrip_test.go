package flowmap

import (
	"reflect"
	"sort"
	"testing"
)

// TestParseSerializeRoundTrip: for each canonical fixture, parse → serialize → parse
// produces structurally equal results (node set + edge set, ignoring order).
func TestParseSerializeRoundTrip(t *testing.T) {
	fixtures := []string{"linear.mermaid", "decision.mermaid", "back-edge-loop.mermaid", "start-end-only.mermaid"}
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			src := readFixture(t, f)
			parsed1, err := ParseWorkflow(src)
			if err != nil {
				t.Fatalf("first parse: %v", err)
			}
			emitted := SerializeWorkflow(parsed1)
			parsed2, err := ParseWorkflow(emitted)
			if err != nil {
				t.Fatalf("second parse of emitted output:\n%s\nerr=%v", emitted, err)
			}

			n1 := nodeSet(parsed1.Nodes)
			n2 := nodeSet(parsed2.Nodes)
			if !reflect.DeepEqual(n1, n2) {
				t.Errorf("node set mismatch:\nfirst:  %+v\nsecond: %+v\nemitted:\n%s", n1, n2, emitted)
			}

			e1 := edgeSet(parsed1.Edges)
			e2 := edgeSet(parsed2.Edges)
			if !reflect.DeepEqual(e1, e2) {
				t.Errorf("edge set mismatch:\nfirst:  %+v\nsecond: %+v\nemitted:\n%s", e1, e2, emitted)
			}
		})
	}
}

func nodeSet(ns []ParsedNode) []ParsedNode {
	out := make([]ParsedNode, len(ns))
	copy(out, ns)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func edgeSet(es []ParsedEdge) []ParsedEdge {
	out := make([]ParsedEdge, len(es))
	copy(out, es)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Label < out[j].Label
	})
	return out
}
