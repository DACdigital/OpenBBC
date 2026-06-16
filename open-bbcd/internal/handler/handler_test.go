package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestError_DeploySentinels(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"not_deployable", types.ErrAgentNotDeployable, http.StatusConflict},
		{"not_deployed", types.ErrAgentNotDeployed, http.StatusConflict},
		{"user_id_required", types.ErrUserIDRequired, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			Error(rr, tc.err)
			if rr.Code != tc.status {
				t.Fatalf("got %d, want %d", rr.Code, tc.status)
			}
		})
	}
}
