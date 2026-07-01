package main

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/line"
)

func TestBuildChannelRegistry(t *testing.T) {
	cfg := &config.Config{
		Line: config.LineConfig{
			ChannelSecret: "secret",
			AccessToken:   "token",
		},
		Telegram: config.TelegramConfig{BotToken: "tg-token"},
		Discord:  config.DiscordConfig{BotToken: "dc-token"},
		Slack:    config.SlackConfig{BotToken: "sl-token", SigningSecret: "sl-secret"},
	}

	r := buildChannelRegistry(cfg)
	names := r.List()
	if len(names) != 4 {
		t.Fatalf("expected 4 channels, got %d (%v)", len(names), names)
	}
}

func TestApplyLineChannelPolicy(t *testing.T) {
	handler := line.NewHandler(nil, "secret", "token")
	allowGroups := true
	applyLineChannelPolicy(handler, config.LineConfig{
		ChannelPolicy: config.ChannelPolicyConfig{
			Enabled:        true,
			AllowGroups:    &allowGroups,
			AllowedSenders: []string{"U-allowed"},
		},
	})

	if !handler.ChannelPolicyConfigured() {
		t.Fatal("expected runtime wiring to inject channel policy")
	}
}

func TestApplyLineChannelPolicyDisabled(t *testing.T) {
	handler := line.NewHandler(nil, "secret", "token")
	applyLineChannelPolicy(handler, config.LineConfig{})

	if handler.ChannelPolicyConfigured() {
		t.Fatal("disabled channel policy should preserve current compatible behavior")
	}
}
