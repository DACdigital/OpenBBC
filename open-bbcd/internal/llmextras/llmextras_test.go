package llmextras

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// fixture reads testdata/llm-request-extras/<name> from the monorepo root,
// found by walking up from the package directory, so the Go and aikdm suites
// share one copy.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(dir, "testdata", "llm-request-extras", name)
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("testdata/llm-request-extras/%s not found above the package directory", name)
		}
		dir = parent
	}
}

type invalidCase struct {
	Name   string `json:"name"`
	Config string `json:"config"`
	Rule   *int   `json:"rule"`
	Field  string `json:"field"`
}

func TestParse_SharedInvalidFixtures(t *testing.T) {
	var doc struct{ Cases []invalidCase }
	if err := json.Unmarshal(fixture(t, "invalid.json"), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			rules, err := Parse(c.Config)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", c.Config, rules)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, EnvVar+": ") {
				t.Errorf("message %q does not start with %q", msg, EnvVar+": ")
			}
			if c.Rule == nil {
				if strings.Contains(msg, "rule ") {
					t.Errorf("whole-config error %q names a rule", msg)
				}
			} else if want := "rule " + strconv.Itoa(*c.Rule) + ":"; !strings.Contains(msg, want) {
				t.Errorf("message %q does not name %q", msg, want)
			}
			if !strings.Contains(msg, c.Field) {
				t.Errorf("message %q does not name field %q", msg, c.Field)
			}
		})
	}
}

type matchingCase struct {
	Name            string          `json:"name"`
	Patterns        []string        `json:"patterns"`
	Model           *string         `json:"model"`
	Expect          *int            `json:"expect"`
	ExpectByService map[string]*int `json:"expect_by_service"`
}

// loadMatching reads matching.json. The unset-model case needs the real
// service default, so it runs in internal/config.
func loadMatching(t *testing.T) []matchingCase {
	t.Helper()
	var doc struct{ Cases []matchingCase }
	if err := json.Unmarshal(fixture(t, "matching.json"), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Cases
}

func rulesFor(patterns []string) []Rule {
	var rules []Rule
	for i, p := range patterns {
		rules = append(rules, Rule{Index: i, Match: p, Headers: map[string]string{"x-rule": strconv.Itoa(i)}})
	}
	return rules
}

func TestResolve_SharedMatchingFixtures(t *testing.T) {
	for _, c := range loadMatching(t) {
		if c.Model == nil {
			continue // effective-model default: covered in internal/config
		}
		t.Run(c.Name, func(t *testing.T) {
			got := Resolve(rulesFor(c.Patterns), *c.Model)
			switch {
			case c.Expect == nil && got != nil:
				t.Fatalf("Resolve = rule %d, want none", got.Index)
			case c.Expect != nil && got == nil:
				t.Fatalf("Resolve = none, want rule %d", *c.Expect)
			case c.Expect != nil && got.Index != *c.Expect:
				t.Fatalf("Resolve = rule %d, want %d", got.Index, *c.Expect)
			}
		})
	}
}

func TestParse_EmptyMeansNoRules(t *testing.T) {
	for _, raw := range []string{"", "  ", "[]", " [ ] "} {
		rules, err := Parse(raw)
		if err != nil || len(rules) != 0 {
			t.Fatalf("Parse(%q) = %v, %v", raw, rules, err)
		}
	}
}

func TestParse_GLMRule(t *testing.T) {
	rules, err := Parse(`[{"match":"*/glm-*","headers":{"x-bf-passthrough-extra-params":"true"},"body":{"chat_template_kwargs":{"reasoning_effort":"low"}}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v", rules)
	}
	r := rules[0]
	if r.Index != 0 || r.Match != "*/glm-*" || r.Headers["x-bf-passthrough-extra-params"] != "true" {
		t.Fatalf("rule = %+v", r)
	}
	want := map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "low"}}
	if got := r.BodyValues(); !reflect.DeepEqual(got, want) {
		t.Fatalf("BodyValues = %#v", got)
	}
	if !reflect.DeepEqual(r.HeaderNames(), []string{"x-bf-passthrough-extra-params"}) || !reflect.DeepEqual(r.BodyKeys(), []string{"chat_template_kwargs"}) {
		t.Fatalf("names = %v keys = %v", r.HeaderNames(), r.BodyKeys())
	}
}

// Empty headers/body count as absent: a rule with an empty headers object
// and a non-empty body is valid and carries no headers.
func TestParse_EmptyHeadersWithBodyIsValid(t *testing.T) {
	rules, err := Parse(`[{"match":"*","headers":{},"body":{"a":1}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules[0].Headers) != 0 || len(rules[0].BodyKeys()) != 1 {
		t.Fatalf("rule = %+v", rules[0])
	}
}

// BodyValues returns a fresh copy each call so an adapter that mutates the
// map cannot change the next request; numbers keep their exact text.
func TestRule_BodyValuesIsAFreshCopy(t *testing.T) {
	rules, err := Parse(`[{"match":"*","body":{"n":12345678901234567890,"o":{"k":"v"}}}]`)
	if err != nil {
		t.Fatal(err)
	}
	a := rules[0].BodyValues()
	a["o"].(map[string]any)["k"] = "changed"
	b := rules[0].BodyValues()
	if b["o"].(map[string]any)["k"] != "v" {
		t.Fatal("BodyValues shares state between calls")
	}
	out, _ := json.Marshal(b["n"])
	if string(out) != "12345678901234567890" {
		t.Fatalf("number = %s", out)
	}
}

func TestMatch_Basics(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*", "", true},
		{"*", "anything/at/all", true},
		{"", "", true},
		{"", "a", false},
		{"?", "", false},
		{"?", "é", true}, // one character, not one byte
		{"**", "x", true},
		{"*x*x", "axbxcx", true},
		{"a*b*c", "abbbc", true},
		{"a*b*c", "acb", false},
	}
	for _, c := range cases {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.p, c.s, got, c.want)
		}
	}
}

// The startup log names header names and body keys, never their values.
func TestLogRules_NeverLogsValues(t *testing.T) {
	const secret = "sk-live-SECRET-0123456789"
	rules, err := Parse(`[{"match":"*/glm-*","headers":{"x-gateway-token":"` + secret + `"},"body":{"chat_template_kwargs":{"reasoning_effort":"` + secret + `"}}},{"match":"*","body":{"temperature":0.7312345}}]`)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	LogRules(logger, rules, Resolve(rules, "openai/vllm/glm-5.3-flash"))
	out := buf.String()
	if strings.Contains(out, secret) || strings.Contains(out, "0.7312345") {
		t.Fatalf("log leaks a value:\n%s", out)
	}
	for _, want := range []string{
		`msg="llm request extras rule" index=0 match=*/glm-* headers=[x-gateway-token] body_keys=[chat_template_kwargs]`,
		`msg="llm request extras rule" index=1 match=* headers=[] body_keys=[temperature]`,
		`msg="llm request extras active" rule=0 match=*/glm-*`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s:\n%s", want, out)
		}
	}
}

func TestLogRules_NoMatchAndNoRules(t *testing.T) {
	rules, _ := Parse(`[{"match":"*/glm-*","body":{"a":1}}]`)
	var buf bytes.Buffer
	LogRules(slog.New(slog.NewTextHandler(&buf, nil)), rules, nil)
	if !strings.Contains(buf.String(), `msg="llm request extras inactive"`) {
		t.Fatalf("log:\n%s", buf.String())
	}
	buf.Reset()
	LogRules(slog.New(slog.NewTextHandler(&buf, nil)), nil, nil)
	if buf.Len() != 0 {
		t.Fatalf("no rules should log nothing, got:\n%s", buf.String())
	}
}
