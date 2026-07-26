package main

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/line"
)

type notificationDestination struct {
	Channel    string
	ChatID     string
	TargetType string
	Source     string
}

func resolveNotificationDestination(cfg *config.Config) (notificationDestination, error) {
	if cfg == nil {
		return notificationDestination{}, fmt.Errorf("notification config is unavailable")
	}
	channel := strings.ToLower(strings.TrimSpace(cfg.Heartbeat.Channel))
	chatID := strings.TrimSpace(cfg.Heartbeat.ChatID)
	if channel == "" && chatID != "" && !isUnresolvedEnvironmentReference(chatID) {
		channel = "line"
	}

	if chatID == "" || isUnresolvedEnvironmentReference(chatID) {
		if channel != "" && channel != "line" {
			return notificationDestination{}, fmt.Errorf("heartbeat.chat_id is not configured for channel %s", channel)
		}
		store := line.NewDirectUserTargetStore(cfg.WorkspaceDir)
		recorded, err := store.Load()
		if err != nil {
			return notificationDestination{}, fmt.Errorf(
				"LINE notification target is not configured; set heartbeat.chat_id or send one direct LINE message for enrollment: %w",
				err,
			)
		}
		return notificationDestination{
			Channel:    "line",
			ChatID:     recorded,
			TargetType: "user",
			Source:     "line_direct_user_store",
		}, nil
	}
	if channel == "" {
		return notificationDestination{}, fmt.Errorf("heartbeat.channel is not configured")
	}

	targetType := "chat"
	if channel == "line" {
		kind, err := line.TargetKind(chatID)
		if err != nil {
			return notificationDestination{}, err
		}
		targetType = kind
	}
	return notificationDestination{
		Channel:    channel,
		ChatID:     chatID,
		TargetType: targetType,
		Source:     "heartbeat.chat_id",
	}, nil
}

func needsLineTargetEnrollment(cfg *config.Config) bool {
	if cfg == nil || !lineWebhookConfigured(cfg) {
		return false
	}
	channel := strings.ToLower(strings.TrimSpace(cfg.Heartbeat.Channel))
	if channel != "" && channel != "line" {
		return false
	}
	chatID := strings.TrimSpace(cfg.Heartbeat.ChatID)
	if chatID != "" && !isUnresolvedEnvironmentReference(chatID) {
		return false
	}
	_, err := line.NewDirectUserTargetStore(cfg.WorkspaceDir).Load()
	return err != nil
}

func isUnresolvedEnvironmentReference(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "${") && strings.Contains(value, "}")
}
