package ui

import (
	"encoding/json"
	"testing"
)

func TestParseModels(t *testing.T) {
	full := Models{
		Teacher: "claude-opus-4-7", Judge: "openai/gpt-4o", UserSimulator: "gemini-2.0-flash",
		Target: "claude-haiku-4-5", UserTurnMode: "verbatim", HistoryMode: "reference",
	}
	cases := []struct {
		name string
		raw  string
		want Models
	}{
		{"all keys plus unknown ones", `{"teacher_model":"claude-opus-4-7","judge_model":"openai/gpt-4o","user_simulator_model":"gemini-2.0-flash","target_agent_model":"claude-haiku-4-5","user_turn_mode":"verbatim","history_mode":"reference","duration_seconds":1.5,"aikdm_version":"0.1.0"}`, full},
		{"eval meta has no teacher", `{"judge_model":"j","target_agent_model":"t"}`, Models{Judge: "j", Target: "t"}},
		{"empty object", `{}`, Models{}},
		{"empty input", ``, Models{}},
		{"null", `null`, Models{}},
		{"malformed", `{"judge_model":`, Models{}},
		{"not an object", `["x"]`, Models{}},
		{"non-string value", `{"target_agent_model":42,"judge_model":"j"}`, Models{Judge: "j"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseModels(json.RawMessage(c.raw)); got != c.want {
				t.Fatalf("ParseModels = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseReportModels(t *testing.T) {
	withModels := `{"schema_version":"training-report-v1","final_score":0.7,"models":{"teacher_model":"claude-opus-4-7","target_agent_model":"claude-haiku-4-5"}}`
	if got := ParseReportModels(json.RawMessage(withModels)); got != (Models{Teacher: "claude-opus-4-7", Target: "claude-haiku-4-5"}) {
		t.Fatalf("with models: %+v", got)
	}
	for _, raw := range []string{
		`{"schema_version":"training-report-v1","final_score":0.7}`, // written before models existed
		``,
		`{"models":"nope"}`,
		`not json`,
	} {
		if got := ParseReportModels(json.RawMessage(raw)); got != (Models{}) {
			t.Errorf("ParseReportModels(%q) = %+v, want zero", raw, got)
		}
	}
}
