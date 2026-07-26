package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	adapterchannels "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/line"
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

func TestResolveNotificationDestinationUsesConfiguredChatID(t *testing.T) {
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		Heartbeat: config.HeartbeatConfig{
			Channel: "line",
			ChatID:  "C0123456789abcdef0123456789abcdef",
		},
	}

	got, err := resolveNotificationDestination(cfg)
	if err != nil {
		t.Fatalf("resolveNotificationDestination failed: %v", err)
	}
	if got.Channel != "line" || got.TargetType != "group" || got.Source != "heartbeat.chat_id" {
		t.Fatalf("unexpected destination: %#v", got)
	}
}

func TestResolveNotificationDestinationFallsBackToRecordedDirectUser(t *testing.T) {
	workspaceDir := t.TempDir()
	store := line.NewDirectUserTargetStore(workspaceDir)
	userID := "U0123456789abcdef0123456789abcdef"
	if _, err := store.Record(userID); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	cfg := &config.Config{
		WorkspaceDir: workspaceDir,
		Heartbeat: config.HeartbeatConfig{
			Channel: "line",
			ChatID:  "${PICOCLAW_HEARTBEAT_CHAT_ID}",
		},
	}

	got, err := resolveNotificationDestination(cfg)
	if err != nil {
		t.Fatalf("resolveNotificationDestination failed: %v", err)
	}
	if got.ChatID != userID || got.TargetType != "user" || got.Source != "line_direct_user_store" {
		t.Fatalf("unexpected destination: %#v", got)
	}
}

func TestRunChannelsSendDryRunDoesNotSend(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	adapter := &fakeOutboundAdapter{name: "line"}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	destination := notificationDestination{
		Channel:    "line",
		ChatID:     "U0123456789abcdef0123456789abcdef",
		TargetType: "user",
		Source:     "heartbeat.chat_id",
	}
	var out bytes.Buffer
	var errOut bytes.Buffer

	code := runChannelsCommandWithDestination(
		[]string{"send", "--message", "test", "--dry-run"},
		registry,
		destination,
		nil,
		&out,
		&errOut,
		func() time.Time { return time.Unix(0, 0).UTC() },
	)

	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if adapter.text != "" {
		t.Fatalf("dry-run unexpectedly sent %q", adapter.text)
	}
	if strings.Contains(out.String()+errOut.String(), destination.ChatID) {
		t.Fatal("output exposed the full LINE target ID")
	}
}

func TestRunChannelsSendUsesExistingAdapter(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	adapter := &fakeOutboundAdapter{name: "line"}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	destination := notificationDestination{
		Channel:    "line",
		ChatID:     "U0123456789abcdef0123456789abcdef",
		TargetType: "user",
		Source:     "line_direct_user_store",
	}
	var out bytes.Buffer
	var errOut bytes.Buffer

	code := runChannelsCommandWithDestination(
		[]string{"send", "--message", "【RenCrow】\n通知テスト"},
		registry,
		destination,
		nil,
		&out,
		&errOut,
		func() time.Time { return time.Unix(0, 0).UTC() },
	)

	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if adapter.chatID != destination.ChatID || adapter.text != "【RenCrow】\n通知テスト" {
		t.Fatalf("unexpected send: chatID=%q text=%q", adapter.chatID, adapter.text)
	}
	if strings.Contains(out.String()+errOut.String(), destination.ChatID) {
		t.Fatal("output exposed the full LINE target ID")
	}
}

func TestRunChannelsSendRejectsMissingDestination(t *testing.T) {
	registry := adapterchannels.NewRegistry()
	var out bytes.Buffer
	var errOut bytes.Buffer

	code := runChannelsCommandWithDestination(
		[]string{"send", "--message", "test"},
		registry,
		notificationDestination{},
		context.Canceled,
		&out,
		&errOut,
		time.Now,
	)

	if code == 0 {
		t.Fatal("missing destination should fail")
	}
}
