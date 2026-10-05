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
