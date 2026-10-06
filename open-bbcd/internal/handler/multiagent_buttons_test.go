package handler_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Spec § REST — eval and training gate → BO buttons: a version with
// agent_tool_enabled renders Evaluate (and Train, see eval_test.go) disabled
// with the tooltip.
const multiAgentTooltip = "Evaluating multi-agent versions is not supported yet"

// evaluateButton returns the <button …>Evaluate</button> markup in body.
func evaluateButton(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<button[^>]*>Evaluate</button>`).FindString(body)
	if m == "" {
		t.Fatalf("no Evaluate button in body")
	}
	return m
}

func assertEvaluateState(t *testing.T, body string, disabled bool) {
	t.Helper()
	btn := evaluateButton(t, body)
	hasHX := strings.Contains(btn, "hx-get=")
	hasDisabled := strings.Contains(btn, " disabled")
	hasTip := strings.Contains(btn, multiAgentTooltip)
	if disabled && (hasHX || !hasDisabled || !hasTip) {
		t.Errorf("want disabled Evaluate with tooltip, got %s", btn)
	}
	if !disabled && (!hasHX || hasDisabled || hasTip) {
		t.Errorf("want enabled Evaluate, got %s", btn)
	}
}

func TestVersionsTab_EvaluateButton(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := &stubAgentDetailStore{groups: []types.AgentGroup{{
			AgentID: "a1",
			Versions: []types.AgentVersionListItem{{
				Version:    &types.AgentVersion{ID: "v1", AgentID: "a1", Status: "READY", AgentToolEnabled: enabled},
				VersionNum: 1,
			}},
		}}}
		h := newAgentDetailHandler(t, store)
		req := httptest.NewRequest(http.MethodGet, "/agents/a1/configure/versions", nil)
		req.SetPathValue("agent_id", "a1")
		rec := httptest.NewRecorder()
		h.Versions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("enabled=%v: status %d", enabled, rec.Code)
		}
		assertEvaluateState(t, rec.Body.String(), enabled)
	}
}

func TestConfiguratorHeader_EvaluateButton(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := &stubConfigStore{
			cfg: sampleConfig(), currentStatus: "READY",
			architecture: []byte(`{"flows":[]}`), prompts: []byte(`{"main_prompt":"x"}`),
			agentToolEnabled: enabled,
		}
		h := newConfigHandler(t, store)
		req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/inputs", nil)
		req.SetPathValue("version_id", "abc")
		rec := httptest.NewRecorder()
		h.Inputs(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("enabled=%v: status %d", enabled, rec.Code)
		}
		assertEvaluateState(t, rec.Body.String(), enabled)
	}
}
