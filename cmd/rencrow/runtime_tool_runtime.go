package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	browseractorinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/browseractor"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	toolharnesspersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolharness"
	securityinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type toolRuntime struct {
	ChatRunnerV2                  *tools.ToolRunner
	WorkerRunnerV2                *tools.ToolRunner
	ChatRuntimeRunnerV2           domaintool.RunnerV2
	WorkerRuntimeRunnerV2         domaintool.RunnerV2
	MovieCatalogLookup            *runtimeMovieCatalogLookup
	MusicCatalogLookup            *runtimeMusicCatalogLookup
	PersonRelatedCatalogLookup    *runtimePersonRelatedCatalogLookup
	PersonRelatedCollectionWorker *runtimePersonRelatedCollectionWorker
	PersonRelatedSummaryWorker    *runtimePersonRelatedSummaryWorker
	PersonRelatedIdentityWorker   *runtimePersonRelatedIdentityWorker
	SubagentMgr                   *subagent.Manager
	ToolMediationRecorder         *toolharnesspersistence.JSONLRecorder
	DataCapabilityCatalog         *runtimeDataCapabilityCatalog
	DataRecallRegistry            *runtimeDataRecallRegistry
	DataWriteRegistry             *runtimeDataWriteRegistry
	KnowledgeMemoryToolStore      interface{ Close() error }
}

func buildToolRuntime(
	cfg *config.Config,
	workerToolProvider llm.ToolCallingProvider,
	runtimeToolRegistry capdomain.ToolRegistry,
	contextBudgetRecorder tools.ContextBudgetUsageRecorder,
	skillCatalog ...*tools.SkillCatalog,
) toolRuntime {
	var workerSkillCatalog *tools.SkillCatalog
	if len(skillCatalog) > 0 {
		workerSkillCatalog = skillCatalog[0]
	}
	return buildToolRuntimeWithCapabilities(
		cfg,
		workerToolProvider,
		runtimeToolRegistry,
		contextBudgetRecorder,
		workerSkillCatalog,
		nil,
	)
}

func buildToolRuntimeWithCapabilities(
	cfg *config.Config,
	workerToolProvider llm.ToolCallingProvider,
	runtimeToolRegistry capdomain.ToolRegistry,
	contextBudgetRecorder tools.ContextBudgetUsageRecorder,
	workerSkillCatalog *tools.SkillCatalog,
	mcpToolCatalog *tools.MCPToolCatalog,
) toolRuntime {
	dataRecallRegistry := newRuntimeDataRecallRegistry()
	dataWriteRegistry := newRuntimeDataWriteRegistry()
	personaWritePaths := []string{
		filepath.Join(cfg.WorkspaceDir, "persona", "mio.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "shiro.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "aka.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "ao.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "gin.md"),
		filepath.Join(cfg.WorkspaceDir, "persona", "kin.md"),
	}
	toolMediationRecorder := buildToolMediationRecorder(cfg)
	movieCatalogPrepareCtx, cancelMovieCatalogPrepare := context.WithTimeout(context.Background(), 10*time.Second)
	movieCatalogLookup, movieCatalogLookupErr := prepareRuntimeMovieCatalogLookup(
		movieCatalogPrepareCtx, cfg.Storage.Databases.MovieCatalog,
	)
	cancelMovieCatalogPrepare()
	if movieCatalogLookupErr != nil {
		log.Printf("Movie catalog lookup Tool unavailable: %v", movieCatalogLookupErr)
	} else {
		log.Printf("Movie catalog lookup Tool ready (indexed read-only execution)")
	}
	musicCatalogPrepareCtx, cancelMusicCatalogPrepare := context.WithTimeout(context.Background(), 10*time.Second)
	musicCatalogLookup, musicCatalogLookupErr := prepareRuntimeMusicCatalogLookup(
		musicCatalogPrepareCtx, cfg.Storage.Databases.HobbyGraph,
	)
	cancelMusicCatalogPrepare()
	if musicCatalogLookupErr != nil {
		log.Printf("Music and lyrics catalog lookup Tools unavailable: %v", musicCatalogLookupErr)
	} else {
		log.Printf("Music and lyrics catalog lookup Tools ready (rights-aware indexed read-only execution)")
	}
	personRelatedPrepareCtx, cancelPersonRelatedPrepare := context.WithTimeout(context.Background(), 10*time.Second)
	personRelatedCatalogLookup, personRelatedCatalogLookupErr := prepareRuntimePersonRelatedCatalogLookup(
		personRelatedPrepareCtx,
		cfg.Storage.Databases.MovieCatalog,
		cfg.Storage.Databases.HobbyGraph,
	)
	cancelPersonRelatedPrepare()
	if personRelatedCatalogLookupErr != nil {
		log.Printf("Person related catalog lookup Tool unavailable: %v", personRelatedCatalogLookupErr)
	} else {
		log.Printf("Person related catalog lookup Tool ready (indexed read-only execution)")
	}
	var personRelatedCatalogCollector *runtimePersonRelatedCatalogCollector
	var personRelatedCollectionWorker *runtimePersonRelatedCollectionWorker
	var personRelatedSummaryWorker *runtimePersonRelatedSummaryWorker
	var personRelatedIdentityWorker *runtimePersonRelatedIdentityWorker
	if personRelatedCatalogLookup != nil {
		providerURL := strings.TrimSpace(cfg.PersonRelatedCatalog.ProviderURL)
		if strings.TrimSpace(providerURL) != "" {
			personRelatedCollectPrepareCtx, cancelPersonRelatedCollectPrepare := context.WithTimeout(context.Background(), 10*time.Second)
			var personRelatedCollectErr error
			personRelatedCatalogCollector, personRelatedCollectErr = prepareRuntimePersonRelatedCatalogCollector(
				personRelatedCollectPrepareCtx,
				cfg.Storage.Databases.MovieCatalog,
				cfg.Storage.Databases.HobbyGraph,
				providerURL,
			)
			cancelPersonRelatedCollectPrepare()
			if personRelatedCollectErr != nil {
				log.Printf("Person related catalog collect Tool unavailable: %v", personRelatedCollectErr)
			} else {
				log.Printf("Person related catalog collect Tool ready (Worker-only provider collection)")
				personRelatedCollectionWorker, personRelatedCollectErr = prepareRuntimePersonRelatedCollectionWorker(personRelatedCatalogCollector, time.Now)
				if personRelatedCollectErr != nil {
					log.Printf("Person related D1 collection worker unavailable: %v", personRelatedCollectErr)
				} else {
					log.Printf("Person related D1 collection worker ready (positive movie/person one-hop sweep)")
				}
			}
		} else {
			log.Printf("Person related catalog collect Tool unavailable: provider URL is not configured")
		}
	}
	if personRelatedCatalogLookup != nil && cfg.PersonRelatedCatalog.SummaryWorker.IsEnabled() {
		providerURL := strings.TrimSpace(cfg.PersonRelatedCatalog.ProviderURL)
		if strings.TrimSpace(providerURL) != "" {
			personRelatedSummaryWorker, personRelatedCatalogLookupErr = prepareRuntimePersonRelatedSummaryWorker(
				cfg.Storage.Databases.HobbyGraph,
				personrelatedcatalogapp.NewHTTPSummaryCollector(providerURL, 90*time.Second),
				cfg.PersonRelatedCatalog.SummaryWorker,
				time.Now,
			)
			if personRelatedCatalogLookupErr != nil {
				log.Printf("Person related summary worker unavailable: %v", personRelatedCatalogLookupErr)
				personRelatedSummaryWorker = nil
			} else {
				personRelatedSummaryWorker.translator = runtimePersonRelatedSummaryTranslator{provider: workerToolProvider}
				log.Printf("Person related summary worker ready (fixed-id bounded enrichment)")
			}
		}
	}
	if personRelatedCatalogLookup != nil && cfg.PersonRelatedCatalog.IdentityMapping.IsEnabled() {
		providerURL := strings.TrimSpace(cfg.PersonRelatedCatalog.ProviderURL)
		if strings.TrimSpace(providerURL) != "" {
			identityResolver := personrelatedcatalogapp.NewHTTPIdentityResolver(providerURL, 90*time.Second)
			personRelatedIdentityWorker, personRelatedCatalogLookupErr = prepareRuntimePersonRelatedIdentityWorker(
				cfg.Storage.Databases.MovieCatalog,
				cfg.Storage.Databases.HobbyGraph,
				identityResolver,
				cfg.PersonRelatedCatalog.IdentityMapping,
				time.Now,
			)
			if personRelatedCatalogLookupErr != nil {
				log.Printf("Person related identity worker unavailable: %v", personRelatedCatalogLookupErr)
				personRelatedIdentityWorker = nil
			} else {
				log.Printf("Person related identity worker ready (fixed-authority bounded resolver)")
			}
		} else {
			log.Printf("Person related identity worker unavailable: provider URL is not configured")
		}
	}
	glossaryLookup, glossaryLookupErr := prepareRuntimeGlossaryLookup(context.Background(), cfg.Storage.Databases.Glossary)
	if glossaryLookupErr != nil {
		log.Printf("Glossary lookup Tool unavailable")
	} else {
		log.Printf("Glossary lookup Tool ready (indexed read-only execution)")
	}
	var knowledgeMemorySearcher tools.KnowledgeMemorySearcher
	var knowledgeMemoryToolStore interface{ Close() error }
	knowledgeMemorySearchReady := false
	knowledgeMemoryState := &datacapability.KnowledgeMemoryState{}
	if cfg != nil && cfg.KnowledgeMemory.IsEnabled() && strings.EqualFold(strings.TrimSpace(cfg.KnowledgeMemory.Storage), "sqlite") {
		knowledgeMemoryState.Configured = true
		path := strings.TrimSpace(cfg.Storage.Databases.KnowledgeMemory)
		if path == "" {
			path = strings.TrimSpace(cfg.KnowledgeMemory.SQLitePath)
		}
		if path == "" {
			knowledgeMemoryState.Configured = false
		} else {
			store, err := knowledgememorypersistence.OpenSQLiteStore(path)
			if err != nil {
				log.Printf("Knowledge Memory indexed Tool unavailable: %v", err)
			} else {
				readiness, readinessErr := store.Readiness(context.Background())
				knowledgeMemoryState.DatabaseAvailable = readiness.DatabaseAvailable
				knowledgeMemoryState.SchemaReady = readiness.SchemaReady
				knowledgeMemoryState.IndexReady = readiness.IndexReady
				knowledgeMemoryState.CoverageState = readiness.Coverage.State
				knowledgeMemoryState.IntegrityState = readiness.IntegrityState
				if readinessErr != nil {
					log.Printf("Knowledge Memory indexed Tool unavailable: readiness check failed: %v", readinessErr)
					_ = store.Close()
				} else if readiness.DatabaseAvailable && readiness.SchemaReady && readiness.IndexReady && readiness.Coverage.State == knowledgememorypersistence.KnowledgeMemoryCoverageReady && readiness.IntegrityState == knowledgememorypersistence.KnowledgeMemoryIntegrityReady {
					knowledgeMemorySearcher = store
					knowledgeMemoryToolStore = store
					knowledgeMemorySearchReady = true
					knowledgeMemoryState.ToolReady = true
					knowledgeMemoryState.PrivateScopeReady = strings.TrimSpace(cfg.Line.ChannelSecret) != "" && strings.TrimSpace(cfg.Line.AccessToken) != ""
					log.Printf("Knowledge Memory indexed Tool ready (SQLite named-index execution)")
				} else {
					_ = store.Close()
					log.Printf("Knowledge Memory indexed Tool unavailable: promotion gate is not ready")
				}
			}
		}
	}
	dataCapabilityCatalog := buildRuntimeDataCapabilityCatalogWithKnowledgeState(cfg, glossaryLookup != nil, movieCatalogLookup != nil, []bool{personRelatedCatalogLookup != nil}, knowledgeMemoryState)
	dataCapabilityCatalog.BindRouteRegistries(dataRecallRegistry, dataWriteRegistry)
	chatToolRunnerCfg := tools.ToolRunnerConfig{
		GoogleAPIKey:               googleSearchValue(cfg.GoogleSearchChat.APIKey, "GOOGLE_API_KEY_CHAT"),
		GoogleSearchEngineID:       googleSearchValue(cfg.GoogleSearchChat.SearchEngineID, "GOOGLE_SEARCH_ENGINE_ID_CHAT"),
		AllowedWritePaths:          personaWritePaths,
		DisableToolHarness:         true,
		DataCapabilityCatalog:      dataCapabilityCatalog,
		KnowledgeMemorySearcher:    knowledgeMemorySearcher,
		KnowledgeMemorySearchReady: knowledgeMemorySearchReady,
	}
	workerToolRunnerCfg := tools.ToolRunnerConfig{
		GoogleAPIKey:               googleSearchValue(cfg.GoogleSearchWorker.APIKey, "GOOGLE_API_KEY_WORKER"),
		GoogleSearchEngineID:       googleSearchValue(cfg.GoogleSearchWorker.SearchEngineID, "GOOGLE_SEARCH_ENGINE_ID_WORKER"),
		ToolRegistry:               runtimeToolRegistry,
		WorkspaceDir:               cfg.WorkspaceDir,
		SkillCatalog:               workerSkillCatalog,
		MCPToolCatalog:             mcpToolCatalog,
		DisableToolHarness:         true,
		DataCapabilityCatalog:      dataCapabilityCatalog,
		OperationalDataRecall:      dataRecallRegistry,
		OperationalDataWrite:       dataWriteRegistry,
		KnowledgeMemorySearcher:    knowledgeMemorySearcher,
		KnowledgeMemorySearchReady: knowledgeMemorySearchReady,
	}
	if movieCatalogLookup != nil {
		chatToolRunnerCfg.MovieCatalogLookup = movieCatalogLookup
		workerToolRunnerCfg.MovieCatalogLookup = movieCatalogLookup
	}
	if musicCatalogLookup != nil {
		chatToolRunnerCfg.MusicCatalogLookup = musicCatalogLookup
		chatToolRunnerCfg.LyricsCatalogLookup = musicCatalogLookup
		workerToolRunnerCfg.MusicCatalogLookup = musicCatalogLookup
		workerToolRunnerCfg.LyricsCatalogLookup = musicCatalogLookup
	}
	if personRelatedCatalogLookup != nil {
		chatToolRunnerCfg.PersonRelatedCatalogLookup = personRelatedCatalogLookup
		workerToolRunnerCfg.PersonRelatedCatalogLookup = personRelatedCatalogLookup
	}
	if personRelatedCatalogCollector != nil {
		workerToolRunnerCfg.PersonRelatedCatalogCollector = personRelatedCatalogCollector
	}
	if glossaryLookup != nil {
		chatToolRunnerCfg.GlossaryLookup = glossaryLookup
		workerToolRunnerCfg.GlossaryLookup = glossaryLookup
	}
	if cfg.BrowserActor.Enabled {
		workerToolRunnerCfg.BrowserActorRunner = browseractorinfra.NewRunner(browserActorConfigFromRuntime(cfg.BrowserActor))
	}
	if cfg.Codex.Enabled {
		workingDir := cfg.Codex.WorkingDir
		if workingDir == "" {
			workingDir = cfg.SelfSourceDir
		}
		workerToolRunnerCfg.CodexRunner = tools.NewCodexExecRunner(
			cfg.Codex.Command,
			workingDir,
			cfg.Codex.Sandbox,
			cfg.Codex.Model,
			time.Duration(cfg.Codex.TimeoutMS)*time.Millisecond,
			cfg.Codex.MaxPromptBytes,
			cfg.Codex.MaxOutputBytes,
			cfg.Codex.EphemeralEnabled(),
		)
		log.Printf("Codex runner enabled (sandbox=%s working_dir=%s)", cfg.Codex.Sandbox, workingDir)
	}

	chatToolRunnerV2 := tools.NewToolRunner(chatToolRunnerCfg)
	workerToolRunnerV2 := tools.NewToolRunner(workerToolRunnerCfg)

	var chatRunnerV2 domaintool.RunnerV2 = chatToolRunnerV2
	var workerRunnerV2 domaintool.RunnerV2 = workerToolRunnerV2
	if runtimeToolRegistry != nil {
		workerRunnerV2 = tools.NewCompositeRunnerV2(workerRunnerV2, runtimeToolRegistry, cfg.WorkspaceDir)
		log.Printf("CompositeRunnerV2 enabled (ToolRegistry fallback for worker)")
	}

	if cfg.Security.Enabled {
		var execRepo domainexecution.Repository
		if cfg.Security.Audit.Enabled && cfg.Security.Audit.Backend == "jsonl" {
			repo, err := executionpersistence.NewJSONLRepository(cfg.Security.Audit.Path)
			if err != nil {
				log.Fatalf("Failed to initialize execution audit repository: %v", err)
			}
			execRepo = repo
		}

		policy := securityinfra.NewPolicyEngine(securityinfra.PolicyConfig{
			Mode:              cfg.Security.PolicyMode,
			NetworkScope:      cfg.Security.NetworkScope,
			NetworkAllowed:    cfg.Security.NetworkAllowlist,
			DenyCommands:      cfg.Security.DenyCommands,
			Workspace:         cfg.WorkspaceDir,
			WorkspaceEnforced: cfg.Security.WorkspaceEnforced,
			SandboxRoot:       filepath.Join(cfg.WorkspaceDir, cfg.Sandbox.Root),
			SandboxWriteOnly:  cfg.Sandbox.Enabled && cfg.Sandbox.DenyOutsideSandboxWrite,
		})

		securedChatRunner, err := securityinfra.NewPolicyRunner(chatToolRunnerV2, policy, execRepo, "chat")
		if err != nil {
			log.Fatalf("Failed to create chat policy runner: %v", err)
		}
		securedWorkerRunner, err := securityinfra.NewPolicyRunner(workerRunnerV2, policy, execRepo, "worker")
		if err != nil {
			log.Fatalf("Failed to create worker policy runner: %v", err)
		}
		chatRunnerV2 = securedChatRunner
		workerRunnerV2 = securedWorkerRunner
		log.Printf("Security policy runner enabled (mode=%s)", cfg.Security.PolicyMode)
	}

	if cfg.ToolHarness.IsEnabled() {
		harnessCfg := tools.ToolHarnessRunnerConfig{
			Mode: cfg.ToolHarness.Mode,
		}
		if toolMediationRecorder != nil {
			harnessCfg.Recorder = toolMediationRecorder
		}
		chatRunnerV2 = tools.NewToolHarnessRunnerWithConfig(chatRunnerV2, harnessCfg)
		workerRunnerV2 = tools.NewToolHarnessRunnerWithConfig(workerRunnerV2, harnessCfg)
		log.Printf("Tool Harness enabled (mode=%s record_events=%t)", cfg.ToolHarness.Mode, cfg.ToolHarness.ShouldRecordEvents())
	} else {
		log.Printf("Tool Harness disabled by config")
	}

	if cfg.AIWorkflow.ContextBudgetTokens > 0 {
		policy := domainai.ContextBudgetPolicy{
			MaxContextTokens: cfg.AIWorkflow.ContextBudgetTokens,
			WarnAtRatio:      cfg.AIWorkflow.ContextBudgetWarnRatio,
			StopAtRatio:      cfg.AIWorkflow.ContextBudgetStopRatio,
		}
		offloadDir := filepath.Join(cfg.WorkspaceDir, "logs", "tool_results")
		chatRunnerV2 = tools.NewContextBudgetRunner(chatRunnerV2, tools.ContextBudgetRunnerConfig{Agent: "Chat", Policy: policy, Recorder: contextBudgetRecorder, OffloadDir: offloadDir})
		workerRunnerV2 = tools.NewContextBudgetRunner(workerRunnerV2, tools.ContextBudgetRunnerConfig{Agent: "Worker", Policy: policy, Recorder: contextBudgetRecorder, OffloadDir: offloadDir})
		log.Printf("Tool context budget runner enabled (max_context_tokens=%d)", cfg.AIWorkflow.ContextBudgetTokens)
	}

	var subagentMgr *subagent.Manager
	if cfg.Subagent.Enabled {
		subagentProvider := resolveSubagentProvider(cfg, workerToolProvider)
		toolDefs := workerToolRunnerV2.ToolDefinitions()
		subagentOpts := []subagent.ManagerOption{}
		if runtimeToolRegistry != nil {
			subagentOpts = append(subagentOpts, subagent.WithToolRegistry(runtimeToolRegistry))
		}
		subagentMgr = subagent.NewManager(
			subagentProvider,
			workerRunnerV2,
			toolDefs,
			toolloop.Config{MaxIterations: cfg.Subagent.MaxIterations},
			subagentOpts...,
		)
		workerToolRunnerV2.RegisterSubagent("worker", tools.NewSubagentFuncFromManager(subagentMgr))
		log.Printf("Subagent enabled (provider: %s, max_iterations: %d)",
			subagentProvider.Name(), cfg.Subagent.MaxIterations)
	} else {
		log.Printf("Subagent disabled")
	}

	log.Printf("ToolRunner initialized: Chat=%d tools, Worker=%d tools",
		len(mustGetToolList(chatRunnerV2)), len(mustGetToolList(workerRunnerV2)))

	if chatToolRunnerCfg.GoogleAPIKey != "" && chatToolRunnerCfg.GoogleSearchEngineID != "" {
		log.Printf("Google Search API (Chat) configured")
	}
	if workerToolRunnerCfg.GoogleAPIKey != "" && workerToolRunnerCfg.GoogleSearchEngineID != "" {
		log.Printf("Google Search API (Worker) configured")
	}

	return toolRuntime{
		ChatRunnerV2:                  chatToolRunnerV2,
		WorkerRunnerV2:                workerToolRunnerV2,
		ChatRuntimeRunnerV2:           chatRunnerV2,
		WorkerRuntimeRunnerV2:         workerRunnerV2,
		MovieCatalogLookup:            movieCatalogLookup,
		MusicCatalogLookup:            musicCatalogLookup,
		PersonRelatedCatalogLookup:    personRelatedCatalogLookup,
		PersonRelatedCollectionWorker: personRelatedCollectionWorker,
		PersonRelatedSummaryWorker:    personRelatedSummaryWorker,
		PersonRelatedIdentityWorker:   personRelatedIdentityWorker,
		SubagentMgr:                   subagentMgr,
		ToolMediationRecorder:         toolMediationRecorder,
		DataCapabilityCatalog:         dataCapabilityCatalog,
		DataRecallRegistry:            dataRecallRegistry,
		DataWriteRegistry:             dataWriteRegistry,
		KnowledgeMemoryToolStore:      knowledgeMemoryToolStore,
	}
}

// googleSearchValue keeps the config file authoritative while allowing
// deployment environments (including the Windows E2E launcher) to provide
// secrets through the documented environment variables.
func googleSearchValue(configValue, envName string) string {
	if value := strings.TrimSpace(configValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func buildToolMediationRecorder(cfg *config.Config) *toolharnesspersistence.JSONLRecorder {
	if cfg == nil || !cfg.ToolHarness.IsEnabled() || !cfg.ToolHarness.ShouldRecordEvents() {
		return nil
	}
	path := cfg.ToolHarness.LogPath
	if path == "" {
		path = filepath.Join(cfg.WorkspaceDir, "logs", "tool_mediation.jsonl")
	}
	recorder, err := toolharnesspersistence.NewJSONLRecorder(path)
	if err != nil {
		log.Printf("Tool Harness mediation recorder disabled: %v", err)
		return nil
	}
	log.Printf("Tool Harness mediation recorder initialized (%s)", path)
	return recorder
}
