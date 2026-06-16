package handler

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/anthropic"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/agui"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
	"gopkg.in/yaml.v3"
)

const (
	ReadTimeout  = 10 * time.Second
	WriteTimeout = 30 * time.Second
	IdleTimeout  = 60 * time.Second
)

func NewAPI(db *sql.DB, store storage.Storage, cfg *config.Config, logger *slog.Logger) http.Handler {
	fatal := func(msg string, err error) {
		logger.Error(msg, slog.Any("error", err))
		os.Exit(1)
	}

	agentRepo := repository.NewAgentRepository(db)
	resourceRepo := repository.NewResourceRepository(db)

	// Load wizard schema from embedded FS.
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		fatal("load wizard schema", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		fatal("parse wizard schema", err)
	}

	uiHandler, err := NewUIHandler(agentRepo, &schema, web.Assets, logger)
	if err != nil {
		fatal("init UI handler", err)
	}
	maxUploadBytes := int64(cfg.Discovery.MaxUploadMB) << 20
	wizardHandler := NewWizardHandler(agentRepo, &schema, store, maxUploadBytes, logger)

	configuratorHandler, err := NewConfiguratorHandler(agentRepo, &schema, web.Assets)
	if err != nil {
		fatal("init configurator handler", err)
	}

	agentHandler := NewAgentHandler(agentRepo)
	resourceHandler := NewResourceHandler(resourceRepo)

	chatRepo := repository.NewChatRepository(db)
	llmClient := anthropic.New(cfg.Anthropic)
	toolHandler := tools.NewMockHandler()

	var transportFactory transport.Factory
	switch cfg.Chat.Transport {
	case "agui":
		transportFactory = agui.NewFactory()
	case "jsonl":
		transportFactory = jsonl.NewFactory()
	default:
		fatal("unknown chat transport", fmt.Errorf("%q", cfg.Chat.Transport))
	}

	orchestrator := chat.NewOrchestrator(agentRepo, chatRepo, llmClient, toolHandler, logger)
	orchestrator.Model = cfg.Anthropic.DefaultModel
	orchestrator.MaxTokens = cfg.Anthropic.MaxTokens
	orchestrator.MaxToolRounds = cfg.Chat.MaxToolRounds

	chatHandler, err := NewChatHandler(agentRepo, chatRepo, orchestrator, transportFactory, web.Assets, logger)
	if err != nil {
		fatal("init chat handler", err)
	}

	deployedRepo := repository.NewDeployedRepository(db)
	deployedChatStore := chat.NewDeployedChatStore(deployedRepo)
	deployedOrchestrator := chat.NewOrchestrator(agentRepo, deployedChatStore, llmClient, toolHandler, logger)
	deployedOrchestrator.Model = cfg.Anthropic.DefaultModel
	deployedOrchestrator.MaxTokens = cfg.Anthropic.MaxTokens
	deployedOrchestrator.MaxToolRounds = cfg.Chat.MaxToolRounds

	deployedHandler := NewDeployedHandler(agentRepo, deployedRepo, deployedChatStore, deployedOrchestrator, transportFactory, logger)

	mux := http.NewServeMux()

	staticFS, err := fs.Sub(web.Assets, "static")
	if err != nil {
		fatal("sub static FS", err)
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
	mux.HandleFunc("GET /agents/{id}/configure", configuratorHandler.Index)
	mux.HandleFunc("GET /agents/{id}/configure/flows", configuratorHandler.Flows)
	mux.HandleFunc("GET /agents/{id}/configure/flows/{flowId}", configuratorHandler.Flows)
	mux.HandleFunc("GET /agents/{id}/configure/skills", configuratorHandler.Skills)
	mux.HandleFunc("GET /agents/{id}/configure/skills/{skillId}", configuratorHandler.Skills)
	mux.HandleFunc("GET /agents/{id}/configure/capabilities", configuratorHandler.Capabilities)
	mux.HandleFunc("GET /agents/{id}/configure/capabilities/{capName}", configuratorHandler.Capabilities)
	mux.HandleFunc("GET /agents/{id}/configure/inputs", configuratorHandler.Inputs)
	mux.HandleFunc("POST /agents/{id}/configure/flows/{flowId}/included", configuratorHandler.FlowIncluded)
	mux.HandleFunc("GET /agents/{id}/configure/skills/new", configuratorHandler.SkillNew)
	mux.HandleFunc("POST /agents/{id}/configure/skills", configuratorHandler.SkillCreate)
	mux.HandleFunc("POST /agents/{id}/configure/skills/{skillId}", configuratorHandler.SkillUpdate)
	mux.HandleFunc("DELETE /agents/{id}/configure/skills/{skillId}", configuratorHandler.SkillDelete)
	mux.HandleFunc("POST /agents/{id}/configure/flows/{flowId}/workflow", configuratorHandler.WorkflowUpdate)
	mux.HandleFunc("GET /agents/{id}/configure/finalize", configuratorHandler.FinalizeConfirm)
	mux.HandleFunc("POST /agents/{id}/finalize", configuratorHandler.Finalize)
	mux.HandleFunc("GET /agents/{id}/config.yaml", configuratorHandler.DownloadYAML)

	mux.HandleFunc("GET /health", Health)
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{id}", agentHandler.Get)
	deployHandler := NewDeployHandler(agentRepo)
	mux.HandleFunc("POST /agents/{id}/deploy", deployHandler.Deploy)
	mux.HandleFunc("POST /agents/{id}/undeploy", deployHandler.Undeploy)
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	mux.HandleFunc("POST /agents/{id}/chat/sessions",           chatHandler.NewSession)
	mux.HandleFunc("GET /agents/{id}/chat",                     chatHandler.SessionList)
	mux.HandleFunc("GET /agents/{id}/chat/{session_id}",        chatHandler.ChatView)
	mux.HandleFunc("PATCH /agents/{id}/chat/{session_id}/title", chatHandler.UpdateSessionTitle)
	mux.HandleFunc("POST /agents/{id}/chat/{session_id}/turn",  chatHandler.Turn)

	mux.HandleFunc("POST /deployed/{agent_id}/sessions", deployedHandler.CreateSession)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions", deployedHandler.ListSessions)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions/{session_id}", deployedHandler.GetSession)
	mux.HandleFunc("PATCH /deployed/{agent_id}/sessions/{session_id}/title", deployedHandler.UpdateTitle)
	mux.HandleFunc("DELETE /deployed/{agent_id}/sessions/{session_id}", deployedHandler.DeleteSession)
	mux.HandleFunc("POST /deployed/{agent_id}/sessions/{session_id}/turn", deployedHandler.Turn)

	return RequestLogger(logger, mux)
}
