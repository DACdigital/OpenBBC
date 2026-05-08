package flowmap

import (
	"testing"
)

func TestValidateWorkflowSkillRefs(t *testing.T) {
	skills := map[string]struct{}{"place-order": {}, "read-self-profile": {}}

	tests := []struct {
		name    string
		mermaid string
		wantErr bool
	}{
		{
			name: "happy path with valid skill nodes",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_a[place-order]\n" +
				"  s_a --> e([end])",
			wantErr: false,
		},
		{
			name: "skill node references unknown skill",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_a[ghost-skill]\n" +
				"  s_a --> e([end])",
			wantErr: true,
		},
		{
			name: "non-skill nodes are not validated against the skill set",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> d{cart empty?}\n" +
				"  d -- no --> s_a[place-order]\n" +
				"  d -- yes --> e([end])\n" +
				"  s_a --> e",
			wantErr: false,
		},
		{
			name: "linear fallback shape",
			mermaid: "flowchart TD\n" +
				"  start([start]) --> s_place_order[place-order]\n" +
				"  s_place_order --> e([end])",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflowSkillRefs(tc.mermaid, skills)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
