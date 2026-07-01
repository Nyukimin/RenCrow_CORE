package main

import (
	"strings"

	discordadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/discord"
	slackadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/slack"
	telegramadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/telegram"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/line"
	attachmentapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
)

func buildChannelRuntimeHandlers(cfg *config.Config, deps *Dependencies, proc messageProcessor) {
	lineHandler := line.NewHandler(proc, cfg.Line.ChannelSecret, cfg.Line.AccessToken)
	lineHandler.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
	applyLineChannelPolicy(lineHandler, cfg.Line)
	deps.lineHandler = lineHandler
	if strings.TrimSpace(cfg.Telegram.BotToken) != "" {
		tg := telegramadapter.NewAdapter(cfg.Telegram.BotToken, proc)
		tg.SetWebhookSecret(cfg.Telegram.WebhookSecret)
		tg.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		deps.telegramHandler = tg
	}
	if strings.TrimSpace(cfg.Discord.BotToken) != "" {
		dc := discordadapter.NewAdapter(cfg.Discord.BotToken, proc)
		dc.SetPublicKeyHex(cfg.Discord.PublicKey)
		dc.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		deps.discordHandler = dc
	}
	if strings.TrimSpace(cfg.Slack.BotToken) != "" {
		sl := slackadapter.NewAdapter(cfg.Slack.BotToken, cfg.Slack.SigningSecret, proc)
		sl.SetAttachmentSaver(attachmentapp.NewStore(cfg.WorkspaceDir))
		deps.slackHandler = sl
	}
}

func applyLineChannelPolicy(lineHandler *line.Handler, cfg config.LineConfig) {
	policy, ok := config.ResolveChannelPolicyConfig(cfg.ChannelPolicy)
	if !ok {
		return
	}
	lineHandler.SetChannelPolicy(policy)
}
