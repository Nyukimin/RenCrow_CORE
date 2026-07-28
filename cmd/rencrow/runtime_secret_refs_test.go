package main

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildSecretRefsFromConfigUsesReferencesWithoutChannelCredentials(t *testing.T) {
	t.Setenv("LLM_OPS_TOKEN", "ops-secret")
	t.Setenv("RENCROW_LLM_API_KEY", "gateway-secret")
	cfg := &config.Config{
		LLMGateway:         config.LLMGatewayConfig{APIKeyEnv: "RENCROW_LLM_API_KEY"},
		LocalLLM:           config.LocalLLMConfig{APIKey: "local-secret"},
		WebwrightFetch:     config.WebwrightFetchConfig{APIKey: "webwright-secret"},
		Claude:             config.ClaudeConfig{APIKey: "claude-secret"},
		DeepSeek:           config.DeepSeekConfig{APIKey: ""},
		OpenAI:             config.OpenAIConfig{APIKey: "openai-secret"},
		GoogleSearchChat:   config.GoogleSearchConfig{APIKey: "google-chat-secret"},
		GoogleSearchWorker: config.GoogleSearchConfig{APIKey: ""},
		Coder1:             config.CoderConfig{APIKey: "coder1-secret"},
		Line:               config.LineConfig{ChannelSecret: "line-secret", AccessToken: "line-token"},
		Telegram:           config.TelegramConfig{BotToken: "telegram-token", WebhookSecret: "telegram-secret"},
		Discord:            config.DiscordConfig{BotToken: "discord-token", PublicKey: "discord-public"},
		Slack:              config.SlackConfig{BotToken: "slack-token", SigningSecret: "slack-secret"},
	}

	refs := buildSecretRefsFromConfig(cfg)
	if len(refs) == 0 {
		t.Fatal("expected secret refs")
	}
	byRef := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if strings.Contains(ref.Ref, "secret") || strings.Contains(ref.Label, "secret") && strings.Contains(ref.Label, "line") {
			t.Fatalf("secret refs should not include secret values or channel labels: %+v", ref)
		}
		byRef[ref.Ref] = ref.Configured
	}
	for _, want := range []string{
		"env:RENCROW_LLM_API_KEY",
		"config:google_search_chat.api_key",
		"env:LLM_OPS_TOKEN",
	} {
		if !byRef[want] {
			t.Fatalf("expected configured secret ref %s in %+v", want, refs)
		}
	}
	for _, excluded := range []string{
		"config:line.channel_secret",
		"config:line.access_token",
		"config:telegram.bot_token",
		"config:discord.bot_token",
		"config:slack.bot_token",
	} {
		if _, ok := byRef[excluded]; ok {
			t.Fatalf("channel credential ref should be excluded: %s", excluded)
		}
	}
	if byRef["config:deepseek.api_key"] {
		t.Fatalf("empty deepseek key should be reported as unconfigured: %+v", refs)
	}
	if byRef["config:google_search_worker.api_key"] {
		t.Fatalf("empty google worker key should be reported as unconfigured: %+v", refs)
	}
}
