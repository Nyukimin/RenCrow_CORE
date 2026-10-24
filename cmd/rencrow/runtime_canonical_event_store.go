package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeSuperAgentStore struct {
	viewer.SuperAgentStateStore
	modulecore.EventStore
}

type runtimeAIWorkflowStore struct {
	viewer.AIWorkflowStateStore
	modulecore.EventStore
}

func openRuntimeCanonicalEventStore(path string, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*eventpersistence.SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("storage.databases.event_store is required")
	}
	writeQueue, err := eventpersistence.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		return nil, fmt.Errorf("configure canonical Event Store write queue: %w", err)
	}
	return eventpersistence.NewSQLiteStoreWithWriteQueue(path, writeQueue)
}

func composeRuntimeAIWorkflowStore(state viewer.AIWorkflowStateStore, events modulecore.EventStore) viewer.AIWorkflowStore {
	if state == nil || events == nil {
		return nil
	}
	return &runtimeAIWorkflowStore{AIWorkflowStateStore: state, EventStore: events}
}

func (s *runtimeAIWorkflowStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	return appendRuntimeAdmittedEvent(ctx, s.EventStore, event, "ai_workflow")
}

func (s *runtimeSuperAgentStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	return appendRuntimeAdmittedEvent(ctx, s.EventStore, event, "superagent")
}

func appendRuntimeAdmittedEvent(ctx context.Context, events modulecore.EventStore, event modulecore.EventEnvelope, component string) error {
	if events == nil {
		return fmt.Errorf("canonical Event Store is unavailable")
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		if event.ComponentID != component {
			return fmt.Errorf("admitted %s event has unexpected component %q", component, event.ComponentID)
		}
		appender, ok := events.(interface {
			AdmitSequenced(context.Context, modulecore.EventEnvelope) (modulecore.EventEnvelope, uint64, error)
		})
		if !ok {
			return fmt.Errorf("canonical Event Store does not support admitted %s events", component)
		}
		_, _, err := appender.AdmitSequenced(ctx, event)
		return err
	}
	return events.Append(ctx, event)
}

func composeRuntimeSuperAgentStore(state viewer.SuperAgentStateStore, events modulecore.EventStore) viewer.SuperAgentStore {
	if state == nil || events == nil {
		return nil
	}
	return &runtimeSuperAgentStore{SuperAgentStateStore: state, EventStore: events}
}
