package handler

import (
	"database/sql"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
	"gopkg.in/yaml.v3"
)

const (
	ReadTimeout  = 10 * time.Second
	WriteTimeout = 30 * time.Second
	IdleTimeout  = 60 * time.Second
)

func NewAPI(db *sql.DB, store storage.Storage, discoveryCfg config.DiscoveryConfig) http.Handler {
	agentRepo := repository.NewAgentRepository(db)
	resourceRepo := repository.NewResourceRepository(db)

	// Load wizard schema from embedded FS.
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		log.Fatalf("load wizard schema: %v", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		log.Fatalf("parse wizard schema: %v", err)
	}

	uiHandler, err := NewUIHandler(agentRepo, &schema, web.Assets)
	if err != nil {
		log.Fatalf("init UI handler: %v", err)
	}
	maxUploadBytes := int64(discoveryCfg.MaxUploadMB) << 20
	wizardHandler := NewWizardHandler(agentRepo, &schema, store, maxUploadBytes)

	agentHandler := NewAgentHandler(agentRepo)
	resourceHandler := NewResourceHandler(resourceRepo)

	mux := http.NewServeMux()

	staticFS, err := fs.Sub(web.Assets, "static")
	if err != nil {
		log.Fatalf("sub static FS: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/agents/ui", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /agents/ui", uiHandler.AgentsPage)
	mux.HandleFunc("GET /agents/new", uiHandler.WizardPage)
	mux.HandleFunc("GET /agents/new/step/{n}", uiHandler.WizardStep)
	mux.HandleFunc("POST /agents/wizard", wizardHandler.Submit)

	mux.HandleFunc("GET /health", Health)
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{id}", agentHandler.Get)
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	return mux
}
