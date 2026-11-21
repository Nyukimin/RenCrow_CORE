package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	discordadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/discord"
	slackadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/slack"
	telegramadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels/telegram"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/line"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func buildChannelRuntimeHandlers(cfg *config.Config, deps *Dependencies, proc messageProcessor) {
	channelProcessor := proc
	if cfg != nil && cfg.Heartbeat.AgentSchedulerEnabled && deps != nil {
		channelProcessor = channelActorAdmissionProcessor{next: proc, admission: func() conversationruntime.ActorAdmission {
			return deps.agentAdmission
		}, afterActivity: func(ctx context.Context, actorID string) error {
			if deps.reconcileActorGameActivity == nil {
				return nil
			}
			return deps.reconcileActorGameActivity(ctx, actorID)
		}}
	}
	lineHandler := line.NewHandler(channelProcessor, cfg.Line.ChannelSecret, cfg.Line.AccessToken)
	lineHandler.SetAttachmentSaver(newRuntimeAttachmentStore(cfg))
	if needsLineTargetEnrollment(cfg) {
		lineHandler.SetDirectUserTargetRecorder(line.NewDirectUserTargetStore(cfg.WorkspaceDir))
	}
	applyLineChannelPolicy(lineHandler, cfg.Line)
	deps.lineHandler = lineHandler
	if strings.TrimSpace(cfg.Telegram.BotToken) != "" {
		tg := telegramadapter.NewAdapter(cfg.Telegram.BotToken, channelProcessor)
		tg.SetWebhookSecret(cfg.Telegram.WebhookSecret)
		tg.SetAttachmentSaver(newRuntimeAttachmentStore(cfg))
		deps.telegramHandler = tg
	}
	if strings.TrimSpace(cfg.Discord.BotToken) != "" {
		dc := discordadapter.NewAdapter(cfg.Discord.BotToken, channelProcessor)
		dc.SetPublicKeyHex(cfg.Discord.PublicKey)
		dc.SetAttachmentSaver(newRuntimeAttachmentStore(cfg))
		deps.discordHandler = dc
	}
	if strings.TrimSpace(cfg.Slack.BotToken) != "" {
		sl := slackadapter.NewAdapter(cfg.Slack.BotToken, cfg.Slack.SigningSecret, channelProcessor)
		sl.SetAttachmentSaver(newRuntimeAttachmentStore(cfg))
		deps.slackHandler = sl
	}
}

type channelActorAdmissionProcessor struct {
	next          messageProcessor
	admission     func() conversationruntime.ActorAdmission
	afterActivity func(context.Context, string) error
}

func (p channelActorAdmissionProcessor) ProcessMessage(ctx context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	actor := strings.ToLower(strings.TrimSpace(req.To))
	if actor == "" && isDirectChatChannel(req.Channel) {
		// The LINE, Telegram, Discord, and Slack adapters submit ordinary user
		// messages without a recipient; the legacy orchestrator routes those to Mio.
		actor = "mio"
	}
	var admission conversationruntime.ActorAdmission
	if p.admission != nil {
		admission = p.admission()
	}
	if admission == nil || !isAgentHeartbeatActor(actor) {
		response, processErr := p.next.ProcessMessage(ctx, req)
		if processErr == nil && p.afterActivity != nil && isAgentHeartbeatActor(actor) {
			if err := p.afterActivity(ctx, actor); err != nil {
				log.Printf("channel post-turn activity reconciliation failed actor=%s: %v", actor, err)
			}
		}
		return response, processErr
	}
	release, err := admission.Acquire(ctx, actor, domaintask.PriorityHigh)
	if err != nil {
		return orchestrator.ProcessMessageResponse{}, fmt.Errorf("acquire %s channel actor: %w", actor, err)
	}
	if release == nil {
		return orchestrator.ProcessMessageResponse{}, fmt.Errorf("acquire %s channel actor: admission returned no release", actor)
	}
	response, processErr := p.next.ProcessMessage(ctx, req)
	release()
	if processErr == nil && p.afterActivity != nil {
		if err := p.afterActivity(ctx, actor); err != nil {
			log.Printf("channel post-turn activity reconciliation failed actor=%s: %v", actor, err)
		}
	}
	return response, processErr
}

func isDirectChatChannel(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "line", "telegram", "discord", "slack":
		return true
	default:
		return false
	}
}

func applyLineChannelPolicy(lineHandler *line.Handler, cfg config.LineConfig) {
	policy, ok := config.ResolveChannelPolicyConfig(cfg.ChannelPolicy)
	if !ok {
		return
	}
	lineHandler.SetChannelPolicy(policy)
}
