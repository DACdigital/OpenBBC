package ui

import "encoding/json"

// Models is the display form of aikdm's model and eval-mode metadata. The
// same keys appear in an eval's aikdm_meta (eval-result-v1) and in a
// training report's "models" block (training-report-v1). A missing or
// non-string key is "", which templates render as "—".
type Models struct {
	Teacher       string // teacher_model (training only)
	Judge         string // judge_model
	UserSimulator string // user_simulator_model
	Target        string // target_agent_model
	UserTurnMode  string // user_turn_mode
	HistoryMode   string // history_mode
}

// ParseModels reads the shared keys from a JSON object: an eval's
// aikdm_meta, or training_report.models. Empty, malformed or non-object
// input yields the zero Models. Unknown keys are ignored.
func ParseModels(raw json.RawMessage) Models {
	var m map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return Models{}
	}
	str := func(key string) string {
		s, _ := m[key].(string)
		return s
	}
	return Models{
		Teacher:       str("teacher_model"),
		Judge:         str("judge_model"),
		UserSimulator: str("user_simulator_model"),
		Target:        str("target_agent_model"),
		UserTurnMode:  str("user_turn_mode"),
		HistoryMode:   str("history_mode"),
	}
}

// ParseReportModels reads a stored training_report's "models" block.
// Reports written before aikdm recorded models have no such key and yield
// the zero Models.
func ParseReportModels(report json.RawMessage) Models {
	var r struct {
		Models json.RawMessage `json:"models"`
	}
	if len(report) == 0 || json.Unmarshal(report, &r) != nil {
		return Models{}
	}
	return ParseModels(r.Models)
}
