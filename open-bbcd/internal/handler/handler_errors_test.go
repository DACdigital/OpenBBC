package handler

import (
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
