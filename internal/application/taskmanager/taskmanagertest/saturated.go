// Package taskmanagertest provides a real, execution-capacity-saturated Task
// owner for tests that must prove an admission refusal leaves nothing behind.
package taskmanagertest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// Saturated is a real Task owner over the JSONL store whose global execution
// capacity is fully used by one running occupant Task. Any admission of a new
// Task is therefore refused with taskmanager.ErrParallelLimit.
type Saturated struct {
	Manager  *taskmanager.Manager
	Occupant domaintask.Task
}

// NewSaturated builds the saturated owner. The store is closed on cleanup.
func NewSaturated(t testing.TB) *Saturated {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("create Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	limits := taskmanager.DefaultParallelLimits()
	limits.Global = 1
	manager := taskmanager.New(store, limits)
	occupant, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title:    "capacity occupant",
		Route:    domaintask.RouteGeneral,
		Assignee: "occupant",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("occupy global capacity: %v", err)
	}
	return &Saturated{Manager: manager, Occupant: occupant}
}

// AssertNothingPersisted fails unless the store holds exactly the occupant
// Task and its single Run, with no other Task, Run, or notification.
func (s *Saturated) AssertNothingPersisted(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	tasks, err := s.Manager.List(ctx, domaintask.Filter{Limit: 10000})
	if err != nil {
		t.Fatalf("list Tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != s.Occupant.TaskID {
		t.Fatalf("a refused admission left Tasks behind: got %d Tasks, want only the occupant", len(tasks))
	}
	runs, err := s.Manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatalf("list Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].TaskID != s.Occupant.TaskID {
		t.Fatalf("a refused admission left Runs behind: got %d Runs, want only the occupant's", len(runs))
	}
	notifications, err := s.Manager.Notifications(ctx, 100, false)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(notifications) != 0 {
		t.Fatalf("a refused admission left %d notifications behind", len(notifications))
	}
}
