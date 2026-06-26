package handler

import (
	"context"
	"database/sql"
	"encoding/json"
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

// configStore satisfies ConfigStore by forwarding methods to AgentVersionRepository.
// After the flow_map_config move (migration 014), every ConfigStore method is
// keyed by version_id and lives on AgentVersionRepository — including
// GetWithAgent, which joins the version's owning agent for display fields.
type configStore struct {
	versions *repository.AgentVersionRepository
}

func (s *configStore) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	return s.versions.GetWithAgent(ctx, versionID)
}

func (s *configStore) GetFlowMapConfig(ctx context.Context, versionID string) ([]byte, string, error) {
	return s.versions.GetFlowMapConfig(ctx, versionID)
}

func (s *configStore) UpdateFlowMapConfig(ctx context.Context, versionID string, cfg []byte) error {
	return s.versions.UpdateFlowMapConfig(ctx, versionID, cfg)
}

func (s *configStore) CreateVersionFromPrompts(ctx context.Context, parentVersionID string, promptsJSON []byte) (string, error) {
	return s.versions.CreateVersionFromPrompts(ctx, parentVersionID, promptsJSON)
}

func (s *configStore) UpdateStatus(ctx context.Context, versionID, expectedFrom, to string) error {
	return s.versions.UpdateStatus(ctx, versionID, expectedFrom, to)
}

func NewAPI(db *sql.DB, store storage.Storage, cfg *config.Config, logger *slog.Logger) http.Handler {
	fatal := func(msg string, err error) {
		logger.Error(msg, slog.Any("error", err))
		os.Exit(1)
	}

	agentRepo := repository.NewAgentRepository(db)
	versionRepo := repository.NewAgentVersionRepository(db)
	resourceRepo := repository.NewResourceRepository(db)
	deployedRepo := repository.NewDeployedRepository(db)

	// Load wizard schema from embedded FS.
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		fatal("load wizard schema", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		fatal("parse wizard schema", err)
	}

	uiHandler, err := NewUIHandler(agentRepo, versionRepo, store, &schema, web.Assets, logger)
	if err != nil {
		fatal("init UI handler", err)
	}
	maxUploadBytes := int64(cfg.Discovery.MaxUploadMB) << 20
	wizardHandler := NewWizardHandler(agentRepo, &schema, store, maxUploadBytes, logger)

	agentHandler := NewAgentHandler(agentRepo)
	resourceHandler := NewResourceHandler(resourceRepo)

	chatRepo := repository.NewChatRepository(db)
	llmClient := anthropic.New(cfg.Anthropic)
	backendRepo := repository.NewToolBackendRepository(db)
	wiringRepo := repository.NewVersionWiringRepository(db)
	agentWiringRepo := repository.NewAgentWiringRepository(db)

	configuratorHandler, err := NewConfiguratorHandler(&configStore{versions: versionRepo}, backendRepo, wiringRepo, agentWiringRepo, &schema, web.Assets)
	if err != nil {
		fatal("init configurator handler", err)
	}

	agentDetailHandler, err := NewAgentDetailHandler(
		&agentDetailStoreAdapter{agents: agentRepo, versions: versionRepo},
		backendRepo, agentWiringRepo, &schema, web.Assets,
	)
	if err != nil {
		fatal("init agent detail handler", err)
	}

	backendsHandler, err := NewBackendsHandler(backendRepo, wiringRepo, web.Assets)
	if err != nil {
		fatal("init backends handler", err)
	}
	// Both BO and deployed orchestrators share the same builder: Builder is
	// stateless (all DB reads happen inside Build, scoped by agent + version).
	builder := tools.NewBuilder(&toolBackendStoreAdapter{
		backend:     backendRepo,
		wiring:      wiringRepo,
		agentWiring: agentWiringRepo,
	})

	var transportFactory transport.Factory
	switch cfg.Chat.Transport {
	case "agui":
		transportFactory = agui.NewFactory()
	case "jsonl":
		transportFactory = jsonl.NewFactory()
	default:
		fatal("unknown chat transport", fmt.Errorf("%q", cfg.Chat.Transport))
	}

	orchestrator := chat.NewOrchestrator(versionRepo, chatRepo, llmClient, builder, logger)
	orchestrator.Model = cfg.Anthropic.DefaultModel
	orchestrator.MaxTokens = cfg.Anthropic.MaxTokens
	orchestrator.MaxToolRounds = cfg.Chat.MaxToolRounds

	chatHandler, err := NewChatHandler(versionRepo, chatRepo, chatRepo, &chatBackendLister{wiring: wiringRepo, agentWiring: agentWiringRepo}, orchestrator, transportFactory, web.Assets, logger)
	if err != nil {
		fatal("init chat handler", err)
	}

	deployedChatStore := chat.NewDeployedChatStore(deployedRepo)
	deployedOrchestrator := chat.NewOrchestrator(versionRepo, deployedChatStore, llmClient, builder, logger)
	deployedOrchestrator.Model = cfg.Anthropic.DefaultModel
	deployedOrchestrator.MaxTokens = cfg.Anthropic.MaxTokens
	deployedOrchestrator.MaxToolRounds = cfg.Chat.MaxToolRounds

	deployedHandler := NewDeployedHandler(versionRepo, deployedRepo, deployedChatStore, deployedOrchestrator, transportFactory, logger)
	deployHandler := NewDeployHandler(agentRepo, versionRepo, agentWiringRepo)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/agents/ui", http.StatusMovedPermanently)
	})

	// UI listing + wizard
	mux.HandleFunc("GET /agents/ui", uiHandler.AgentsPage)
	mux.HandleFunc("GET /agents/new", uiHandler.WizardPage)
	mux.HandleFunc("GET /agents/new/step/{n}", uiHandler.WizardStep)
	mux.HandleFunc("POST /agents/wizard", wizardHandler.Submit)

	// Per-version configurator. Flows / Skills / Endpoints are nested under
	// the Architecture primary tab; Inputs, Finalize, and the YAML download are
	// siblings. RegisterConfiguratorRedirects below 301s the pre-redesign tab
	// URLs to their /architecture/ equivalents.
	mux.HandleFunc("GET /agent_versions/{version_id}/configure", configuratorHandler.Index)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/flows", configuratorHandler.Flows)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/flows/{flowId}", configuratorHandler.Flows)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/skills", configuratorHandler.Skills)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/skills/{skillId}", configuratorHandler.Skills)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/endpoints", configuratorHandler.Endpoints)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/endpoints/{endpointID}", configuratorHandler.Endpoints)
	mux.HandleFunc("POST /agent_versions/{version_id}/endpoints/{endpointID}/backend", configuratorHandler.SetEndpointBackend)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/inputs", configuratorHandler.Inputs)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/prompts", configuratorHandler.Prompts)
	mux.HandleFunc("POST /agent_versions/{version_id}/configure/prompts", configuratorHandler.SavePrompts)
	mux.HandleFunc("POST /agent_versions/{version_id}/configure/architecture/flows/{flowId}/included", configuratorHandler.FlowIncluded)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/skills/new", configuratorHandler.SkillNew)
	mux.HandleFunc("POST /agent_versions/{version_id}/configure/architecture/skills", configuratorHandler.SkillCreate)
	mux.HandleFunc("POST /agent_versions/{version_id}/configure/architecture/skills/{skillId}", configuratorHandler.SkillUpdate)
	mux.HandleFunc("DELETE /agent_versions/{version_id}/configure/architecture/skills/{skillId}", configuratorHandler.SkillDelete)
	mux.HandleFunc("POST /agent_versions/{version_id}/configure/architecture/flows/{flowId}/workflow", configuratorHandler.WorkflowUpdate)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/finalize", configuratorHandler.FinalizeConfirm)
	mux.HandleFunc("POST /agent_versions/{version_id}/finalize", configuratorHandler.Finalize)
	mux.HandleFunc("GET /agent_versions/{version_id}/config.yaml", configuratorHandler.DownloadYAML)
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture/mcp", configuratorHandler.MCPSubtab)
	mux.HandleFunc("POST /agent_versions/{version_id}/architecture/mcp/{backendID}/toggle", configuratorHandler.ToggleMCPBackend)
	mux.HandleFunc("POST /agent_versions/{version_id}/architecture/mcp/{backendID}/note", configuratorHandler.UpdateMCPNote)
	// Convenience alias under the new top-level "MCP" version tab.
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/mcp", configuratorHandler.MCPSubtab)
	RegisterConfiguratorRedirects(mux)

	// Agent-level detail page: tabbed Versions / Inputs / Architecture.
	mux.HandleFunc("GET /agents/{agent_id}/configure", agentDetailHandler.Index)
	mux.HandleFunc("GET /agents/{agent_id}/configure/versions", agentDetailHandler.Versions)
	mux.HandleFunc("GET /agents/{agent_id}/configure/inputs", agentDetailHandler.Inputs)
	mux.HandleFunc("GET /agents/{agent_id}/configure/architecture/{subtab}", agentDetailHandler.Architecture)
	mux.HandleFunc("GET /agents/{agent_id}/configure/architecture/{subtab}/{selectedID}", agentDetailHandler.Architecture)
	mux.HandleFunc("POST /agents/{agent_id}/configure/architecture/endpoints/{endpointID}/backend", agentDetailHandler.SetEndpointBackend)

	// MCP / tool backends CRUD
	mux.HandleFunc("GET /mcp", backendsHandler.List)
	mux.HandleFunc("GET /mcp/new", backendsHandler.New)
	mux.HandleFunc("POST /mcp", backendsHandler.Create)
	mux.HandleFunc("POST /mcp/test", backendsHandler.TestConnection)
	mux.HandleFunc("GET /mcp/{id}", backendsHandler.Edit)
	mux.HandleFunc("POST /mcp/{id}", backendsHandler.Update)
	mux.HandleFunc("POST /mcp/{id}/delete", backendsHandler.Delete)

	// Per-version BO chat
	mux.HandleFunc("POST /agent_versions/{version_id}/chat/sessions", chatHandler.NewSession)
	mux.HandleFunc("GET /agent_versions/{version_id}/chat", chatHandler.SessionList)
	mux.HandleFunc("GET /agent_versions/{version_id}/chat/{session_id}", chatHandler.ChatView)
	mux.HandleFunc("PATCH /agent_versions/{version_id}/chat/{session_id}/title", chatHandler.UpdateSessionTitle)
	mux.HandleFunc("POST /agent_versions/{version_id}/chat/{session_id}/turn", chatHandler.Turn)
	mux.HandleFunc("GET /agent_versions/{version_id}/chat/{session_id}/headers", chatHandler.ShowHeaderOverridesModal)
	mux.HandleFunc("POST /agent_versions/{version_id}/chat/{session_id}/headers", chatHandler.UpdateHeaderOverrides)

	// Per-agent deploy/undeploy + confirm modals
	mux.HandleFunc("POST /agents/{agent_id}/deploy", deployHandler.Deploy)
	mux.HandleFunc("POST /agents/{agent_id}/undeploy", deployHandler.Undeploy)
	mux.HandleFunc("GET /agents/{agent_id}/discovery", uiHandler.DiscoveryDownload)
	mux.HandleFunc("GET /agents/{agent_id}/deploy/confirm", uiHandler.DeployConfirm)
	mux.HandleFunc("GET /agents/{agent_id}/undeploy/confirm", uiHandler.UndeployConfirm)

	// JSON API (per-agent)
	mux.HandleFunc("GET /health", Health)
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{agent_id}", agentHandler.Get)
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	// Deployed runtime (unchanged URLs)
	mux.HandleFunc("POST /deployed/{agent_id}/sessions", deployedHandler.CreateSession)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions", deployedHandler.ListSessions)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions/{session_id}", deployedHandler.GetSession)
	mux.HandleFunc("PATCH /deployed/{agent_id}/sessions/{session_id}/title", deployedHandler.UpdateTitle)
	mux.HandleFunc("DELETE /deployed/{agent_id}/sessions/{session_id}", deployedHandler.DeleteSession)
	mux.HandleFunc("POST /deployed/{agent_id}/sessions/{session_id}/turn", deployedHandler.Turn)

	// Static
	staticFS, err := fs.Sub(web.Assets, "static")
	if err != nil {
		fatal("sub static FS", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	return RequestLogger(logger, mux)
}

// toolBackendStoreAdapter implements tools.BackendStore by delegating to the
// repository types. Endpoint wiring is agent-keyed (agentWiring); MCP
// attachments remain per-version (wiring).
type toolBackendStoreAdapter struct {
	backend     *repository.ToolBackendRepository
	wiring      *repository.VersionWiringRepository
	agentWiring *repository.AgentWiringRepository
}

func (a *toolBackendStoreAdapter) GetBackend(ctx context.Context, id string) (string, string, json.RawMessage, error) {
	be, err := a.backend.Get(ctx, id)
	if err != nil {
		return "", "", nil, err
	}
	return string(be.Kind), be.Name, be.Config, nil
}

func (a *toolBackendStoreAdapter) EndpointBackends(ctx context.Context, agentID string) (map[string]string, error) {
	return a.agentWiring.ListEndpointBackends(ctx, agentID)
}

func (a *toolBackendStoreAdapter) MCPAttachments(ctx context.Context, versionID string) (map[string]string, error) {
	atts, err := a.wiring.ListMCPAttachments(ctx, versionID)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, att := range atts {
		m[att.BackendID] = att.Note
	}
	return m, nil
}

// chatBackendLister implements VersionBackendLister. Mixes the two wiring
// repos because the chat header-overrides modal needs ListBackendsForVersion
// (per-version union across HTTP+MCP) AND ListEndpointBackends keyed by
// agent for the unmapped-endpoints banner.
type chatBackendLister struct {
	wiring      *repository.VersionWiringRepository
	agentWiring *repository.AgentWiringRepository
}

func (c *chatBackendLister) ListBackendsForVersion(ctx context.Context, versionID string) ([]*types.ToolBackend, error) {
	return c.wiring.ListBackendsForVersion(ctx, versionID)
}
func (c *chatBackendLister) ListEndpointBackends(ctx context.Context, agentID string) (map[string]string, error) {
	return c.agentWiring.ListEndpointBackends(ctx, agentID)
}

// agentDetailStoreAdapter implements AgentDetailStore by forwarding to the
// underlying repos. Lives here so handler/agent_detail.go doesn't depend on
// repository types.
type agentDetailStoreAdapter struct {
	agents   *repository.AgentRepository
	versions *repository.AgentVersionRepository
}

func (a *agentDetailStoreAdapter) GetByID(ctx context.Context, agentID string) (*types.Agent, error) {
	return a.agents.GetByID(ctx, agentID)
}
func (a *agentDetailStoreAdapter) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return a.agents.ListGrouped(ctx)
}
func (a *agentDetailStoreAdapter) GetFlowMapConfigForAgent(ctx context.Context, agentID string) ([]byte, string, error) {
	return a.versions.GetFlowMapConfigForAgent(ctx, agentID)
}
