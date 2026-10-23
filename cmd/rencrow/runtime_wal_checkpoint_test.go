package main

import (
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
)

func TestDependenciesShutdownClosesPersonaAndAIWorkflowSQLiteStores(t *testing.T) {
	root := t.TempDir()
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(root, "event_store.db"))
	if err != nil {
		t.Fatal(err)
	}
	personaStore, err := persona.NewSQLiteStore(filepath.Join(root, "persona.db"))
	if err != nil {
		_ = eventStore.Close()
		t.Fatal(err)
	}
	aiWorkflowStore, err := aiworkflow.NewSQLiteStore(filepath.Join(root, "ai_workflow.db"))
	if err != nil {
		_ = personaStore.Close()
		_ = eventStore.Close()
		t.Fatal(err)
	}

	deps := &Dependencies{
		canonicalEventStore:  eventStore,
		personaRuntimeStore:  personaStore,
		aiWorkflowStateStore: aiWorkflowStore,
	}
	deps.Shutdown()
	for name, status := range map[string]func() bool{
		"event_store": func() bool {
			checkpoint := eventStore.CheckpointStatus()
			return checkpoint.CheckpointAttempted && !checkpoint.LastSuccessAt.IsZero() && checkpoint.LastErrorClass == "none"
		},
		"persona": func() bool {
			checkpoint := personaStore.CheckpointStatus()
			return checkpoint.CheckpointAttempted && !checkpoint.LastSuccessAt.IsZero() && checkpoint.LastErrorClass == "none"
		},
		"ai_workflow": func() bool {
			checkpoint := aiWorkflowStore.CheckpointStatus()
			return checkpoint.CheckpointAttempted && !checkpoint.LastSuccessAt.IsZero() && checkpoint.LastErrorClass == "none"
		},
	} {
		if !status() {
			t.Errorf("%s store did not complete its final checkpoint during shutdown", name)
		}
	}

	// Repeated shutdown remains safe after the store owners have closed.
	deps.Shutdown()
}
