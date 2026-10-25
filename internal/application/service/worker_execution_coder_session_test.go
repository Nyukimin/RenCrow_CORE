package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/testsupport/testimpact"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func coderTaskSessionContext(
	t *testing.T,
	worker *workerExecutionService,
	taskID modulecore.TaskID,
	moduleRoot string,
	readOnly bool,
	sessionID modulecore.SessionID,
) context.Context {
	t.Helper()
	task := domaintask.Task{
		TaskID:          taskID,
		Title:           "coder task session fixture",
		Assignee:        "shiro",
		Route:           domaintask.RouteCODE1,
		ModuleID:        "module-" + taskID.String(),
		ModuleRoot:      moduleRoot,
		ReadOnly:        readOnly,
		OriginSessionID: sessionID,
	}
	if _, err := worker.owner.Create(context.Background(), task, domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	run, err := worker.owner.StartRunWithReason(context.Background(), taskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, run.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope(
		string(modulecore.NewRequestID()),
		domaintool.ActorKindAgent,
		"shiro",
		"",
		[]string{domaintool.DataScopePublic},
		domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope)
}

func coderTaskPatch(t *testing.T, root, filename, content string) *proposal.Proposal {
	t.Helper()
	patchJSON, err := json.Marshal([]map[string]any{{
		"type":    "file_edit",
		"action":  "create",
		"target":  filepath.Join(root, filename),
		"content": content,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return proposal.NewProposal("write task output", string(patchJSON), "", "")
}

func TestBeginCoderTaskExecutionRequiresTaskRootAndExactIdentity(t *testing.T) {
	root := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")

	if _, err := worker.BeginCoderTaskExecution(ctx, modulecore.NewTaskID()); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("task identity mismatch was not rejected: %v", err)
	}
	if _, err := worker.BeginCoderTaskExecution(ctx, taskID); err != nil {
		t.Fatalf("valid task root was rejected: %v", err)
	}

	missingRootTask := modulecore.NewTaskID()
	missingRootCtx := coderTaskSessionContext(t, worker, missingRootTask, "", false, "")
	if _, err := worker.BeginCoderTaskExecution(missingRootCtx, missingRootTask); err == nil || !strings.Contains(err.Error(), "ModuleRoot") {
		t.Fatalf("missing authoritative Task.ModuleRoot was not rejected: %v", err)
	}
}

func TestCoderTaskSessionReadOnlyRequiresObservationAndRejectsPatchAndTests(t *testing.T) {
	root := makeCanonicalImpactWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "known.txt"), []byte("known"), 0o644); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, true, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready {
		t.Fatal("final-only read-only task was accepted without an observation")
	}
	results, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "cat known.txt"}})
	if err != nil || len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("read-only observation failed: results=%#v err=%v", results, err)
	}
	if _, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "forbidden.txt", "no")); err == nil {
		t.Fatal("read-only patch proposal was accepted")
	}
	if _, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "go test ./..."}}); err == nil {
		t.Fatal("read-only executable test observation was accepted")
	}
	evidence, err = session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Ready || !evidence.ReadOnly || len(evidence.ChangedFiles) != 0 {
		t.Fatalf("read-only observation did not complete cleanly: %#v", evidence)
	}
}

func TestCoderTaskSessionWriteUsesActualChangesAndSurvivesAutocommit(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	root := makeCanonicalImpactWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "preexisting.txt"), []byte("before session"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testImpactWorkerConfig(root, os.Args[0])
	cfg.AutoCommit = true
	cfg.GitTimeout = 10
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limits := taskmanager.DefaultParallelLimits()
	owner := taskmanager.New(store, limits)
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	worker := NewWorkerExecutionService(cfg, owner)
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "new.txt", "written by worker"))
	if err != nil {
		t.Fatalf("owned patch execution failed: result=%#v err=%v", result, err)
	}
	if result == nil || !result.Success || result.TestStatus != "passed" || result.TestReceipt == "" || result.GitCommit == "" {
		t.Fatalf("patch result lost owner verification or auto-commit evidence: %#v", result)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Ready || evidence.TestStatus != "passed" || evidence.TestReceipt != result.TestReceipt {
		t.Fatalf("write completion evidence is incomplete: %#v", evidence)
	}
	if want := []string{"new.txt"}; !reflect.DeepEqual(evidence.ChangedFiles, want) {
		t.Fatalf("changed files=%q, want only actual session change %q", evidence.ChangedFiles, want)
	}
}

func TestCoderTaskSessionFailedVerificationIsNotClearedByUnrelatedRead(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "failed_step")
	root := makeCanonicalImpactWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "known.txt"), []byte("known"), 0o644); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "go test ./..."}})
	if err == nil || len(failed) != 1 || failed[0].TestStatus != "failed" {
		t.Fatalf("expected owner verification failure: results=%#v err=%v", failed, err)
	}
	read, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "cat known.txt"}})
	if err != nil || len(read) != 1 || read[0].Status != "ok" {
		t.Fatalf("unrelated read failed: results=%#v err=%v", read, err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready || !strings.Contains(evidence.FailureReason, "verification") || evidence.TestStatus != "failed" {
		t.Fatalf("unrelated read cleared failed verification: %#v", evidence)
	}
}

func TestCoderTaskSessionRevisedPatchClearsFailedCanonicalVerification(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "failed_step")
	root := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "go test ./..."}})
	if err == nil || len(failed) != 1 || failed[0].TestStatus != "failed" {
		t.Fatalf("expected initial owner verification failure: results=%#v err=%v", failed, err)
	}

	t.Setenv(testimpact.HelperModeEnv, "passed")
	result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "revised.txt", "verified revision"))
	if err != nil || result == nil || !result.Success || result.TestStatus != "passed" {
		t.Fatalf("successful revised patch and canonical test did not recover: result=%#v err=%v", result, err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Ready || evidence.TestStatus != "passed" || !reflect.DeepEqual(evidence.ChangedFiles, []string{"revised.txt"}) {
		t.Fatalf("revised canonical verification did not clear its prior failure: %#v", evidence)
	}
}

func TestCoderTaskSessionParseFailureRequiresCheckedPatch(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	root := makeCanonicalImpactWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "known.txt"), []byte("known"), 0o644); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "first.txt", "verified")); err != nil || result == nil || !result.Success {
		t.Fatalf("initial verified patch failed: result=%#v err=%v", result, err)
	}
	if err := session.RecordLoopFailure(ctx, "response must be one JSON object"); err != nil {
		t.Fatal(err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready || !strings.Contains(evidence.FailureReason, "CoderLoop protocol") {
		t.Fatalf("invalid protocol response was not retained: %#v", evidence)
	}
	if _, err := session.ExecuteObservation(ctx, []ObservationAction{{Action: "shell_command", Target: "cat known.txt"}}); err != nil {
		t.Fatalf("unrelated read failed: %v", err)
	}
	evidence, err = session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready || !strings.Contains(evidence.FailureReason, "CoderLoop protocol") {
		t.Fatalf("unrelated read cleared write-session protocol failure: %#v", evidence)
	}
	if result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "revised.txt", "checked revision")); err != nil || result == nil || !result.Success {
		t.Fatalf("checked revision did not resolve protocol failure: result=%#v err=%v", result, err)
	}
	evidence, err = session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Ready || !reflect.DeepEqual(evidence.ChangedFiles, []string{"first.txt", "revised.txt"}) {
		t.Fatalf("successful checked revision did not clear protocol failure: %#v", evidence)
	}
}

func TestCoderTaskSessionInvalidatesVerificationAfterWorkspaceChange(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	root := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "new.txt", "verified"))
	if err != nil || result == nil || result.TestStatus != "passed" {
		t.Fatalf("worker patch verification failed: result=%#v err=%v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("changed after test"), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready || !strings.Contains(evidence.FailureReason, "changed after") {
		t.Fatalf("post-verification change did not invalidate completion: %#v", evidence)
	}
}

func TestCoderTaskSessionBindsVerificationBeforeAutocommitHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git post-commit executable hooks are not portable to Windows")
	}
	t.Setenv(testimpact.HelperModeEnv, "passed")
	root := makeCanonicalImpactWorkspace(t)
	hookPath := filepath.Join(root, ".git", "hooks", "post-commit")
	hook := "#!/bin/sh\nif [ -f verified.txt ]; then printf '%s\\n' 'changed after verification' > verified.txt; fi\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testImpactWorkerConfig(root, os.Args[0])
	cfg.AutoCommit = true
	cfg.GitTimeout = 10
	worker := newTestWorkerExecutionService(t, cfg)
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	session, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "verified.txt", "content verified before post-commit"))
	if err != nil || result == nil || !result.Success || result.GitCommit == "" {
		t.Fatalf("hooked auto-commit patch did not return success: result=%#v err=%v", result, err)
	}
	evidence, err := session.CompletionEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Ready || !strings.Contains(evidence.FailureReason, "changed after") {
		t.Fatalf("post-verification auto-commit hook mutation was accepted: %#v", evidence)
	}
}

func TestCoderTaskSessionFinalEvidenceRechecksContextAndRunAfterSnapshot(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	root := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	sessionValue, err := worker.BeginCoderTaskExecution(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionValue.(*workerCoderTaskExecutionSession)
	if result, err := session.ExecuteProposal(ctx, coderTaskPatch(t, root, "cancel-check.txt", "verified")); err != nil || result == nil || !result.Success {
		t.Fatalf("verified patch failed: result=%#v err=%v", result, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	_, err = session.completionEvidence(cancelCtx, func(snapshotCtx context.Context, root string, scratch bool, timeout int) (map[string]coderWorkspaceEntry, error) {
		snapshot, err := captureCoderWorkspaceSnapshot(snapshotCtx, root, scratch, timeout)
		cancel()
		return snapshot, err
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("cancellation after final snapshot was accepted: %v", err)
	}

	otherRoot := makeCanonicalImpactWorkspace(t)
	otherTaskID := modulecore.NewTaskID()
	otherCtx := coderTaskSessionContext(t, worker, otherTaskID, otherRoot, false, "")
	otherValue, err := worker.BeginCoderTaskExecution(otherCtx, otherTaskID)
	if err != nil {
		t.Fatal(err)
	}
	other := otherValue.(*workerCoderTaskExecutionSession)
	if result, err := other.ExecuteProposal(otherCtx, coderTaskPatch(t, otherRoot, "run-check.txt", "verified")); err != nil || result == nil || !result.Success {
		t.Fatalf("second verified patch failed: result=%#v err=%v", result, err)
	}
	_, err = other.completionEvidence(otherCtx, func(snapshotCtx context.Context, root string, scratch bool, timeout int) (map[string]coderWorkspaceEntry, error) {
		snapshot, err := captureCoderWorkspaceSnapshot(snapshotCtx, root, scratch, timeout)
		if err != nil {
			return nil, err
		}
		if _, err := worker.owner.Fail(snapshotCtx, otherTaskID, "terminated during completion capture", nil); err != nil {
			return nil, err
		}
		return snapshot, nil
	})
	if err == nil || !strings.Contains(err.Error(), "admission expired") {
		t.Fatalf("run invalidation after final snapshot was accepted: %v", err)
	}
}

func TestCoderTaskSessionsKeepFourTaskRootsAndReceiptsIndependent(t *testing.T) {
	t.Setenv(testimpact.HelperModeEnv, "passed")
	roots := make([]string, 4)
	for index := range roots {
		roots[index] = makeCanonicalImpactWorkspace(t)
	}
	cfg := testImpactWorkerConfig(roots[0], os.Args[0])
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	limits := taskmanager.DefaultParallelLimits()
	limits.Global = 4
	limits.PerModule = 1
	limits.CodingTasks = 4
	owner := taskmanager.New(store, limits)
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	worker := NewWorkerExecutionService(cfg, owner)
	sharedHistory := modulecore.NewSessionID()
	taskIDs := make([]modulecore.TaskID, len(roots))
	contexts := make([]context.Context, len(roots))
	sessions := make([]CoderTaskExecutionSession, len(roots))
	for index, root := range roots {
		taskIDs[index] = modulecore.NewTaskID()
		contexts[index] = coderTaskSessionContext(t, worker, taskIDs[index], root, false, sharedHistory)
		session, err := worker.BeginCoderTaskExecution(contexts[index], taskIDs[index])
		if err != nil {
			t.Fatalf("begin task %s session: %v", taskIDs[index], err)
		}
		sessions[index] = session
	}

	var wait sync.WaitGroup
	errorsByTask := make([]error, len(roots))
	evidenceByTask := make([]CoderTaskCompletionEvidence, len(roots))
	proposals := make([]*proposal.Proposal, len(roots))
	for index, root := range roots {
		filename := fmt.Sprintf("task-%d.txt", index+1)
		proposals[index] = coderTaskPatch(t, root, filename, fmt.Sprintf("content-%d", index+1))
	}
	for index := range roots {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, err := sessions[index].ExecuteProposal(contexts[index], proposals[index])
			if err != nil {
				errorsByTask[index] = err
				return
			}
			if result == nil || !result.Success || result.TestStatus != "passed" {
				errorsByTask[index] = fmt.Errorf("task %s returned no passed patch receipt", taskIDs[index])
				return
			}
			evidenceByTask[index], err = sessions[index].CompletionEvidence(contexts[index])
			errorsByTask[index] = err
		}(index)
	}
	wait.Wait()
	for index, evidence := range evidenceByTask {
		if errorsByTask[index] != nil {
			t.Fatalf("task %s execution failed: %v", taskIDs[index], errorsByTask[index])
		}
		if !evidence.Ready || evidence.TaskID != taskIDs[index] || evidence.TestStatus != "passed" || !reflect.DeepEqual(evidence.ChangedFiles, []string{fmt.Sprintf("task-%d.txt", index+1)}) {
			t.Fatalf("task %s received another session's evidence: %#v", taskIDs[index], evidence)
		}
		if !strings.HasPrefix(evidence.TestReceipt, filepath.Join(roots[index], "Tmp", "test-results")+string(os.PathSeparator)) {
			t.Fatalf("task %s receipt escaped its module root: %q", taskIDs[index], evidence.TestReceipt)
		}
	}
}

func TestExecuteProposalInWorkspaceCannotOverrideTaskModuleRoot(t *testing.T) {
	root := makeCanonicalImpactWorkspace(t)
	otherRoot := makeCanonicalImpactWorkspace(t)
	worker := newTestWorkerExecutionService(t, testImpactWorkerConfig(root, os.Args[0]))
	taskID := modulecore.NewTaskID()
	ctx := coderTaskSessionContext(t, worker, taskID, root, false, "")
	_, err := worker.ExecuteProposalInWorkspace(ctx, taskID, coderTaskPatch(t, otherRoot, "wrong.txt", "wrong root"), otherRoot)
	if err == nil || !strings.Contains(err.Error(), "cannot override authoritative Task.ModuleRoot") {
		t.Fatalf("workspace override was not rejected: %v", err)
	}
}
