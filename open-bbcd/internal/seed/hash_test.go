package seed

import (
	"encoding/json"
	"testing"
)

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

// Numbers compare by value, not literal text: Go's exponent form and
// Postgres jsonb's plain form canonicalise identically.
func TestCanonicalJSON_NumberForms(t *testing.T) {
	cases := [][2]string{
		{`{"a":1e-07,"b":1e+21}`, `{"a":0.0000001,"b":1000000000000000000000}`},
		{`{"a":1}`, `{"a":1.0}`},
		{`{"a":[2.50]}`, `{"a":[2.5]}`},
		{`{"a":9007199254740993}`, `{"a":9007199254740993}`},
	}
	for _, c := range cases {
		x, err := CanonicalJSON(json.RawMessage(c[0]))
		if err != nil {
			t.Fatal(err)
		}
		y, err := CanonicalJSON(json.RawMessage(c[1]))
		if err != nil {
			t.Fatal(err)
		}
		if string(x) != string(y) {
			t.Errorf("%s → %s, %s → %s; want equal", c[0], x, c[1], y)
		}
	}
	// Integers beyond float64 precision keep their exact value.
	got, _ := CanonicalJSON(json.RawMessage(`{"a":9007199254740993}`))
	if string(got) != `{"a":9007199254740993}` {
		t.Errorf("big int = %s", got)
	}
}
