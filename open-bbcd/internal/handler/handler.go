package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

type ErrorResponse struct {
	Error string `json:"error"`
}

func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("error encoding response: %v", err)
	}
}

func DecodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return err
	}
	return nil
}

// isValidJobStatus checks the status against the shared PENDING → IN_PROGRESS →
// DONE/FAILED state machine used by both evals and training sessions.
func isValidJobStatus(s string) bool {
	switch s {
	case "PENDING", "IN_PROGRESS", "DONE", "FAILED":
		return true
	}
	return false
}

// ParseListParams reads ?status=&limit= from the request. Empty status is
// allowed (means "no filter"). Non-empty status is validated against the shared
// job state machine. Limit must be a positive int; empty means default. On
// validation failure, writes a JSON 400 to w and returns ok=false.
//
// Repo layer clamps limit to [1, 500]; the handler doesn't reject limit>500.
func ParseListParams(w http.ResponseWriter, r *http.Request) (status string, limit int, ok bool) {
	status = r.URL.Query().Get("status")
	if status != "" && !isValidJobStatus(status) {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid status"})
		return "", 0, false
	}
	limit = 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			JSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid limit"})
			return "", 0, false
		}
		limit = n
	}
	return status, limit, true
}

func Error(w http.ResponseWriter, err error) {
	JSON(w, statusFor(err), ErrorResponse{Error: err.Error()})
}

// statusFor maps a sentinel error (possibly wrapped) to its HTTP status;
// unknown errors are 500.
func statusFor(err error) int {
	status := http.StatusInternalServerError

	switch {
	case errors.Is(err, types.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, types.ErrSkillReferenced),
		errors.Is(err, types.ErrInvalidAgentStatus),
		errors.Is(err, types.ErrBundleAlreadySet),
		errors.Is(err, types.ErrAgentNotRunnable),
		errors.Is(err, types.ErrAgentNotDeployable),
		errors.Is(err, types.ErrAgentNotDeployed),
		errors.Is(err, types.ErrToolBackendInUse),
		errors.Is(err, types.ErrUnmappedEndpoints),
		errors.Is(err, types.ErrAgentInUse),
		errors.Is(err, types.ErrVersionInUse),
		errors.Is(err, types.ErrVersionHasChildren),
		errors.Is(err, types.ErrSessionNoFeedback),
		errors.Is(err, types.ErrSessionAlreadyInDataset),
		errors.Is(err, types.ErrSessionInDataset),
		errors.Is(err, types.ErrSessionLocked),
		errors.Is(err, types.ErrDatasetVersionClosed),
		errors.Is(err, types.ErrEvalNotPending),
		errors.Is(err, types.ErrEvalAlreadyFinal),
		errors.Is(err, types.ErrTrainingSessionConflict),
		errors.Is(err, types.ErrPendingArtifactCap),
		errors.Is(err, types.ErrArtifactConsumed),
		errors.Is(err, types.ErrVersionLocked),
		errors.Is(err, types.ErrEvalOrTrainingActive),
		errors.Is(err, types.ErrBindingConflict),
		errors.Is(err, types.ErrVersionReferenced),
		errors.Is(err, types.ErrMultiAgentEvalUnsupported),
		errors.Is(err, types.ErrToolNameCollision):
		status = http.StatusConflict
	case errors.Is(err, types.ErrSessionAgentMismatch):
		status = http.StatusForbidden
	case errors.Is(err, errAgentsFormInvalid),
		errors.Is(err, types.ErrNameRequired),
		errors.Is(err, types.ErrPromptRequired),
		errors.Is(err, types.ErrAgentRequired),
		errors.Is(err, types.ErrDiscoveryFileRequired),
		errors.Is(err, types.ErrDiscoveryFileTooLarge),
		errors.Is(err, types.ErrDiscoveryFileBadExtension),
		errors.Is(err, types.ErrFlowMapInvalid),
		errors.Is(err, types.ErrCapabilityReadOnly),
		errors.Is(err, types.ErrInvalidSkillRole),
		errors.Is(err, types.ErrCustomSkillNameRequired),
		errors.Is(err, types.ErrUserIDRequired),
		errors.Is(err, types.ErrToolBackendNameRequired),
		errors.Is(err, types.ErrToolBackendKindInvalid),
		errors.Is(err, types.ErrToolBackendNameTaken),
		errors.Is(err, types.ErrAgentNameMismatch),
		errors.Is(err, types.ErrFeedbackNotAssistant),
		errors.Is(err, types.ErrFeedbackCommentRequired),
		errors.Is(err, types.ErrFeedbackCriteriaRequired),
		errors.Is(err, types.ErrDatasetNameRequired),
		errors.Is(err, types.ErrDatasetVersionNotClosed),
		errors.Is(err, types.ErrDatasetMissingCriteria),
		errors.Is(err, types.ErrTrainingSessionEvalNotEligible),
		errors.Is(err, types.ErrEmptyTurn),
		errors.Is(err, types.ErrTargetNotRunnable),
		errors.Is(err, types.ErrTopologyCycle):
		status = http.StatusBadRequest
	case errors.Is(err, types.ErrLLMUnavailable),
		errors.Is(err, types.ErrToolHandlerFailed):
		status = http.StatusBadGateway
	}

	return status
}

// validUUID reports whether id is a canonical 36-char UUID. uuid.Parse also
// accepts urn:uuid:, braced and bare-hex forms; require the canonical length
// so only ids Postgres accepts pass (others would surface as a 500). Shared
// by the BO and deployed path-id checks.
func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}
