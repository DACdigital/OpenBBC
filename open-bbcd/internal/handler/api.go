package handler

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
)

// Server timeouts
const (
	ReadTimeout  = 10 * time.Second
	WriteTimeout = 30 * time.Second
	IdleTimeout  = 60 * time.Second
)

// NewAPI creates and returns an http.Handler with all routes registered.
// main.go should only start this server, not be aware of individual endpoints.
func NewAPI(db *sql.DB) http.Handler {
	agentRepo := repository.NewAgentRepository(db)
	resourceRepo := repository.NewResourceRepository(db)

	agentHandler := NewAgentHandler(agentRepo)
	resourceHandler := NewResourceHandler(resourceRepo)

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", Health)

	// Agents
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{id}", agentHandler.Get)

	// Resources
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	return mux
}
