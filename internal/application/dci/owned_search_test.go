package dci

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOwnedSearcherCompletesCanonicalDCIActionAndAttempt(t *testing.T) {
	corpus := t.TempDir()
	if err := os.WriteFile(filepath.Join(corpus, "spec.md"), []byte("canonical DCI action evidence\n"), 0o600); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	explorer := NewExplorer(Config{
		Enabled:      true,
		ActorKind:    "agent",
		ActorID:      "shiro",
		Allowlist:    []string{corpus},
		MaxEvidence:  1,
		MaxFilesRead: 1,
	}, &memoryTraceStore{}, WithEventAppender(&recordingEventAppender{}))
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("create action store: %v", err)
	}
	actions := actionmanager.New(actionStore)
	searcher, err := NewOwnedSearcher(explorer, actions)
	if err != nil {
		t.Fatalf("create owned searcher: %v", err)
	}
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()

	result, err := searcher.SearchAs(context.Background(), "canonical DCI", taskID, runID, "agent", "shiro", "request-1")
	if err != nil {
		t.Fatalf("owned search: %v", err)
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(storedActions) != 1 {
		t.Fatalf("actions=%#v, want one", storedActions)
	}
	action := storedActions[0]
	if action.Kind != domainaction.KindDCI || action.Status != domainaction.StatusSucceeded || action.ActionID != result.Trace.ActionID || action.ActionID != result.Pack.ActionID {
		t.Fatalf("action/result identity mismatch: action=%#v result=%#v", action, result)
	}
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].AttemptID != action.CurrentAttemptID || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("attempts=%#v action=%#v", attempts, action)
	}
}

func TestNewOwnedSearcherRejectsMissingActionOwner(t *testing.T) {
	explorer := NewExplorer(Config{Enabled: true}, &memoryTraceStore{}, WithEventAppender(&recordingEventAppender{}))
	if _, err := NewOwnedSearcher(explorer, nil); err == nil {
		t.Fatal("missing Action owner unexpectedly accepted")
	}
}
