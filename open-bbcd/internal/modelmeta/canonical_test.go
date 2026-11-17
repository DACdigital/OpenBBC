package modelmeta

import "testing"

func TestCanonicalModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"gpt-4o", "openai/gpt-4o"},
		{"openai-o3", "openai/openai-o3"},
		{"gemini-2.0-flash", "gemini/gemini-2.0-flash"},
		{"anthropic/claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"openai/gpt-4o", "openai/gpt-4o"},
		{"openrouter/anthropic/claude-3.5", "openrouter/anthropic/claude-3.5"},
		{"mistral-large", "mistral-large"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CanonicalModel(c.in); got != c.want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTargetDiffers(t *testing.T) {
	cases := []struct {
		target, runtime string
		want            bool
	}{
		{"claude-haiku-4-5", "anthropic/claude-haiku-4-5", false},
		{"anthropic/claude-haiku-4-5", "claude-haiku-4-5", false},
		{"claude-haiku-4-5", "claude-haiku-4-5", false},
		{"gpt-4o", "openai/gpt-4o", false},
		{"claude-haiku-4-5", "claude-sonnet-4-6", true},
		{"claude-haiku-4-5", "openai/gpt-4o", true},
		{"", "claude-sonnet-4-6", false},
		{"claude-haiku-4-5", "", false},
	}
	for _, c := range cases {
		if got := TargetDiffers(c.target, c.runtime); got != c.want {
			t.Errorf("TargetDiffers(%q, %q) = %v, want %v", c.target, c.runtime, got, c.want)
		}
	}
}

func TestNewView(t *testing.T) {
	m := Models{Target: "claude-haiku-4-5", Judge: "j"}
	if v := NewView(m, "anthropic/claude-haiku-4-5"); v.Mismatch || v.Runtime != "anthropic/claude-haiku-4-5" || v.Models != m {
		t.Fatalf("same model: %+v", v)
	}
	if v := NewView(m, "claude-sonnet-4-6"); !v.Mismatch {
		t.Fatalf("different model: %+v", v)
	}
	if v := NewView(Models{}, "claude-sonnet-4-6"); v.Mismatch {
		t.Fatalf("empty target: %+v", v)
	}
	if v := NewView(m, ""); v.Mismatch {
		t.Fatalf("empty runtime: %+v", v)
	}
}
