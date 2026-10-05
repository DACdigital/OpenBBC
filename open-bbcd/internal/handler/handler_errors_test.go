package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestError_SessionArtifactStatuses(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{types.ErrEmptyTurn, http.StatusBadRequest},
		{types.ErrPendingArtifactCap, http.StatusConflict},
		{types.ErrArtifactConsumed, http.StatusConflict},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		Error(rec, c.err)
		if rec.Code != c.want {
			t.Errorf("Error(%v) = %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{types.ErrVersionLocked, http.StatusConflict},
		{types.ErrEvalOrTrainingActive, http.StatusConflict},
		{types.ErrBindingConflict, http.StatusConflict},
		{types.ErrVersionReferenced, http.StatusConflict},
		{types.ErrMultiAgentEvalUnsupported, http.StatusConflict},
		{types.ErrToolNameCollision, http.StatusConflict},
		{types.ErrTargetNotRunnable, http.StatusBadRequest},
		{types.ErrTopologyCycle, http.StatusBadRequest},
		{types.ErrNotFound, http.StatusNotFound},
		{types.ErrSessionLocked, http.StatusConflict},
		{types.ErrEmptyTurn, http.StatusBadRequest},
		{types.ErrLLMUnavailable, http.StatusBadGateway},
		{types.ErrSessionAgentMismatch, http.StatusForbidden},
		{errAgentsFormInvalid, http.StatusBadRequest},
		{errAgentsBadForm("enabled must be on or off"), http.StatusBadRequest},
		{errors.New("x"), http.StatusInternalServerError},
		{fmt.Errorf("wrap: %w", types.ErrBindingConflict), http.StatusConflict},
	}
	for _, c := range cases {
		if got := statusFor(c.err); got != c.want {
			t.Errorf("statusFor(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
