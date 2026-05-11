package flowmap

import (
	"strings"
	"testing"
)

func TestSerializeWorkflow_Linear(t *testing.T) {
	wf := ParsedWorkflow{
		Nodes: []ParsedNode{
			{ID: "start", Kind: NodeStart, Label: "start"},
			{ID: "s_place_order", Kind: NodeSkill, Label: "place-order"},
			{ID: "e", Kind: NodeEnd, Label: "end"},
		},
		Edges: []ParsedEdge{
			{From: "start", To: "s_place_order"},
			{From: "s_place_order", To: "e"},
		},
	}
	got := SerializeWorkflow(wf)
	if !strings.HasPrefix(got, "flowchart TD\n") {
		t.Errorf("output should start with `flowchart TD\\n`, got %q", got[:minInt(40, len(got))])
	}
	if !strings.Contains(got, "start([start])") {
		t.Errorf("missing start node: %s", got)
	}
	if !strings.Contains(got, "s_place_order[place-order]") {
		t.Errorf("missing skill node: %s", got)
	}
	if !strings.Contains(got, "e([end])") {
		t.Errorf("missing end node: %s", got)
	}
	if !strings.Contains(got, "start --> s_place_order") || !strings.Contains(got, "s_place_order --> e") {
		t.Errorf("missing expected edges: %s", got)
	}
}

func TestSerializeWorkflow_Decision_LabeledEdges(t *testing.T) {
	wf := ParsedWorkflow{
		Nodes: []ParsedNode{
			{ID: "start", Kind: NodeStart, Label: "start"},
			{ID: "d", Kind: NodeDecision, Label: "ready?"},
			{ID: "e", Kind: NodeEnd, Label: "end"},
		},
		Edges: []ParsedEdge{
			{From: "start", To: "d"},
			{From: "d", To: "e", Label: "yes"},
			{From: "d", To: "start", Label: "no"},
		},
	}
	got := SerializeWorkflow(wf)
	if !strings.Contains(got, "d{ready?}") {
		t.Errorf("decision node should serialize as `d{ready?}`: %s", got)
	}
	if !strings.Contains(got, "d -- yes --> e") {
		t.Errorf("labeled edge should serialize as `d -- yes --> e`: %s", got)
	}
	if !strings.Contains(got, "d -- no --> start") {
		t.Errorf("back-edge should serialize: %s", got)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
