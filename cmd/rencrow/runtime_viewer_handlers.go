package main

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	characterruntimeapp "github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	avatarfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/avatar"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func buildViewerRuntimeHandlers(
	cfg *config.Config,
	deps *Dependencies,
	l1Store *l1sqlite.L1SQLiteStore,
	realMgr *conversationpersistence.RealConversationManager,
	reportPath string,
	gamePlayProvider llm.LLMProvider,
	gameDecisionProvider viewer.GameAgentDecisionProvider,
) {
	databasePaths := viewerDatabasePaths(cfg)
	movieOptions := viewer.MovieCatalogOptions{DBPath: databasePaths.MovieCatalog}
	hobbyOptions := viewer.HobbyGraphOptions{DBPath: databasePaths.HobbyGraph}
	deps.viewerMemoryOwner = nil
	deps.viewerMemoryChatGPTImportOwner = nil
	if ownerHandler, chatGPTOwnerHandler, err := newConfiguredMemoryOwnerHandlers(cfg, l1Store); err != nil {
		log.Printf("WARN: authenticated memory owner API disabled: %v", err)
	} else {
		deps.viewerMemoryOwner = ownerHandler
		deps.viewerMemoryChatGPTImportOwner = chatGPTOwnerHandler
	}
	if l1Store == nil {
		deps.viewerRecallTraces = viewer.HandleRecallTraces(nil)
		deps.viewerMemoryLayers = viewer.HandleMemoryLayers(nil, nil)
		deps.viewerMemoryProfilePromotions = viewer.HandleMemoryProfilePromotions(nil)
		deps.viewerMemoryProfileRetry = viewer.HandleMemoryProfilePromotionRetry(nil)
		deps.viewerSourceRegistry = viewer.HandleSourceRegistry(nil)
		deps.viewerDomainGraphAssertions = viewer.HandleDomainGraphAssertions(nil)
		deps.viewerMovieDomainGraphSync = viewer.HandleMovieDomainGraphSync(movieOptions, nil)
		deps.viewerHobbyDomainGraphSync = viewer.HandleHobbyDomainGraphSync(hobbyOptions, nil)
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
		deps.viewerMemoryProfilePromotions = viewer.HandleMemoryProfilePromotions(l1Store)
		deps.viewerMemoryProfileRetry = viewer.HandleMemoryProfilePromotionRetry(l1Store)
		deps.viewerSourceRegistry = viewer.HandleSourceRegistry(l1Store)
		deps.viewerDomainGraphAssertions = viewer.HandleDomainGraphAssertions(l1Store)
		deps.viewerMovieDomainGraphSync = viewer.HandleMovieDomainGraphSync(movieOptions, l1Store)
		deps.viewerHobbyDomainGraphSync = viewer.HandleHobbyDomainGraphSync(hobbyOptions, l1Store)
	}
	gameBridgeStorePath := defaultGameBridgeStorePath(cfg.WorkspaceDir)
	var gameBridgeStore *viewer.GameBridgeStore
	gameBridgeResultMode := "candidate_ack"
	if gameBridgeStorePath != "" {
		gameBridgeStore = viewer.NewGameBridgeStore(gameBridgeStorePath)
		gameBridgeResultMode = "persisted_candidate"
		log.Printf("Viewer game bridge candidate store enabled: %s", gameBridgeStorePath)
	}
	gameBridgeStatusOptions := viewer.GameBridgeStatusOptions{
		ConversationEngineEnabled: realMgr != nil,
		L1StoreEnabled:            l1Store != nil,
		ResultMode:                gameBridgeResultMode,
		DecisionMode:              "agent",
		SupportedGames:            []string{"nethack"},
	}
	deps.viewerGamesStatus = viewer.HandleGameBridgeStatus(gameBridgeStatusOptions)
	deps.viewerGamesResult = viewer.HandleGameBridgeResult(gameBridgeStore)
	deps.viewerGamesSessions = viewer.HandleGameBridgeSessions(gameBridgeStore, gameBridgeStatusOptions)
	deps.viewerGamesEvents = viewer.HandleGameBridgeEvents(gameBridgeStore)
	deps.viewerGamesDecision = viewer.HandleGameAgentDecision(gameDecisionProvider)
	gameObserverProxyOptions := viewer.GameObserverProxyOptions{ObserverBaseURL: cfg.Games.ObserverURL}
	deps.viewerGamesObserverPage = viewer.HandleGameObserverPage(gameObserverProxyOptions)
	deps.viewerGamesObserverProxy = viewer.HandleGameObserverProxy(gameObserverProxyOptions)
	deps.viewerGamesLaunch = viewer.HandleGameLaunch(viewer.GameLaunchOptions{
		ObserverBaseURL: gameObserverProxyOptions.ObserverBaseURL,
		Store:           gameBridgeStore,
	})
	// ペルソナ自発プレイ (マルチペルソナ WP6)。LLM プロバイダが無ければ
	// 起動しない（「遊びたい」は判断であり、ランダムでは代替しない）。
	if cfg.Games.AutoPlay.Enabled && gamePlayProvider != nil {
		autoplayOptions := viewer.GameAutoplayOptions{
			Provider: gamePlayProvider,
			Launch: viewer.GameLaunchOptions{
				ObserverBaseURL: gameObserverProxyOptions.ObserverBaseURL,
				Store:           gameBridgeStore,
			},
			Personas:          cfg.Games.AutoPlay.Personas,
			MaxSessionsPerDay: cfg.Games.AutoPlay.MaxSessionsPerDay,
		}
		if gameBridgeStore != nil {
			autoplayOptions.Recall = gameBridgeStore
		}
		deps.gameAutoplay = viewer.NewGameAutoplayService(autoplayOptions)
		deps.gameAutoplay.Start()
		log.Printf("Games autoplay enabled: personas=%v provider=%s", cfg.Games.AutoPlay.Personas, gamePlayProvider.Name())
	}

	hub := viewer.NewEventHub(200)
	deps.eventHub = hub
	deps.viewerPromptDebug = viewer.HandlePromptDebugLogs(viewer.PromptDebugLogOptions{})
	setIdleChatViewerClientCount(hub.ClientCount)
	hub.SetClientCountListener(handleIdleChatViewerClientCountChanged)
	taskVisibility := runtimePublicTaskVisibility(deps.sessionVisibility)
	if deps.canonicalEventStore != nil {
		eventLogStore, err := viewer.NewCanonicalEventLog(deps.canonicalEventStore)
		if err != nil {
			log.Fatalf("Failed to initialize Viewer Canonical Event projection: %v", err)
		}
		deps.eventLogStore = eventLogStore
		hub.SetReplayReader(eventLogStore)
		hub.SetSessionPublicReplayReader(eventLogStore)
	}
	if reportStore, err := executionpersistence.NewJSONLReportStore(reportPath); err != nil {
		deps.monitorStore = viewer.NewMonitorStore(nil, deps.eventLogStore)
		setRuntimeMonitorEventVisibility(deps.monitorStore, deps.sessionVisibility)
		deps.eventRelay = &idleAwareEventListener{hub: hub, monitor: deps.monitorStore, archive: deps.eventLogStore, responseMarker: realMgr}
		deps.viewerStatus = viewer.HandleMonitorStatus(deps.monitorStore)
		deps.viewerAgents = viewer.HandleMonitorAgents(deps.monitorStore)
		deps.viewerAgentDetail = viewer.HandleMonitorAgentDetail(deps.monitorStore)
		deps.viewerLogs = viewer.HandleMonitorLogs(deps.monitorStore)
		deps.viewerAuditSummary = viewer.HandleMonitorAuditSummary(deps.monitorStore)
		log.Printf("WARN: evidence API disabled: %v", err)
	} else {
		deps.reportStore = reportStore
		deps.monitorStore = viewer.NewMonitorStore(reportStore, deps.eventLogStore)
		setRuntimeMonitorEventVisibility(deps.monitorStore, deps.sessionVisibility)
		deps.eventRelay = &idleAwareEventListener{hub: hub, monitor: deps.monitorStore, archive: deps.eventLogStore, responseMarker: realMgr}
		deps.viewerStatus = viewer.HandleMonitorStatus(deps.monitorStore)
		deps.viewerAgents = viewer.HandleMonitorAgents(deps.monitorStore)
		deps.viewerAgentDetail = viewer.HandleMonitorAgentDetail(deps.monitorStore)
		deps.viewerLogs = viewer.HandleMonitorLogs(deps.monitorStore)
		deps.viewerAuditSummary = viewer.HandleMonitorAuditSummary(deps.monitorStore)
		deps.evidenceHandler = viewer.HandleEvidenceRecentWithPublicVisibility(reportStore, deps.taskStore, taskVisibility)
		deps.evidenceDetail = viewer.HandleEvidenceDetailWithPublicVisibility(reportStore, deps.taskStore, taskVisibility)
		deps.evidenceSummary = viewer.HandleEvidenceSummary(reportStore)
		log.Printf("Viewer evidence API enabled: %s", reportPath)
	}
	deps.characterRuntime = avatarfeature.HandleCharacterRuntime(characterruntimeapp.NewService(), deps.eventRelay)
	graphEvents, _ := deps.canonicalEventStore.(viewer.IdentityGraphEventReader)
	deps.identityGraph = viewer.HandleIdentityGraphWithPublicVisibility(graphEvents, nil, nil, nil)
	if deps.taskStore == nil || deps.taskManager == nil {
		log.Printf("WARN: task API disabled: canonical Task owner unavailable")
	} else {
		deps.tasks = viewer.HandleTasksWithPublicVisibility(deps.taskStore, taskVisibility)
		deps.taskDetail = viewer.HandleTaskDetailWithPublicVisibility(deps.taskStore, taskVisibility)
		deps.identityGraph = viewer.HandleIdentityGraphWithPublicVisibility(
			graphEvents,
			deps.taskStore,
			taskVisibility,
			runtimePublicSessionVisibility(deps.sessionVisibility),
		)
		deps.taskNotifications = viewer.HandleTaskNotificationsWithPublicVisibility(deps.taskStore, taskVisibility)
		log.Printf("Viewer task API and Orchestrator task lifecycle enabled")
	}
}

func runtimePublicTaskVisibility(sessions *runtimeSessionVisibility) viewer.PublicTaskVisibility {
	return func(ctx context.Context, task domaintask.Task) (bool, error) {
		provenance := task.OriginPublication
		if provenance == nil || task.OriginSessionID.Validate() != nil || sessions == nil || sessions.reader == nil {
			return false, nil
		}
		if err := provenance.Validate(task.OriginSessionID); err != nil ||
			provenance.Audience != domainconversation.SessionAudiencePublic || provenance.OwnerID != "" {
			return false, nil
		}
		current, err := sessions.reader.CurrentPublicationBinding(ctx, string(task.OriginSessionID))
		if err != nil {
			return false, err
		}
		if err := current.Validate(); err != nil {
			return false, err
		}
		address := current.ChannelAddress()
		return current.SessionID() == task.OriginSessionID &&
			current.PublicationIncarnation() == provenance.PublicationIncarnation &&
			address.ChannelType() == provenance.Channel &&
			address.ExternalConversationID() == provenance.ChannelAddress &&
			current.Audience() == provenance.Audience && current.OwnerID() == provenance.OwnerID, nil
	}
}

func runtimePublicSessionVisibility(sessions *runtimeSessionVisibility) viewer.PublicSessionVisibility {
	return func(ctx context.Context, envelope modulecore.EventEnvelope) (bool, error) {
		if envelope.SessionID.Validate() != nil || envelope.Payload == nil || sessions == nil {
			return false, nil
		}
		encoded, err := json.Marshal(envelope.Payload)
		if err != nil {
			return false, nil
		}
		var event orchestrator.OrchestratorEvent
		if err := json.Unmarshal(encoded, &event); err != nil || event.SessionID != envelope.SessionID {
			return false, nil
		}
		binding, visible, err := sessions.ResolveEvent(ctx, event, "")
		if err != nil || !visible {
			return false, err
		}
		return sessions.IsCurrent(ctx, binding)
	}
}

func setRuntimeMonitorEventVisibility(store *viewer.MonitorStore, sessions *runtimeSessionVisibility) {
	if store == nil {
		return
	}
	if sessions == nil {
		store.SetPublicEventVisibilityPredicate(nil)
		return
	}
	store.SetPublicEventVisibilityPredicate(func(ctx context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
		switch visibility := strings.ToLower(strings.TrimSpace(event.Visibility)); visibility {
		case "", "public":
		case "private":
			return false, nil
		default:
			return false, nil
		}

		if event.TaskID != "" {
			if err := event.TaskID.Validate(); err != nil {
				return false, nil
			}
		}
		if event.SessionID == "" {
			return event.Type == "agent.unavailable" && event.TaskID == "", nil
		}
		if err := event.SessionID.Validate(); err != nil {
			return false, nil
		}
		_, visible, err := sessions.ResolveEvent(ctx, event, "")
		return visible, err
	})
}

func defaultGameBridgeStorePath(workspaceDir string) string {
	if workspaceDir == "" {
		return ""
	}
	return filepath.Join(workspaceDir, "logs", "game_bridge_events.jsonl")
}
