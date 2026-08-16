package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	chatgptimport "github.com/Nyukimin/RenCrow_CORE/internal/application/chatgptimport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// newConfiguredMemoryOwnerHandlers reads the local_agent_ops token once during
// startup and projects the configured user into both authenticated owner APIs.
func newConfiguredMemoryOwnerHandlers(cfg *config.Config, store *l1sqlite.L1SQLiteStore) (http.HandlerFunc, http.HandlerFunc, error) {
	if cfg == nil || !cfg.LocalAgentOps.Enabled {
		return nil, nil, nil
	}
	userID := strings.TrimSpace(cfg.LocalAgentOps.UserID)
	if userID == "" || !agentOpsUserIDPattern.MatchString(userID) {
		return nil, nil, errors.New("local_agent_ops.user_id is invalid")
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		return nil, nil, err
	}

	var memoryStore viewer.MemoryOwnerStore
	var chatGPTService viewer.ChatGPTImportOwnerService
	var chatGPTStore viewer.ChatGPTImportOwnerStore
	if store != nil {
		memoryStore = store
		chatGPTService = chatgptimport.NewService(store, chatgptimport.ServiceOptions{})
		chatGPTStore = store
	}
	return viewer.NewMemoryOwnerHandler(memoryStore, userID, token),
		viewer.NewMemoryChatGPTOwnerHandler(chatGPTService, chatGPTStore, cfg.Storage.Memory.RawSourceDir, userID, token),
		nil
}

// newConfiguredMemoryOwnerHandler keeps the existing single-handler startup
// boundary while sharing the one-read credential path with the ChatGPT owner.
func newConfiguredMemoryOwnerHandler(cfg *config.Config, store *l1sqlite.L1SQLiteStore) (http.HandlerFunc, error) {
	ownerHandler, _, err := newConfiguredMemoryOwnerHandlers(cfg, store)
	if err != nil {
		return nil, err
	}
	return ownerHandler, nil
}
