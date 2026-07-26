package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/line"
)

func buildAssistantLineNotificationHandler(cfg *config.Config) http.HandlerFunc {
	if cfg == nil {
		return unavailableAssistantLineNotificationHandler()
	}
	registry := buildOutboundChannelRegistry(cfg)
	adapter, ok := registry.Get("line")
	if !ok {
		return unavailableAssistantLineNotificationHandler()
	}
	receipts := line.NewAssistantPushReceiptStore(
		filepath.Join(strings.TrimSpace(cfg.WorkspaceDir), "state", "assistant_line_push_receipts.jsonl"),
	)
	handler := line.NewAssistantNotificationHandler(
		adapter,
		func() (string, string, error) {
			destination, err := resolveNotificationDestination(cfg)
			if err != nil {
				return "", "", err
			}
			if destination.Channel != "line" {
				return "", "", fmt.Errorf("configured notification channel is not LINE")
			}
			return destination.ChatID, destination.TargetType, nil
		},
		receipts,
		time.Now,
	)
	return handler.ServeHTTP
}

func unavailableAssistantLineNotificationHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "LINE notification transport unavailable", http.StatusServiceUnavailable)
	}
}
