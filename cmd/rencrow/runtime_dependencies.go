package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	configpolicy "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config/policybundle"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/modulebridge"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	artifactcleanupapp "github.com/Nyukimin/RenCrow_CORE/internal/application/artifactcleanup"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	complexityapp "github.com/Nyukimin/RenCrow_CORE/internal/application/complexity"
	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	durablestoreapp "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	historyrepairapp "github.com/Nyukimin/RenCrow_CORE/internal/application/historyrepair"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	knowledgememoryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	newsbriefapp "github.com/Nyukimin/RenCrow_CORE/internal/application/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	otelexportapp "github.com/Nyukimin/RenCrow_CORE/internal/application/otelexport"
	packagevalidationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/packagevalidation"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	sandboxapp "github.com/Nyukimin/RenCrow_CORE/internal/application/sandbox"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	skillapp "github.com/Nyukimin/RenCrow_CORE/internal/application/skillgovernance"
	superagentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	xbookmarkworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/xbookmarkworkflow"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	backlogfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	browsertracepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	personapersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
	policydecisionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/policydecision"
	revenuepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
	sandboxpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
	skillpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	xbookmarkworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/xbookmarkworkflow"
	personainfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/userhome"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

// Dependencies はアプリケーション依存関係
type runtimeCanonicalEventStore interface {
	modulecore.EventStore
	Close() error
}

type Dependencies struct {
	redisHealthCheck               func(context.Context) error
	dataCapabilityCatalog          *runtimeDataCapabilityCatalog
	lineHandler                    http.Handler
	telegramHandler                http.Handler
	discordHandler                 http.Handler
	slackHandler                   http.Handler
	eventHub                       *viewer.EventHub                            // live viewer
	monitorStore                   *viewer.MonitorStore                        // viewer monitor snapshots
	eventLogStore                  *viewer.CanonicalEventLog                   // canonical orchestrator event projection
	canonicalEventStore            runtimeCanonicalEventStore                  // canonical append-only Event Store
	reportStore                    *executionpersistence.JSONLReportStore      // execution evidence store
	eventRelay                     *idleAwareEventListener                     // viewer + idlechat stop relay
	viewerStatus                   http.HandlerFunc                            // viewer status API
	viewerAgents                   http.HandlerFunc                            // viewer agents API
	viewerAgentDetail              http.HandlerFunc                            // viewer agent detail API
	tasks                          http.HandlerFunc                            // canonical Task list API
	taskDetail                     http.HandlerFunc                            // canonical Task detail API
	taskNotifications              http.HandlerFunc                            // canonical Task interrupt notification API
	taskManager                    *taskmanager.Manager                        // shared canonical Task lifecycle owner
	viewerLogs                     http.HandlerFunc                            // viewer logs API
	viewerPromptDebug              http.HandlerFunc                            // LLM prompt boundary debug API
	viewerAuditSummary             http.HandlerFunc                            // viewer audit summary API
	viewerSend                     http.HandlerFunc                            // viewer message sender
	agentOps                       http.HandlerFunc                            // local authenticated Agent OPS ingress
	viewerGamesStatus              http.HandlerFunc                            // RenCrow_GAMES bridge status API
	viewerGamesResult              http.HandlerFunc                            // RenCrow_GAMES result callback API
	viewerGamesSessions            http.HandlerFunc                            // RenCrow_GAMES recent session observer API
	viewerGamesEvents              http.HandlerFunc                            // RenCrow_GAMES candidate event observer API
	viewerGamesDecision            http.HandlerFunc                            // Agent-owned game turn decision API
	viewerGamesObserverPage        http.HandlerFunc                            // RenCrow_GAMES live observer UI proxy page
	viewerGamesLaunch              http.HandlerFunc                            // RenCrow_GAMES launch proxy (マルチペルソナ WP5)
	gameAutoplay                   *viewer.GameAutoplayService                 // ペルソナ自発プレイランナー (マルチペルソナ WP6)
	viewerGamesObserverProxy       http.HandlerFunc                            // RenCrow_GAMES live observer API proxy
	viewerTradeStatus              http.HandlerFunc                            // RenCrow_TRADE read-only status projection
	viewerTradePolicyEvaluation    http.HandlerFunc                            // RenCrow_TRADE pure policy diagnostic evaluation
	viewerTradeRiskPreview         http.HandlerFunc                            // RenCrow_TRADE non-mutating portfolio risk preview
	viewerTradeSimulationCommit    http.HandlerFunc                            // RenCrow_TRADE simulation-only atomic portfolio commit
	viewerTradeShadowObservation   http.HandlerFunc                            // RenCrow_TRADE immutable Shadow observation record
	viewerTradeShadowOutcome       http.HandlerFunc                            // RenCrow_TRADE immutable Shadow outcome record
	viewerTradeShadowOutcomeReport http.HandlerFunc                            // RenCrow_TRADE read-only Shadow outcome report
	viewerTradeShadowReview        http.HandlerFunc                            // RenCrow_TRADE immutable Shadow outcome review record
	viewerTradeShadowReviewReport  http.HandlerFunc                            // RenCrow_TRADE read-only Shadow review report
	historyRepairJSONL             http.HandlerFunc                            // viewer JSONL history repair API
	packageValidation              http.HandlerFunc                            // viewer package/update validation API
	characterRuntime               http.HandlerFunc                            // viewer six-character conversation runtime API
	extensionHealth                http.HandlerFunc                            // viewer plugin / extension health API
	otelExport                     http.HandlerFunc                            // viewer OpenTelemetry export API
	artifactCleanup                http.HandlerFunc                            // viewer stale artifact cleanup API
	repairRunner                   viewer.RepairTaskRunner                     // viewer repair Task runner
	voiceDirectHandler             voiceDirectFinalHandler                     // VDS llm.final -> SSE
	evidenceHandler                http.HandlerFunc                            // viewer evidence API
	evidenceDetail                 http.HandlerFunc                            // viewer evidence detail API
	evidenceSummary                http.HandlerFunc                            // viewer evidence summary API
	glossaryRecent                 http.HandlerFunc                            // viewer glossary API
	viewerMemorySnapshot           http.HandlerFunc                            // viewer memory/news/recall API
	viewerMemoryOwner              http.HandlerFunc                            // authenticated CMD memory owner API
	viewerMemoryChatGPTImportOwner http.HandlerFunc                            // authenticated ChatGPT Common Raw owner API
	viewerMemoryLayers             http.HandlerFunc                            // viewer memory layer API
	viewerMemoryEvents             http.HandlerFunc                            // viewer L1 event/search cache API
	viewerMemoryState              http.HandlerFunc                            // viewer memory state API
	viewerMemoryPromote            http.HandlerFunc                            // viewer memory promote API
	viewerMemoryUser               http.HandlerFunc                            // viewer user memory API
	viewerMemoryUserState          http.HandlerFunc                            // viewer user memory state API
	viewerMemoryUserForget         http.HandlerFunc                            // viewer user memory forget API
	viewerMemoryUserSupersede      http.HandlerFunc                            // viewer user memory supersede API
	viewerMemoryRecallPack         http.HandlerFunc                            // viewer memory recall pack API
	viewerMemoryProfilePromotions  http.HandlerFunc                            // async ProfilePromotion job API
	viewerMemoryProfileRetry       http.HandlerFunc                            // explicit ProfilePromotion retry API
	viewerRecallTraces             http.HandlerFunc                            // viewer recall trace API
	viewerSourceRegistry           http.HandlerFunc                            // viewer source registry API
	viewerXBookmarkWorkflow        http.HandlerFunc                            // explicit X Bookmark utilization workflows
	viewerDomainGraphAssertions    http.HandlerFunc                            // viewer domain graph assertion API
	viewerMovieDomainGraphSync     http.HandlerFunc                            // viewer movie domain graph sync API
	viewerHobbyDomainGraphSync     http.HandlerFunc                            // viewer hobby domain graph sync API
	verificationRecent             http.HandlerFunc                            // viewer verification recent API
	verificationDetail             http.HandlerFunc                            // viewer verification detail API
	verificationSummary            http.HandlerFunc                            // viewer verification summary API
	toolHarnessRecent              http.HandlerFunc                            // viewer tool harness mediation API
	globalPolicyStatus             http.HandlerFunc                            // read-only Global Policy Bundle status API
	globalPolicyStore              *configpolicy.Store                         // immutable active policy snapshot and reload state
	globalPolicyDecisions          http.HandlerFunc                            // read-only common policy decision evidence API
	globalPolicyDecisionStore      *policydecisionpersistence.JSONLStore       // append-only common policy decision evidence store
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
	revenuePolicyDecision          http.HandlerFunc                            // viewer revenue policy decision API
	revenueDailyRoutine            http.HandlerFunc                            // viewer revenue daily routine draft report API
	revenueChannelDraft            http.HandlerFunc                            // viewer revenue channel draft API
	revenueExternalSendApply       http.HandlerFunc                            // viewer revenue external send apply audit API
	revenueOpportunities           http.HandlerFunc                            // viewer economic opportunities API
	revenueEconomicTasks           http.HandlerFunc                            // viewer economic tasks API
	revenueDeliveries              http.HandlerFunc                            // viewer economic deliveries API
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
	superAgentStore                viewer.SuperAgentStore                      // SuperAgent runtime telemetry store
	superAgentRunController        *superagentapp.RunController                // SuperAgent runtime pause/resume controller
	aiWorkflowStatus               http.HandlerFunc                            // viewer AI workflow status API
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
	atlasService                   *backlogapp.Service                         // Atlas lifecycle owner service
	atlasHandler                   http.HandlerFunc                            // Atlas read projection + authenticated owner API
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
	idleChatSurfacePresence        *idleChatSurfacePresenceController          // PORTAL Chat/IdleChat surface lease arbitration
	dailyNewsBriefReader           domainnews.DailyNewsBriefReader             // scheduled cache with persistent L1 fallback
	sshTransports                  map[string]domaintransport.Transport        // v4 SSH transports
	heartbeatSvc                   *heartbeat.HeartbeatService                 // heartbeat service
	advisorCloser                  interface{ Close() error }                  // advisor SQLite store, when configured
	durableStoreWorkflow           orchestrator.DurableStoreWorkflow           // Chat起点の永続Store判定
	durableStoreCloser             interface{ Close() error }                  // workflow decision SQLite store
	conversationArchiveCloser      interface{ Close() error }                  // CORE-owned L2 archive and request receipts
	conversationCloser             interface{ Close() error }                  // primary conversation manager or L1 store
	knowledgeMemoryToolStore       interface{ Close() error }                  // indexed Tool search store
	knowledgeMemoryViewerStore     interface{ Close() error }                  // writable Viewer store, when configured
	advisorScoreCancel             context.CancelFunc                          // Advisor daily score job
	memoryPromotionCancel          context.CancelFunc                          // async ProfilePromotion worker
	personRelatedSummaryCancel     context.CancelFunc                          // fixed-ID related-work summary worker
	personRelatedIdentityCancel    context.CancelFunc                          // fixed-authority person identity worker
	personRelatedCollectionCancel  context.CancelFunc                          // positive movie/person D1 category collector
	toolRegistry                   capdomain.ToolRegistry                      // Phase 4: Shiro ツール共有用 ToolRegistry
	workerToolRunner               domaintool.RunnerV2                         // production Worker tool execution/listing boundary
	personRelatedCatalogLookup     viewer.PersonRelatedCatalogProvider         // read-only Viewer projection over the startup lookup instance
	personRelatedCatalogPeople     viewer.PersonRelatedCatalogPeopleProvider   // indexed explicitly assessed people projection
	serenaMCPClient                serenaMCPClient                             // lifecycle owner for the connected Serena MCP process
	moduleChatService              chatModuleService                           // module contract view of Chat service
	moduleLLMProviders             map[string]modulellm.Provider               // module contract view of LLM providers
	moduleTTSProvider              moduletts.Provider                          // module contract view of primary TTS provider
	moduleTTSPlayback              moduletts.PlaybackStateObserver             // module contract view of Viewer playback state
	moduleSTTViewerInput           modulestt.ViewerInputObserver               // module contract view of Viewer STT input state
	moduleWorkerExecutor           moduleworker.Executor                       // module contract view of Worker executor
	moduleHealth                   http.HandlerFunc                            // module boundary health API
	llmBusyTracker                 *llmBusyTracker                             // runtime LLM execution tracker for IdleChat gating
	llmGatewayProcess              *os.Process                                 // local RenCrow_LLM process started by CORE
	webGatherDeps                  func() webGatherCLIDeps                     // web-gather diagnostics dependencies for the Public API
}

// Shutdown はリソースを解放
func (d *Dependencies) Shutdown() {
	if d.llmGatewayProcess != nil {
		if err := d.llmGatewayProcess.Kill(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already finished") {
			log.Printf("Failed to stop CORE-started RenCrow_LLM Gateway: %v", err)
		}
	}
	if d.serenaMCPClient != nil {
		d.serenaMCPClient.Stop()
	}
	if d.memoryPromotionCancel != nil {
		d.memoryPromotionCancel()
	}
	if d.personRelatedSummaryCancel != nil {
		d.personRelatedSummaryCancel()
	}
	if d.personRelatedIdentityCancel != nil {
		d.personRelatedIdentityCancel()
	}
	if d.personRelatedCollectionCancel != nil {
		d.personRelatedCollectionCancel()
	}
	if d.advisorScoreCancel != nil {
		d.advisorScoreCancel()
	}
	if d.pronunciationCheckCancel != nil {
		d.pronunciationCheckCancel()
	}
	if d.gameAutoplay != nil {
		d.gameAutoplay.Stop()
	}
	if d.heartbeatSvc != nil {
		d.heartbeatSvc.Stop()
	}
	if d.advisorCloser != nil {
		if err := d.advisorCloser.Close(); err != nil {
			log.Printf("Failed to close advisor store: %v", err)
		}
	}
	if d.durableStoreCloser != nil {
		if err := d.durableStoreCloser.Close(); err != nil {
			log.Printf("Failed to close durable store workflow registry: %v", err)
		}
	}
	if d.conversationArchiveCloser != nil {
		if err := d.conversationArchiveCloser.Close(); err != nil {
			log.Printf("Failed to close Conversation Archive store: %v", err)
		}
	}
	if d.conversationCloser != nil {
		if err := d.conversationCloser.Close(); err != nil {
			log.Printf("Failed to close Conversation runtime: %v", err)
		}
	}
	if d.knowledgeMemoryToolStore != nil {
		if err := d.knowledgeMemoryToolStore.Close(); err != nil {
			log.Printf("Failed to close Knowledge Memory Tool store: %v", err)
		}
	}
	if d.knowledgeMemoryViewerStore != nil {
		if err := d.knowledgeMemoryViewerStore.Close(); err != nil {
			log.Printf("Failed to close Knowledge Memory Viewer store: %v", err)
		}
	}
	if d.idleChatOrch != nil {
		if d.idleChatSurfacePresence != nil {
			d.idleChatSurfacePresence.Close()
		}
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
	if d.canonicalEventStore != nil {
		if err := d.canonicalEventStore.Close(); err != nil {
			log.Printf("Failed to close Canonical Event Store: %v", err)
		}
	}
	log.Println("Shutdown complete")
}

// prepareAtlasLifecycleService applies the one startup migration before
// lease recovery. Any migration or recovery failure returns no service so the
// caller cannot expose a misleading Atlas lifecycle projection.
func prepareAtlasLifecycleService(ctx context.Context, service *backlogapp.Service) (*backlogapp.Service, bool, error) {
	if service == nil {
		return nil, false, nil
	}
	migrated, err := service.MigrateLegacyAtlasLifecycle(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := service.Recover(ctx); err != nil {
		return nil, migrated, err
	}
	return service, migrated, nil
}

// buildDependencies は依存関係を構築
func buildDependencies(cfg *config.Config) *Dependencies {
	runtimeToolRegistry := buildRuntimeToolRegistry(cfg)
	nodeCaps := buildCapabilityRuntime(cfg, runtimeToolRegistry)
	canonicalEventStore, err := openRuntimeCanonicalEventStore(cfg.Storage.Databases.EventStore)
	if err != nil {
		log.Fatalf("Failed to initialize Canonical Event Store: %v", err)
	}
	aiWorkflowStore := composeRuntimeAIWorkflowStore(buildAIWorkflowStateStore(cfg), canonicalEventStore)
	llmBusyTracker := newLLMBusyTracker()
	llmRuntime := buildLLMRuntimeProviders(cfg, aiWorkflowStore, llmBusyTracker)
	classifier := routing.NewLLMClassifier(llmRuntime.Chat, cfg.Prompts.Classifier)
	ruleDictionary := routing.NewRuleDictionary()
	skillLoader := domaincontext.NewSkillsLoader("")
	runtimeSkillMetadata, skillLoadErr := skillLoader.LoadAllFromDirs(cfg.SkillGovernance.SkillRoots...)
	if skillLoadErr != nil {
		log.Printf("WARN: failed to load runtime skills: %v", skillLoadErr)
		runtimeSkillMetadata = nil
	}
	if runtimeSkillMetadata == nil {
		runtimeSkillMetadata = []domaincontext.SkillMetadata{}
	}
	runtimeSkillCatalog := toolsinfra.NewSkillCatalog(runtimeSkillMetadata)
	// Skill manifests are retained for later Skill Governance persistence.
	runtimeSkillManifests := loadSkillGovernanceManifests(cfg.SkillGovernance.SkillRoots)
	serenaWorkspace := cfg.SelfSourceDir
	if serenaWorkspace == "" {
		if abs, err := filepath.Abs(cfg.Worker.Workspace); err == nil {
			serenaWorkspace = abs
		} else {
			serenaWorkspace = cfg.Worker.Workspace
		}
	}
	serenaRuntime := newSerenaMCPRuntime(
		context.Background(),
		envBool("RENCROW_ENABLE_SERENA_MCP"),
		serenaWorkspace,
		productionSerenaMCPClientFactory,
	)
	if serenaRuntime.state == serenaMCPStateAvailable {
		log.Printf("Serena MCP ready: %d tools available", len(serenaRuntime.catalog.Entries()))
	} else {
		log.Printf("Serena MCP unavailable (%s): %s", serenaRuntime.state, serenaRuntime.reason)
	}
	toolRuntime := buildToolRuntimeWithCapabilities(
		cfg,
		llmRuntime.WorkerToolProvider,
		runtimeToolRegistry,
		aiWorkflowStore,
		runtimeSkillCatalog,
		serenaRuntime.catalog,
	)
	advisorRuntime, err := buildAdvisorRuntime(cfg, toolRuntime.WorkerRuntimeRunnerV2)
	if err != nil {
		log.Fatalf("Failed to initialize Advisor runtime: %v", err)
	}
	mcpClient := mcp.NewMCPClient()
	log.Printf("MCPClient initialized with %d servers", len(mcpClient.ListServers()))
	conversationRuntime := buildConversationRuntime(cfg, llmRuntime.Primary, toolRuntime.ChatRunnerV2, toolRuntime.WorkerRunnerV2)
	glossaryRuntime := buildGlossaryRuntime(cfg)
	// Conversation runtime may attach late-bound web_gather adapters to the
	// production Worker runner. Build the Agent snapshot only after every
	// runtime Tool has been registered so awareness and execution stay equal.
	mcpObservations := append([]runtimeMCPObservation(nil), serenaRuntime.observations...)
	mcpObservations = append(mcpObservations, observeGenericMCPClient(context.Background(), mcpClient)...)
	runtimeCapabilityContext := runtimeCapabilityContextFromWorkerRunnerWithSkills(
		context.Background(),
		toolRuntime.WorkerRuntimeRunnerV2,
		runtimeSkillMetadata,
		runtimeSkillManifests,
		mcpObservations,
	)
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
	if serenaRuntime.client != nil {
		workerExecutionService.SetMCPToolCaller(serenaRuntime.client)
	}

	deps := &Dependencies{serenaMCPClient: serenaRuntime.client, canonicalEventStore: canonicalEventStore}
	dataRecallRegistry := toolRuntime.DataRecallRegistry
	dataWriteRegistry := toolRuntime.DataWriteRegistry
	if err := registerRuntimeDataRecallCanonicalEvents(dataRecallRegistry, canonicalEventStore); err != nil {
		log.Fatalf("Failed to register Canonical Event data recall: %v", err)
	}
	deps.conversationArchiveCloser = conversationRuntime.ArchiveCloser
	deps.conversationCloser = conversationRuntime.Closer
	if conversationRuntime.Manager != nil {
		deps.redisHealthCheck = conversationRuntime.Manager.RedisHealth
	}
	if toolRuntime.MovieCatalogLookup != nil {
		if err := registerRuntimeDataWriteMovieCatalog(dataWriteRegistry, toolRuntime.MovieCatalogLookup); err != nil {
			log.Fatalf("Failed to register Movie Catalog data write: %v", err)
		}
		if err := registerRuntimeDataRecallMovieCatalog(dataRecallRegistry, toolRuntime.MovieCatalogLookup); err != nil {
			log.Fatalf("Failed to register Movie Catalog data recall: %v", err)
		}
	}
	if toolRuntime.MusicCatalogLookup != nil {
		if err := registerRuntimeDataWriteHobbyGraph(dataWriteRegistry, toolRuntime.MusicCatalogLookup); err != nil {
			log.Fatalf("Failed to register Hobby Graph data write: %v", err)
		}
		if err := registerRuntimeDataRecallHobbyGraph(dataRecallRegistry, toolRuntime.MusicCatalogLookup); err != nil {
			log.Fatalf("Failed to register Hobby Graph data recall: %v", err)
		}
	}
	if runtimeToolRegistry != nil {
		if err := registerRuntimeDataRecallToolRegistry(dataRecallRegistry, runtimeToolRegistry); err != nil {
			log.Fatalf("Failed to register Tool Registry data recall: %v", err)
		}
		if err := registerRuntimeDataWriteToolRegistry(dataWriteRegistry, cfg.WorkspaceDir, runtimeToolRegistry); err != nil {
			log.Fatalf("Failed to register Tool Registry data write: %v", err)
		}
	}
	if conversationRuntime.L1Store != nil {
		if err := registerRuntimeDataRecallConversationL1(dataRecallRegistry, conversationRuntime.L1Store); err != nil {
			log.Fatalf("Failed to register Conversation L1 data recall: %v", err)
		}
		if err := registerRuntimeDataWriteConversationL1(dataWriteRegistry, conversationRuntime.L1Store); err != nil {
			log.Fatalf("Failed to register Conversation L1 data write: %v", err)
		}
	}
	if conversationRuntime.L1Store != nil && conversationRuntime.ArchiveStore != nil {
		if err := registerRuntimeDataRecallConversationArchive(dataRecallRegistry, conversationRuntime.ArchiveStore); err != nil {
			log.Fatalf("Failed to register Conversation Archive data recall: %v", err)
		}
		if err := registerRuntimeDataWriteConversationArchive(dataWriteRegistry, conversationRuntime.L1Store, conversationRuntime.ArchiveStore); err != nil {
			log.Fatalf("Failed to register Conversation Archive data write: %v", err)
		}
	}
	if glossaryRuntime.IndexedLookup != nil {
		var err error
		if glossaryRuntime.CandidateStore != nil {
			err = registerRuntimeDataRecallGlossary(dataRecallRegistry, glossaryRuntime.IndexedLookup, glossaryRuntime.CandidateStore)
		} else {
			err = registerRuntimeDataRecallGlossary(dataRecallRegistry, glossaryRuntime.IndexedLookup)
		}
		if err != nil {
			log.Fatalf("Failed to register Glossary data recall: %v", err)
		}
	}
	if glossaryRuntime.CandidateStore != nil {
		if err := registerRuntimeDataWriteGlossary(dataWriteRegistry, glossaryRuntime.CandidateStore); err != nil {
			log.Fatalf("Failed to register Glossary data write: %v", err)
		}
	}
	durableStoreWorkflow, durableStoreCloser, err := buildDurableStoreRuntime(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize durable store workflow: %v", err)
	}
	deps.durableStoreWorkflow = durableStoreWorkflow
	deps.durableStoreCloser = durableStoreCloser
	if durableRecallStore, ok := durableStoreCloser.(durablestoreapp.Store); ok {
		if err := registerRuntimeDataRecallDurableStoreWorkflow(dataRecallRegistry, durableRecallStore); err != nil {
			log.Fatalf("Failed to register Durable Store data recall: %v", err)
		}
		if err := registerRuntimeDataWriteDurableStoreWorkflow(dataWriteRegistry, durableStoreWorkflow); err != nil {
			log.Fatalf("Failed to register Durable Store data write: %v", err)
		}
	}
	deps.globalPolicyStore = configpolicy.NewStore(cfg.WorkspaceDir)
	globalPolicy := deps.globalPolicyStore.Status()
	deps.globalPolicyStatus = viewer.HandleGlobalPolicyStatus(deps.globalPolicyStore)
	policyDecisionStore, err := policydecisionpersistence.NewJSONLStore(filepath.Join(cfg.WorkspaceDir, "logs", "policy_decisions.jsonl"))
	if err != nil {
		log.Printf("Global policy decision store unavailable: %v", err)
	} else {
		deps.globalPolicyDecisionStore = policyDecisionStore
		deps.globalPolicyDecisions = viewer.HandlePolicyDecisions(policyDecisionStore)
	}
	if err := wireRuntimeTradeInvestmentRoutes(
		context.Background(),
		cfg,
		dataRecallRegistry,
		dataWriteRegistry,
		deps.globalPolicyStore,
		deps.globalPolicyDecisionStore,
	); err != nil {
		log.Fatalf("Failed to register TRADE investment data routes: %v", err)
	}
	deps.viewerTradeStatus = newTradeStatusHandler(cfg)
	deps.viewerTradePolicyEvaluation = newTradePolicyEvaluationHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeRiskPreview = newTradeRiskPreviewHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeSimulationCommit = newTradeSimulationCommitHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeShadowObservation = newTradeShadowObservationHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeShadowOutcome = newTradeShadowOutcomeHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeShadowOutcomeReport = newTradeShadowOutcomeReportHandler(cfg)
	deps.viewerTradeShadowReview = newTradeShadowReviewHandler(cfg, deps.globalPolicyStore, deps.globalPolicyDecisionStore)
	deps.viewerTradeShadowReviewReport = newTradeShadowReviewReportHandler(cfg)
	log.Printf(
		"Global Policy Bundle state=%s contract=%s revision=%s",
		globalPolicy.State,
		globalPolicy.ContractRevision,
		globalPolicy.BundleRevision,
	)
	deps.advisorCloser = advisorRuntime.Closer
	if err := registerRuntimeDataRecallAdvisor(dataRecallRegistry, advisorRuntime.Store); err != nil {
		log.Fatalf("Failed to register Advisor data recall: %v", err)
	}
	advisorOwnerStore, ok := advisorRuntime.Store.(runtimeAdvisorAdoptionStore)
	if !ok {
		log.Fatalf("Failed to initialize Advisor data write owner store")
	}
	if err := registerRuntimeDataWriteAdvisor(dataWriteRegistry, advisorOwnerStore); err != nil {
		log.Fatalf("Failed to register Advisor data write: %v", err)
	}
	if err := registerRuntimeDataRecallAdvisorAdoptions(dataRecallRegistry, advisorOwnerStore); err != nil {
		log.Fatalf("Failed to register Advisor adoption data recall: %v", err)
	}
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
	deps.knowledgeMemoryToolStore = toolRuntime.KnowledgeMemoryToolStore
	deps.workerToolRunner = toolRuntime.WorkerRuntimeRunnerV2
	if toolRuntime.PersonRelatedSummaryWorker != nil {
		deps.personRelatedSummaryCancel = startRuntimePersonRelatedSummaryWorker(
			toolRuntime.PersonRelatedSummaryWorker,
			newBackgroundJobFailureReporter(deps.eventRelay),
		)
	}
	if toolRuntime.PersonRelatedIdentityWorker != nil {
		deps.personRelatedIdentityCancel = startRuntimePersonRelatedIdentityWorker(
			toolRuntime.PersonRelatedIdentityWorker,
			newBackgroundJobFailureReporter(deps.eventRelay),
		)
	}
	if toolRuntime.PersonRelatedCollectionWorker != nil {
		deps.personRelatedCollectionCancel = startRuntimePersonRelatedCollectionWorker(
			toolRuntime.PersonRelatedCollectionWorker,
			newBackgroundJobFailureReporter(deps.eventRelay),
		)
	}
	if toolRuntime.PersonRelatedCatalogLookup != nil {
		lookup := toolRuntime.PersonRelatedCatalogLookup
		deps.personRelatedCatalogLookup = func(ctx context.Context, personID, category string, limit int) (personrelatedcatalogapp.LookupResult, error) {
			return lookup.LookupByPersonID(ctx, personID, category, limit)
		}
		deps.personRelatedCatalogPeople = lookup.EligiblePeople
	} else {
		deps.personRelatedCatalogLookup = nil
		deps.personRelatedCatalogPeople = nil
	}
	deps.dataCapabilityCatalog = toolRuntime.DataCapabilityCatalog
	deps.backlogStore = viewer.NewBacklogStore(filepath.Join(cfg.WorkspaceDir, "logs", "backlog.jsonl"))
	workflowResults := xbookmarkworkflowpersistence.NewJSONLStore(filepath.Join(cfg.WorkspaceDir, "logs", "x_bookmark_workflows.jsonl"))
	if conversationRuntime.L1Store == nil {
		deps.viewerXBookmarkWorkflow = viewer.HandleXBookmarkWorkflow(nil)
	} else {
		workflowService := xbookmarkworkflowapp.NewService(
			conversationRuntime.L1Store,
			workflowResults,
			llmRuntime.Worker,
			newConfiguredImageGateway(cfg),
			deps.backlogStore,
		)
		deps.viewerXBookmarkWorkflow = viewer.HandleXBookmarkWorkflow(workflowService)
	}
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
	gamePlayProvider := selectChatConversationProvider(llmRuntime.ChatWorker, llmRuntime.Chat)
	buildViewerRuntimeHandlers(
		cfg,
		deps,
		conversationRuntime.L1Store,
		conversationRuntime.Manager,
		reportPath,
		gamePlayProvider,
		newGameAgentDecisionService(agents),
	)
	if defaultTaskStorePath(cfg.WorkspaceDir) != "" && deps.taskManager == nil {
		log.Fatal("Failed to initialize canonical Task lifecycle owner")
	}
	deps.webGatherDeps = newWebGatherDiagnosticsDeps(cfg, conversationRuntime.L1Store)
	deps.advisorScoreCancel = startAdvisorScoreJob(
		advisorRuntime.Store,
		advisorRuntime.Profiles,
		newBackgroundJobFailureReporter(deps.eventRelay),
	)
	if conversationRuntime.ProfilePromotion != nil {
		deps.memoryPromotionCancel = startMemoryPromotionWorker(
			conversationRuntime.ProfilePromotion,
			llmBusyTracker,
			time.Duration(cfg.Conversation.ProfilePromotionIdleGraceSeconds)*time.Second,
			time.Duration(cfg.Conversation.ProfilePromotionTimeoutSeconds)*time.Second,
			newBackgroundJobFailureReporter(deps.eventRelay),
		)
	}
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
		for _, manifest := range runtimeSkillManifests {
			if err := skillStore.SaveSkillManifest(context.Background(), manifest); err != nil {
				log.Printf("WARN: failed to record skill manifest %s: %v", manifest.SkillID, err)
			}
		}
		deps.skillBootstrap = skillapp.NewBootstrapService(skillStore)
		if err := registerRuntimeDataRecallSkillGovernance(dataRecallRegistry, skillStore); err != nil {
			log.Fatalf("Failed to register Skill Governance data recall: %v", err)
		}
		skillOwnerStore, ok := skillStore.(runtimeSkillContributionGateStore)
		if !ok {
			log.Fatalf("Failed to initialize Skill Governance data write owner store")
		}
		if err := registerRuntimeDataWriteSkillGovernance(dataWriteRegistry, skillOwnerStore); err != nil {
			log.Fatalf("Failed to register Skill Governance data write: %v", err)
		}
		if err := registerRuntimeDataRecallSkillContributionGates(dataRecallRegistry, skillOwnerStore); err != nil {
			log.Fatalf("Failed to register Skill Governance contribution gate data recall: %v", err)
		}
		deps.coderProposalEvidence = skillapp.NewCoderEvidenceService("").WithTranscriptStore(skillStore)
		deps.skillGovernanceRecent = viewer.HandleSkillGovernanceRecent(skillStore)
		deps.skillGovernanceBoot = viewer.HandleSkillGovernanceBootstrap(skillStore)
		deps.skillContributionGate = viewer.HandleSkillGovernanceContributionGate(skillStore)
		deps.skillChangeGate = viewer.HandleSkillGovernanceSkillChange(skillStore)
		deps.skillChangeEval = viewer.HandleSkillGovernanceSkillChangeEval(skillStore)
		deps.skillExternalPRSubmit = viewer.HandleSkillGovernanceExternalPRSubmit(skillStore)
	}
	if cfg.DCI.IsEnabled() {
		dciStore, err := dcipersistence.NewSQLiteStore(cfg.DCI.SQLitePath)
		if err != nil {
			log.Fatalf("Failed to initialize DCI SQLite store: %v", err)
		}
		deps.dciTraceStore = dciStore
		if err := registerRuntimeDataRecallDCI(dataRecallRegistry, dciStore); err != nil {
			log.Fatalf("Failed to register DCI data recall: %v", err)
		}
		if canonicalEventStore != nil && conversationRuntime.L1Store != nil && conversationRuntime.ArchiveStore != nil {
			identityEvidenceVerifier := dcipersistence.NewIdentityEvidenceVerifier(dciStore, canonicalEventStore, conversationRuntime.L1Store, conversationRuntime.ArchiveStore)
			if err := registerRuntimeDataRecallDCIIdentityEvidence(dataRecallRegistry, identityEvidenceVerifier); err != nil {
				log.Fatalf("Failed to register DCI identity evidence data recall: %v", err)
			}
		} else {
			log.Printf("WARN: DCI identity evidence data recall unavailable: owner dependencies missing")
		}
		deps.dciRecent = viewer.HandleDCIRecent(dciStore)
		dciOptions := []dciapp.Option{
			dciapp.WithToolRunner(toolRuntime.WorkerRuntimeRunnerV2),
			dciapp.WithSkillBootstrap(deps.skillBootstrap),
			dciapp.WithEventAppender(canonicalEventStore),
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
			ActorKind:         "agent",
			ActorID:           "shiro",
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
		if err := registerRuntimeDataWriteDCI(dataWriteRegistry, dciStore, dciExplorer); err != nil {
			log.Fatalf("Failed to register DCI data write: %v", err)
		}
		if err := registerRuntimeDataRecallDCISearchResult(dataRecallRegistry, dciStore); err != nil {
			log.Fatalf("Failed to register DCI exact search result data recall: %v", err)
		}
		deps.dciSearcher = dciExplorer
		var dciOwnerToken []byte
		if cfg.LocalAgentOps.Enabled {
			if token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile); err != nil {
				log.Printf("DCI owner API unavailable: %v", err)
			} else {
				dciOwnerToken = token
			}
		}
		deps.dciSearch = viewer.NewDCISearchHandler(dciExplorer, cfg.LocalAgentOps.UserID, dciOwnerToken)
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
		if err := registerRuntimeDataRecallSandbox(dataRecallRegistry, sandboxStore); err != nil {
			log.Fatalf("Failed to register Sandbox data recall: %v", err)
		}
		sandboxOwnerStore, ok := sandboxStore.(runtimeSandboxPromotionGateStore)
		if !ok {
			log.Fatalf("Failed to initialize Sandbox data write owner store")
		}
		if err := registerRuntimeDataWriteSandbox(dataWriteRegistry, sandboxOwnerStore); err != nil {
			log.Fatalf("Failed to register Sandbox data write: %v", err)
		}
		if err := registerRuntimeDataRecallSandboxPromotionGates(dataRecallRegistry, sandboxOwnerStore); err != nil {
			log.Fatalf("Failed to register Sandbox promotion gate data recall: %v", err)
		}
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
		if err := registerRuntimeDataRecallWorkstream(dataRecallRegistry, workstreamStore); err != nil {
			log.Fatalf("Failed to register Workstream data recall: %v", err)
		}
		ownerStore, ok := workstreamStore.(runtimeWorkstreamGoalStore)
		if !ok {
			log.Fatalf("Failed to initialize Workstream data write owner store")
		}
		if err := registerRuntimeDataWriteWorkstream(dataWriteRegistry, ownerStore); err != nil {
			log.Fatalf("Failed to register Workstream data write: %v", err)
		}
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
		}
	}
	// Atlas shares the established Backlog JSONL and Workstream store. No
	// lifecycle state is initialized when either owner store is unavailable;
	// reads still expose an empty/legacy-safe projection and writes fail closed.
	deps.atlasService = backlogapp.NewService(deps.backlogStore, deps.workstreamStore)
	if llmRuntime.Worker != nil {
		deps.atlasService.WithRevalidationEvaluator(backlogapp.NewLLMRevalidationEvaluator(llmRuntime.Worker, "shiro"))
		log.Printf("Atlas maturation revalidation evaluator enabled via RenCrow_LLM Worker")
	} else {
		log.Printf("WARN: Atlas maturation revalidation evaluator unavailable: Worker route is not configured")
	}
	atlasBackfillReady := false
	if catalog, err := backlogfeature.LoadAtlasCatalog(); err != nil {
		log.Printf("Atlas catalog unavailable: %v", err)
	} else {
		deps.atlasService.WithCatalog([]map[string]any{{
			"schema_version": catalog.SchemaVersion,
			"bootstrap_at":   catalog.BootstrapAt,
			"source":         catalog.Source,
		}}).WithFeatures(catalog.Features).WithModules(catalog.Modules)
	}
	if pkg, err := backlogfeature.LoadBackfillPackage(); err != nil {
		log.Printf("Atlas backfill unavailable: %v", err)
	} else {
		deps.atlasService.WithFeatures(pkg.FeatureMaps())
		if report, reconcileErr := deps.atlasService.ReconcileBackfill(context.Background(), pkg); reconcileErr != nil {
			log.Printf("Atlas backfill reconcile failed: %v", reconcileErr)
		} else {
			log.Printf("Atlas backfill reconciled: imported=%d updated=%d skipped=%d", report.Imported, report.Updated, report.Skipped)
			atlasBackfillReady = true
		}
	}
	if !atlasBackfillReady {
		// Do not expose a projection backed by an alternate or partial Atlas
		// source when the canonical embedded package cannot be reconciled.
		deps.atlasService = nil
	} else {
		atlasService, migrated, startupErr := prepareAtlasLifecycleService(context.Background(), deps.atlasService)
		if startupErr != nil {
			// A failed lifecycle migration or lease recovery must not leave a
			// pre-revision-2 completion visible as current Atlas state.
			log.Printf("Atlas lifecycle startup migration/recovery failed: %v", startupErr)
			deps.atlasService = nil
		} else {
			deps.atlasService = atlasService
			if migrated {
				log.Printf("Atlas lifecycle legacy state migrated or repaired to revision 2")
			}
		}
	}
	if deps.atlasService != nil {
		if verifier, verifierErr := newAtlasEvidenceVerifier(cfg, deps.reportStore); verifierErr != nil {
			// Keep the owner service present for read-only projection, but leave
			// its verifier nil so every evidence-gated owner write fails closed.
			log.Printf("WARN: Atlas evidence verifier unavailable: %v", verifierErr)
		} else {
			deps.atlasService.WithEvidenceVerifier(verifier)
			log.Printf("Atlas evidence verifier enabled (embedded specs, execution reports, deployment receipts)")
		}
	}
	var atlasToken []byte
	if cfg.LocalAgentOps.Enabled {
		if token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile); err != nil {
			log.Printf("Atlas owner API unavailable: %v", err)
		} else {
			atlasToken = token
		}
	}
	deps.atlasHandler = viewer.NewAtlasHandler(deps.atlasService, cfg.LocalAgentOps.UserID, atlasToken)
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
		if err := registerRuntimeDataRecallRevenue(dataRecallRegistry, revenueStore); err != nil {
			log.Fatalf("Failed to register Revenue data recall: %v", err)
		}
		ownerStore, ok := revenueStore.(runtimeRevenueOpportunityStore)
		if !ok {
			log.Fatalf("Failed to initialize Revenue data write owner store")
		}
		if err := registerRuntimeDataWriteRevenue(dataWriteRegistry, ownerStore); err != nil {
			log.Fatalf("Failed to register Revenue data write: %v", err)
		}
		deps.revenueStatus = viewer.HandleRevenueStatus(revenueStore, viewer.RevenueEconomicObjectiveSettings{
			Enabled: cfg.EconomicObjective.Enabled, DraftOnly: cfg.EconomicObjective.DraftOnlyEnabled(),
		})
		deps.revenueMarket = viewer.HandleRevenueMarketResearchCreate(revenueStore)
		deps.revenueSNSPost = viewer.HandleRevenueSNSPostMetricCreate(revenueStore)
		deps.revenueProduct = viewer.HandleRevenueProductCreate(revenueStore)
		deps.revenueCustomerVoice = viewer.HandleRevenueCustomerVoiceCreate(revenueStore)
		deps.revenueEvent = viewer.HandleRevenueEventCreate(revenueStore)
		deps.revenuePolicyDecision = viewer.HandleRevenuePolicyDecision(revenueStore)
		deps.revenueDailyRoutine = viewer.HandleRevenueDailyRoutineReportCreate(revenueStore)
		deps.revenueChannelDraft = viewer.HandleRevenueChannelDraftCreate(revenueStore)
		deps.revenueExternalSendApply = viewer.HandleRevenueExternalSendApply(revenueStore)
		deps.revenueOpportunities = viewer.HandleRevenueOpportunities(revenueStore)
		deps.revenueEconomicTasks = viewer.HandleRevenueEconomicTasks(revenueStore)
		deps.revenueDeliveries = viewer.HandleRevenueDeliveries(revenueStore)
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
		if err := registerRuntimeDataRecallPersonaArchitecture(dataRecallRegistry, personaStore); err != nil {
			log.Fatalf("Failed to register Persona Architecture data recall: %v", err)
		}
		personaOwnerStore, ok := personaStore.(runtimePersonaObservationStore)
		if !ok {
			log.Fatalf("Failed to initialize Persona Architecture data write owner store")
		}
		if err := registerRuntimeDataRecallPersonaArchitectureObservations(dataRecallRegistry, personaOwnerStore); err != nil {
			log.Fatalf("Failed to register Persona Architecture observation data recall: %v", err)
		}
		if err := registerRuntimeDataWritePersonaArchitecture(dataWriteRegistry, personaOwnerStore); err != nil {
			log.Fatalf("Failed to register Persona Architecture data write: %v", err)
		}
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
		if err := registerRuntimeDataRecallBrowserTraceToAPI(dataRecallRegistry, browserTraceStore); err != nil {
			log.Fatalf("Failed to register Browser Trace data recall: %v", err)
		}
		browserTraceOwnerStore, ok := browserTraceStore.(runtimeBrowserTraceValidationStore)
		if !ok {
			log.Fatalf("Failed to initialize Browser Trace data write owner store")
		}
		if err := registerRuntimeDataRecallBrowserTraceValidationReviews(dataRecallRegistry, browserTraceOwnerStore); err != nil {
			log.Fatalf("Failed to register Browser Trace validation review data recall: %v", err)
		}
		if err := registerRuntimeDataWriteBrowserTraceToAPI(dataWriteRegistry, browserTraceOwnerStore); err != nil {
			log.Fatalf("Failed to register Browser Trace data write: %v", err)
		}
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
		if err := registerRuntimeDataRecallComplexityHotspot(dataRecallRegistry, complexityStore); err != nil {
			log.Fatalf("Failed to register Complexity Hotspot data recall: %v", err)
		}
		complexityOwnerStore, ok := complexityStore.(runtimeComplexityHotspotReviewStore)
		if !ok {
			log.Fatalf("Failed to initialize Complexity Hotspot data write owner store")
		}
		if err := registerRuntimeDataWriteComplexityHotspot(dataWriteRegistry, complexityOwnerStore); err != nil {
			log.Fatalf("Failed to register Complexity Hotspot data write: %v", err)
		}
		if err := registerRuntimeDataRecallComplexityReviews(dataRecallRegistry, complexityOwnerStore); err != nil {
			log.Fatalf("Failed to register Complexity Hotspot review data recall: %v", err)
		}
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
		var superAgentStateStore viewer.SuperAgentStateStore
		if cfg.SuperAgentHarness.Storage == "sqlite" {
			store, err := superagentpersistence.NewSQLiteStore(cfg.SuperAgentHarness.SQLitePath, cfg.SuperAgentHarness.MaxContextPackTokens)
			if err != nil {
				log.Fatalf("Failed to initialize SuperAgent Harness SQLite store: %v", err)
			}
			superAgentStateStore = store
		} else {
			superAgentStateStore = superagentpersistence.NewJSONLStore(cfg.SuperAgentHarness.LogPath, cfg.SuperAgentHarness.MaxContextPackTokens)
		}
		superAgentStore := composeRuntimeSuperAgentStore(superAgentStateStore, canonicalEventStore)
		deps.superAgentStore = superAgentStore
		if err := registerRuntimeDataRecallSuperAgentHarness(dataRecallRegistry, superAgentStore); err != nil {
			log.Fatalf("Failed to register SuperAgent Harness data recall: %v", err)
		}
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
	}
	if aiWorkflowStore != nil {
		deps.aiWorkflowStore = aiWorkflowStore
		if err := registerRuntimeDataRecallAIWorkflow(dataRecallRegistry, aiWorkflowStore); err != nil {
			log.Fatalf("Failed to register AI Workflow data recall: %v", err)
		}
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
			AllowedActors:   cfg.AIWorkflow.ExternalControlAllowedActors,
			AllowedChannels: cfg.AIWorkflow.ExternalControlAllowedChannels,
			AllowedActions:  cfg.AIWorkflow.ExternalControlAllowedActions,
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
			path := strings.TrimSpace(cfg.Storage.Databases.KnowledgeMemory)
			if path == "" {
				path = strings.TrimSpace(cfg.KnowledgeMemory.SQLitePath)
			}
			// Viewer writes use a separate existing-file read/write handle. It
			// never creates or migrates a missing/partial database; Agent Tool
			// queries use the read-only handle owned by toolRuntime.
			store, err := knowledgememorypersistence.OpenSQLiteStoreWritable(path)
			if err != nil {
				log.Printf("Knowledge Memory SQLite Viewer unavailable: %v", err)
			} else if err := store.EnsureOwnerRouteSchema(context.Background()); err != nil {
				_ = store.Close()
				log.Printf("Knowledge Memory Owner route schema unavailable: %v", err)
			} else {
				knowledgeMemoryStore = store
				deps.knowledgeMemoryViewerStore = store
				if searcher, ok := toolRuntime.KnowledgeMemoryToolStore.(runtimeKnowledgeMemoryIndexedSearcher); ok {
					if err := registerRuntimeDataWriteKnowledgeMemory(dataWriteRegistry, store); err != nil {
						log.Fatalf("Failed to register Knowledge Memory data write: %v", err)
					}
					if err := registerRuntimeDataRecallKnowledgeMemory(dataRecallRegistry, store, searcher); err != nil {
						log.Fatalf("Failed to register Knowledge Memory data recall: %v", err)
					}
				} else {
					if err := registerRuntimeDataRecallKnowledgeMemoryCandidate(dataRecallRegistry, store); err != nil {
						log.Fatalf("Failed to register Knowledge Memory candidate recall: %v", err)
					}
					if err := registerRuntimeDataRecallKnowledgeMemoryRequests(dataRecallRegistry, store); err != nil {
						log.Fatalf("Failed to register Knowledge Memory request recall: %v", err)
					}
				}
			}
		} else {
			knowledgeMemoryStore = knowledgememorypersistence.NewJSONLStore(cfg.KnowledgeMemory.LogPath)
		}
		if knowledgeMemoryStore != nil {
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
	}
	runtimeCapabilityContext = combineRuntimeCapabilityContexts(
		runtimeCapabilityContext,
		renderRuntimeDataRouteContext(dataRecallRegistry, dataWriteRegistry),
	)
	applyRuntimeAgentCapabilityContext(
		cfg,
		agents,
		runtimeCapabilityContext,
		llmRuntime.Coder1,
		llmRuntime.Coder2,
		llmRuntime.Coder3,
		llmRuntime.Coder4,
	)
	deps.recallTraceStore = conversationRuntime.L1Store
	verificationRuntime := buildVerificationRuntime(cfg, deps, conversationRuntime.L1Store)

	ttsRuntime := buildTTSEntryRuntime(cfg)
	vtuberBridge := buildVTuberBridge(cfg)
	lipSync := newTTSVTuberLipSync(vtuberBridge)
	ttsBridge := buildTTSClientBridge(
		cfg,
		func(ev orchestrator.OrchestratorEvent) {
			if deps.eventRelay != nil {
				if err := deps.eventRelay.OnEvent(ev); err != nil {
					log.Printf("[TTS] event publication failed type=%s session_id=%s: %v", ev.Type, ev.SessionID, err)
				}
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
			if publishErr := deps.eventRelay.OnEvent(orchestrator.NewEvent(
				"registry.error", "system", "subagent", err.Error(),
				"", "", "", "system", "system",
			)); publishErr != nil {
				log.Printf("[ToolRegistry] event publication failed: %v", publishErr)
			}
		})
	}

	bridges := buildViewerBridgeHandlers(cfg, deps, reportPath, ttsRuntime, sessionRuntime.SessionRepo)
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
	var idleChatWorkerNotifier agentOpsWorkerBusyNotifier
	if deps.idleChatOrch != nil {
		idleChatWorkerNotifier = deps.idleChatOrch
	}
	agentOpsHandler, err := newConfiguredAgentOpsHandler(cfg, agents.Shiro, idleChatWorkerNotifier)
	if err != nil {
		log.Fatalf("Failed to initialize local Agent OPS ingress: %v", err)
	}
	deps.agentOps = agentOpsHandler
	var persistentNewsReader domainnews.DailyNewsBriefReader
	if conversationRuntime.L1Store != nil {
		persistentNewsReader = newsbriefapp.NewL1Reader(conversationRuntime.L1Store)
	}
	deps.dailyNewsBriefReader = newsbriefapp.NewFallbackReader(deps.idleChatOrch, persistentNewsReader)
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
	buildHeartbeatRuntime(cfg, deps, agents.Shiro, sessionRuntime.MemoryStore, conversationRuntime.L1Store)
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

func buildAIWorkflowStateStore(cfg *config.Config) viewer.AIWorkflowStateStore {
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
	home, err := userhome.Dir()
	if err != nil {
		log.Printf("WARN: session log sources disabled: %v", err)
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
