package main

import (
	"context"
	"errors"
	"testing"

	adapterchannels "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

type fakeOutboundAdapter struct {
	name   string
	chatID string
	text   string
	err    error
}

func (a *fakeOutboundAdapter) Name() string { return a.name }

func (a *fakeOutboundAdapter) Send(ctx context.Context, chatID, text string) error {
	a.chatID = chatID
	a.text = text
	return a.err
}

func (a *fakeOutboundAdapter) Probe(ctx context.Context) error { return nil }

func TestChannelNotificationSenderSendsThroughConfiguredChannel(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	slack := &fakeOutboundAdapter{name: "slack"}
	if err := registry.Register(slack); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	sender := &channelNotificationSender{
		registry: registry,
		channel:  "slack",
		chatID:   "C123",
	}

	if err := sender.SendNotification(context.Background(), "alert"); err != nil {
		t.Fatalf("SendNotification failed: %v", err)
	}
	if slack.chatID != "C123" || slack.text != "alert" {
		t.Fatalf("unexpected send payload: chatID=%q text=%q", slack.chatID, slack.text)
	}
}

func TestChannelNotificationSenderSkipsMissingConfig(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	if err := registry.Register(&fakeOutboundAdapter{name: "telegram"}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}

	tests := []struct {
		name    string
		channel string
		chatID  string
	}{
		{name: "missing channel", channel: "", chatID: "123"},
		{name: "missing chat id", channel: "telegram", chatID: ""},
		{name: "adapter missing", channel: "discord", chatID: "123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &channelNotificationSender{
				registry: registry,
				channel:  tt.channel,
				chatID:   tt.chatID,
			}
			if err := sender.SendNotification(context.Background(), "alert"); err != nil {
				t.Fatalf("expected skip without error, got %v", err)
			}
		})
	}
}

func TestChannelNotificationSenderReturnsAdapterError(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	if err := registry.Register(&fakeOutboundAdapter{name: "discord", err: errors.New("send failed")}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	sender := &channelNotificationSender{
		registry: registry,
		channel:  "discord",
		chatID:   "987",
	}

	if err := sender.SendNotification(context.Background(), "alert"); err == nil {
		t.Fatal("expected adapter error")
	}
}

func TestBuildHeartbeatNotificationSender(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantNil bool
	}{
		{
			name: "disabled notification config",
			cfg: &config.Config{
				Heartbeat: config.HeartbeatConfig{},
			},
			wantNil: true,
		},
		{
			name: "telegram notification config",
			cfg: &config.Config{
				Telegram:  config.TelegramConfig{BotToken: "token"},
				Heartbeat: config.HeartbeatConfig{Channel: "telegram", ChatID: "123"},
			},
		},
		{
			name: "legacy chat id defaults to line",
			cfg: &config.Config{
				Line:      config.LineConfig{AccessToken: "token"},
				Heartbeat: config.HeartbeatConfig{ChatID: "U123"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildHeartbeatNotificationSender(tt.cfg)
			if tt.wantNil && got != nil {
				t.Fatalf("expected nil sender, got %#v", got)
			}
			if !tt.wantNil && got == nil {
				t.Fatal("expected sender, got nil")
			}
		})
	}
}

func TestBuildOutboundChannelRegistryIncludesLineWithAccessTokenOnly(t *testing.T) {
	cfg := &config.Config{
		Line:      config.LineConfig{AccessToken: "line-token"},
		Telegram:  config.TelegramConfig{BotToken: "tg-token"},
		Discord:   config.DiscordConfig{BotToken: "dc-token"},
		Slack:     config.SlackConfig{BotToken: "sl-token"},
		Heartbeat: config.HeartbeatConfig{Channel: "line", ChatID: "U123"},
	}

	registry := buildOutboundChannelRegistry(cfg)
	for _, name := range []string{"line", "telegram", "discord", "slack"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("expected outbound channel %s to be registered", name)
		}
	}

}
