package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"path/filepath"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	characterruntimeapp "github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	jobpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/job"
)

func buildViewerRuntimeHandlers(
	cfg *config.Config,
	deps *Dependencies,
	l1Store *l1sqlite.L1SQLiteStore,
	realMgr *conversationpersistence.RealConversationManager,
	reportPath string,
	gameDecisionProvider llm.LLMProvider,
) {
	if l1Store == nil {
		deps.viewerRecallTraces = viewer.HandleRecallTraces(nil)
		deps.viewerMemoryLayers = viewer.HandleMemoryLayers(nil, nil)
		deps.viewerSourceRegistry = viewer.HandleSourceRegistry(nil)
		deps.viewerDomainGraphAssertions = viewer.HandleDomainGraphAssertions(nil)
		deps.viewerMovieDomainGraphSync = viewer.HandleMovieDomainGraphSync(viewer.MovieCatalogOptions{}, nil)
		deps.viewerHobbyDomainGraphSync = viewer.HandleHobbyDomainGraphSync(viewer.HobbyGraphOptions{}, nil)
	}
	if l1Store != nil {
		deps.viewerRecallTraces = viewer.HandleRecallTraces(l1Store)
		deps.viewerMemorySnapshot = viewer.HandleMemorySnapshot(l1Store)
		deps.viewerMemoryLayers = viewer.HandleMemoryLayers(l1Store, realMgr)
		deps.viewerMemoryEvents = viewer.HandleMemoryEvents(l1Store)
		deps.viewerMemoryState = viewer.HandleMemoryState(l1Store)
		deps.viewerMemoryPromote = viewer.HandleMemoryPromote(l1Store)
		deps.viewerMemoryUser = viewer.HandleUserMemory(l1Store)
		deps.viewerMemoryUserState = viewer.HandleUserMemoryState(l1Store)
		deps.viewerMemoryUserForget = viewer.HandleUserMemoryForget(l1Store)
		deps.viewerMemoryUserSupersede = viewer.HandleUserMemorySupersede(l1Store)
		deps.viewerMemoryRecallPack = viewer.HandleMemoryRecallPack(l1Store, realMgr, l1Store)
		deps.viewerSourceRegistry = viewer.HandleSourceRegistry(l1Store)
		deps.viewerDomainGraphAssertions = viewer.HandleDomainGraphAssertions(l1Store)
		deps.viewerMovieDomainGraphSync = viewer.HandleMovieDomainGraphSync(viewer.MovieCatalogOptions{}, l1Store)
		deps.viewerHobbyDomainGraphSync = viewer.HandleHobbyDomainGraphSync(viewer.HobbyGraphOptions{}, l1Store)
	}
	gameBridgeStorePath := defaultGameBridgeStorePath(cfg.WorkspaceDir)
	var gameBridgeStore *viewer.GameBridgeStore
	var gameDecisionGenerator viewer.GameDecisionGenerator
	gameBridgeResultMode := "candidate_ack"
	gameBridgeDecisionMode := "deterministic_stub"
	if gameBridgeStorePath != "" {
		gameBridgeStore = viewer.NewGameBridgeStore(gameBridgeStorePath)
		gameBridgeResultMode = "persisted_candidate"
		log.Printf("Viewer game bridge candidate store enabled: %s", gameBridgeStorePath)
	}
	if gameDecisionProvider != nil {
		gameDecisionGenerator = viewer.NewLLMGameDecisionGenerator(gameDecisionProvider)
		gameBridgeDecisionMode = "llm"
		log.Printf("Viewer game bridge LLM decision enabled: provider=%s", gameDecisionProvider.Name())
	}
	gameBridgeStatusOptions := viewer.GameBridgeStatusOptions{
		ConversationEngineEnabled: realMgr != nil,
		L1StoreEnabled:            l1Store != nil,
		LLMRouterEnabled:          gameDecisionGenerator != nil,
		DecisionMode:              gameBridgeDecisionMode,
		ResultMode:                gameBridgeResultMode,
	}
	deps.viewerGamesStatus = viewer.HandleGameBridgeStatus(gameBridgeStatusOptions)
	deps.viewerGamesDecision = viewer.HandleGameBridgeDecision(viewer.GameBridgeDecisionOptions{
		RecallReader: gameBridgeStore,
		Generator:    gameDecisionGenerator,
	})
	deps.viewerGamesResult = viewer.HandleGameBridgeResult(gameBridgeStore)
	deps.viewerGamesSessions = viewer.HandleGameBridgeSessions(gameBridgeStore, gameBridgeStatusOptions)
	deps.viewerGamesEvents = viewer.HandleGameBridgeEvents(gameBridgeStore)
	gameObserverProxyOptions := viewer.GameObserverProxyOptions{}
	deps.viewerGamesObserverPage = viewer.HandleGameObserverPage(gameObserverProxyOptions)
	deps.viewerGamesObserverProxy = viewer.HandleGameObserverProxy(gameObserverProxyOptions)
	deps.viewerGamesLaunch = viewer.HandleGameLaunch(viewer.GameLaunchOptions{
		ObserverBaseURL: gameObserverProxyOptions.ObserverBaseURL,
		Store:           gameBridgeStore,
	})

	hub := viewer.NewEventHub(200)
	deps.eventHub = hub
	deps.characterRuntime = viewer.HandleCharacterRuntime(characterruntimeapp.NewService(), hub)
	setIdleChatViewerClientCount(hub.ClientCount)
	hub.SetClientCountListener(handleIdleChatViewerClientCountChanged)
	if cfg.ViewerLog.Enabled {
		eventLogPath := cfg.ViewerLog.Path
		if eventLogStore, err := viewer.NewEventLogStore(eventLogPath); err != nil {
			log.Printf("WARN: viewer event log disabled: %v", err)
		} else {
			deps.eventLogStore = eventLogStore
			log.Printf("Viewer event log enabled: %s", eventLogPath)
			gcPath := filepath.Join(filepath.Dir(eventLogPath), "orchestrator_event_gc.jsonl")
			if gcSvc, err := viewer.NewEventLogGCService(eventLogStore, gcPath, cfg.ViewerLog.RetentionDays, cfg.ViewerLog.GCIntervalMinutes); err != nil {
				log.Printf("WARN: viewer event log GC disabled: %v", err)
			} else {
				deps.eventLogGC = gcSvc
				deps.eventLogGC.Start()
				log.Printf("Viewer event log GC enabled: %s", gcPath)
			}
		}
	}
	if reportStore, err := executionpersistence.NewJSONLReportStore(reportPath); err != nil {
		deps.monitorStore = viewer.NewMonitorStore(nil, deps.eventLogStore)
		deps.eventRelay = &idleAwareEventListener{hub: hub, monitor: deps.monitorStore, archive: deps.eventLogStore}
		deps.viewerStatus = viewer.HandleMonitorStatus(deps.monitorStore)
		deps.viewerAgents = viewer.HandleMonitorAgents(deps.monitorStore)
		deps.viewerAgentDetail = viewer.HandleMonitorAgentDetail(deps.monitorStore)
		deps.viewerJobs = viewer.HandleMonitorJobs(deps.monitorStore)
		deps.viewerLogs = viewer.HandleMonitorLogs(deps.monitorStore)
		deps.viewerAuditSummary = viewer.HandleMonitorAuditSummary(deps.monitorStore)
		deps.viewerJobDetail = viewer.HandleMonitorJobDetail(deps.monitorStore)
		log.Printf("WARN: evidence API disabled: %v", err)
	} else {
		deps.reportStore = reportStore
		deps.monitorStore = viewer.NewMonitorStore(reportStore, deps.eventLogStore)
		deps.eventRelay = &idleAwareEventListener{hub: hub, monitor: deps.monitorStore, archive: deps.eventLogStore}
		deps.viewerStatus = viewer.HandleMonitorStatus(deps.monitorStore)
		deps.viewerAgents = viewer.HandleMonitorAgents(deps.monitorStore)
		deps.viewerAgentDetail = viewer.HandleMonitorAgentDetail(deps.monitorStore)
		deps.viewerJobs = viewer.HandleMonitorJobs(deps.monitorStore)
		deps.viewerLogs = viewer.HandleMonitorLogs(deps.monitorStore)
		deps.viewerAuditSummary = viewer.HandleMonitorAuditSummary(deps.monitorStore)
		deps.viewerJobDetail = viewer.HandleMonitorJobDetail(deps.monitorStore)
		deps.evidenceHandler = viewer.HandleEvidenceRecent(reportStore)
		deps.evidenceDetail = viewer.HandleEvidenceDetail(reportStore)
		deps.evidenceSummary = viewer.HandleEvidenceSummary(reportStore)
		log.Printf("Viewer evidence API enabled: %s", reportPath)
	}
	jobStorePath := defaultParallelJobStorePath(cfg.WorkspaceDir)
	if jobStorePath == "" {
		log.Printf("WARN: parallel job API disabled: workspace_dir is empty")
	} else if jobStore, err := jobpersistence.NewJSONLStore(jobStorePath); err != nil {
		log.Printf("WARN: parallel job API disabled: %v", err)
	} else {
		deps.parallelJobs = viewer.HandleParallelJobs(jobStore)
		deps.parallelJobDetail = viewer.HandleParallelJobDetail(jobStore)
		deps.jobNotifications = viewer.HandleJobNotifications(jobStore)
		log.Printf("Viewer parallel job API enabled: %s", jobStorePath)
	}
}

func defaultGameBridgeStorePath(workspaceDir string) string {
	if workspaceDir == "" {
		return ""
	}
	return filepath.Join(workspaceDir, "logs", "game_bridge_events.jsonl")
}
