package taskmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validDelegationReport(taskID modulecore.TaskID, runID modulecore.RunID) domainconversation.ReportEnvelope {
	return domainconversation.ReportEnvelope{
		ReportID: modulecore.NewResponseID(), ReportRevision: 1,
		DelegationID: modulecore.NewRequestID(), DelegationRevision: 7,
		WorkTaskID: taskID, WorkRunID: runID,
		Status:  domainconversation.ReportStatusSucceeded,
		Summary: "completed", Result: "the full report body",
	}
}

func TestCompleteWithReportClosesRunAndPersistsPendingReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	report := validDelegationReport(task.TaskID, run.RunID)

	completed, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, report)
	if err != nil {
		t.Fatalf("CompleteWithReport: %v", err)
	}
	if completed.Status != domaintask.StatusSucceeded {
		t.Fatalf("completed Task status = %s", completed.Status)
	}
	pending, err := manager.ListPendingReports(ctx)
	if err != nil {
		t.Fatalf("ListPendingReports: %v", err)
	}
	if len(pending) != 1 || pending[0].ReportID != report.ReportID || pending[0].Envelope.Result != report.Result || pending[0].TrustedSender != "shiro" {
		t.Fatalf("pending reports = %#v", pending)
	}
	persistedRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("persisted Run status = %s", persistedRun.Status)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := New(reopened, DefaultParallelLimits())
	pending, err = restarted.ListPendingReports(ctx)
	if err != nil {
		t.Fatalf("ListPendingReports after restart: %v", err)
	}
	if len(pending) != 1 || pending[0].Envelope.Result != report.Result || pending[0].TrustedSender != "shiro" {
		t.Fatalf("restarted pending reports = %#v", pending)
	}
}

func TestCompleteWithReportExactRetryAndChangedContentConflict(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	report := validDelegationReport(task.TaskID, run.RunID)

	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, report); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, report); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	changed := report
	changed.Result += " changed"
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, changed); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed-content retry = %v, want ErrTaskOperationConflict", err)
	}
	pending, err := manager.ListPendingReports(ctx)
	if err != nil || len(pending) != 1 || pending[0].Envelope.Result != report.Result {
		t.Fatalf("pending after retry = %#v, %v", pending, err)
	}
}

func TestCompleteWithReportRejectsWrongActorAndStaleRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	wrongActorReport := validDelegationReport(task.TaskID, run.RunID)
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "mio", domaintask.StatusSucceeded, wrongActorReport); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("wrong actor error = %v, want ErrRunConflict", err)
	}
	if pending, err := manager.ListPendingReports(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("wrong actor wrote report: pending=%#v err=%v", pending, err)
	}

	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, "done", ""); err != nil {
		t.Fatal(err)
	}
	latest, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	staleReport := validDelegationReport(task.TaskID, run.RunID)
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, staleReport); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("stale Run error = %v, want ErrRunConflict", err)
	}
	persistedTask, err := manager.Get(ctx, task.TaskID)
	if err != nil || persistedTask.Status != domaintask.StatusRunning {
		t.Fatalf("stale completion changed current Task: status=%s err=%v", persistedTask.Status, err)
	}
	persistedRun, err := manager.GetRun(ctx, latest.RunID)
	if err != nil || persistedRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("stale completion changed latest Run: status=%s err=%v", persistedRun.Status, err)
	}
}

func TestCompleteWithReportPreservesEnvelopeOverGenericReceiptLimit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	report := validDelegationReport(task.TaskID, run.RunID)
	report.Summary = strings.Repeat("s", domainconversation.MaxReportSummaryBytes)
	report.Result = strings.Repeat("r", domainconversation.MaxReportResultBytes)
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, report); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join(root, "task_delegation_reports.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) <= 64*1024 {
		t.Fatalf("report journal length = %d, want over 64KiB", len(journal))
	}
	pending, err := manager.ListPendingReports(ctx)
	if err != nil || len(pending) != 1 || pending[0].Envelope.Summary != report.Summary || pending[0].Envelope.Result != report.Result {
		t.Fatalf("large report was not preserved: pending=%d err=%v", len(pending), err)
	}
	receipt, err := store.LookupTaskOperation(ctx, "delegation-report:"+string(report.ReportID))
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Result) >= 1024 {
		t.Fatalf("operation receipt result is %d bytes, want a small reference", len(receipt.Result))
	}
}

func TestCompleteWithReportRollsBackWhenBatchIsCanceledAfterReportAppend(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	report := validDelegationReport(task.TaskID, run.RunID)
	for i := 0; i < domainconversation.MaxReportLimitations; i++ {
		report.Limitations = append(report.Limitations, strings.Repeat("l", domainconversation.MaxReportLimitationBytes))
	}
	reportPath := filepath.Join(root, "task_delegation_reports.jsonl")
	before := make(map[string][]byte)
	for _, name := range []string{"task_state.jsonl", "task_run.jsonl", "task_notifications.jsonl", "task_execution_fences.jsonl", "task_operation_receipts.jsonl", "task_delegation_reports.jsonl"} {
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		before[name] = data
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var observed atomic.Bool
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for {
			info, statErr := os.Stat(reportPath)
			if statErr == nil && info.Size() > 0 {
				observed.Store(true)
				cancel()
				return
			}
			select {
			case <-batchCtx.Done():
				return
			case <-time.After(100 * time.Microsecond):
			}
		}
	}()
	_, err = manager.CompleteWithReport(batchCtx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, report)
	<-stop
	if err == nil || !observed.Load() {
		t.Fatalf("CompleteWithReport error=%v reportObserved=%v; want cancellation after report append", err, observed.Load())
	}
	for name, want := range before {
		got, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		if !errors.Is(readErr, os.ErrNotExist) && string(got) != string(want) {
			t.Fatalf("batch failure changed %s", name)
		}
		if errors.Is(readErr, os.ErrNotExist) && len(want) != 0 {
			t.Fatalf("batch failure removed non-empty %s", name)
		}
	}
	persistedTask, err := manager.Get(ctx, task.TaskID)
	if err != nil || persistedTask.Status != domaintask.StatusRunning {
		t.Fatalf("failed batch changed Task: status=%s err=%v", persistedTask.Status, err)
	}
	persistedRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil || persistedRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("failed batch changed Run: status=%s err=%v", persistedRun.Status, err)
	}
	if pending, err := manager.ListPendingReports(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("failed batch left pending report: %#v %v", pending, err)
	}
}

func TestV2TaskLayoutCanWriteAndRestartWithDelegationReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{
		Index: true, Persist: true, Rotation: true, RotationSizeValve: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "shiro")
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, "first run", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(root, "LAYOUT")
	layoutData, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(layoutData), "\n"), "\n")
	if len(lines) != 9 || lines[0] != "tasks-layout/v3" || !strings.HasPrefix(lines[7], "frozen task_delegation_reports.jsonl ") {
		t.Fatalf("unexpected generated v3 LAYOUT: %q", layoutData)
	}
	legacyLayout := append([]string{"tasks-layout/v2"}, lines[1:7]...)
	legacyLayout = append(legacyLayout, "end")
	if err := os.WriteFile(layoutPath, []byte(strings.Join(legacyLayout, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "index", "tasks.tsi")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	store, err = taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatalf("open old v2 layout: %v", err)
	}
	manager = New(store, DefaultParallelLimits())
	if prior, err := manager.Get(ctx, task.TaskID); err != nil || prior.Status != domaintask.StatusSucceeded {
		t.Fatalf("v2 Task record not preserved: status=%s err=%v", prior.Status, err)
	}
	priorRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil || priorRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("v2 Run record not preserved: status=%s err=%v", priorRun.Status, err)
	}
	latest, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	report := validDelegationReport(task.TaskID, latest.RunID)
	if _, err := manager.CompleteWithReport(ctx, task.TaskID, latest.RunID, "shiro", domaintask.StatusSucceeded, report); err != nil {
		t.Fatalf("write report to v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatalf("restart v2 store: %v", err)
	}
	defer store.Close()
	restarted := New(store, DefaultParallelLimits())
	pending, err := restarted.ListPendingReports(ctx)
	if err != nil || len(pending) != 1 || pending[0].Envelope.Result != report.Result {
		t.Fatalf("v2 report after restart: %#v err=%v", pending, err)
	}
	if prior, err := restarted.Get(ctx, task.TaskID); err != nil || prior.Status != domaintask.StatusSucceeded {
		t.Fatalf("v2 Task history after report: status=%s err=%v", prior.Status, err)
	}
	finalLayout, err := os.ReadFile(layoutPath)
	if err != nil || !strings.HasPrefix(string(finalLayout), "tasks-layout/v2\n") {
		t.Fatalf("v2 LAYOUT was rewritten: %q err=%v", finalLayout, err)
	}
}
