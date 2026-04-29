package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
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
		slog.Error("encode response", slog.Any("error", err))
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
	case errors.Is(err, types.ErrNameRequired),
		errors.Is(err, types.ErrPromptRequired),
		errors.Is(err, types.ErrAgentRequired):
		status = http.StatusBadRequest
	}

	JSON(w, status, ErrorResponse{Error: err.Error()})
}
