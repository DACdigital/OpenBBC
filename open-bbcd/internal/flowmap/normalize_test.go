package flowmap

import (
	"strings"
	"testing"
)

func TestNormalizeMermaid_AlreadyClean_RoundTrips(t *testing.T) {
	src := "flowchart TD\n" +
		"  start([start])\n" +
		"  s_x[list-catalog]\n" +
		"  e([end])\n" +
		"  start --> s_x\n" +
		"  s_x --> e\n"

	out, err := NormalizeMermaid(src)
	if err != nil {
		t.Fatalf("NormalizeMermaid: %v", err)
	}
	wf, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("normalized output should parse cleanly: %v\nout:\n%s", err, out)
	}
	if len(wf.Nodes) != 3 {
		t.Errorf("Nodes = %d, want 3", len(wf.Nodes))
	}
	if len(wf.Edges) != 2 {
		t.Errorf("Edges = %d, want 2", len(wf.Edges))
	}
}

func TestNormalizeMermaid_RewritesPipeLabelToDoubleDash(t *testing.T) {
	src := "flowchart TD\n" +
		"  start([start]) --> g{ok?}\n" +
		"  g -->|yes| s_x[skill-a]\n" +
		"  g -->|no| e([end])\n" +
		"  s_x --> e\n"

	out, err := NormalizeMermaid(src)
	if err != nil {
		t.Fatalf("NormalizeMermaid: %v\nout:\n%s", err, out)
	}
	if strings.Contains(out, "|yes|") || strings.Contains(out, "|no|") {
		t.Errorf("pipe-label syntax should be rewritten away; got:\n%s", out)
	}
	if !strings.Contains(out, "g -- yes --> s_x") {
		t.Errorf("expected `g -- yes --> s_x` in output:\n%s", out)
	}
	if !strings.Contains(out, "g -- no --> e") {
		t.Errorf("expected `g -- no --> e` in output:\n%s", out)
	}
	wf, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("output should parse: %v", err)
	}
	if len(wf.Nodes) != 4 {
		t.Errorf("Nodes = %d, want 4 (start, g, s_x, e)", len(wf.Nodes))
	}
}

func TestNormalizeMermaid_BypassesParallelFanout(t *testing.T) {
	src := "flowchart TD\n" +
		"  start([start]) --> p{{parallel}}\n" +
		"  p --> a[skill-a]\n" +
		"  p --> b[skill-b]\n" +
		"  a --> e([end])\n" +
		"  b --> e\n"

	out, err := NormalizeMermaid(src)
	if err != nil {
		t.Fatalf("NormalizeMermaid: %v\nout:\n%s", err, out)
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		t.Errorf("parallel-fanout shape should be removed; got:\n%s", out)
	}
	wf, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("output should parse: %v", err)
	}
	// Expect nodes: start, a, b, e (parallel p dropped).
	ids := map[string]bool{}
	for _, n := range wf.Nodes {
		ids[n.ID] = true
	}
	if ids["p"] {
		t.Errorf("parallel node `p` should be dropped; nodes: %+v", wf.Nodes)
	}
	for _, want := range []string{"start", "a", "b", "e"} {
		if !ids[want] {
			t.Errorf("missing expected node %q; nodes: %+v", want, wf.Nodes)
		}
	}
	// Expect cross-product edges: start->a, start->b. And original a->e, b->e.
	edge := func(from, to string) bool {
		for _, e := range wf.Edges {
			if e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	for _, want := range [][2]string{{"start", "a"}, {"start", "b"}, {"a", "e"}, {"b", "e"}} {
		if !edge(want[0], want[1]) {
			t.Errorf("missing expected edge %s->%s; edges: %+v", want[0], want[1], wf.Edges)
		}
	}
}

func TestNormalizeMermaid_MiniERPCheckout(t *testing.T) {
	// The actual flow from miniERP discovery; combines pipe-labels AND parallel.
	src := "flowchart TD\n" +
		"  start([start]) --> p{{parallel}}\n" +
		"  p --> s_list_catalog[list-catalog]\n" +
		"  p --> s_list_past_orders[list-past-orders]\n" +
		"  s_list_catalog --> g{user confirms place order?}\n" +
		"  s_list_past_orders --> g\n" +
		"  g -->|yes| s_place_order[place-order]\n" +
		"  g -->|no| e([end])\n" +
		"  s_place_order --> e\n"

	out, err := NormalizeMermaid(src)
	if err != nil {
		t.Fatalf("NormalizeMermaid: %v\nout:\n%s", err, out)
	}
	wf, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("output should parse: %v\nout:\n%s", err, out)
	}
	// All six business nodes preserved; `p` dropped.
	want := []string{"start", "s_list_catalog", "s_list_past_orders", "g", "s_place_order", "e"}
	got := map[string]bool{}
	for _, n := range wf.Nodes {
		got[n.ID] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("missing node %q; got %+v", id, wf.Nodes)
		}
	}
	if got["p"] {
		t.Errorf("parallel node `p` should be dropped")
	}
	// Decision edges are labeled with the narrowed dialect.
	hasLabeled := false
	for _, e := range wf.Edges {
		if e.From == "g" && e.To == "s_place_order" && e.Label == "yes" {
			hasLabeled = true
		}
	}
	if !hasLabeled {
		t.Errorf("expected `g -- yes --> s_place_order`; edges: %+v", wf.Edges)
	}
}
