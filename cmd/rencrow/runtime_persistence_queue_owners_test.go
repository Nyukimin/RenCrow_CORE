package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	personapersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
)

func TestRuntimeBindsPersonaAndAIWorkflowOwnersToSharedQueue(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	}()

	otherQueue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := otherQueue.Shutdown(ctx); err != nil {
			t.Errorf("other queue shutdown: %v", err)
		}
	}()

	personaSQLite, err := personapersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "persona.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer personaSQLite.Close()
	personaJSONL := personapersistence.NewJSONLStore(t.TempDir())
	aiWorkflowSQLite, err := aiworkflowpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "ai-workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer aiWorkflowSQLite.Close()
	aiWorkflowJSONL := aiworkflowpersistence.NewJSONLStore(t.TempDir())

	for name, owner := range map[string]any{
		"Persona SQLite":     personaSQLite,
		"Persona JSONL":      personaJSONL,
		"AI Workflow SQLite": aiWorkflowSQLite,
		"AI Workflow JSONL":  aiWorkflowJSONL,
	} {
		t.Run(name, func(t *testing.T) {
			if err := bindRuntimeOwnerPersistenceQueue(owner, queue); err != nil {
				t.Fatalf("bindRuntimeOwnerPersistenceQueue(): %v", err)
			}
			if !runtimeConfiguredOwnerMatchesQueue(true, owner, queue) {
				t.Fatal("owner did not confirm shared queue identity")
			}
			if runtimeConfiguredOwnerMatchesQueue(true, owner, otherQueue) {
				t.Fatal("owner matched a different queue")
			}
		})
	}

	var _ interface {
		SetPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) error
		MatchesPersistenceQueue(*persistencequeue.Queue[persistencequeue.PersistenceCommand]) bool
	} = personaSQLite
}
