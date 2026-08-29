package main

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeAIWorkflowStore struct {
	viewer.AIWorkflowStateStore
	modulecore.EventStore
}

type runtimeSuperAgentStore struct {
	viewer.SuperAgentStateStore
	modulecore.EventStore
}

func openRuntimeCanonicalEventStore(path string) (*eventpersistence.SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("storage.databases.event_store is required")
	}
	return eventpersistence.NewSQLiteStore(path)
}

func composeRuntimeAIWorkflowStore(state viewer.AIWorkflowStateStore, events modulecore.EventStore) viewer.AIWorkflowStore {
	if state == nil || events == nil {
		return nil
	}
	return &runtimeAIWorkflowStore{AIWorkflowStateStore: state, EventStore: events}
}

func composeRuntimeSuperAgentStore(state viewer.SuperAgentStateStore, events modulecore.EventStore) viewer.SuperAgentStore {
	if state == nil || events == nil {
		return nil
	}
	return &runtimeSuperAgentStore{SuperAgentStateStore: state, EventStore: events}
}
