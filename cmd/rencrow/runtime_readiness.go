package main

import (
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func buildRuntimeDependencyReadiness(cfg *config.Config, dependencies *Dependencies) viewer.RuntimeDependencyReadiness {
	conversationEnabled := cfg != nil && cfg.Conversation.Enabled
	l1SQLiteConfigPresent := cfg != nil && strings.TrimSpace(cfg.Conversation.L1SQLitePath) != ""
	slackWebhookRegistered := dependencies != nil && dependencies.slackHandler != nil
	discordWebhookRegistered := dependencies != nil && dependencies.discordHandler != nil
	telegramWebhookRegistered := dependencies != nil && dependencies.telegramHandler != nil
	distributedEnabled := cfg != nil && cfg.Distributed.Enabled
	distributedTransportsPresent := cfg != nil && len(cfg.Distributed.Transports) > 0
	distributedSSHConfigured := false
	if cfg != nil {
		for _, transport := range cfg.Distributed.Transports {
			if strings.TrimSpace(transport.Type) == "ssh" {
				distributedSSHConfigured = true
				break
			}
		}
	}
	distributedSSHConnected := dependencies != nil && len(dependencies.sshTransports) > 0
	distributedLocalTransport := dependencies != nil && len(dependencies.localTransports) > 0
	memoryLayersAvailable := conversationEnabled && l1SQLiteConfigPresent
	memoryLayersStatusAvailable := dependencies != nil && dependencies.viewerMemoryLayers != nil
	sourceRegistryAvailable := conversationEnabled && l1SQLiteConfigPresent
	sourceRegistryStatusAvailable := dependencies != nil && dependencies.viewerSourceRegistry != nil
	domainGraphAvailable := conversationEnabled && l1SQLiteConfigPresent
	domainGraphStatusAvailable := dependencies != nil && dependencies.viewerDomainGraphAssertions != nil
	sandboxEnabled := cfg != nil && cfg.Sandbox.Enabled
	sandboxStatusAvailable := dependencies != nil && dependencies.sandboxStatus != nil
	knowledgeMemoryEnabled := cfg != nil && cfg.KnowledgeMemory.IsEnabled()
	knowledgeMemoryStatusAvailable := dependencies != nil && dependencies.knowledgeMemoryStatus != nil
	browserTraceAPIEnabled := cfg != nil && cfg.BrowserTraceToAPI.IsEnabled()
	browserTraceAPIStatusAvailable := dependencies != nil && dependencies.browserTraceAPIStatus != nil
	browserTraceAPIFetcherAvailable := dependencies != nil && dependencies.browserTraceAPIFetcherProposal != nil
	return viewer.RuntimeDependencyReadiness{
		SlackCredentialsPresent:      envPresent("SLACK_BOT_TOKEN") && envPresent("SLACK_SIGNING_SECRET"),
		SlackWebhookRegistered:       slackWebhookRegistered,
		SlackFilePayloadPipeline:     slackWebhookRegistered,
		DiscordCredentialsPresent:    envPresent("DISCORD_BOT_TOKEN") && envPresent("DISCORD_PUBLIC_KEY"),
		DiscordWebhookRegistered:     discordWebhookRegistered,
		DiscordFilePayloadPipeline:   discordWebhookRegistered,
		TelegramCredentialsPresent:   envPresent("TELEGRAM_BOT_TOKEN") && envPresent("TELEGRAM_WEBHOOK_SECRET"),
		TelegramWebhookRegistered:    telegramWebhookRegistered,
		TelegramFilePayloadPipeline:  telegramWebhookRegistered,
		STTGatewayEnvPresent:         envPresent("STT_GATEWAY_URL") || envPresent("RENCROW_STT_URL"),
		TTSProviderEnvPresent:        envPresent("TTS_PROVIDER_URL") || envPresent("TTS_PROVIDER") || envPresent("IRODORI_BASE_URL") || envPresent("SBV2_BASE_URL"),
		DistributedEnabled:           distributedEnabled,
		DistributedTransportsPresent: distributedTransportsPresent,
		DistributedSSHConfigured:     distributedSSHConfigured,
		DistributedSSHConnected:      distributedSSHConnected,
		DistributedLocalTransport:    distributedLocalTransport,
		ConversationEnabled:          conversationEnabled,
		L1SQLiteConfigPresent:        l1SQLiteConfigPresent,
		MemoryLayersAvailable:        memoryLayersAvailable,
		MemoryLayersStatus:           memoryLayersStatusAvailable,
		SourceRegistryAvailable:      sourceRegistryAvailable,
		SourceRegistryStatus:         sourceRegistryStatusAvailable,
		DomainGraphAvailable:         domainGraphAvailable,
		DomainGraphStatus:            domainGraphStatusAvailable,
		KnowledgeMemoryEnabled:       knowledgeMemoryEnabled,
		KnowledgeMemoryStatus:        knowledgeMemoryStatusAvailable,
		BrowserTraceAPIEnabled:       browserTraceAPIEnabled,
		BrowserTraceAPIStatus:        browserTraceAPIStatusAvailable,
		BrowserTraceAPIFetcher:       browserTraceAPIFetcherAvailable,
		SandboxEnabled:               sandboxEnabled,
		SandboxStatusAvailable:       sandboxStatusAvailable,
	}
}

func envPresent(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}
