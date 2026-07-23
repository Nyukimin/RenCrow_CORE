package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/modulebridge"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	artifactcleanupapp "github.com/Nyukimin/RenCrow_CORE/internal/application/artifactcleanup"
	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	complexityapp "github.com/Nyukimin/RenCrow_CORE/internal/application/complexity"
	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	historyrepairapp "github.com/Nyukimin/RenCrow_CORE/internal/application/historyrepair"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	knowledgememoryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	otelexportapp "github.com/Nyukimin/RenCrow_CORE/internal/application/otelexport"
	packagevalidationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/packagevalidation"
	sandboxapp "github.com/Nyukimin/RenCrow_CORE/internal/application/sandbox"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	skillapp "github.com/Nyukimin/RenCrow_CORE/internal/application/skillgovernance"
	superagentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	mcpinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	browsertracepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	personapersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
	revenuepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
	sandboxpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
	skillpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	personainfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

// Dependencies はアプリケーション依存関係
type Dependencies struct {
	lineHandler                    http.Handler
	telegramHandler                http.Handler
	discordHandler                 http.Handler
	slackHandler                   http.Handler
	eventHub                       *viewer.EventHub                            // live viewer
	monitorStore                   *viewer.MonitorStore                        // viewer monitor snapshots
	eventLogStore                  *viewer.EventLogStore                       // persisted orchestrator event log
	eventLogGC                     *viewer.EventLogGCService                   // persisted event log GC
	reportStore                    *executionpersistence.JSONLReportStore      // execution evidence store
	eventRelay                     *idleAwareEventListener                     // viewer + idlechat stop relay
	viewerStatus                   http.HandlerFunc                            // viewer status API
	viewerAgents                   http.HandlerFunc                            // viewer agents API
	viewerAgentDetail              http.HandlerFunc                            // viewer agent detail API
	viewerJobs                     http.HandlerFunc                            // viewer jobs API
	parallelJobs                   http.HandlerFunc                            // Mio parallel jobs API
	parallelJobDetail              http.HandlerFunc                            // Mio parallel job detail API
	jobNotifications               http.HandlerFunc                            // Mio parallel job interrupt notification API
	viewerLogs                     http.HandlerFunc                            // viewer logs API
	viewerAuditSummary             http.HandlerFunc                            // viewer audit summary API
	viewerJobDetail                http.HandlerFunc                            // viewer job detail API
	viewerSend                     http.HandlerFunc                            // viewer message sender
	viewerGamesStatus              http.HandlerFunc                            // RenCrow_GAMES bridge status API
	viewerGamesDecision            http.HandlerFunc                            // RenCrow_GAMES synchronous decision API
	viewerGamesResult              http.HandlerFunc                            // RenCrow_GAMES result callback API
	viewerGamesSessions            http.HandlerFunc                            // RenCrow_GAMES recent session observer API
	viewerGamesEvents              http.HandlerFunc                            // RenCrow_GAMES candidate event observer API
	viewerGamesObserverPage        http.HandlerFunc                            // RenCrow_GAMES live observer UI proxy page
	viewerGamesLaunch              http.HandlerFunc                            // RenCrow_GAMES launch proxy (マルチペルソナ WP5)
	gameAutoplay                   *viewer.GameAutoplayService                 // ペルソナ自発プレイランナー (マルチペルソナ WP6)
	viewerGamesObserverProxy       http.HandlerFunc                            // RenCrow_GAMES live observer API proxy
	live2DChatResponder            viewer.Live2DChatResponder                  // viewer Live2D chat -> orchestrator adapter
	historyRepairJSONL             http.HandlerFunc                            // viewer JSONL history repair API
	packageValidation              http.HandlerFunc                            // viewer package/update validation API
	characterRuntime               http.HandlerFunc                            // viewer six-character conversation runtime API
	extensionHealth                http.HandlerFunc                            // viewer plugin / extension health API
	otelExport                     http.HandlerFunc                            // viewer OpenTelemetry export API
	artifactCleanup                http.HandlerFunc                            // viewer stale artifact cleanup API
	repairRunner                   viewer.RepairJobRunner                      // viewer repair job runner
	voiceDirectHandler             voiceDirectFinalHandler                     // VDS llm.final -> SSE
	evidenceHandler                http.HandlerFunc                            // viewer evidence API
	evidenceDetail                 http.HandlerFunc                            // viewer evidence detail API
	evidenceSummary                http.HandlerFunc                            // viewer evidence summary API
	glossaryRecent                 http.HandlerFunc                            // viewer glossary API
	viewerMemorySnapshot           http.HandlerFunc                            // viewer memory/news/recall API
	viewerMemoryLayers             http.HandlerFunc                            // viewer memory layer API
	viewerMemoryEvents             http.HandlerFunc                            // viewer L1 event/search cache API
	viewerMemoryState              http.HandlerFunc                            // viewer memory state API
	viewerMemoryPromote            http.HandlerFunc                            // viewer memory promote API
	viewerMemoryUser               http.HandlerFunc                            // viewer user memory API
	viewerMemoryUserState          http.HandlerFunc                            // viewer user memory state API
	viewerMemoryUserForget         http.HandlerFunc                            // viewer user memory forget API
	viewerMemoryUserSupersede      http.HandlerFunc                            // viewer user memory supersede API
	viewerMemoryRecallPack         http.HandlerFunc                            // viewer memory recall pack API
	viewerRecallTraces             http.HandlerFunc                            // viewer recall trace API
	viewerSourceRegistry           http.HandlerFunc                            // viewer source registry API
	viewerDomainGraphAssertions    http.HandlerFunc                            // viewer domain graph assertion API
	viewerMovieDomainGraphSync     http.HandlerFunc                            // viewer movie domain graph sync API
	viewerHobbyDomainGraphSync     http.HandlerFunc                            // viewer hobby domain graph sync API
	verificationRecent             http.HandlerFunc                            // viewer verification recent API
	verificationDetail             http.HandlerFunc                            // viewer verification detail API
	verificationSummary            http.HandlerFunc                            // viewer verification summary API
	toolHarnessRecent              http.HandlerFunc                            // viewer tool harness mediation API
	dciRecent                      http.HandlerFunc                            // viewer DCI trace API
	dciSearch                      http.HandlerFunc                            // viewer DCI manual search API
	dciSearcher                    orchestrator.DCISearcher                    // message orchestrator explicit DCI trigger
	dciTraceStore                  any                                         // DCI trace store for cross-feature candidate extraction
	recallTraceStore               orchestrator.RecallTraceStore               // DCI / recall trace API store
	sandboxStatus                  http.HandlerFunc                            // viewer sandbox promotion API
	sandboxPromotion               http.HandlerFunc                            // viewer sandbox promotion request API
	sandboxPromotionApply          http.HandlerFunc                            // viewer sandbox promotion apply checkpoint API
	sandboxPromotionRollback       http.HandlerFunc                            // viewer sandbox promotion rollback checkpoint API
	sandboxPromotionPreview        http.HandlerFunc                            // viewer sandbox promotion diff preview API
	sandboxPromotionManualReview   http.HandlerFunc                            // viewer sandbox high-risk promotion review workflow API
	sandboxWorktreeCreate          http.HandlerFunc                            // viewer sandbox code worktree create API
	sandboxWorktreeClose           http.HandlerFunc                            // viewer sandbox code worktree close API
	skillGovernanceRecent          http.HandlerFunc                            // viewer skill governance API
	skillGovernanceBoot            http.HandlerFunc                            // viewer skill governance bootstrap API
	skillContributionGate          http.HandlerFunc                            // viewer contribution gate API
	skillChangeGate                http.HandlerFunc                            // viewer skill change gate API
	skillChangeEval                http.HandlerFunc                            // viewer skill change eval runner API
	skillExternalPRSubmit          http.HandlerFunc                            // viewer external PR submit audit API
	skillBootstrap                 *skillapp.BootstrapService                  // runtime skill bootstrap logger
	coderProposalEvidence          orchestrator.CoderProposalEvidenceRecorder  // Coder proposal evidence files for Skill Change Eval
	workstreamStatus               http.HandlerFunc                            // viewer workstream status API
	workstreamGoal                 http.HandlerFunc                            // viewer workstream goal API
	workstreamArtifact             http.HandlerFunc                            // viewer workstream artifact API
	workstreamAnnotation           http.HandlerFunc                            // viewer workstream artifact annotation API
	workstreamSteering             http.HandlerFunc                            // viewer workstream steering API
	workstreamHeartbeat            http.HandlerFunc                            // viewer workstream heartbeat API
	workstreamVaultUpdate          http.HandlerFunc                            // viewer workstream vault update log API
	workstreamVaultReview          http.HandlerFunc                            // viewer workstream vault update review API
	workstreamVaultPreview         http.HandlerFunc                            // viewer workstream vault update preview API
	revenueStatus                  http.HandlerFunc                            // viewer revenue status API
	revenueMarket                  http.HandlerFunc                            // viewer revenue market research API
	revenueSNSPost                 http.HandlerFunc                            // viewer revenue SNS post metric API
	revenueProduct                 http.HandlerFunc                            // viewer revenue product API
	revenueCustomerVoice           http.HandlerFunc                            // viewer revenue customer voice API
	revenueEvent                   http.HandlerFunc                            // viewer revenue event API
	revenueHumanDecisionGate       http.HandlerFunc                            // viewer revenue human decision gate API
	revenueHumanDecisionReview     http.HandlerFunc                            // viewer revenue human decision gate review API
	revenueDailyRoutine            http.HandlerFunc                            // viewer revenue daily routine draft report API
	revenueChannelDraft            http.HandlerFunc                            // viewer revenue channel draft API
	revenueExternalSendApply       http.HandlerFunc                            // viewer revenue external send apply audit API
	revenueOpportunities           http.HandlerFunc                            // viewer economic opportunities API
	revenueEconomicTasks           http.HandlerFunc                            // viewer economic tasks API
	revenueEconomicReflections     http.HandlerFunc                            // viewer economic reflections API
	revenueReflectionFromEvent     http.HandlerFunc                            // viewer reflection from revenue event API
	revenueOpportunityGoal         http.HandlerFunc                            // viewer opportunity to workstream goal API
	advisorStatus                  http.HandlerFunc                            // viewer advisor aggregate status API
	advisorRuns                    http.HandlerFunc                            // viewer advisor run records API
	advisorScores                  http.HandlerFunc                            // viewer advisor score snapshots API
	agentProfiles                  http.HandlerFunc                            // viewer agent profiles API
	agentPolicyDecisions           http.HandlerFunc                            // viewer agent policy decision traces API
	knowledgeRelations             http.HandlerFunc                            // viewer knowledge relation expansion API
	knowledgeRelationSummary       http.HandlerFunc                            // viewer knowledge relation summary API
	personaObservation             http.HandlerFunc                            // viewer persona observation status API
	personaDiscomfort              http.HandlerFunc                            // viewer persona discomfort log API
	personaTrigger                 http.HandlerFunc                            // viewer persona trigger log API
	personaCanonical               http.HandlerFunc                            // viewer persona canonical response log API
	personaObservationLog          http.HandlerFunc                            // viewer persona observation log API
	personaObservationAggregate    http.HandlerFunc                            // viewer persona daily/weekly/monthly observation aggregate API
	personaMetaUpdate              http.HandlerFunc                            // viewer persona meta profile update API
	personaMetaUpdateReview        http.HandlerFunc                            // viewer persona meta profile update review API
	personaSession                 http.HandlerFunc                            // viewer persona interface session API
	personaRuntimeStore            orchestrator.PersonaRuntimeRecorder         // Chat runtime persona observation recorder
	personaTriggerDefinitions      []domainpersona.TriggerDefinition           // Chat runtime trigger matcher definitions
	personaCanonicalResponses      []domainpersona.CanonicalResponseDefinition // Chat runtime canonical response definitions
	browserTraceAPIStatus          http.HandlerFunc                            // viewer browser trace to API status API
	browserTraceAPIDiscover        http.HandlerFunc                            // viewer browser trace to API discover API
	browserTraceAPIValidation      http.HandlerFunc                            // viewer browser trace API validation review API
	browserTraceAPIFetcherProposal http.HandlerFunc                            // viewer browser trace to API fetcher proposal API
	complexityHotspotStatus        http.HandlerFunc                            // viewer complexity hotspot status API
	complexityHotspotScan          http.HandlerFunc                            // viewer complexity hotspot scan API
	complexityHotspotProposal      http.HandlerFunc                            // viewer complexity hotspot proposal mode API
	complexityHotspotConcreteDiff  http.HandlerFunc                            // viewer complexity concrete diff proposal API
	complexityHotspotCoderDiff     http.HandlerFunc                            // viewer complexity Coder-generated concrete diff API
	superAgentStatus               http.HandlerFunc                            // viewer superagent harness status API
	superAgentRun                  http.HandlerFunc                            // viewer superagent run API
	superAgentRunPause             http.HandlerFunc                            // viewer superagent run pause API
	superAgentRunResume            http.HandlerFunc                            // viewer superagent run resume API
	superAgentRunQueue             http.HandlerFunc                            // viewer superagent run queue API
	superAgentRunQueueClaim        http.HandlerFunc                            // viewer superagent run queue claim API
	superAgentRunQueueComplete     http.HandlerFunc                            // viewer superagent run queue complete API
	superAgentSubagentTask         http.HandlerFunc                            // viewer subagent task API
	superAgentContextPack          http.HandlerFunc                            // viewer context pack API
	superAgentMessageChannel       http.HandlerFunc                            // viewer message channel API
	superAgentTraceEvent           http.HandlerFunc                            // viewer trace event API
	superAgentStore                viewer.SuperAgentStore                      // SuperAgent runtime telemetry store
	superAgentRunController        *superagentapp.RunController                // SuperAgent runtime pause/resume controller
	aiWorkflowStatus               http.HandlerFunc                            // viewer AI workflow status API
	aiWorkflowEvent                http.HandlerFunc                            // viewer AI workflow event API
	aiWorkflowProjectMemory        http.HandlerFunc                            // viewer project memory index API
	aiWorkflowWorktree             http.HandlerFunc                            // viewer worktree registry API
	aiWorkflowCommand              http.HandlerFunc                            // viewer command registry API
	aiWorkflowCommandRun           http.HandlerFunc                            // viewer command run API
	aiWorkflowContextUsage         http.HandlerFunc                            // viewer context usage API
	aiWorkflowContextBudget        http.HandlerFunc                            // viewer context budget check API
	aiWorkflowExternalControl      http.HandlerFunc                            // viewer external control policy check API
	aiWorkflowHeavyWorker          http.HandlerFunc                            // viewer heavy worker policy API
	aiWorkflowHeavyRuntime         http.HandlerFunc                            // viewer heavy worker runtime diagnostics API
	aiWorkflowProjectInit          http.HandlerFunc                            // viewer project init pack API
	aiWorkflowWorktreeCreate       http.HandlerFunc                            // viewer git worktree create API
	aiWorkflowWorktreeClose        http.HandlerFunc                            // viewer git worktree close API
	aiWorkflowStore                viewer.AIWorkflowStore                      // workflow telemetry store
	schedulerStatus                http.HandlerFunc                            // viewer in-app scheduler API
	schedulerStore                 viewer.SchedulerStore                       // in-app scheduler persistent store
	pronunciationCheckCancel       context.CancelFunc                          // TTS pronunciation CORE task
	knowledgeMemoryStatus          http.HandlerFunc                            // viewer knowledge memory status API
	personalArchiveCreate          http.HandlerFunc                            // viewer personal archive API
	creativeKnowledgeCreate        http.HandlerFunc                            // viewer creative knowledge API
	newsKnowledgeCreate            http.HandlerFunc                            // viewer news knowledge API
	dailyIntakeRuleCreate          http.HandlerFunc                            // viewer daily intake rule API
	temporalMemoryCreate           http.HandlerFunc                            // viewer temporal memory marker API
	knowledgeMemoryReview          http.HandlerFunc                            // viewer knowledge memory review API
	dreamConsolidationCreate       http.HandlerFunc                            // viewer dream consolidation run API
	dreamConsolidationProposal     http.HandlerFunc                            // viewer dream consolidation proposal API
	dreamConsolidationReview       http.HandlerFunc                            // viewer dream consolidation review API
	backlogStore                   *viewer.BacklogStore                        // Backlog intake store shared by Viewer and Heartbeat
	workstreamStore                heartbeat.WorkstreamHeartbeatStore          // Workstream heartbeat draft runner
	revenueStore                   heartbeat.RevenueDailyRoutineStore          // Revenue daily routine draft runner
	entryHandler                   http.HandlerFunc                            // unified entry endpoint
	chromeBridge                   http.HandlerFunc                            // chrome bridge endpoint
	chromeBridgeStatus             http.HandlerFunc                            // chrome bridge status endpoint
	chromeBridgeEvents             http.HandlerFunc                            // chrome bridge SSE endpoint
	distOrch                       *orchestrator.DistributedOrchestrator       // v4 distributed orchestrator
	router                         *transport.MessageRouter                    // v4 distributed mode
	localTransports                map[string]*transport.LocalTransport        // v4 local transports
	idleChatOrch                   *idlechat.IdleChatOrchestrator              // v4 idle chat
	idleChatStartGate              idleChatStartGate                           // IdleChat 起動前の LLM Ops ガード
	sshTransports                  map[string]domaintransport.Transport        // v4 SSH transports
	heartbeatSvc                   *heartbeat.HeartbeatService                 // heartbeat service
	advisorCloser                  interface{ Close() error }                  // advisor SQLite store, when configured
	toolRegistry                   capdomain.ToolRegistry                      // Phase 4: Shiro ツール共有用 ToolRegistry
	moduleChatService              chatModuleService                           // module contract view of Chat service
	moduleLLMProviders             map[string]modulellm.Provider               // module contract view of LLM providers
	moduleTTSProvider              moduletts.Provider                          // module contract view of primary TTS provider
	moduleTTSPlayback              moduletts.PlaybackStateObserver             // module contract view of Viewer playback state
	moduleSTTViewerInput           modulestt.ViewerInputObserver               // module contract view of Viewer STT input state
	moduleWorkerExecutor           moduleworker.Executor                       // module contract view of Worker executor
	moduleHealth                   http.HandlerFunc                            // module boundary health API
	llmBusyTracker                 *llmBusyTracker                             // runtime LLM execution tracker for IdleChat gating
}

type idleChatStartGate interface {
	PrepareIdleChatStart(context.Context) error
}

// Shutdown はリソースを解放
func (d *Dependencies) Shutdown() {
	if d.pronunciationCheckCancel != nil {
		d.pronunciationCheckCancel()
	}
	if d.gameAutoplay != nil {
		d.gameAutoplay.Stop()
	}
	if d.eventLogGC != nil {
		d.eventLogGC.Stop()
	}
	if d.heartbeatSvc != nil {
		d.heartbeatSvc.Stop()
	}
	if d.advisorCloser != nil {
		if err := d.advisorCloser.Close(); err != nil {
			log.Printf("Failed to close advisor store: %v", err)
		}
	}
	if d.idleChatOrch != nil {
		d.idleChatOrch.Stop()
	}
	for name, t := range d.sshTransports {
		if err := t.Close(); err != nil {
			log.Printf("Failed to close SSH transport for %s: %v", name, err)
		}
	}
	for name, t := range d.localTransports {
		if err := t.Close(); err != nil {
			log.Printf("Failed to close Local transport for %s: %v", name, err)
		}
	}
	if d.router != nil {
		d.router.Stop()
	}
	if d.toolRegistry != nil {
		if err := d.toolRegistry.Close(); err != nil {
			log.Printf("Failed to close ToolRegistry: %v", err)
		}
	}
	log.Println("Shutdown complete")
}

// buildDependencies は依存関係を構築
func buildDependencies(cfg *config.Config) *Dependencies {
	runtimeToolRegistry := buildRuntimeToolRegistry(cfg)
	nodeCaps := buildCapabilityRuntime(cfg, runtimeToolRegistry)
	aiWorkflowStore := buildAIWorkflowStore(cfg)
	llmBusyTracker := newLLMBusyTracker()
	llmRuntime := buildLLMRuntimeProviders(cfg, aiWorkflowStore, llmBusyTracker)
	classifier := routing.NewLLMClassifier(llmRuntime.Chat, cfg.Prompts.Classifier)
	ruleDictionary := routing.NewRuleDictionary()
	toolRuntime := buildToolRuntime(cfg, llmRuntime.WorkerToolProvider, runtimeToolRegistry, aiWorkflowStore)
	advisorRuntime, err := buildAdvisorRuntime(cfg, toolRuntime.WorkerRuntimeRunnerV2)
	if err != nil {
		log.Fatalf("Failed to initialize Advisor runtime: %v", err)
	}
	mcpClient := mcp.NewMCPClient()
	log.Printf("MCPClient initialized with %d servers", len(mcpClient.ListServers()))
	conversationRuntime := buildConversationRuntime(cfg, llmRuntime.Primary, toolRuntime.ChatRunnerV2, toolRuntime.WorkerRunnerV2)
	glossaryRuntime := buildGlossaryRuntime(cfg)
	agents := buildAgentRuntime(
		cfg,
		llmRuntime.Chat,
		llmRuntime.ChatWorker,
		llmRuntime.Worker,
		llmRuntime.Heavy,
		llmRuntime.Wild,
		classifier,
		ruleDictionary,
		toolRuntime.ChatRuntimeRunnerV2,
		toolRuntime.WorkerRuntimeRunnerV2,
		mcpClient,
		conversationRuntime.Engine,
		glossaryRuntime.RecentContext,
		conversationRuntime.Manager,
		conversationRuntime.L1Store,
		toolRuntime.SubagentMgr,
		advisorRuntime.Service,
		advisorRuntime.Policy,
	)
	sessionRuntime := buildSessionRuntime(cfg)
	workerExecutionService := service.NewWorkerExecutionService(cfg.Worker)
	log.Printf("WorkerExecutionService initialized (Workspace: %s, Parallel: %v)",
		cfg.Worker.Workspace, cfg.Worker.ParallelExecution)

	if envBool("RENCROW_ENABLE_SERENA_MCP") {
		// Serena MCP クライアントを起動してCoderLoopの観測アクションに接続
		// SelfSourceDir（絶対パス）を渡す。未設定なら Worker.Workspace を絶対化して使う
		serenaWorkspace := cfg.SelfSourceDir
		if serenaWorkspace == "" {
			if abs, err := filepath.Abs(cfg.Worker.Workspace); err == nil {
				serenaWorkspace = abs
			} else {
				serenaWorkspace = cfg.Worker.Workspace
			}
		}
		serenaClient := mcpinfra.NewSerenaClient(serenaWorkspace)
		if err := serenaClient.Start(context.Background()); err != nil {
			log.Printf("Serena MCP client failed to start (non-fatal): %v", err)
		} else {
			workerExecutionService.SetMCPToolCaller(serenaClient)
			if tools, err := serenaClient.ListTools(context.Background()); err == nil {
				log.Printf("Serena MCP ready: %d tools available (%v)", len(tools), tools[:min(5, len(tools))])
			}
		}
	} else {
		log.Printf("Serena MCP client disabled (set RENCROW_ENABLE_SERENA_MCP=true to enable)")
	}

	deps := &Dependencies{}
	deps.advisorCloser = advisorRuntime.Closer
	deps.advisorStatus = viewer.HandleAdvisorsStatus(viewer.AdvisorStatusOptions{
		Store: advisorRuntime.Store, AdvisorProfiles: advisorRuntime.Profiles, AgentProfiles: advisorRuntime.AgentProfiles,
	})
	deps.advisorRuns = viewer.HandleAdvisorRuns(advisorRuntime.Store)
	deps.advisorScores = viewer.HandleAdvisorScores(advisorRuntime.Store)
	deps.agentProfiles = viewer.HandleAgentProfiles(advisorRuntime.AgentProfiles)
	deps.agentPolicyDecisions = viewer.HandleAgentPolicyDecisions(advisorRuntime.Store)
	knowledgeRelationOptions := viewer.KnowledgeRelationHandlerOptions{
		Store: conversationRuntime.L1Store, Enabled: cfg.KnowledgeRelation.Enabled, MaxHops: cfg.KnowledgeRelation.MaxHops,
	}
	deps.knowledgeRelations = viewer.HandleKnowledgeRelations(knowledgeRelationOptions)
	deps.knowledgeRelationSummary = viewer.HandleKnowledgeRelationSummary(knowledgeRelationOptions)
	deps.llmBusyTracker = llmBusyTracker
	deps.moduleLLMProviders = llmRuntime.ModuleProviders
	deps.moduleWorkerExecutor = modulebridge.NewRuntimeWorkerExecutor(workerExecutionService)
	deps.moduleTTSPlayback = ttsPlaybackStateObserver{}
	if ttsSel, ok := buildPrimaryTTSProvider(cfg); ok {
		deps.moduleTTSProvider = ttsSel.Module
	}
	deps.glossaryRecent = glossaryRuntime.RecentHandler
	deps.toolRegistry = runtimeToolRegistry
	deps.backlogStore = viewer.NewBacklogStore(filepath.Join(cfg.WorkspaceDir, "logs", "backlog.jsonl"))
	deps.schedulerStore = schedulerpersistence.NewJSONLStore(filepath.Join(cfg.WorkspaceDir, "logs", "scheduler"))
	deps.schedulerStatus = viewer.HandleScheduler(deps.schedulerStore)
	deps.historyRepairJSONL = viewer.HandleHistoryRepairJSONL(historyrepairapp.NewJSONLRepairService(
		cfg.WorkspaceDir,
		filepath.Join(cfg.WorkspaceDir, "logs", "history_repair.jsonl"),
	))
	deps.packageValidation = viewer.HandlePackageValidation(packagevalidationapp.NewService(cfg.WorkspaceDir))
	deps.otelExport = viewer.HandleOTelExport(otelexportapp.NewService(os.Getenv("RENCROW_OTEL_ENDPOINT")))
	deps.artifactCleanup = viewer.HandleArtifactCleanup(artifactcleanupapp.NewService(
		cfg.WorkspaceDir,
		filepath.Join(cfg.WorkspaceDir, "logs", "artifact_cleanup.jsonl"),
	))
	reportPath := defaultExecutionReportPath(cfg.WorkspaceDir)
	gameDecisionProvider := selectChatConversationProvider(llmRuntime.ChatWorker, llmRuntime.Chat)
	buildViewerRuntimeHandlers(cfg, deps, conversationRuntime.L1Store, conversationRuntime.Manager, reportPath, gameDecisionProvider)
	startConversationBackgroundJobs(cfg, conversationRuntime, deps.eventRelay)
	if toolRuntime.ToolMediationRecorder != nil {
		deps.toolHarnessRecent = viewer.HandleToolHarnessRecent(toolRuntime.ToolMediationRecorder)
	}
	if cfg.SkillGovernance.IsEnabled() {
		type skillGovernanceRuntimeStore interface {
			viewer.SkillGovernanceStore
			SaveSkillManifest(context.Context, domainskill.SkillManifest) error
			ListSkillManifests(context.Context, int) ([]domainskill.SkillManifest, error)
			SaveCoderTranscriptEntry(context.Context, domainskill.CoderTranscriptEntry) error
			ListCoderTranscriptEntries(context.Context, int) ([]domainskill.CoderTranscriptEntry, error)
		}
		var skillStore skillGovernanceRuntimeStore
		if cfg.SkillGovernance.Storage == "sqlite" {
			store, err := skillpersistence.NewSQLiteStore(cfg.SkillGovernance.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize Skill Governance SQLite store: %v", err)
			}
			skillStore = store
		} else {
			skillStore = skillpersistence.NewJSONLStore(cfg.SkillGovernance.RegistryPath)
		}
		for _, manifest := range loadSkillGovernanceManifests(cfg.SkillGovernance.SkillRoots) {
			if err := skillStore.SaveSkillManifest(context.Background(), manifest); err != nil {
				log.Printf("WARN: failed to record skill manifest %s: %v", manifest.SkillID, err)
			}
		}
		deps.skillBootstrap = skillapp.NewBootstrapService(skillStore)
		deps.coderProposalEvidence = skillapp.NewCoderEvidenceService("").WithTranscriptStore(skillStore)
		deps.skillGovernanceRecent = viewer.HandleSkillGovernanceRecent(skillStore)
		deps.skillGovernanceBoot = viewer.HandleSkillGovernanceBootstrap(skillStore)
		deps.skillContributionGate = viewer.HandleSkillGovernanceContributionGate(skillStore)
		deps.skillChangeGate = viewer.HandleSkillGovernanceSkillChange(skillStore)
		deps.skillChangeEval = viewer.HandleSkillGovernanceSkillChangeEval(skillStore)
		deps.skillExternalPRSubmit = viewer.HandleSkillGovernanceExternalPRSubmit(skillStore)
	}
	if cfg.DCI.IsEnabled() {
		var dciStore interface {
			dciapp.TraceStore
			ListRecent(limit int) ([]domaindci.SearchTrace, error)
		}
		if cfg.DCI.Storage == "sqlite" {
			store, err := dcipersistence.NewSQLiteStore(cfg.DCI.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize DCI SQLite store: %v", err)
			}
			dciStore = store
		} else {
			dciStore = dcipersistence.NewJSONLStore(cfg.DCI.TracePath)
		}
		deps.dciTraceStore = dciStore
		deps.dciRecent = viewer.HandleDCIRecent(dciStore)
		dciOptions := []dciapp.Option{
			dciapp.WithToolRunner(toolRuntime.WorkerRuntimeRunnerV2),
			dciapp.WithSkillBootstrap(deps.skillBootstrap),
		}
		if conversationRuntime.L1Store != nil {
			dciOptions = append(dciOptions, dciapp.WithSourceCandidateStore(dcipersistence.NewL1SourceCandidateStore(conversationRuntime.L1Store, "kb:dci")))
			dciOptions = append(dciOptions, dciapp.WithSourceMetadataRanker(dcipersistence.NewL1SourceMetadataRanker(conversationRuntime.L1Store)))
			dciOptions = append(dciOptions, dciapp.WithSourceCandidateProvider(dcipersistence.NewL1KnowledgeFTSCandidateProvider(conversationRuntime.L1Store, cfg.DCI.KnowledgeFTSDomains)))
		}
		if conversationRuntime.Manager != nil {
			dciOptions = append(dciOptions, dciapp.WithSourceCandidateProvider(dcipersistence.NewVectorKBCandidateProvider(conversationRuntime.Manager, cfg.DCI.KnowledgeFTSDomains)))
		}
		// セッションログ候補プロバイダーを構築（設定またはデフォルト）
		sessionLogSources := buildSessionLogSources(cfg)
		if len(sessionLogSources) > 0 {
			dciOptions = append(dciOptions, dciapp.WithSourceCandidateProvider(
				dcipersistence.NewSessionLogCandidateProvider(sessionLogSources),
			))
			log.Printf("DCI session log sources registered: %d source(s)", len(sessionLogSources))
		}

		// セッションログディレクトリをCorpusAllowlistに自動追加
		allowlist := cfg.DCI.CorpusAllowlist
		for _, src := range sessionLogSources {
			allowlist = append(allowlist, os.ExpandEnv(src.PathDir))
		}

		dciExplorer := dciapp.NewExplorer(dciapp.Config{
			Enabled:           cfg.DCI.IsEnabled(),
			Allowlist:         allowlist,
			DenylistPatterns:  cfg.DCI.CorpusDenylist,
			ExplicitKeywords:  cfg.DCI.ExplicitKeywords,
			MaxSeconds:        cfg.DCI.MaxSeconds,
			MaxSteps:          cfg.DCI.MaxSteps,
			MaxCandidateFiles: cfg.DCI.MaxCandidateFiles,
			MaxFilesRead:      cfg.DCI.MaxFilesRead,
			MaxEvidence:       cfg.DCI.MaxEvidence,
			MaxSnippetChars:   cfg.DCI.MaxSnippetChars,
		}, dciStore, dciOptions...)
		deps.dciSearcher = dciExplorer
		deps.dciSearch = viewer.HandleDCISearch(dciExplorer)
	}
	type sandboxRuntimeStore interface {
		viewer.SandboxLister
		viewer.SandboxPromotionStore
		sandboxapp.WorktreeSandboxStore
	}
	var sandboxStore sandboxRuntimeStore
	var promotionDiffPreviewer *sandboxapp.PromotionDiffApplier
	if !cfg.Sandbox.Enabled {
		deps.sandboxStatus = viewer.HandleSandboxStatus(nil)
	}
	if cfg.Sandbox.Enabled {
		if cfg.Sandbox.Storage == "sqlite" {
			store, err := sandboxpersistence.NewSQLiteStore(cfg.Sandbox.SQLitePath)
			if err != nil {
				log.Fatalf("failed to initialize sandbox sqlite store: %v", err)
			}
			sandboxStore = store
		} else {
			sandboxStore = sandboxpersistence.NewJSONLStore(filepath.Join(cfg.WorkspaceDir, "logs", "sandbox"))
		}
		deps.sandboxStatus = viewer.HandleSandboxStatus(sandboxStore)
		deps.sandboxPromotion = viewer.HandleSandboxPromotionRequest(sandboxStore)
		promotionDiffPreviewer = sandboxapp.NewPromotionDiffApplier(
			filepath.Join(cfg.WorkspaceDir, cfg.Sandbox.Root),
			cfg.Sandbox.Promotion.ApplyRoot,
		)
		deps.sandboxPromotionPreview = viewer.HandleSandboxPromotionDiffPreview(promotionDiffPreviewer)
		var promotionDiffApplier *sandboxapp.PromotionDiffApplier
		if cfg.Sandbox.Promotion.ApplyRoot != "" {
			promotionDiffApplier = promotionDiffPreviewer
		}
		deps.sandboxPromotionApply = viewer.HandleSandboxPromotionApplyWithVerifierAndApplier(
			sandboxStore,
			sandboxapp.NewPostApplyVerificationRunner(toolRuntime.WorkerRuntimeRunnerV2, filepath.Join(cfg.WorkspaceDir, cfg.Sandbox.Root)),
			promotionDiffApplier,
		)
		if promotionDiffApplier != nil {
			deps.sandboxPromotionRollback = viewer.HandleSandboxPromotionRollback(sandboxStore, promotionDiffApplier)
		}
	}
	if cfg.Workstream.IsEnabled() {
		var workstreamStore viewer.WorkstreamStore
		if cfg.Workstream.Storage == "sqlite" {
			store, err := workstreampersistence.NewSQLiteStoreWithVault(cfg.Workstream.SQLitePath, cfg.Workstream.VaultRoot)
			if err != nil {
				log.Fatalf("Failed to initialize Workstream SQLite store: %v", err)
			}
			workstreamStore = store
		} else {
			workstreamStore = workstreampersistence.NewJSONLStoreWithVault(cfg.Workstream.LogPath, cfg.Workstream.VaultRoot)
		}
		deps.workstreamStore = workstreamStore
		deps.workstreamStatus = viewer.HandleWorkstreamStatus(workstreamStore)
		deps.workstreamGoal = viewer.HandleWorkstreamGoalCreate(workstreamStore)
		deps.workstreamArtifact = viewer.HandleWorkstreamArtifactCreate(workstreamStore)
		deps.workstreamAnnotation = viewer.HandleWorkstreamAnnotationCreate(workstreamStore)
		deps.workstreamSteering = viewer.HandleWorkstreamSteeringCreate(workstreamStore)
		deps.workstreamHeartbeat = viewer.HandleWorkstreamHeartbeatCreate(workstreamStore)
		deps.workstreamVaultUpdate = viewer.HandleWorkstreamVaultUpdateCreate(workstreamStore)
		deps.workstreamVaultReview = viewer.HandleWorkstreamVaultUpdateReview(workstreamStore)
		deps.workstreamVaultPreview = viewer.HandleWorkstreamVaultUpdatePreview(workstreamStore)
		if sandboxStore != nil && promotionDiffPreviewer != nil {
			deps.sandboxPromotionManualReview = viewer.HandleSandboxPromotionManualReview(promotionDiffPreviewer, workstreamStore, sandboxStore)
		}
	}
	if cfg.Revenue.IsEnabled() {
		var revenueStore viewer.RevenueStore
		if cfg.Revenue.Storage == "sqlite" {
			store, err := revenuepersistence.NewSQLiteStore(cfg.Revenue.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize Revenue SQLite store: %v", err)
			}
			revenueStore = store
		} else {
			revenueStore = revenuepersistence.NewJSONLStore(cfg.Revenue.LogPath)
		}
		deps.revenueStore = revenueStore
		deps.revenueStatus = viewer.HandleRevenueStatus(revenueStore, viewer.RevenueEconomicObjectiveSettings{
			Enabled: cfg.EconomicObjective.Enabled, DraftOnly: cfg.EconomicObjective.DraftOnlyEnabled(),
		})
		deps.revenueMarket = viewer.HandleRevenueMarketResearchCreate(revenueStore)
		deps.revenueSNSPost = viewer.HandleRevenueSNSPostMetricCreate(revenueStore)
		deps.revenueProduct = viewer.HandleRevenueProductCreate(revenueStore)
		deps.revenueCustomerVoice = viewer.HandleRevenueCustomerVoiceCreate(revenueStore)
		deps.revenueEvent = viewer.HandleRevenueEventCreate(revenueStore)
		deps.revenueHumanDecisionGate = viewer.HandleRevenueHumanDecisionGate(revenueStore)
		deps.revenueHumanDecisionReview = viewer.HandleRevenueHumanDecisionGateReview(revenueStore)
		deps.revenueDailyRoutine = viewer.HandleRevenueDailyRoutineReportCreate(revenueStore)
		deps.revenueChannelDraft = viewer.HandleRevenueChannelDraftCreate(revenueStore)
		deps.revenueExternalSendApply = viewer.HandleRevenueExternalSendApply(revenueStore)
		deps.revenueOpportunities = viewer.HandleRevenueOpportunities(revenueStore)
		deps.revenueEconomicTasks = viewer.HandleRevenueEconomicTasks(revenueStore)
		deps.revenueEconomicReflections = viewer.HandleRevenueEconomicReflections(revenueStore)
		deps.revenueReflectionFromEvent = viewer.HandleRevenueReflectionFromEvent(revenueStore)
		deps.revenueOpportunityGoal = viewer.HandleRevenueOpportunityWorkstreamGoal(revenueStore, deps.workstreamStore)
	}
	if cfg.PersonaArchitecture.IsEnabled() {
		var personaStore viewer.PersonaObservationStore
		if cfg.PersonaArchitecture.Storage == "sqlite" {
			store, err := personapersistence.NewSQLiteStoreWithMetaRoot(cfg.PersonaArchitecture.SQLitePath, cfg.PersonaArchitecture.CharacterRoot)
			if err != nil {
				log.Fatalf("Failed to initialize Persona Architecture SQLite store: %v", err)
			}
			personaStore = store
		} else {
			store := personapersistence.NewJSONLStoreWithMetaRoot(cfg.PersonaArchitecture.LogPath, cfg.PersonaArchitecture.CharacterRoot)
			if err := store.CompactOperationalLogs(); err != nil {
				log.Printf("WARN: persona operational log GC failed: %v", err)
			}
			personaStore = store
		}
		characters, err := personainfra.LoadCharacters(cfg.PersonaArchitecture.CharacterRoot)
		if err != nil {
			log.Fatalf("Failed to load Persona Architecture characters: %v", err)
		}
		deps.personaRuntimeStore = personaStore
		personaDefinitionOptions := personaRuntimeDefinitionOptionsFromConfig(cfg.PersonaArchitecture)
		deps.personaTriggerDefinitions = buildPersonaRuntimeTriggerDefinitionsWithOptions(characters, personaDefinitionOptions)
		deps.personaCanonicalResponses = buildPersonaRuntimeCanonicalResponsesWithOptions(characters, personaDefinitionOptions)
		deps.personaObservation = viewer.HandlePersonaObservationStatus(personaStore, characters)
		deps.personaDiscomfort = viewer.HandlePersonaDiscomfortCreate(personaStore)
		deps.personaTrigger = viewer.HandlePersonaTriggerLogCreate(personaStore)
		deps.personaCanonical = viewer.HandlePersonaCanonicalResponseLogCreate(personaStore)
		deps.personaObservationLog = viewer.HandlePersonaObservationLogCreate(personaStore)
		deps.personaObservationAggregate = viewer.HandlePersonaObservationAggregate(personaStore)
		deps.personaMetaUpdate = viewer.HandlePersonaMetaProfileUpdateCreate(personaStore)
		deps.personaMetaUpdateReview = viewer.HandlePersonaMetaProfileUpdateReview(personaStore)
		deps.personaSession = viewer.HandlePersonaInterfaceSessionCreate(personaStore)
	}
	if cfg.BrowserTraceToAPI.IsEnabled() {
		var browserTraceStore viewer.BrowserTraceAPIStore
		if cfg.BrowserTraceToAPI.Storage == "sqlite" {
			store, err := browsertracepersistence.NewSQLiteStore(cfg.BrowserTraceToAPI.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize Browser Trace to API SQLite store: %v", err)
			}
			browserTraceStore = store
		} else {
			browserTraceStore = browsertracepersistence.NewJSONLStore(cfg.BrowserTraceToAPI.LogPath)
		}
		deps.browserTraceAPIStatus = viewer.HandleBrowserTraceAPIStatus(browserTraceStore)
		var candidateSink viewer.BrowserTraceAPICandidateSink
		if conversationRuntime.L1Store != nil {
			candidateSink = browsertracepersistence.NewL1APICandidateStore(conversationRuntime.L1Store, "kb:browser_trace_api")
		}
		var workstreamArtifactSink viewer.BrowserTraceWorkstreamArtifactSink
		if ws, ok := deps.workstreamStore.(viewer.BrowserTraceWorkstreamArtifactSink); ok {
			workstreamArtifactSink = ws
		}
		validationPolicy := browsertraceapp.DefaultValidationPolicy()
		validationPolicy.ReadOnlyOnly = cfg.BrowserTraceToAPI.ReadOnlyOnly
		validationPolicy.RequireTermsReview = cfg.BrowserTraceToAPI.RequireTermsReview
		validationPolicy.DenySensitiveFlows = append([]string(nil), cfg.BrowserTraceToAPI.DenySensitiveFlows...)
		deps.browserTraceAPIDiscover = viewer.HandleBrowserTraceAPIDiscoverWithPolicy(browserTraceStore, browsertraceapp.NewDiscovererWithAcceptedPaths(cfg.BrowserTraceToAPI.AcceptedPaths), candidateSink, workstreamArtifactSink, validationPolicy)
		deps.browserTraceAPIValidation = viewer.HandleBrowserTraceAPIValidationReview(browserTraceStore)
		deps.browserTraceAPIFetcherProposal = viewer.HandleBrowserTraceAPIFetcherProposal(browserTraceStore, workstreamArtifactSink)
	}
	if cfg.ComplexityHotspot.IsEnabled() {
		var complexityStore viewer.ComplexityHotspotStore
		if cfg.ComplexityHotspot.Storage == "sqlite" {
			store, err := complexitypersistence.NewSQLiteStore(cfg.ComplexityHotspot.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize Complexity Hotspot SQLite store: %v", err)
			}
			complexityStore = store
		} else {
			complexityStore = complexitypersistence.NewJSONLStore(cfg.ComplexityHotspot.LogPath)
		}
		deps.complexityHotspotStatus = viewer.HandleComplexityHotspotStatus(complexityStore)
		var workstreamArtifactSink viewer.ComplexityWorkstreamArtifactSink
		if ws, ok := deps.workstreamStore.(viewer.ComplexityWorkstreamArtifactSink); ok {
			workstreamArtifactSink = ws
		}
		deps.complexityHotspotScan = viewer.HandleComplexityHotspotScan(complexityStore, complexityapp.NewAnalyzer(), deps.skillBootstrap, workstreamArtifactSink, deps.dciTraceStore)
		if ws, ok := deps.workstreamStore.(viewer.ComplexityProposalWorkstreamSink); ok {
			deps.complexityHotspotProposal = viewer.HandleComplexityHotspotProposalWithSandbox(complexityStore, ws, sandboxStore)
		}
		deps.complexityHotspotConcreteDiff = viewer.HandleComplexityHotspotConcreteDiffWithSandbox(complexityStore, workstreamArtifactSink, sandboxStore)
		deps.complexityHotspotCoderDiff = buildComplexityHotspotCoderDiffHandler(complexityStore, llmRuntime, workstreamArtifactSink, sandboxStore)
	}
	if cfg.SuperAgentHarness.IsEnabled() {
		var superAgentStore viewer.SuperAgentStore
		if cfg.SuperAgentHarness.Storage == "sqlite" {
			store, err := superagentpersistence.NewSQLiteStore(cfg.SuperAgentHarness.SQLitePath, cfg.SuperAgentHarness.MaxContextPackTokens)
			if err != nil {
				log.Fatalf("Failed to initialize SuperAgent Harness SQLite store: %v", err)
			}
			superAgentStore = store
		} else {
			superAgentStore = superagentpersistence.NewJSONLStore(cfg.SuperAgentHarness.LogPath, cfg.SuperAgentHarness.MaxContextPackTokens)
		}
		deps.superAgentStore = superAgentStore
		deps.superAgentRunController = superagentapp.NewRunController()
		if toolRuntime.SubagentMgr != nil {
			toolRuntime.SubagentMgr.SetSuperAgentRecorder(superAgentStore)
		}
		deps.superAgentStatus = viewer.HandleSuperAgentStatusWithRuntimeConfig(superAgentStore, viewer.SuperAgentRuntimeConfig{
			RunQueueSchedulerEnabled:     cfg.SuperAgentHarness.RunQueueSchedulerEnabled,
			RunQueueSchedulerIntervalSec: cfg.SuperAgentHarness.RunQueueSchedulerIntervalSec,
			RunQueueSchedulerClaimLimit:  cfg.SuperAgentHarness.RunQueueSchedulerClaimLimit,
		})
		deps.superAgentRun = viewer.HandleSuperAgentAgentRunCreate(superAgentStore)
		deps.superAgentRunPause = viewer.HandleSuperAgentRunPauseWithController(superAgentStore, deps.superAgentRunController)
		deps.superAgentRunResume = viewer.HandleSuperAgentRunResumeWithController(superAgentStore, deps.superAgentRunController)
		deps.superAgentRunQueue = viewer.HandleSuperAgentRunQueueCreate(superAgentStore)
		deps.superAgentRunQueueClaim = viewer.HandleSuperAgentRunQueueClaim(superAgentStore)
		deps.superAgentRunQueueComplete = viewer.HandleSuperAgentRunQueueComplete(superAgentStore)
		deps.superAgentSubagentTask = viewer.HandleSuperAgentSubagentTaskCreate(superAgentStore)
		deps.superAgentContextPack = viewer.HandleSuperAgentContextPackCreate(superAgentStore)
		deps.superAgentMessageChannel = viewer.HandleSuperAgentMessageChannelCreate(superAgentStore)
		deps.superAgentTraceEvent = viewer.HandleSuperAgentTraceEventCreate(superAgentStore)
	}
	if aiWorkflowStore != nil {
		deps.aiWorkflowStore = aiWorkflowStore
		if commands, err := aiworkflowapp.RegisterCommandFiles(context.Background(), aiWorkflowStore, aiworkflowapp.CommandRegistryScanOptions{RepoRoot: "."}); err != nil {
			log.Printf("Failed to register AI Workflow command files: %v", err)
		} else if len(commands) > 0 {
			log.Printf("AI Workflow command files registered: %d", len(commands))
		}
		deps.aiWorkflowStatus = viewer.HandleAIWorkflowStatusWithPolicy(aiWorkflowStore, domainai.ContextBudgetPolicy{
			MaxContextTokens: cfg.AIWorkflow.ContextBudgetTokens,
			WarnAtRatio:      cfg.AIWorkflow.ContextBudgetWarnRatio,
			StopAtRatio:      cfg.AIWorkflow.ContextBudgetStopRatio,
		})
		deps.aiWorkflowEvent = viewer.HandleAIWorkflowEventCreate(aiWorkflowStore)
		deps.aiWorkflowProjectMemory = viewer.HandleAIWorkflowProjectMemoryCreate(aiWorkflowStore)
		deps.aiWorkflowWorktree = viewer.HandleAIWorkflowWorktreeCreate(aiWorkflowStore)
		deps.aiWorkflowCommand = viewer.HandleAIWorkflowCommandCreate(aiWorkflowStore)
		deps.aiWorkflowCommandRun = viewer.HandleAIWorkflowCommandRun(aiWorkflowStore, deps.skillBootstrap)
		deps.aiWorkflowContextUsage = viewer.HandleAIWorkflowContextUsageCreate(aiWorkflowStore)
		deps.aiWorkflowContextBudget = viewer.HandleAIWorkflowContextBudgetCheck(aiWorkflowStore, domainai.ContextBudgetPolicy{
			MaxContextTokens: cfg.AIWorkflow.ContextBudgetTokens,
			WarnAtRatio:      cfg.AIWorkflow.ContextBudgetWarnRatio,
			StopAtRatio:      cfg.AIWorkflow.ContextBudgetStopRatio,
		})
		deps.aiWorkflowExternalControl = viewer.HandleAIWorkflowExternalControlCheck(aiWorkflowStore, domainai.ExternalControlPolicy{
			AllowedActors:    cfg.AIWorkflow.ExternalControlAllowedActors,
			AllowedChannels:  cfg.AIWorkflow.ExternalControlAllowedChannels,
			AllowedActions:   cfg.AIWorkflow.ExternalControlAllowedActions,
			ApprovalRequired: cfg.AIWorkflow.ExternalControlApprovalRequired,
		})
		deps.aiWorkflowHeavyWorker = viewer.HandleAIWorkflowHeavyWorkerEvaluate(aiWorkflowStore, domainai.HeavyWorkerPolicy{
			Enabled:                 cfg.AIWorkflow.HeavyWorkerEnabled,
			RequireReason:           cfg.AIWorkflow.HeavyWorkerRequireReason,
			FileCountThreshold:      cfg.AIWorkflow.HeavyWorkerFileThreshold,
			SpecCountThreshold:      cfg.AIWorkflow.HeavyWorkerSpecThreshold,
			FailedAttemptsThreshold: cfg.AIWorkflow.HeavyWorkerRetryThreshold,
		})
		deps.aiWorkflowProjectInit = viewer.HandleAIWorkflowProjectInit(aiworkflowapp.NewProjectScanner(aiWorkflowStore), cfg.AIWorkflow.ProjectMemoryRoot)
		worktreeManager := aiworkflowapp.NewWorktreeManager(aiWorkflowStore)
		deps.aiWorkflowWorktreeCreate = viewer.HandleAIWorkflowWorktreeCreateRuntime(worktreeManager, cfg.AIWorkflow.WorktreeBaseDir)
		deps.aiWorkflowWorktreeClose = viewer.HandleAIWorkflowWorktreeCloseRuntime(worktreeManager, cfg.AIWorkflow.WorktreeBaseDir)
		if sandboxStore != nil {
			worktreeSandboxManager := sandboxapp.NewWorktreeSandboxManager(worktreeManager, sandboxStore)
			deps.sandboxWorktreeCreate = viewer.HandleSandboxWorktreeCreate(worktreeSandboxManager, cfg.AIWorkflow.WorktreeBaseDir)
			deps.sandboxWorktreeClose = viewer.HandleSandboxWorktreeClose(worktreeSandboxManager, cfg.AIWorkflow.WorktreeBaseDir)
		}
	}
	if cfg.KnowledgeMemory.IsEnabled() {
		var knowledgeMemoryStore viewer.KnowledgeMemoryStore
		if cfg.KnowledgeMemory.Storage == "sqlite" {
			store, err := knowledgememorypersistence.NewSQLiteStore(cfg.KnowledgeMemory.SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize Knowledge Memory SQLite store: %v", err)
			}
			knowledgeMemoryStore = store
		} else {
			knowledgeMemoryStore = knowledgememorypersistence.NewJSONLStore(cfg.KnowledgeMemory.LogPath)
		}
		knowledgeMemoryStore = knowledgememorypersistence.WithL1Connection(knowledgeMemoryStore, conversationRuntime.L1Store)
		if dailyRules, ok := knowledgeMemoryStore.(knowledgememoryapp.DailyIntakeRuleStore); ok && conversationRuntime.L1Store != nil {
			startDailyIntakeSweeper(dailyRules, knowledgememorypersistence.NewDailyIntakeRegistryAdapter(conversationRuntime.L1Store), newBackgroundJobFailureReporter(deps.eventRelay))
		}
		deps.knowledgeMemoryStatus = viewer.HandleKnowledgeMemoryStatus(knowledgeMemoryStore)
		deps.personalArchiveCreate = viewer.HandlePersonalArchiveCreate(knowledgeMemoryStore)
		deps.creativeKnowledgeCreate = viewer.HandleCreativeKnowledgeCreate(knowledgeMemoryStore)
		deps.newsKnowledgeCreate = viewer.HandleNewsKnowledgeCreate(knowledgeMemoryStore)
		deps.dailyIntakeRuleCreate = viewer.HandleDailyIntakeRuleCreate(knowledgeMemoryStore)
		deps.temporalMemoryCreate = viewer.HandleTemporalMemoryMarkerCreate(knowledgeMemoryStore)
		deps.knowledgeMemoryReview = viewer.HandleKnowledgeMemoryReview(knowledgeMemoryStore)
		deps.dreamConsolidationCreate = viewer.HandleDreamConsolidationRunCreate(knowledgeMemoryStore)
		deps.dreamConsolidationProposal = viewer.HandleDreamConsolidationProposalCreate(knowledgeMemoryStore)
		deps.dreamConsolidationReview = viewer.HandleDreamConsolidationReview(knowledgeMemoryStore)
	}
	deps.recallTraceStore = conversationRuntime.L1Store
	verificationRuntime := buildVerificationRuntime(cfg, deps, conversationRuntime.L1Store)

	ttsRuntime := buildTTSEntryRuntime(cfg)
	vtuberBridge := buildVTuberBridge(cfg)
	lipSync := newTTSVTuberLipSync(vtuberBridge)
	ttsBridge := buildTTSClientBridge(
		cfg,
		func(ev orchestrator.OrchestratorEvent) {
			if deps.eventRelay != nil {
				deps.eventRelay.OnEvent(ev)
			}
		},
		func(sessionID, characterID, text string) {
			if lipSync != nil {
				lipSync.OnChunkReady(sessionID, characterID, text)
			}
		},
		func(sessionID, characterID string) {
			if lipSync != nil {
				lipSync.OnSessionCompleted(sessionID, characterID)
			}
		},
	)

	// NI-003: ToolRegistry エラーを SSE でユーザーに通知する
	if toolRuntime.SubagentMgr != nil && deps.eventRelay != nil {
		toolRuntime.SubagentMgr.SetRegistryErrorHandler(func(err error) {
			deps.eventRelay.OnEvent(orchestrator.NewEvent(
				"registry.error", "system", "subagent", err.Error(),
				"", "", "system", "system", "system",
			))
		})
	}

	bridges := buildViewerBridgeHandlers(cfg, deps, reportPath, ttsRuntime)
	buildIdleChatRuntime(
		cfg,
		deps,
		llmRuntime.Chat,
		llmRuntime.Worker,
		llmRuntime.ChatWorker,
		llmRuntime.Heavy,
		llmRuntime.Wild,
		sessionRuntime.CentralMemory,
		llmRuntime.Coder2,
		glossaryRuntime.RecentTopics,
		newRuntimeDailySourceBriefResearch(conversationRuntime.WebGatherFetcher, toolRuntime.WorkerRuntimeRunnerV2),
		ttsBridge,
	)
	startMovieCatalogBackfillJob(cfg, newBackgroundJobFailureReporter(deps.eventRelay))
	buildOrchestratorRuntime(
		cfg,
		deps,
		sessionRuntime.SessionRepo,
		agents,
		llmRuntime,
		workerExecutionService,
		nodeCaps,
		sessionRuntime.CentralMemory,
		ttsBridge,
		vtuberBridge,
		bridges,
		verificationRuntime,
	)
	buildHeartbeatRuntime(cfg, deps, agents.Shiro, sessionRuntime.MemoryStore)
	buildPronunciationCheckRuntime(cfg, deps)
	deps.extensionHealth = buildExtensionHealthHandler(cfg, deps)

	log.Println("Dependency injection complete")
	return deps
}

func buildExtensionHealthHandler(cfg *config.Config, deps *Dependencies) http.HandlerFunc {
	item := func(id string, kind string, name string, source string, configured bool, loaded bool, message string) viewer.ExtensionHealthItem {
		status := ""
		if loaded {
			status = "ok"
		}
		return viewer.ExtensionHealthItem{
			ID:         id,
			Kind:       kind,
			Name:       name,
			Source:     source,
			Status:     status,
			Configured: configured,
			Loaded:     loaded,
			Message:    message,
		}
	}
	items := []viewer.ExtensionHealthItem{
		item("tool-registry", "tool", "Tool Registry", "runtime", true, deps.toolRegistry != nil, ""),
		item("module-llm", "module", "RenCrow LLM Module", "runtime", true, len(deps.moduleLLMProviders) > 0, ""),
		item("module-worker", "module", "RenCrow Worker Module", "runtime", true, deps.moduleWorkerExecutor != nil, ""),
		item("module-stt", "module", "RenCrow STT Module", "runtime", true, deps.moduleSTTViewerInput != nil, ""),
		item("module-tts", "module", "RenCrow TTS Module", "runtime", true, deps.moduleTTSProvider != nil, ""),
		item("character-runtime", "module", "Six Character Runtime", "runtime", true, deps.characterRuntime != nil, ""),
		item("scheduler", "extension", "In-App Scheduler", "runtime", true, deps.schedulerStatus != nil, ""),
		item("history-repair", "extension", "JSONL History Repair", "runtime", true, deps.historyRepairJSONL != nil, ""),
		item("package-validation", "extension", "Package Update Validation", "runtime", true, deps.packageValidation != nil, ""),
		item("skill-governance", "skill", "Skill Governance", "config", cfg.SkillGovernance.IsEnabled(), deps.skillGovernanceRecent != nil, ""),
		item("sandbox", "extension", "Sandbox Promotion Gate", "config", cfg.Sandbox.Enabled, deps.sandboxStatus != nil, ""),
		item("browser-trace-api", "extension", "Browser Trace API Discovery", "config", cfg.BrowserTraceToAPI.IsEnabled(), deps.browserTraceAPIStatus != nil, ""),
		item("superagent-harness", "extension", "SuperAgent Harness", "config", cfg.SuperAgentHarness.IsEnabled(), deps.superAgentStatus != nil, ""),
		item("ai-workflow", "extension", "AI Workflow", "config", cfg.AIWorkflow.IsEnabled(), deps.aiWorkflowStatus != nil, ""),
		item("knowledge-memory", "extension", "Knowledge Memory", "config", cfg.KnowledgeMemory.IsEnabled(), deps.knowledgeMemoryStatus != nil, ""),
	}
	return viewer.HandleExtensionHealth(viewer.ExtensionHealthOptions{Items: items})
}

func selectComplexityCoder(runtime llmRuntimeProviders) *coderAdapter {
	if runtime.Coder3 != nil {
		return runtime.Coder3
	}
	if runtime.Coder2 != nil {
		return runtime.Coder2
	}
	if runtime.Coder1 != nil {
		return runtime.Coder1
	}
	return runtime.Coder4
}

func buildComplexityHotspotCoderDiffHandler(store viewer.ComplexityHotspotStore, runtime llmRuntimeProviders, workstreamSink viewer.ComplexityWorkstreamArtifactSink, sandboxStore viewer.SandboxPromotionStore) http.HandlerFunc {
	coder := selectComplexityCoder(runtime)
	if coder == nil {
		return viewer.HandleComplexityHotspotCoderDiffWithSandbox(store, nil, workstreamSink, sandboxStore)
	}
	return viewer.HandleComplexityHotspotCoderDiffWithSandbox(store, complexityapp.NewCoderDiffService(coder), workstreamSink, sandboxStore)
}

func buildAIWorkflowStore(cfg *config.Config) viewer.AIWorkflowStore {
	if cfg == nil || !cfg.AIWorkflow.IsEnabled() {
		return nil
	}
	if cfg.AIWorkflow.Storage == "sqlite" {
		store, err := aiworkflowpersistence.NewSQLiteStore(cfg.AIWorkflow.SQLitePath)
		if err != nil {
			log.Fatalf("Failed to initialize AI Workflow SQLite store: %v", err)
		}
		return store
	}
	store := aiworkflowpersistence.NewJSONLStore(cfg.AIWorkflow.LogPath)
	if err := store.CompactOperationalLogs(); err != nil {
		log.Printf("WARN: AI Workflow operational log GC failed: %v", err)
	}
	return store
}

func loadSkillGovernanceManifests(skillRoots []string) []domainskill.SkillManifest {
	manifests, err := domainskill.LoadManifestsFromDirs(skillRoots...)
	if err != nil {
		log.Printf("WARN: failed to load skill governance manifests: %v", err)
		return nil
	}
	return manifests
}

type personaRuntimeDefinitionOptions struct {
	triggerCategoryPath            string
	canonicalResponsePath          string
	canonicalResponseCooldownTurns int
	canonicalResponseMaxPerSession int
}

func defaultPersonaRuntimeDefinitionOptions() personaRuntimeDefinitionOptions {
	return personaRuntimeDefinitionOptions{
		triggerCategoryPath:            "triggers",
		canonicalResponsePath:          "canonical_responses",
		canonicalResponseCooldownTurns: 5,
		canonicalResponseMaxPerSession: 3,
	}
}

func personaRuntimeDefinitionOptionsFromConfig(cfg config.PersonaArchitectureConfig) personaRuntimeDefinitionOptions {
	opts := defaultPersonaRuntimeDefinitionOptions()
	if strings.TrimSpace(cfg.TriggerCategoryPath) != "" {
		opts.triggerCategoryPath = strings.Trim(strings.TrimSpace(cfg.TriggerCategoryPath), "/")
	}
	if strings.TrimSpace(cfg.CanonicalResponsePath) != "" {
		opts.canonicalResponsePath = strings.Trim(strings.TrimSpace(cfg.CanonicalResponsePath), "/")
	}
	if cfg.CanonicalResponseCooldownTurns > 0 {
		opts.canonicalResponseCooldownTurns = cfg.CanonicalResponseCooldownTurns
	}
	if cfg.CanonicalResponseMaxPerSession > 0 {
		opts.canonicalResponseMaxPerSession = cfg.CanonicalResponseMaxPerSession
	}
	return opts
}

func buildPersonaRuntimeTriggerDefinitions(characters map[string]domainpersona.CharacterProfile) []domainpersona.TriggerDefinition {
	return buildPersonaRuntimeTriggerDefinitionsWithOptions(characters, defaultPersonaRuntimeDefinitionOptions())
}

func buildPersonaRuntimeTriggerDefinitionsWithOptions(characters map[string]domainpersona.CharacterProfile, opts personaRuntimeDefinitionOptions) []domainpersona.TriggerDefinition {
	definitions := make([]domainpersona.TriggerDefinition, 0)
	for characterID, profile := range characters {
		for key, content := range profile.Persona {
			if !isPersonaKeyUnder(key, opts.triggerCategoryPath) {
				continue
			}
			category := personaCategoryFromKey(key, opts.triggerCategoryPath)
			keywords := triggerKeywordsFromMarkdown(content)
			if len(keywords) == 0 {
				continue
			}
			definitions = append(definitions, domainpersona.TriggerDefinition{
				TriggerID:   characterID + ":" + key,
				CharacterID: characterID,
				Category:    category,
				Keywords:    keywords,
				Priority:    len(keywords),
			})
		}
	}
	return definitions
}

func buildPersonaRuntimeCanonicalResponses(characters map[string]domainpersona.CharacterProfile) []domainpersona.CanonicalResponseDefinition {
	return buildPersonaRuntimeCanonicalResponsesWithOptions(characters, defaultPersonaRuntimeDefinitionOptions())
}

func buildPersonaRuntimeCanonicalResponsesWithOptions(characters map[string]domainpersona.CharacterProfile, opts personaRuntimeDefinitionOptions) []domainpersona.CanonicalResponseDefinition {
	definitions := make([]domainpersona.CanonicalResponseDefinition, 0)
	for characterID, profile := range characters {
		for key, content := range profile.Persona {
			if !isPersonaKeyUnder(key, opts.canonicalResponsePath) {
				continue
			}
			response := canonicalResponseTextFromMarkdown(content)
			if response == "" {
				continue
			}
			category := personaCategoryFromKey(key, opts.canonicalResponsePath)
			definitions = append(definitions, domainpersona.CanonicalResponseDefinition{
				ResponseID:       characterID + ":" + key,
				CharacterID:      characterID,
				Category:         category,
				Response:         response,
				RequiredContexts: []string{category},
				CooldownTurns:    opts.canonicalResponseCooldownTurns,
				MaxPerSession:    opts.canonicalResponseMaxPerSession,
				Priority:         1,
			})
		}
	}
	return definitions
}

func isPersonaKeyUnder(key string, root string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	root = strings.ToLower(strings.Trim(strings.TrimSpace(root), "/"))
	if root == "" {
		return false
	}
	return key == root || strings.HasPrefix(key, root+"/")
}

func personaCategoryFromKey(key string, root string) string {
	key = strings.Trim(strings.TrimSpace(key), "/")
	root = strings.Trim(strings.TrimSpace(root), "/")
	if root != "" && strings.HasPrefix(key, root+"/") {
		key = strings.TrimPrefix(key, root+"/")
	}
	if strings.Contains(key, "/") {
		key = strings.Split(key, "/")[0]
	}
	if strings.TrimSpace(key) != "" && key != root {
		return strings.TrimSpace(key)
	}
	return "general"
}

func canonicalResponseTextFromMarkdown(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if len(lines) > 0 {
				break
			}
			continue
		}
		line = strings.TrimLeft(line, "-* ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func triggerKeywordsFromMarkdown(content string) []string {
	seen := map[string]struct{}{}
	var keywords []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*0123456789. ")
		line = strings.Trim(line, "` ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, part := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == '、' || r == '/' || r == '|' || r == '・'
		}) {
			keyword := strings.TrimSpace(part)
			if keyword == "" {
				continue
			}
			if _, ok := seen[keyword]; ok {
				continue
			}
			seen[keyword] = struct{}{}
			keywords = append(keywords, keyword)
		}
	}
	return keywords
}

// buildSessionLogSources は設定からSessionLogSourcesを構築する。
// 設定が空の場合はデフォルト（RenCrow/Codex/Claude）を返す。
func buildSessionLogSources(cfg *config.Config) []dcipersistence.SessionLogSource {
	if len(cfg.DCI.SessionLogSources) > 0 {
		sources := make([]dcipersistence.SessionLogSource, 0, len(cfg.DCI.SessionLogSources))
		for _, s := range cfg.DCI.SessionLogSources {
			sources = append(sources, dcipersistence.SessionLogSource{
				Name:    s.Name,
				PathDir: os.ExpandEnv(s.PathDir),
				Format:  dcipersistence.SessionLogFormat(s.Format),
			})
		}
		return sources
	}
	// デフォルト: RenCrow/Codex/Claude の既知パス
	home := os.Getenv("HOME")
	if home == "" {
		return nil
	}
	return []dcipersistence.SessionLogSource{
		{
			Name:    "rencrow",
			PathDir: filepath.Join(home, ".rencrow", "logs", "sessions"),
			Format:  dcipersistence.SessionLogFormatRenCrow,
		},
		{
			Name:    "codex",
			PathDir: filepath.Join(home, ".codex", "sessions"),
			Format:  dcipersistence.SessionLogFormatCodex,
		},
		{
			Name:    "claude",
			PathDir: filepath.Join(home, ".claude", "projects", "-home-nyukimi-rencrow-multiLLM"),
			Format:  dcipersistence.SessionLogFormatClaude,
		},
	}
}
