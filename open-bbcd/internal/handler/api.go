package handler

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
	"gopkg.in/yaml.v3"
)

const (
	ReadTimeout  = 10 * time.Second
	WriteTimeout = 30 * time.Second
	IdleTimeout  = 60 * time.Second
)

func NewAPI(db *sql.DB, logger *slog.Logger) http.Handler {
	agentRepo := repository.NewAgentRepository(db)
	resourceRepo := repository.NewResourceRepository(db)

	// Load wizard schema from embedded FS.
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		logger.Error("load wizard schema", slog.Any("error", err))
		panic("load wizard schema: " + err.Error())
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		logger.Error("parse wizard schema", slog.Any("error", err))
		panic("parse wizard schema: " + err.Error())
	}

	uiHandler, err := NewUIHandler(agentRepo, &schema, web.Assets, logger)
	if err != nil {
		logger.Error("init UI handler", slog.Any("error", err))
		panic("init UI handler: " + err.Error())
	}
	wizardHandler := NewWizardHandler(agentRepo, &schema, logger)
	agentHandler := NewAgentHandler(agentRepo, logger)
	resourceHandler := NewResourceHandler(resourceRepo, logger)

	mux := http.NewServeMux()

	// Static files.
	staticFS, err := fs.Sub(web.Assets, "static")
	if err != nil {
		logger.Error("sub static FS", slog.Any("error", err))
		panic("sub static FS: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// UI routes.
	// Catch-all: redirect root to /agents/ui; 404 everything else unmatched.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/agents/ui", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /agents/ui", uiHandler.AgentsPage)
	mux.HandleFunc("GET /agents/edit", uiHandler.EditPage)
	mux.HandleFunc("POST /agents/edit", uiHandler.EditSubmit)
	mux.HandleFunc("GET /agents/new", uiHandler.WizardPage)
	mux.HandleFunc("GET /agents/new/step/{n}", uiHandler.WizardStep)
	mux.HandleFunc("POST /agents/wizard", wizardHandler.Submit)

	// JSON REST API.
	mux.HandleFunc("GET /health", Health)
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{id}", agentHandler.Get) // fixed paths above take precedence
	mux.HandleFunc("GET /agents/{id}/versions", agentHandler.ListVersions)
	mux.HandleFunc("POST /agents/{id}/versions", agentHandler.CreateVersion)
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	return RequestLogger(logger, mux)
}
