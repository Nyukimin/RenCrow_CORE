package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// newConfiguredMemoryOwnerHandler reads the local_agent_ops token once during
// startup and projects the configured user into the CORE owner handler.
func newConfiguredMemoryOwnerHandler(cfg *config.Config, store *l1sqlite.L1SQLiteStore) (http.HandlerFunc, error) {
	if cfg == nil || !cfg.LocalAgentOps.Enabled {
		return nil, nil
	}
	userID := strings.TrimSpace(cfg.LocalAgentOps.UserID)
	if userID == "" {
		return nil, errors.New("local_agent_ops.user_id is empty")
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		return nil, err
	}
	return viewer.NewMemoryOwnerHandler(store, userID, token), nil
}
