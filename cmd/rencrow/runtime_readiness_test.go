package main

import (
	"net/http"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
)

func TestBuildRuntimeDependencyReadinessRequiresCredentialPairs(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("SLACK_SIGNING_SECRET", "")
	t.Setenv("DISCORD_BOT_TOKEN", "discord-token")
	t.Setenv("DISCORD_PUBLIC_KEY", "discord-public")
	t.Setenv("TELEGRAM_BOT_TOKEN", "telegram-token")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "telegram-secret")
	t.Setenv("STT_GATEWAY_URL", "wss://127.0.0.1:8443/stt/stream")
	t.Setenv("TTS_PROVIDER_URL", "")
	t.Setenv("TTS_PROVIDER", "")
	t.Setenv("IRODORI_BASE_URL", "")
	t.Setenv("SBV2_BASE_URL", "")

	readiness := buildRuntimeDependencyReadiness(&config.Config{}, nil)
	if readiness.SlackCredentialsPresent {
		t.Fatal("slack readiness should require both bot token and signing secret")
	}
	if !readiness.DiscordCredentialsPresent {
		t.Fatal("discord readiness should be present when bot token and public key are set")
	}
	if !readiness.TelegramCredentialsPresent {
		t.Fatal("telegram readiness should be present when bot token and webhook secret are set")
	}
	if !readiness.STTGatewayEnvPresent {
		t.Fatal("stt gateway readiness should be present when STT_GATEWAY_URL is set")
	}
	if readiness.TTSProviderEnvPresent {
		t.Fatal("tts provider readiness should be false when provider envs are empty")
	}
}

func TestBuildRuntimeDependencyReadinessAcceptsAlternateAudioEnv(t *testing.T) {
	t.Setenv("RENCROW_STT_URL", "wss://127.0.0.1:8443/stt/stream")
	t.Setenv("IRODORI_BASE_URL", "http://127.0.0.1:7870")

	readiness := buildRuntimeDependencyReadiness(&config.Config{}, nil)
	if !readiness.STTGatewayEnvPresent {
		t.Fatal("stt gateway readiness should accept RENCROW_STT_URL")
	}
	if !readiness.TTSProviderEnvPresent {
		t.Fatal("tts provider readiness should accept IRODORI_BASE_URL")
	}
}

func TestBuildRuntimeDependencyReadinessReportsSourceRegistryAvailability(t *testing.T) {
	disabled := buildRuntimeDependencyReadiness(&config.Config{}, &Dependencies{
		viewerMemoryLayers:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		viewerSourceRegistry:        http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		viewerDomainGraphAssertions: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if disabled.ConversationEnabled || disabled.L1SQLiteConfigPresent || disabled.MemoryLayersAvailable || disabled.SourceRegistryAvailable || disabled.DomainGraphAvailable {
		t.Fatalf("L1-backed readiness should be false without conversation L1 config: %+v", disabled)
	}
	if !disabled.MemoryLayersStatus || !disabled.SourceRegistryStatus || !disabled.DomainGraphStatus {
		t.Fatalf("disabled L1 runtime should still expose blocked memory/source/domain graph status routes: %+v", disabled)
	}

	enabled := buildRuntimeDependencyReadiness(&config.Config{
		Conversation: config.ConversationConfig{
			Enabled:      true,
			L1SQLitePath: "/home/nyukimi/.rencrow/l1_memory.db",
		},
	}, &Dependencies{
		viewerMemoryLayers:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		viewerSourceRegistry:        http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		viewerDomainGraphAssertions: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if !enabled.ConversationEnabled || !enabled.L1SQLiteConfigPresent || !enabled.MemoryLayersAvailable || !enabled.MemoryLayersStatus || !enabled.SourceRegistryAvailable || !enabled.SourceRegistryStatus || !enabled.DomainGraphAvailable || !enabled.DomainGraphStatus {
		t.Fatalf("L1-backed readiness should be true with conversation L1 config: %+v", enabled)
	}
}

func TestBuildRuntimeDependencyReadinessReportsSandboxStatusAvailability(t *testing.T) {
	disabled := buildRuntimeDependencyReadiness(&config.Config{}, &Dependencies{
		sandboxStatus: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if disabled.SandboxEnabled || !disabled.SandboxStatusAvailable {
		t.Fatalf("sandbox disabled runtime should still expose blocked status route: %+v", disabled)
	}

	enabled := buildRuntimeDependencyReadiness(&config.Config{
		Sandbox: config.SandboxConfig{Enabled: true},
	}, &Dependencies{
		sandboxStatus: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if !enabled.SandboxEnabled || !enabled.SandboxStatusAvailable {
		t.Fatalf("sandbox enabled runtime should expose enabled status route: %+v", enabled)
	}
}

func TestBuildRuntimeDependencyReadinessReportsKnowledgeAndBrowserTraceRouteAvailability(t *testing.T) {
	disabledFlag := false
	disabled := buildRuntimeDependencyReadiness(&config.Config{
		KnowledgeMemory:   config.KnowledgeMemoryConfig{Enabled: &disabledFlag},
		BrowserTraceToAPI: config.BrowserTraceToAPIConfig{Enabled: &disabledFlag},
	}, nil)
	if disabled.KnowledgeMemoryEnabled || disabled.KnowledgeMemoryStatus || disabled.BrowserTraceAPIEnabled || disabled.BrowserTraceAPIStatus || disabled.BrowserTraceAPIFetcher {
		t.Fatalf("disabled knowledge/browser trace readiness should not claim enabled or route availability: %+v", disabled)
	}

	enabled := buildRuntimeDependencyReadiness(&config.Config{}, &Dependencies{
		knowledgeMemoryStatus:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		browserTraceAPIStatus:          http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		browserTraceAPIFetcherProposal: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if !enabled.KnowledgeMemoryEnabled || !enabled.KnowledgeMemoryStatus || !enabled.BrowserTraceAPIEnabled || !enabled.BrowserTraceAPIStatus || !enabled.BrowserTraceAPIFetcher {
		t.Fatalf("enabled knowledge/browser trace readiness should expose status route availability: %+v", enabled)
	}
}

func TestBuildRuntimeDependencyReadinessReportsChannelWebhookAndFilePayloadPipeline(t *testing.T) {
	readiness := buildRuntimeDependencyReadiness(&config.Config{}, &Dependencies{
		slackHandler:   http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		discordHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if !readiness.SlackWebhookRegistered || !readiness.SlackFilePayloadPipeline {
		t.Fatalf("slack webhook/file payload pipeline readiness should follow registered handler: %+v", readiness)
	}
	if !readiness.DiscordWebhookRegistered || !readiness.DiscordFilePayloadPipeline {
		t.Fatalf("discord webhook/file payload pipeline readiness should follow registered handler: %+v", readiness)
	}
	if readiness.TelegramWebhookRegistered || readiness.TelegramFilePayloadPipeline {
		t.Fatalf("telegram webhook/file payload pipeline readiness should be false without handler: %+v", readiness)
	}
}

func TestBuildRuntimeDependencyReadinessReportsDistributedTransportState(t *testing.T) {
	readiness := buildRuntimeDependencyReadiness(&config.Config{
		Distributed: config.DistributedConfig{
			Enabled: true,
			Transports: map[string]config.TransportConfig{
				"coder1": {Type: "local"},
				"coder3": {Type: "ssh", RemoteHost: "192.168.1.10:22", RemoteUser: "rencrow", SSHKeyPath: "/tmp/key"},
			},
		},
	}, &Dependencies{
		localTransports: map[string]*transport.LocalTransport{"coder1": transport.NewLocalTransport()},
		sshTransports:   map[string]domaintransport.Transport{"coder3": stubTransport{}},
	})
	if !readiness.DistributedEnabled || !readiness.DistributedTransportsPresent || !readiness.DistributedSSHConfigured || !readiness.DistributedSSHConnected || !readiness.DistributedLocalTransport {
		t.Fatalf("distributed readiness should expose enabled/configured/connected state: %+v", readiness)
	}
}
