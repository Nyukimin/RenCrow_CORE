package main

import (
	"context"
	"log"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	memorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/memory"
)

const idleChatSequenceStallThreshold = 2 * time.Minute

// channelNotificationSender sends Heartbeat notifications through the configured channel adapter.
type channelNotificationSender struct {
	registry *adapterchannels.Registry
	channel  string
	chatID   string
}

type idleChatSequenceMonitorAdapter struct {
	orch *idlechat.IdleChatOrchestrator
}

func buildHeartbeatNotificationSender(cfg *config.Config) heartbeat.NotificationSender {
	channel := strings.ToLower(strings.TrimSpace(cfg.Heartbeat.Channel))
	chatID := strings.TrimSpace(cfg.Heartbeat.ChatID)
	if channel == "" && chatID != "" {
		channel = "line"
	}
	if channel == "" && chatID == "" {
		return nil
	}
	return &channelNotificationSender{
		registry: buildOutboundChannelRegistry(cfg),
		channel:  channel,
		chatID:   chatID,
	}
}

func (s *channelNotificationSender) SendNotification(ctx context.Context, message string) error {
	if s.channel == "" {
		log.Printf("[Heartbeat] notification skipped: heartbeat.channel not set")
		return nil
	}
	if s.chatID == "" {
		log.Printf("[Heartbeat] notification skipped: heartbeat.chat_id not set (channel=%s)", s.channel)
		return nil
	}
	adapter, ok := s.registry.Get(s.channel)
	if !ok {
		log.Printf("[Heartbeat] notification skipped: channel adapter not configured (channel=%s)", s.channel)
		return nil
	}
	return adapter.Send(ctx, s.chatID, message)
}

func (m idleChatSequenceMonitorAdapter) CheckIdleChatSequence(ctx context.Context, now time.Time) heartbeat.IdleChatSequenceCheck {
	_ = ctx
	if m.orch == nil {
		return heartbeat.IdleChatSequenceCheck{Status: "disabled", CheckedAt: now.UTC()}
	}
	snapshot := m.orch.WatchdogSnapshot(now)
	report := heartbeat.IdleChatSequenceCheck{
		Status:     "inactive",
		Active:     snapshot.ChatActive,
		Stage:      snapshot.Stage,
		Detail:     snapshot.Detail,
		SessionID:  snapshot.SessionID,
		Generation: snapshot.Generation,
		AgeSeconds: snapshot.AgeSeconds,
		CheckedAt:  now.UTC(),
	}
	if snapshot.ChatActive {
		report.Status = "ok"
	}
	if recovery, ok := m.orch.RecoverIfStalled(now, idleChatSequenceStallThreshold, "heartbeat_idlechat_sequence_stall"); ok {
		resetIdleChatTTSQueue()
		report.Status = "recovered"
		report.Recovered = true
		report.Active = recovery.Before.ChatActive
		report.Stage = recovery.Before.Stage
		report.Detail = recovery.Before.Detail
		report.SessionID = recovery.Before.SessionID
		report.Generation = recovery.Before.Generation
		report.AgeSeconds = recovery.Before.AgeSeconds
		report.Action = recovery.Action + "_and_reset_tts_queue"
	}
	m.orch.RefillForecastTopicStockIfIdle("heartbeat")
	return report
}

func buildHeartbeatRuntime(
	cfg *config.Config,
	deps *Dependencies,
	shiroAgent *agent.ShiroAgent,
	memStore *memorypersistence.FileStore,
) {
	if !cfg.Heartbeat.Enabled {
		return
	}
	heartbeatSvc := heartbeat.NewHeartbeatService(
		shiroAgent,
		buildHeartbeatNotificationSender(cfg),
		cfg.WorkspaceDir,
		cfg.Heartbeat.Interval,
	)
	heartbeatSvc.WithMemoryStore(memStore)
	heartbeatSvc.WithEventListener(deps.eventRelay)
	if deps.idleChatOrch != nil {
		heartbeatSvc.WithIdleChatSequenceMonitor(idleChatSequenceMonitorAdapter{orch: deps.idleChatOrch})
	}
	if deps.workstreamStore != nil {
		heartbeatSvc.WithWorkstreamStore(deps.workstreamStore)
	}
	if deps.backlogStore != nil {
		heartbeatSvc.WithBacklogStore(deps.backlogStore)
	}
	if deps.revenueStore != nil {
		heartbeatSvc.WithRevenueDailyRoutineStore(deps.revenueStore)
		if discoveryStore, ok := deps.revenueStore.(heartbeat.EconomicObjectiveDiscoveryStore); ok {
			var goalStore heartbeat.EconomicObjectiveGoalStore
			if candidate, ok := deps.workstreamStore.(heartbeat.EconomicObjectiveGoalStore); ok {
				goalStore = candidate
			}
			heartbeatSvc.WithEconomicObjectiveDiscovery(discoveryStore, goalStore, heartbeat.EconomicObjectiveDiscoveryOptions{
				Enabled:                   cfg.EconomicObjective.Enabled,
				DraftOnly:                 cfg.EconomicObjective.DraftOnlyEnabled(),
				HeartbeatDiscoveryEnabled: cfg.EconomicObjective.HeartbeatDiscoveryEnabled,
				DailyOpportunityLimit:     cfg.EconomicObjective.DailyOpportunityLimit,
			})
		} else if cfg.EconomicObjective.HeartbeatDiscoveryEnabled {
			log.Printf("[Heartbeat] economic opportunity discovery unavailable: revenue store does not implement discovery interface")
		}
	}
	if deps.skillBootstrap != nil {
		heartbeatSvc.WithSkillBootstrap(deps.skillBootstrap)
	}
	heartbeatSvc.Start()
	deps.heartbeatSvc = heartbeatSvc
	log.Printf("HeartbeatService enabled (interval: %dm, workspace: %s)", cfg.Heartbeat.Interval, cfg.WorkspaceDir)
}
