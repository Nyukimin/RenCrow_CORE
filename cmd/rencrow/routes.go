package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	aiworkflowfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/aiworkflow"
	channelsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/channels"
	gamesfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/games"
	governancefeature "github.com/Nyukimin/RenCrow_CORE/internal/features/governance"
	idlechatfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/idlechat"
	knowledgefeature "github.com/Nyukimin/RenCrow_CORE/internal/features/knowledge"
	llmfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/llm"
	memoryfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/memory"
	opsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/ops"
	reportsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/reports"
	sandboxfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/sandbox"
	sourcefeature "github.com/Nyukimin/RenCrow_CORE/internal/features/source"
	sttfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/stt"
	superagentfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/superagent"
	ttsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/tts"
	viewerfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/viewer"
	voicefeature "github.com/Nyukimin/RenCrow_CORE/internal/features/voice"
	webfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/web"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func registerChannelRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	channelsfeature.RegisterRoutes(mux, channelsfeature.Dependencies{Routes: channelsfeature.Routes{
		Webhook:            dependencies.lineHandler,
		TelegramWebhook:    dependencies.telegramHandler,
		DiscordWebhook:     dependencies.discordHandler,
		SlackWebhook:       dependencies.slackHandler,
		Entry:              dependencies.entryHandler,
		ChromeBridge:       dependencies.chromeBridge,
		ChromeBridgeStatus: dependencies.chromeBridgeStatus,
		ChromeBridgeEvents: dependencies.chromeBridgeEvents,
	}})
}

func registerViewerBaseRoutes(mux *http.ServeMux, cfg *config.Config, dependencies *Dependencies, debugSystemOpts viewer.DebugSystemOptions) {
	viewerfeature.RegisterBaseRoutes(mux, viewerfeature.Dependencies{Base: viewerfeature.BaseRoutes{
		Page:                         viewer.HandlePage,
		Asset:                        viewer.HandleAsset,
		RuntimeConfig:                viewer.HandleRuntimeConfig(debugSystemOpts),
		Logo:                         viewer.HandleLogo,
		MioLipSyncClosed:             viewer.HandleMioLipSyncClosed,
		MioLipSyncOpen:               viewer.HandleMioLipSyncOpen,
		MioPortrait:                  viewer.HandleMioPortrait,
		ShiroPortrait:                viewer.HandleShiroPortrait,
		ShiroLipSyncClosed:           viewer.HandleShiroLipSyncClosed,
		ShiroLipSyncOpen:             viewer.HandleShiroLipSyncOpen,
		CharacterState:               viewer.HandleCharacterState,
		CharacterManifest:            viewer.HandleCharacterManifest,
		LayeredCharacterState:        viewer.HandleLayeredCharacterState,
		LayeredCharacterMouth:        viewer.HandleLayeredCharacterMouth,
		LayeredCharacterManifest:     viewer.HandleLayeredCharacterManifest,
		Live2DCharacter:              viewer.HandleLive2DCharacter,
		Live2DCharacterEmbed:         viewer.HandleLive2DCharacterEmbed,
		Live2DAsset:                  viewer.HandleLive2DAsset,
		Live2DChat:                   viewer.HandleLive2DChat,
		Live2DEmotionControl:         viewer.HandleLive2DEmotionControl,
		Live2DChatAPI:                viewer.HandleLive2DChatAPIWithResponder(newLive2DOrchestratorResponder(dependencies)),
		Events:                       dependencies.eventHub.HandleSSE,
		DebugSystem:                  viewer.HandleDebugSystemSnapshot(debugSystemOpts),
		DocsSearch:                   viewer.HandleDocsSearch(),
		DocsDetail:                   viewer.HandleDocsDetail(),
		HistoryRepairJSONL:           dependencies.historyRepairJSONL,
		PackageValidation:            dependencies.packageValidation,
		CharacterRuntime:             dependencies.characterRuntime,
		ExtensionHealth:              dependencies.extensionHealth,
		OTELExport:                   dependencies.otelExport,
		ArtifactCleanup:              dependencies.artifactCleanup,
		AssetsGitStatus:              viewer.HandleAssetsGitStatus(defaultAssetsGitRepoPath()),
		MovieCatalog:                 viewer.HandleMovieCatalog(viewer.MovieCatalogOptions{}),
		MovieCatalogFetch:            viewer.HandleMovieCatalogFetch(viewer.MovieCatalogOptions{}),
		MovieCatalogPreference:       viewer.HandleMovieCatalogPreference(viewer.MovieCatalogOptions{}),
		MovieTopicCandidatesGenerate: viewer.HandleMovieTopicCandidatesGenerate(viewer.MovieCatalogOptions{}),
		HobbyGraph:                   viewer.HandleHobbyGraph(viewer.HobbyGraphOptions{}),
		HobbyGraphBootstrap:          viewer.HandleHobbyGraphBootstrap(viewer.HobbyGraphOptions{}),
		HobbyGraphInteraction:        viewer.HandleHobbyGraphInteraction(viewer.HobbyGraphOptions{}),
		HobbyGraphRelation:           viewer.HandleHobbyGraphRelation(viewer.HobbyGraphOptions{}),
		HobbyTopicCandidatesGenerate: viewer.HandleHobbyTopicCandidatesGenerate(viewer.HobbyGraphOptions{}),
		InvestmentStatus:             viewer.HandleInvestmentStatus(defaultInvestmentDBPath()),
		InvestmentNotify:             viewer.HandleInvestmentNotify(dependencies.eventHub),
	}})
}

func registerOpsRoutes(mux *http.ServeMux, cfg *config.Config, dependencies *Dependencies) {
	if dependencies.backlogStore == nil {
		dependencies.backlogStore = viewer.NewBacklogStore(filepath.Join(cfg.WorkspaceDir, "logs", "backlog.jsonl"))
	}
	opsfeature.RegisterRoutes(mux, opsfeature.Dependencies{Routes: opsfeature.Routes{
		Status:                 dependencies.viewerStatus,
		Agents:                 dependencies.viewerAgents,
		AgentDetail:            dependencies.viewerAgentDetail,
		Jobs:                   dependencies.viewerJobs,
		ParallelJobs:           dependencies.parallelJobs,
		ParallelJobDetail:      dependencies.parallelJobDetail,
		JobNotifications:       dependencies.jobNotifications,
		Logs:                   dependencies.viewerLogs,
		AuditSummary:           dependencies.viewerAuditSummary,
		JobDetail:              dependencies.viewerJobDetail,
		RepairRun:              viewer.HandleRepairRunWithRunner(dependencies.eventRelay, dependencies.repairRunner),
		Backlog:                viewer.HandleBacklog(dependencies.backlogStore),
		Scheduler:              dependencies.schedulerStatus,
		Workstreams:            dependencies.workstreamStatus,
		WorkstreamGoals:        dependencies.workstreamGoal,
		WorkstreamArtifacts:    dependencies.workstreamArtifact,
		WorkstreamAnnotations:  dependencies.workstreamAnnotation,
		WorkstreamSteering:     dependencies.workstreamSteering,
		WorkstreamHeartbeats:   dependencies.workstreamHeartbeat,
		WorkstreamVaultUpdates: dependencies.workstreamVaultUpdate,
		WorkstreamVaultReview:  dependencies.workstreamVaultReview,
		WorkstreamVaultPreview: dependencies.workstreamVaultPreview,
		Revenue:                dependencies.revenueStatus,
		RevenueMarketResearch:  dependencies.revenueMarket,
		RevenueSNSPosts:        dependencies.revenueSNSPost,
		RevenueProducts:        dependencies.revenueProduct,
		RevenueCustomerVoices:  dependencies.revenueCustomerVoice,
		RevenueEvents:          dependencies.revenueEvent,
		RevenueDecisionGate:    dependencies.revenueHumanDecisionGate,
		RevenueDecisionReview:  dependencies.revenueHumanDecisionReview,
		RevenueDailyRoutine:    dependencies.revenueDailyRoutine,
		RevenueChannelDrafts:   dependencies.revenueChannelDraft,
		RevenueExternalSend:    dependencies.revenueExternalSendApply,
		RevenueOpportunities:   dependencies.revenueOpportunities,
		RevenueEconomicTasks:   dependencies.revenueEconomicTasks,
		RevenueReflections:     dependencies.revenueEconomicReflections,
		RevenueReflectionEvent: dependencies.revenueReflectionFromEvent,
		RevenueOpportunityGoal: dependencies.revenueOpportunityGoal,
		Advisors:               dependencies.advisorStatus,
		AdvisorRuns:            dependencies.advisorRuns,
		AdvisorScores:          dependencies.advisorScores,
		AgentProfiles:          dependencies.agentProfiles,
		AgentPolicyDecisions:   dependencies.agentPolicyDecisions,
	}})
}

func registerLLMOpsRoutes(mux *http.ServeMux, cfg *config.Config, dependencies *Dependencies, debugSystemOpts *viewer.DebugSystemOptions) {
	llmOpsOpts := viewer.LLMOpsProxyOptions{
		BaseURL: cfg.LLMOps.BaseURL,
		Token:   strings.TrimSpace(os.Getenv("LLM_OPS_TOKEN")),
	}
	if debugSystemOpts != nil {
		dependencies.aiWorkflowHeavyRuntime = viewer.HandleAIWorkflowHeavyWorkerRuntimeDiagnostics(viewer.HeavyWorkerRuntimeDiagnosticsOptions{
			LocalLLMEnabled:  debugSystemOpts.LocalLLM.Enabled,
			Provider:         debugSystemOpts.LocalLLM.Provider,
			EffectiveBaseURL: debugSystemOpts.LocalLLM.HeavyBaseURL,
			EffectiveModel:   debugSystemOpts.LocalLLM.HeavyModel,
			TimeoutSec:       debugSystemOpts.LocalLLM.TimeoutSec,
			LLMOpsConfigured: debugSystemOpts.LLMOpsConfigured,
			LLMOpsEnabled:    debugSystemOpts.LLMOpsEnabled,
			LLMOpsBaseURL:    debugSystemOpts.LLMOpsBaseURL,
			LLMOps:           llmOpsOpts,
		})
	}
	if debugSystemOpts == nil || !debugSystemOpts.LLMOpsEnabled {
		return
	}
	dependencies.idleChatStartGate = viewer.NewLLMOpsIdleChatGate(llmOpsOpts)
	llmfeature.RegisterRoutes(mux, llmfeature.Dependencies{LLMOps: llmfeature.LLMOpsRoutes{
		Health:  viewer.HandleLLMOpsHealth(llmOpsOpts),
		Status:  viewer.HandleLLMOpsStatus(llmOpsOpts),
		Start:   viewer.HandleLLMOpsStart(llmOpsOpts),
		Stop:    viewer.HandleLLMOpsStop(llmOpsOpts),
		Restart: viewer.HandleLLMOpsRestart(llmOpsOpts),
	}})
	log.Printf("Viewer: MLX llm-ops proxy -> %s", strings.TrimRight(strings.TrimSpace(cfg.LLMOps.BaseURL), "/"))
}

func registerSTTAndAudioRoutes(mux *http.ServeMux, cfg *config.Config, sttRuntime sttRuntime, voiceChatRuntime voiceChatRuntime, dependencies *Dependencies) {
	sttRoutes := sttRuntimeRoutes(sttRuntime)
	sttRoutes.ClientLog = viewer.HandleSTTClientLogSave(modulestt.DefaultViewerClientLogPath)
	sttRoutes.WAV = viewer.HandleSTTInputWAVSave(modulestt.DefaultViewerLatestWAVPath, modulestt.DefaultViewerArchiveDir)
	sttRoutes.RawWAV = viewer.HandleSTTInputRawWAVSave(modulestt.DefaultViewerLatestRawWAVPath, modulestt.DefaultViewerArchiveDir)
	sttRoutes.AutoTest = viewer.HandleSTTAutoTest(modulestt.DefaultViewerAutoTestScriptPath, modulestt.DefaultViewerLatestWAVPath, modulestt.DefaultViewerAutoTestOutputPath)
	sttRoutes.AdminRestart = viewer.HandleSTTRestart(viewer.STTAdminOptions{BaseURL: sttRuntime.DebugOptions.STTBaseURL})
	dependencies.moduleSTTViewerInput = newSTTViewerInputObserver(sttRuntime)
	voicefeature.RegisterRoutes(mux, voicefeature.Dependencies{
		Routes: voicefeature.Routes{
			VoiceChat:         voiceChatRuntime.WSHandler,
			AudioRouterEvents: viewer.HandleAudioRouterSSE(dependencies.eventHub),
			ActiveControl:     handleViewerActiveClaim(dependencies.eventHub.OnEvent),
		},
		STT: sttfeature.Dependencies{Routes: sttRoutes},
		TTS: ttsfeature.Dependencies{Routes: ttsfeature.Routes{
			Audio:       handleTTSAudio(cfg.TTS.OutputDir, cfg.TTS.HTTPBaseURL),
			PlaybackAck: handleTTSPlaybackAck(),
		}},
	})
	registerModuleRoutes(mux, dependencies, sttRuntime)
}

func registerWebRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	webfeature.RegisterRoutes(mux, webfeature.Dependencies{Routes: webfeature.Routes{
		BrowserTraceAPIStatus:          dependencies.browserTraceAPIStatus,
		BrowserTraceAPIDiscover:        dependencies.browserTraceAPIDiscover,
		BrowserTraceAPIValidation:      dependencies.browserTraceAPIValidation,
		BrowserTraceAPIFetcherProposal: dependencies.browserTraceAPIFetcherProposal,
		ComplexityHotspotStatus:        dependencies.complexityHotspotStatus,
		ComplexityHotspotScan:          dependencies.complexityHotspotScan,
		ComplexityHotspotProposal:      dependencies.complexityHotspotProposal,
		ComplexityHotspotConcreteDiff:  dependencies.complexityHotspotConcreteDiff,
		ComplexityHotspotCoderDiff:     dependencies.complexityHotspotCoderDiff,
	}})
}

func registerKnowledgeMemorySourceRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	knowledgefeature.RegisterRoutes(mux, knowledgefeature.Dependencies{Routes: knowledgefeature.Routes{
		GlossaryRecent:             dependencies.glossaryRecent,
		KnowledgeMemoryStatus:      dependencies.knowledgeMemoryStatus,
		PersonalArchiveCreate:      dependencies.personalArchiveCreate,
		CreativeKnowledgeCreate:    dependencies.creativeKnowledgeCreate,
		NewsKnowledgeCreate:        dependencies.newsKnowledgeCreate,
		DailyIntakeRuleCreate:      dependencies.dailyIntakeRuleCreate,
		TemporalMemoryCreate:       dependencies.temporalMemoryCreate,
		KnowledgeMemoryReview:      dependencies.knowledgeMemoryReview,
		DreamConsolidationCreate:   dependencies.dreamConsolidationCreate,
		DreamConsolidationProposal: dependencies.dreamConsolidationProposal,
		DreamConsolidationReview:   dependencies.dreamConsolidationReview,
		KnowledgeRelations:         dependencies.knowledgeRelations,
		KnowledgeRelationSummary:   dependencies.knowledgeRelationSummary,
	}})
	memoryfeature.RegisterRoutes(mux, memoryfeature.Dependencies{Routes: memoryfeature.Routes{
		Snapshot:      dependencies.viewerMemorySnapshot,
		Layers:        dependencies.viewerMemoryLayers,
		Events:        dependencies.viewerMemoryEvents,
		State:         dependencies.viewerMemoryState,
		Promote:       dependencies.viewerMemoryPromote,
		User:          dependencies.viewerMemoryUser,
		UserState:     dependencies.viewerMemoryUserState,
		UserForget:    dependencies.viewerMemoryUserForget,
		UserSupersede: dependencies.viewerMemoryUserSupersede,
		RecallPack:    dependencies.viewerMemoryRecallPack,
		RecallTraces:  dependencies.viewerRecallTraces,
	}})
	sourcefeature.RegisterRoutes(mux, sourcefeature.Dependencies{Routes: sourcefeature.Routes{
		Registry:              dependencies.viewerSourceRegistry,
		DomainGraphAssertions: dependencies.viewerDomainGraphAssertions,
		MovieDomainGraphSync:  dependencies.viewerMovieDomainGraphSync,
		HobbyDomainGraphSync:  dependencies.viewerHobbyDomainGraphSync,
	}})
}

func registerGovernanceSecurityReportRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	reportsfeature.RegisterRoutes(mux, reportsfeature.Dependencies{Routes: reportsfeature.Routes{
		EvidenceRecent:      dependencies.evidenceHandler,
		EvidenceDetail:      dependencies.evidenceDetail,
		EvidenceSummary:     dependencies.evidenceSummary,
		VerificationRecent:  dependencies.verificationRecent,
		VerificationDetail:  dependencies.verificationDetail,
		VerificationSummary: dependencies.verificationSummary,
	}})
	governancefeature.RegisterRoutes(mux, governancefeature.Dependencies{Routes: governancefeature.Routes{
		ToolHarnessRecent:           dependencies.toolHarnessRecent,
		DCIRecent:                   dependencies.dciRecent,
		DCISearch:                   dependencies.dciSearch,
		SkillGovernanceRecent:       dependencies.skillGovernanceRecent,
		SkillGovernanceBoot:         dependencies.skillGovernanceBoot,
		SkillContributionGate:       dependencies.skillContributionGate,
		SkillChangeGate:             dependencies.skillChangeGate,
		SkillChangeEval:             dependencies.skillChangeEval,
		SkillExternalPRSubmit:       dependencies.skillExternalPRSubmit,
		PersonaObservation:          dependencies.personaObservation,
		PersonaDiscomfort:           dependencies.personaDiscomfort,
		PersonaTrigger:              dependencies.personaTrigger,
		PersonaCanonical:            dependencies.personaCanonical,
		PersonaObservationLog:       dependencies.personaObservationLog,
		PersonaObservationAggregate: dependencies.personaObservationAggregate,
		PersonaMetaUpdate:           dependencies.personaMetaUpdate,
		PersonaMetaUpdateReview:     dependencies.personaMetaUpdateReview,
		PersonaSession:              dependencies.personaSession,
	}})
	sandboxfeature.RegisterRoutes(mux, sandboxfeature.Dependencies{Routes: sandboxfeature.Routes{
		Status:                dependencies.sandboxStatus,
		Promotion:             dependencies.sandboxPromotion,
		PromotionApply:        dependencies.sandboxPromotionApply,
		PromotionRollback:     dependencies.sandboxPromotionRollback,
		PromotionPreview:      dependencies.sandboxPromotionPreview,
		PromotionManualReview: dependencies.sandboxPromotionManualReview,
		WorktreeCreate:        dependencies.sandboxWorktreeCreate,
		WorktreeClose:         dependencies.sandboxWorktreeClose,
	}})
	superagentfeature.RegisterRoutes(mux, superagentfeature.Dependencies{Routes: superagentfeature.Routes{
		Status:           dependencies.superAgentStatus,
		Run:              dependencies.superAgentRun,
		RunPause:         dependencies.superAgentRunPause,
		RunResume:        dependencies.superAgentRunResume,
		RunQueue:         dependencies.superAgentRunQueue,
		RunQueueClaim:    dependencies.superAgentRunQueueClaim,
		RunQueueComplete: dependencies.superAgentRunQueueComplete,
		SubagentTask:     dependencies.superAgentSubagentTask,
		ContextPack:      dependencies.superAgentContextPack,
		MessageChannel:   dependencies.superAgentMessageChannel,
		TraceEvent:       dependencies.superAgentTraceEvent,
	}})
	aiworkflowfeature.RegisterRoutes(mux, aiworkflowfeature.Dependencies{Routes: aiworkflowfeature.Routes{
		Status:                  dependencies.aiWorkflowStatus,
		Event:                   dependencies.aiWorkflowEvent,
		ProjectMemory:           dependencies.aiWorkflowProjectMemory,
		Worktree:                dependencies.aiWorkflowWorktree,
		Command:                 dependencies.aiWorkflowCommand,
		CommandRun:              dependencies.aiWorkflowCommandRun,
		ContextUsage:            dependencies.aiWorkflowContextUsage,
		ContextBudget:           dependencies.aiWorkflowContextBudget,
		ExternalControl:         dependencies.aiWorkflowExternalControl,
		HeavyWorker:             dependencies.aiWorkflowHeavyWorker,
		HeavyRuntimeDiagnostics: dependencies.aiWorkflowHeavyRuntime,
		ProjectInit:             dependencies.aiWorkflowProjectInit,
		WorktreeCreate:          dependencies.aiWorkflowWorktreeCreate,
		WorktreeClose:           dependencies.aiWorkflowWorktreeClose,
	}})
}

func registerViewerDynamicRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	if dependencies.viewerSend != nil {
		mux.HandleFunc("/viewer/send", dependencies.viewerSend)
	}
	gamesfeature.RegisterRoutes(mux, gamesfeature.Dependencies{Routes: gamesfeature.Routes{
		Status:        dependencies.viewerGamesStatus,
		Decision:      dependencies.viewerGamesDecision,
		Result:        dependencies.viewerGamesResult,
		Sessions:      dependencies.viewerGamesSessions,
		Events:        dependencies.viewerGamesEvents,
		ObserverPage:  dependencies.viewerGamesObserverPage,
		ObserverProxy: dependencies.viewerGamesObserverProxy,
	}})
}

func defaultInvestmentDBPath() string {
	if env := strings.TrimSpace(os.Getenv("RENCROW_DATA_DB")); env != "" {
		return env
	}
	return filepath.Join("rencrow-data", "data", "rencrow.db")
}

func registerIdleChatRoutes(mux *http.ServeMux, dependencies *Dependencies) {
	if dependencies.idleChatOrch == nil {
		return
	}
	idlechatfeature.RegisterRoutes(mux, idlechatfeature.Dependencies{Routes: idlechatfeature.Routes{
		Start:       dependencies.handleIdleChatStart(),
		Stop:        dependencies.handleIdleChatStop(),
		Interrupt:   dependencies.handleIdleChatInterrupt(),
		Status:      dependencies.handleIdleChatStatus(),
		Logs:        dependencies.handleIdleChatLogs(),
		Forecast:    dependencies.handleIdleChatForecast(),
		Story:       dependencies.handleIdleChatStory(),
		StorySimple: dependencies.handleIdleChatStorySimple(),
	}})
}

func registerHealthRoutes(mux *http.ServeMux, dependencies *Dependencies, cfg *config.Config) {
	healthHandler := dependencies.buildHealthHandler(cfg)
	mux.HandleFunc("/health", healthHandler.HandleHealth)
	mux.HandleFunc("/ready", healthHandler.HandleReady)
}
