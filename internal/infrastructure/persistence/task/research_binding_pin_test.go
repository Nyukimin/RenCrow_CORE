package task

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func boundResearchPinTask(index int) domaintask.Task {
	task := indexedTestTask(index, indexTestNow)
	task.Route = domaintask.RouteResearch
	task.OwnerID = "local-research-owner"
	task.Assignee = task.OwnerID
	task.ResearchBinding = &domaintask.ResearchExecutionBinding{
		SchemaVersion: domaintask.ResearchExecutionBindingSchema,
		DataScope:     "user",
		OwnerID:       task.OwnerID,
		OwnerRoute:    "core.research.local",
	}
	return task
}

func nextResearchPinVersion(task domaintask.Task) domaintask.Task {
	task.UpdatedAt = task.UpdatedAt.Add(time.Second)
	return task
}

func TestIndexedResearchBindingIsPinnedAcrossUpdatesAndRestart(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	legacy := indexedTestTask(31, indexTestNow)
	legacy.Route = domaintask.RouteResearch
	legacy.OwnerID = "local-research-owner"
	legacy.Assignee = legacy.OwnerID
	if err := store.SaveTask(ctx, legacy); err != nil {
		t.Fatalf("save legacy unbound Research Task: %v", err)
	}
	backfilled := nextResearchPinVersion(legacy)
	backfilled.ResearchBinding = boundResearchPinTask(31).ResearchBinding
	if err := store.SaveTask(ctx, backfilled); !errors.Is(err, domaintask.ErrResearchExecutionBindingImmutable) {
		t.Fatalf("Research binding backfill error = %v, want immutable error", err)
	}

	bound := boundResearchPinTask(32)
	if err := store.SaveTask(ctx, bound); err != nil {
		t.Fatalf("save first bound Research Task: %v", err)
	}
	if err := store.SaveTask(ctx, nextResearchPinVersion(bound)); err != nil {
		t.Fatalf("unchanged Research binding update: %v", err)
	}
	if err := store.Transaction(ctx, func(tx domaintask.Store) error {
		current, err := tx.GetTask(ctx, bound.TaskID)
		if err != nil {
			return err
		}
		changed := nextResearchPinVersion(current)
		changed.OwnerID = "other-research-owner"
		changed.Assignee = changed.OwnerID
		binding := *changed.ResearchBinding
		binding.OwnerID = changed.OwnerID
		changed.ResearchBinding = &binding
		if err := tx.SaveTask(ctx, changed); !errors.Is(err, domaintask.ErrResearchExecutionBindingImmutable) {
			t.Fatalf("Task transaction binding owner change error = %v, want immutable error", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Task transaction: %v", err)
	}
	removed := nextResearchPinVersion(bound)
	removed.ResearchBinding = nil
	if err := store.SaveTask(ctx, removed); !errors.Is(err, domaintask.ErrResearchExecutionBindingImmutable) {
		t.Fatalf("Research binding removal error = %v, want immutable error", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close indexed store: %v", err)
	}
	store = openIndexed(t, root)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveTask(ctx, nextResearchPinVersion(bound)); err != nil {
		t.Fatalf("unchanged Research binding after reopen: %v", err)
	}
	if err := store.SaveTask(ctx, removed); !errors.Is(err, domaintask.ErrResearchExecutionBindingImmutable) {
		t.Fatalf("Research binding removal after reopen error = %v, want immutable error", err)
	}
}

func TestResearchBindingTransitionsAreRejectedByTaskLogFold(t *testing.T) {
	legacy := boundResearchPinTask(33)
	legacy.ResearchBinding = nil
	legacyUpdate := nextResearchPinVersion(legacy)
	if _, err := foldTaskRecords([]domaintask.Task{legacy, legacyUpdate}); err != nil {
		t.Fatalf("legacy Task without Research binding stopped loading: %v", err)
	}
	bound := boundResearchPinTask(34)
	unchanged := nextResearchPinVersion(bound)
	if _, err := foldTaskRecords([]domaintask.Task{bound, unchanged}); err != nil {
		t.Fatalf("unchanged Research binding history rejected: %v", err)
	}
	changedOwner := nextResearchPinVersion(bound)
	changedOwner.OwnerID = "other-research-owner"
	changedOwner.Assignee = changedOwner.OwnerID
	changedBinding := *changedOwner.ResearchBinding
	changedBinding.OwnerID = changedOwner.OwnerID
	changedOwner.ResearchBinding = &changedBinding
	removed := nextResearchPinVersion(bound)
	removed.ResearchBinding = nil
	backfilled := nextResearchPinVersion(legacy)
	backfilled.ResearchBinding = bound.ResearchBinding
	for _, tc := range []struct {
		name string
		rows []domaintask.Task
	}{
		{name: "owner change", rows: []domaintask.Task{bound, changedOwner}},
		{name: "removal", rows: []domaintask.Task{bound, removed}},
		{name: "legacy backfill", rows: []domaintask.Task{legacy, backfilled}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := foldTaskRecords(tc.rows); !errors.Is(err, domaintask.ErrResearchExecutionBindingImmutable) {
				t.Fatalf("Task log fold error = %v, want immutable error", err)
			}
		})
	}
}
