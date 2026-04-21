package handler

import (
	"net/http"
)

type HealthResponse struct {
	Status string `json:"status"`
}

func Health(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, HealthResponse{Status: "ok"})
}
