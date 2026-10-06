package repository

import (
	"errors"
	"testing"

	"github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestTranslateBindingInsertErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"name check", &pq.Error{Code: "23514", Constraint: "agent_version_subagent_name_check"}, errInvalidBindingName},
		{"unique", &pq.Error{Code: "23505", Constraint: "agent_version_subagent_pkey"}, types.ErrBindingConflict},
	}
	for _, c := range cases {
		if got := translateBindingInsertErr(c.err); !errors.Is(got, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Any other check violation (e.g. the self-binding CHECK) passes through.
	other := &pq.Error{Code: "23514", Constraint: "agent_version_subagent_check"}
	if got := translateBindingInsertErr(other); got != error(other) {
		t.Fatalf("other check: got %v, want the original error", got)
	}
}

func TestTranslateVersionFK(t *testing.T) {
	for _, c := range []string{
		"agent_version_subagent_target_version_id_fkey",
		"deployed_sessions_agent_version_id_fkey",
	} {
		if got := translateVersionFK(&pq.Error{Code: "23503", Constraint: c}); !errors.Is(got, types.ErrVersionReferenced) {
			t.Fatalf("%s: got %v, want ErrVersionReferenced", c, got)
		}
	}
	// RESTRICT FKs from training_sessions / evals and non-FK errors pass through.
	for _, e := range []*pq.Error{
		{Code: "23503", Constraint: "training_sessions_parent_version_id_fkey"},
		{Code: "23503", Constraint: "evals_agent_version_id_fkey"},
		{Code: "23505", Constraint: "deployed_sessions_agent_version_id_fkey"},
	} {
		if got := translateVersionFK(e); got != error(e) {
			t.Fatalf("%+v: got %v, want the original error", e, got)
		}
	}
	if translateVersionFK(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
