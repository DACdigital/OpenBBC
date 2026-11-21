package handler

import (
	"context"
	"testing"
)

// TestEvalStoreAdapter_GetBundle_JoinsSkillsUnchanged pins the exported
// bundle bytes (spec skill-tool-version-prompts AC5): every skills_meta entry
// in order, including empty prompts; skill_prompts-only names dropped.
func TestEvalStoreAdapter_GetBundle_JoinsSkillsUnchanged(t *testing.T) {
	db := openTestDBForHandlers(t)
	var agentID, versionID, bareAgentID, bareVersionID string
	if err := db.QueryRow(`INSERT INTO agents (name, architecture) VALUES ('ga', $1::jsonb) RETURNING id::text`,
		`{"metadata":{"agent":"x"},"tools":[{"name":"t1"}],"skills_meta":[{"name":"b","description":"db"},{"name":"a"},{"name":"empty","description":"de"}]}`,
	).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO agent_versions (agent_id, status, prompts) VALUES ($1::uuid, 'READY', $2::jsonb) RETURNING id::text`,
		agentID, `{"main_prompt":"MAIN","skill_prompts":{"a":"PA","b":"PB","orphan":"PO"}}`,
	).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO agents (name, architecture) VALUES ('gb', '{"tools":[]}'::jsonb) RETURNING id::text`).Scan(&bareAgentID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO agent_versions (agent_id, status, prompts) VALUES ($1::uuid, 'READY', '{"main_prompt":"M"}'::jsonb) RETURNING id::text`, bareAgentID).Scan(&bareVersionID); err != nil {
		t.Fatal(err)
	}

	a := &evalStoreAdapter{db: db}
	for _, tc := range []struct{ id, want string }{
		{versionID, `{"metadata":{"agent":"x"},"main_prompt":"MAIN","tools":[{"name":"t1"}],"skills":[{"name":"b","description":"db","prompt":"PB"},{"name":"a","prompt":"PA"},{"name":"empty","description":"de"}]}`},
		{bareVersionID, `{"main_prompt":"M","tools":[]}`},
	} {
		got, err := a.GetBundle(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("GetBundle: %v", err)
		}
		if string(got) != tc.want {
			t.Fatalf("GetBundle =\n%s\nwant\n%s", got, tc.want)
		}
	}
}
