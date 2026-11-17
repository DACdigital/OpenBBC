package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPI_ModelMetaWiring checks newAPI's wiring for eval-training-model-meta:
// the runtime model reaches the eval and training handlers, and the training
// repository reaches the agent Versions tab. The config has no LLM block, so
// the runtime model is Anthropic.DefaultModel (claude-sonnet-4-6).
func TestAPI_ModelMetaWiring(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newRuntimeAPI(t, db, nil) // no turns are run

	var agentID, parentID, trainedID, datasetID, dvID, evalID, sessionID string
	mustScan := func(dst *string, q string, args ...any) {
		t.Helper()
		if err := db.QueryRow(q, args...).Scan(dst); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustScan(&agentID, `INSERT INTO agents (name) VALUES ('wire-a') RETURNING id::text`)
	mustScan(&parentID, `INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID)
	mustScan(&trainedID, `INSERT INTO agent_versions (agent_id, parent_version_id, status) VALUES ($1::uuid, $2::uuid, 'READY') RETURNING id::text`, agentID, parentID)
	mustScan(&datasetID, `INSERT INTO datasets (name) VALUES ('wire-ds') RETURNING id::text`)
	mustScan(&dvID, `INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID)
	mustScan(&evalID, `INSERT INTO evals (agent_version_id, dataset_version_id, status, score, completed_at, aikdm_meta)
		VALUES ($1::uuid, $2::uuid, 'DONE', 0.5, now(), '{"target_agent_model":"claude-haiku-4-5","judge_model":"claude-haiku-4-5"}'::jsonb)
		RETURNING id::text`, parentID, dvID)
	mustScan(&sessionID, `INSERT INTO training_sessions (source_eval_id, parent_version_id, new_version_id, status, completed_at, training_report)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'DONE', now(), '{"models":{"target_agent_model":"claude-haiku-4-5"}}'::jsonb)
		RETURNING id::text`, evalID, parentID, trainedID)

	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	const notice = "Scored on claude-haiku-4-5; this open-bbcd runs agents on claude-sonnet-4-6."
	if body := get("/evals/" + evalID); !strings.Contains(body, notice) {
		t.Errorf("eval detail missing notice %q", notice)
	}
	if body := get("/training-sessions/" + sessionID); !strings.Contains(body, notice) {
		t.Errorf("training detail missing notice %q", notice)
	}
	link := `Trained on <a href="/training-sessions/` + sessionID + `">claude-haiku-4-5</a>`
	if body := get("/agents/" + agentID + "/configure/versions"); !strings.Contains(body, link) {
		t.Errorf("versions tab missing %q", link)
	}
}
