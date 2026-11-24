package seed

import (
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func interpSet() *Set {
	return &Set{
		Backends: []Backend{{Name: "b", Kind: "mcp_client", Source: "f.yaml", Config: map[string]any{
			"url":     "https://${HOST}/mcp",
			"literal": "$${X}",
			"n":       3,
			"headers": map[string]any{"A": "${TOKEN}"},
			"list":    []any{"${HOST}", map[string]any{"k": "${HOST}"}},
		}}},
		Versions: []Version{{StaticID: "v", Prompts: map[string]any{"main_prompt": "${HOST}"}}},
		Agents:   []Agent{{StaticID: "a", Architecture: map[string]any{"x": "${HOST}"}}},
	}
}

func TestInterpolate(t *testing.T) {
	s := interpSet()
	tok := "a:b#c\"d\ne"
	if err := Interpolate(s, env(map[string]string{"HOST": "h", "TOKEN": tok})); err != nil {
		t.Fatal(err)
	}
	c := s.Backends[0].Config
	if c["url"] != "https://h/mcp" || c["literal"] != "${X}" || c["n"] != 3 {
		t.Errorf("config = %v", c)
	}
	if c["headers"].(map[string]any)["A"] != tok {
		t.Errorf("token not verbatim: %v", c["headers"])
	}
	l := c["list"].([]any)
	if l[0] != "h" || l[1].(map[string]any)["k"] != "h" {
		t.Errorf("list = %v", l)
	}
	if s.Versions[0].Prompts["main_prompt"] != "${HOST}" || s.Agents[0].Architecture["x"] != "${HOST}" {
		t.Error("non-backend strings were touched")
	}

	err := Interpolate(interpSet(), env(map[string]string{"HOST": "h"}))
	if err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequiredEnv(t *testing.T) {
	got := RequiredEnv(interpSet())
	if want := []string{"HOST", "TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
