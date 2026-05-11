package flowmap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mermaid", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseWorkflow_Linear(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "linear.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Nodes) != 3 {
		t.Fatalf("Nodes = %d, want 3 (start, s_place_order, end): %+v", len(wf.Nodes), wf.Nodes)
	}
	byID := indexByID(wf.Nodes)
	if byID["start"].Kind != NodeStart {
		t.Errorf("start kind = %q, want start", byID["start"].Kind)
	}
	if byID["s_place_order"].Kind != NodeSkill || byID["s_place_order"].Label != "place-order" {
		t.Errorf("s_place_order = %+v", byID["s_place_order"])
	}
	if byID["e"].Kind != NodeEnd {
		t.Errorf("e kind = %q, want end", byID["e"].Kind)
	}
	if len(wf.Edges) != 2 {
		t.Errorf("Edges = %d, want 2: %+v", len(wf.Edges), wf.Edges)
	}
}

func TestParseWorkflow_Decision(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "decision.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byID := indexByID(wf.Nodes)
	if byID["d"].Kind != NodeDecision || byID["d"].Label != "cart empty?" {
		t.Errorf("d = %+v, want decision with label 'cart empty?'", byID["d"])
	}

	var yesEdge *ParsedEdge
	for i := range wf.Edges {
		if wf.Edges[i].Label == "yes" {
			yesEdge = &wf.Edges[i]
			break
		}
	}
	if yesEdge == nil || yesEdge.From != "d" || yesEdge.To != "e" {
		t.Errorf("expected `d -- yes --> e` edge, edges = %+v", wf.Edges)
	}
}

func TestParseWorkflow_BackEdge(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "back-edge-loop.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var back *ParsedEdge
	for i := range wf.Edges {
		if wf.Edges[i].From == "d" && wf.Edges[i].To == "s_a" {
			back = &wf.Edges[i]
			break
		}
	}
	if back == nil || back.Label != "no" {
		t.Errorf("expected back-edge `d -- no --> s_a`, edges = %+v", wf.Edges)
	}
}

func TestParseWorkflow_StartEndOnly(t *testing.T) {
	wf, err := ParseWorkflow(readFixture(t, "start-end-only.mermaid"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Nodes) != 2 || len(wf.Edges) != 1 {
		t.Errorf("Nodes=%d Edges=%d, want 2 and 1", len(wf.Nodes), len(wf.Edges))
	}
}

func TestParseWorkflow_RejectsParallelFanout(t *testing.T) {
	src := "flowchart TD\n  start([start]) --> p{{parallel}}\n  p --> e([end])"
	_, err := ParseWorkflow(src)
	if err == nil {
		t.Fatal("Parse should reject {{...}} (parallel fanout) in PR3 scope")
	}
}

func TestParseWorkflow_MissingHeader(t *testing.T) {
	src := "  start([start]) --> e([end])"
	_, err := ParseWorkflow(src)
	if err == nil || !errors.Is(err, ErrMermaidInvalid) {
		t.Errorf("expected ErrMermaidInvalid, got %v", err)
	}
}

func TestParseWorkflow_UnknownNodeRef(t *testing.T) {
	src := "flowchart TD\n  start([start]) --> phantom"
	_, err := ParseWorkflow(src)
	if err == nil || !errors.Is(err, ErrMermaidInvalid) {
		t.Errorf("expected ErrMermaidInvalid for edge referencing undeclared node, got %v", err)
	}
}

func indexByID(nodes []ParsedNode) map[string]ParsedNode {
	m := make(map[string]ParsedNode, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}
