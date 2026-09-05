package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreKeepsLatestCanonicalTaskState(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteCode, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.Status = domaintask.StatusRunning
	value.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domaintask.StatusRunning {
		t.Fatalf("status = %s, want running", got.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "task_state.jsonl")); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"task_context.jsonl", "task_notifications.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, filename)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONLStoreRejectsLegacyFilesAndUnknownAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "job_state.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONLStore(root); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy store result = %v", err)
	}

	root = t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statePath, []byte(`{"job_id":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListTasks(context.Background(), domaintask.Filter{}); err == nil {
		t.Fatal("legacy JSON alias was accepted")
	}
}

func TestJSONLStoreContextAndNotificationUseTaskID(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(context.Background(), domaintask.SharedRoleContext{TaskID: value.TaskID, CurrentPlan: "plan", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	contextValue, err := store.GetContext(context.Background(), value.TaskID)
	if err != nil || contextValue.CurrentPlan != "plan" {
		t.Fatalf("context = %#v err=%v", contextValue, err)
	}
	notification := domaintask.NewNotification(value, now)
	if err := store.SaveNotification(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListNotifications(context.Background(), 10, false)
	if err != nil || len(items) != 1 || items[0].TaskID != value.TaskID {
		t.Fatalf("notifications = %#v err=%v", items, err)
	}
}
