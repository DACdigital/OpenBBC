package seed

import "testing"

func TestCanonicalJSON(t *testing.T) {
	b, err := CanonicalJSON(map[string]any{"b": 1, "a": []any{map[string]any{"z": "<>", "y": 1.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":[{"y":1.5,"z":"<>"}],"b":1}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}

func TestContentHash_OrderInsensitive(t *testing.T) {
	mk := func(mcp []MCPRef, subs []SubAgentRef) string {
		v := Version{Prompts: map[string]any{"main_prompt": "p", "skill_prompts": map[string]any{"b": "1", "a": "2"}}, MCPBackends: mcp, SubAgents: subs}
		c, err := v.Content()
		if err != nil {
			t.Fatal(err)
		}
		return ContentHash(c)
	}
	m1 := []MCPRef{{"a", "n1"}, {"b", "n2"}}
	m2 := []MCPRef{{"b", "n2"}, {"a", "n1"}}
	s1 := []SubAgentRef{{"x", "t1", "n"}, {"y", "t2", "n"}}
	s2 := []SubAgentRef{{"y", "t2", "n"}, {"x", "t1", "n"}}
	h := mk(m1, s1)
	if len(h) != 64 || h != mk(m2, s2) {
		t.Fatal("permutation changed hash")
	}
	if h == mk([]MCPRef{{"a", "changed"}, {"b", "n2"}}, s1) {
		t.Error("changed mcp note did not change hash")
	}
	if h == mk(m1, []SubAgentRef{{"x", "t1", "changed"}, {"y", "t2", "n"}}) {
		t.Error("changed sub-agent note did not change hash")
	}
}
