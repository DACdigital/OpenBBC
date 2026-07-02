package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
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

func Error(w http.ResponseWriter, err error) {
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
		errors.Is(err, types.ErrEvalAlreadyFinal):
		status = http.StatusConflict
	case errors.Is(err, types.ErrSessionAgentMismatch):
		status = http.StatusForbidden
	case errors.Is(err, types.ErrNameRequired),
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
		errors.Is(err, types.ErrDatasetNameRequired),
		errors.Is(err, types.ErrDatasetVersionNotClosed),
		errors.Is(err, types.ErrDatasetMissingCriteria):
		status = http.StatusBadRequest
	case errors.Is(err, types.ErrLLMUnavailable),
		errors.Is(err, types.ErrToolHandlerFailed):
		status = http.StatusBadGateway
	}

	JSON(w, status, ErrorResponse{Error: err.Error()})
}
