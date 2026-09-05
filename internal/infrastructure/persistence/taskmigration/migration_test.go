package taskmigration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunDryRunAndApplyPreserveHistoryAndCanonicalReferences(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	messageID := modulecore.NewMessageID()
	states := []legacyState{
		{JobID: "job-parent", Title: "parent", Route: domaintask.RouteCode, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityNormal, CreatedBy: "mio", InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now},
		{JobID: "job-parent", Title: "parent", Route: domaintask.RouteCode, Status: string(domaintask.StatusRunning), Priority: domaintask.PriorityNormal, CreatedBy: "mio", InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now.Add(time.Minute), StartedAt: timePointer(now.Add(time.Minute))},
		{JobID: "job-child", Title: "child", Route: domaintask.RouteResearch, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityHigh, CreatedBy: "ren", ParentJobID: "job-parent", DependencyJobIDs: []string{"job-parent"}, ParentMessageID: string(messageID), SupersedesJobID: "job-parent", InterruptPolicy: domaintask.InterruptSilent, CreatedAt: now, UpdatedAt: now},
	}
	contexts := []legacyContext{{JobID: "job-child", UserIntent: "continue", UpdatedAt: now}}
	notifications := []legacyNotification{{Type: "job.status", Level: domaintask.NotificationDone, JobID: "job-parent", Title: "parent", Status: string(domaintask.StatusSucceeded), Interrupt: true, CreatedAt: now}}
	writeLegacySource(t, source, states, contexts, notifications)

	dryReceiptPath := filepath.Join(root, "dry.json")
	dry, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: source, ReceiptPath: dryReceiptPath})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Status != "ready" || dry.LegacyStateRows != 3 || dry.TaskStateRows != 3 || dry.LegacyTaskIDs != 2 || dry.LegacyRowsRemaining != 0 {
		t.Fatalf("dry receipt = %#v", dry)
	}
	applyReceiptPath := filepath.Join(root, "apply.json")
	applied, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: source, OutputDir: output, ReceiptPath: applyReceiptPath, DryRunReceipt: dryReceiptPath})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" || applied.SourceSHA256 != dry.SourceSHA256 || applied.MappingSHA256 != dry.MappingSHA256 || applied.OutputSHA256 != dry.OutputSHA256 {
		t.Fatalf("apply receipt = %#v, dry = %#v", applied, dry)
	}

	parentRaw, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "job_state", "job_id", "job-parent")
	if err != nil {
		t.Fatal(err)
	}
	childRaw, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "job_state", "job_id", "job-child")
	if err != nil {
		t.Fatal(err)
	}
	parentID, childID := modulecore.TaskID(parentRaw), modulecore.TaskID(childRaw)
	store, err := taskpersistence.NewJSONLStore(output)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := store.GetTask(context.Background(), parentID)
	if err != nil || parent.Status != domaintask.StatusRunning || parent.OwnerID != "mio" {
		t.Fatalf("parent = %#v err=%v", parent, err)
	}
	child, err := store.GetTask(context.Background(), childID)
	if err != nil || child.ParentTaskID != parentID || len(child.DependencyTaskIDs) != 1 || child.DependencyTaskIDs[0] != parentID || child.SupersedesTaskID != parentID || child.OriginMessageID != messageID || child.OwnerID != "ren" {
		t.Fatalf("child = %#v err=%v", child, err)
	}
	shared, err := store.GetContext(context.Background(), childID)
	if err != nil || shared.TaskID != childID {
		t.Fatalf("context = %#v err=%v", shared, err)
	}
	items, err := store.ListNotifications(context.Background(), 10, false)
	if err != nil || len(items) != 1 || items[0].TaskID != parentID || items[0].Type != "task.notification" {
		t.Fatalf("notifications = %#v err=%v", items, err)
	}
	stateBytes, err := os.ReadFile(filepath.Join(output, taskStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(stateBytes), "\n") != len(states) || strings.Contains(string(stateBytes), "job_id") || strings.Contains(string(stateBytes), "created_by") {
		t.Fatalf("migrated state did not preserve canonical append history: %s", stateBytes)
	}
}

func TestRunAcceptsTheObservedEmptyThreeFileStore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacySource(t, source, nil, nil, nil)
	receipt, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: source, ReceiptPath: filepath.Join(root, "dry.json")})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SourceFiles != 3 || receipt.LegacyStateRows != 0 || receipt.TaskStateRows != 0 || receipt.LegacyRowsRemaining != 0 {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRunRejectsAmbiguousOrLegacySource(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	valid := legacyState{JobID: "job-one", Title: "one", Route: domaintask.RouteGeneral, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	tests := []struct {
		name     string
		state    []legacyState
		context  []legacyContext
		rawState string
		wantCode string
	}{
		{name: "waiting user", state: []legacyState{{JobID: "job-one", Title: "one", Route: domaintask.RouteGeneral, Status: "waiting_user", Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}}, wantCode: "waiting_user_forbidden"},
		{name: "ambiguous origin", state: []legacyState{func() legacyState { value := valid; value.ParentConversationID = "chat-old"; return value }()}, wantCode: "origin_unsupported"},
		{name: "unknown route", state: []legacyState{func() legacyState { value := valid; value.Route = domaintask.Route("DIRECT"); return value }()}, wantCode: "state_schema_invalid"},
		{name: "dangling context", state: []legacyState{valid}, context: []legacyContext{{JobID: "job-missing", UpdatedAt: now}}, wantCode: "dangling_reference"},
		{name: "relationship cycle", state: []legacyState{
			{JobID: "job-one", Title: "one", Route: domaintask.RouteGeneral, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityNormal, ParentJobID: "job-two", InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now},
			{JobID: "job-two", Title: "two", Route: domaintask.RouteGeneral, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityNormal, ParentJobID: "job-one", InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now},
		}, wantCode: "relationship_cycle"},
		{name: "unknown field", rawState: `{"job_id":"job-one","title":"one","route":"GENERAL","status":"queued","priority":"normal","interrupt_policy":"silent","created_at":"2026-09-05T04:00:00Z","updated_at":"2026-09-05T04:00:00Z","secret":"unexpected"}` + "\n", wantCode: "state_schema_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			writeLegacySource(t, source, test.state, test.context, nil)
			if test.rawState != "" {
				if err := os.WriteFile(filepath.Join(source, stateFilename), []byte(test.rawState), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: source, ReceiptPath: filepath.Join(root, "dry.json")})
			if err == nil || receipt.ErrorCode != test.wantCode {
				t.Fatalf("receipt=%#v err=%v, want %s", receipt, err, test.wantCode)
			}
		})
	}
}

func TestRunApplyRejectsSourceDriftAndNonFreshOutput(t *testing.T) {
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	valid := legacyState{JobID: "job-one", Title: "one", Route: domaintask.RouteGeneral, Status: string(domaintask.StatusQueued), Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacySource(t, source, []legacyState{valid}, nil, nil)
	dryPath := filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: source, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	valid.UpdatedAt = valid.UpdatedAt.Add(time.Second)
	writeLegacySource(t, source, []legacyState{valid}, nil, nil)
	receipt, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: source, OutputDir: output, ReceiptPath: filepath.Join(root, "apply.json"), DryRunReceipt: dryPath})
	if err == nil || receipt.ErrorCode != "dry_run_mismatch" {
		t.Fatalf("source drift receipt=%#v err=%v", receipt, err)
	}

	root = t.TempDir()
	source = filepath.Join(root, "source")
	output = filepath.Join(root, "output")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacySource(t, source, []legacyState{valid}, nil, nil)
	dryPath = filepath.Join(root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: source, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "unexpected"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err = Run(context.Background(), Options{Mode: ModeApply, SourceDir: source, OutputDir: output, ReceiptPath: filepath.Join(root, "apply.json"), DryRunReceipt: dryPath})
	if err == nil || receipt.ErrorCode != "invalid_output" {
		t.Fatalf("non-fresh output receipt=%#v err=%v", receipt, err)
	}
}

func writeLegacySource(t *testing.T, root string, states []legacyState, contexts []legacyContext, notifications []legacyNotification) {
	t.Helper()
	writeJSONL := func(name string, values any) {
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		var builder strings.Builder
		for _, row := range rows {
			builder.Write(row)
			builder.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(builder.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONL(stateFilename, states)
	writeJSONL(contextFilename, contexts)
	writeJSONL(notificationsFilename, notifications)
}

func timePointer(value time.Time) *time.Time { return &value }
