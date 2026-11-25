package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"

	"database/sql"
)

// seedStaticAgent creates agent "sales" with one DRAFT version "sales_v1".
// With deploy, the version is READY and deployed.
func seedStaticAgent(t *testing.T, db *sql.DB, deploy bool) (agentID, versionID string) {
	t.Helper()
	ctx := context.Background()
	agent, version, err := repository.NewAgentRepository(db).CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "sales-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET static_id='sales' WHERE id=$1`, agent.ID); err != nil {
		t.Fatalf("agent static_id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_versions SET static_id='sales_v1', status='READY' WHERE id=$1`, version.ID); err != nil {
		t.Fatalf("version static_id: %v", err)
	}
	if deploy {
		if _, err := repository.NewAgentVersionRepository(db).Deploy(ctx, version.ID); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	}
	return agent.ID, version.ID
}

func newResolveAPI(t *testing.T, db *sql.DB) http.Handler {
	t.Helper()
	return NewAPI(db, &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096},
		Chat:      config.ChatConfig{Transport: "jsonl", MaxToolRounds: 10},
	}, testLogger())
}

func do(api http.Handler, method, target, ct, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	return rr
}

func TestDeploy_ByStaticIDs(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	agentID, versionID := seedStaticAgent(t, db, false)

	rr := do(api, "POST", "/agents/sales/deploy", "application/json", `{"version_id":"sales_v1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	var resp deployResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Version.Status != "DEPLOYED" || resp.Version.ID != versionID || resp.Agent.ID != agentID {
		t.Fatalf("unexpected response: %+v / %+v", resp.Agent, resp.Version)
	}
}

func TestUndeploy_ByStaticID(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	seedStaticAgent(t, db, true)

	rr := do(api, "POST", "/agents/sales/undeploy", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agent_versions WHERE static_id='sales_v1'`).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestDeployed_SessionsSameViaStaticAndUUID(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	agentID, _ := seedStaticAgent(t, db, true)

	rr := do(api, "POST", "/deployed/sales/sessions?user_id=u", "application/json", `{"user_id":"u"}`)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created types.DeployedSession
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.AgentID != agentID {
		t.Fatalf("session agent %q, want resolved UUID %q", created.AgentID, agentID)
	}

	byUUID := do(api, "GET", "/deployed/"+agentID+"/sessions/"+created.ID+"?user_id=u", "", "")
	byStatic := do(api, "GET", "/deployed/sales/sessions/"+created.ID+"?user_id=u", "", "")
	if byUUID.Code != http.StatusOK || byStatic.Code != http.StatusOK {
		t.Fatalf("uuid=%d static=%d", byUUID.Code, byStatic.Code)
	}
	if byUUID.Body.String() != byStatic.Body.String() {
		t.Fatalf("bodies differ:\n%s\n%s", byUUID.Body.String(), byStatic.Body.String())
	}

	// Created via UUID, read via static ID.
	rr = do(api, "POST", "/deployed/"+agentID+"/sessions", "application/json", `{"user_id":"u"}`)
	var second types.DeployedSession
	_ = json.Unmarshal(rr.Body.Bytes(), &second)
	if g := do(api, "GET", "/deployed/sales/sessions/"+second.ID+"?user_id=u", "", ""); g.Code != http.StatusOK {
		t.Fatalf("reverse: %d %s", g.Code, g.Body.String())
	}
}

func TestResolve_UpperCaseUUID(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	agentID, versionID := seedStaticAgent(t, db, false)
	upperAgent, upperVersion := strings.ToUpper(agentID), strings.ToUpper(versionID)

	rr := do(api, "POST", "/agents/"+upperAgent+"/deploy", "application/json", `{"version_id":"`+upperVersion+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rr.Code, rr.Body.String())
	}
	var resp deployResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Agent.ID != agentID || resp.Version.ID != versionID {
		t.Fatalf("not canonical lower-case: %s / %s", resp.Agent.ID, resp.Version.ID)
	}

	if rr := do(api, "POST", "/deployed/"+upperAgent+"/sessions", "application/json", `{"user_id":"u"}`); rr.Code >= 300 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(api, "GET", "/deployed/"+upperAgent+"/sessions?user_id=u", "", "")
	lower := do(api, "GET", "/deployed/"+agentID+"/sessions?user_id=u", "", "")
	if rr.Code != http.StatusOK || rr.Body.String() != lower.Body.String() {
		t.Fatalf("upper=%d %s lower=%d %s", rr.Code, rr.Body.String(), lower.Code, lower.Body.String())
	}
	if !strings.Contains(rr.Body.String(), agentID) {
		t.Fatalf("list should contain the session under the canonical agent id: %s", rr.Body.String())
	}

	// 32-hex dashless form resolves too.
	dashless := strings.ReplaceAll(agentID, "-", "")
	if rr := do(api, "GET", "/deployed/"+dashless+"/sessions?user_id=u", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("dashless: %d %s", rr.Code, rr.Body.String())
	}
}

func TestResolve_UnknownStaticID404(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	seedStaticAgent(t, db, true)

	for _, tc := range []struct{ method, target, ct, body string }{
		{"POST", "/agents/nope/deploy", "application/json", `{"version_id":"sales_v1"}`},
		{"POST", "/agents/sales/deploy", "application/json", `{"version_id":"nope_v1"}`},
		{"POST", "/agents/nope/undeploy", "", ""},
		{"GET", "/deployed/nope/sessions?user_id=u", "", ""},
	} {
		if rr := do(api, tc.method, tc.target, tc.ct, tc.body); rr.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404 (%s)", tc.method, tc.target, rr.Code, rr.Body.String())
		}
	}
}
