package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestRecoverActionRunsAfterRestartClosesExactStaleRun(t *testing.T) {
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTasks, err := taskmanager.NewWithExpectedCriteriaRevision(firstStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	task, err := firstTasks.Create(context.Background(), domaintask.Task{
		Title: "restart recovery", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.CompleteAttempt(context.Background(), action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())

	recovered, err := recoverActionRunsAfterRestart(context.Background(), actions, secondTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRun, err := secondTasks.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredTask.Status != domaintask.StatusSucceeded || recoveredRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("recovered task=%#v run=%#v", recoveredTask, recoveredRun)
	}
}

func TestRecoverActionRunsAfterRestartPreservesOpenNativeUnknown(t *testing.T) {
	root := t.TempDir()
	taskRoot := filepath.Join(root, "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTasks, err := taskmanager.NewWithExpectedCriteriaRevision(firstStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	actionRoot := filepath.Join(root, "actions")
	firstActionStore, err := actionpersistence.NewJSONLStore(actionRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstActions := actionmanager.New(firstActionStore)
	task, err := firstTasks.Create(context.Background(), domaintask.Task{
		Title: "native restart recovery", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := firstActions.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
		TaskID: task.TaskID, RunID: run.RunID, ExpectedCriteriaRevision: task.ExpectedCriteriaRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := firstActions.PrepareNativeMutation(context.Background(), actionmanager.PrepareNativeMutationInput{
		ActionID: action.ActionID, AttemptID: attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen, Key: "open", Payload: []byte(`{"idempotency_key":"open"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := firstActions.MarkNativeMutationDeliveryUnknown(context.Background(), action.ActionID, attempt.AttemptID, domainaction.NativeMutationSlotOpen); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())
	secondActionStore, err := actionpersistence.NewJSONLStore(actionRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondActions := actionmanager.New(secondActionStore)

	recovered, err := recoverActionRunsAfterRestart(context.Background(), secondActions, secondTasks)
	if err != nil || recovered != 0 {
		t.Fatalf("open native delivery_unknown must remain untouched at startup: recovered=%d err=%v", recovered, err)
	}
	recoveredAction, err := secondActions.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	gotAttempts, err := secondActions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil || len(gotAttempts) != 1 || gotAttempts[0].AttemptID != attempt.AttemptID {
		t.Fatalf("load reopened native Attempt: attempts=%d err=%v", len(gotAttempts), err)
	}
	recoveredAttempt := gotAttempts[0]
	recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRun, err := secondTasks.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredAction.Status != domainaction.StatusOpen || recoveredAttempt.Status != domainaction.AttemptStatusRunning || recoveredAttempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusDeliveryUnknown || len(recoveredAttempt.NativeDelegation.RunResult) != 0 || recoveredTask.Status != domaintask.StatusRunning || recoveredRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("restart must preserve unknown native state: action=%+v attempt=%+v task=%+v run=%+v", recoveredAction, recoveredAttempt, recoveredTask, recoveredRun)
	}
}

func TestRecoverActionRunsAfterRestartRequiresAcceptedNativeRunResultForTaskSuccess(t *testing.T) {
	for _, tc := range []struct {
		name         string
		verification string
		withProof    bool
		wantTask     domaintask.Status
	}{
		{name: "completed and passed with proof", verification: "passed", withProof: true, wantTask: domaintask.StatusSucceeded},
		{name: "completed and passed without proof", verification: "passed", wantTask: domaintask.StatusFailed},
		{name: "completed but not verified", verification: "not_run", wantTask: domaintask.StatusFailed},
		{name: "completed with failed verification", verification: "failed", wantTask: domaintask.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			taskRoot := filepath.Join(root, "tasks")
			firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
			if err != nil {
				t.Fatal(err)
			}
			revision := strings.Repeat("a", 64)
			firstTasks, err := taskmanager.NewWithExpectedCriteriaRevision(firstStore, taskmanager.DefaultParallelLimits(), revision)
			if err != nil {
				t.Fatal(err)
			}
			actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(root, "actions"))
			if err != nil {
				t.Fatal(err)
			}
			actions := actionmanager.New(actionStore)
			task, err := firstTasks.Create(context.Background(), domaintask.Task{
				Title: "native terminal recovery", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
			}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
			if err != nil {
				t.Fatal(err)
			}
			action, attempt, err := actions.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
				TaskID: task.TaskID, RunID: run.RunID, ExpectedCriteriaRevision: revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			startBytes, start := validNativeStart(t)
			acceptNativeMutation(t, actions, action, attempt, domainaction.NativeMutationSlotOpen, "open", []byte(`{"idempotency_key":"open"}`), []byte(`{}`))
			acceptNativeMutation(t, actions, action, attempt, domainaction.NativeMutationSlotStart, "start", []byte(`{"idempotency_key":"start"}`), startBytes)
			runBytes, evidenceID := validNativeRunResult(t, start, tc.verification, revision)
			attemptStatus, actionStatus := domainaction.AttemptStatusFailed, domainaction.StatusFailed
			var proof *domainaction.NativeDelegationProof
			if tc.withProof {
				proof = nativeCriteriaProof(runBytes, revision, evidenceID)
				attemptStatus, actionStatus = domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded
			}
			if _, _, err := actions.CompleteNativeDelegation(context.Background(), actionmanager.CompleteNativeDelegationInput{
				ActionID: action.ActionID, AttemptID: attempt.AttemptID, RunResult: runBytes, Proof: proof,
				AttemptStatus: attemptStatus, ActionStatus: actionStatus, Summary: "native terminal result",
			}); err != nil {
				t.Fatal(err)
			}
			if err := firstStore.Close(); err != nil {
				t.Fatal(err)
			}
			secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = secondStore.Close() })
			secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())
			recovered, err := recoverActionRunsAfterRestart(context.Background(), actions, secondTasks)
			if err != nil || recovered != 1 {
				t.Fatalf("recover=%d err=%v", recovered, err)
			}
			recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if recoveredTask.Status != tc.wantTask {
				t.Fatalf("Task status=%s want=%s for native verification %s", recoveredTask.Status, tc.wantTask, tc.verification)
			}
		})
	}
}

func acceptNativeMutation(t *testing.T, actions *actionmanager.Manager, action domainaction.Action, attempt domainaction.Attempt, slot domainaction.NativeMutationSlot, key string, payload, result []byte) {
	t.Helper()
	if _, _, err := actions.PrepareNativeMutation(context.Background(), actionmanager.PrepareNativeMutationInput{
		ActionID: action.ActionID, AttemptID: attempt.AttemptID, Slot: slot, Key: key, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.MarkNativeMutationDeliveryUnknown(context.Background(), action.ActionID, attempt.AttemptID, slot); err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.RecordNativeMutationOutcome(context.Background(), actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: action.ActionID, AttemptID: attempt.AttemptID, Slot: slot, Status: domainaction.NativeMutationStatusAccepted, Result: result,
	}); err != nil {
		t.Fatal(err)
	}
}

type nativeStartIdentity struct {
	runID  string
	taskID string
}

func validNativeStart(t *testing.T) ([]byte, nativeStartIdentity) {
	t.Helper()
	const taskID = "tsk_01a11f9a-c8f0-7468-8269-160443726790"
	const runID = "run_01a11f9a-c8f0-746e-a8c7-3bf9c6e27e84"
	const raw = `{"accepted":true,"deadline_at":"2026-10-09T08:40:00Z","effective_limits":{"deadline_seconds":1,"max_capture_bytes":2048,"max_generation_attempts":1,"max_model_steps":1,"max_tool_calls_per_step":1},"intake":{"accepted_at":"2026-10-09T07:40:00Z","accepted_sequence":1,"caller_profile_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","declared_origin":"automation","effective_origin":"automation","entrypoint":"stdio_core","evidence_id":"evd_01a11f9a-c8f0-74ca-89d2-7eae75c238fb","message_id":"msg_01a11f9a-c8f0-74c3-852b-7847c57827ca","principal":"core:local","proof_basis":"automation","proof_digest":null,"receipt_id":"rcp_01a11f9a-c8f0-7475-9d19-9f5cd9afd25c","thread_id":"thr_01a11f9a-c8f0-745e-a6bd-1c816412d607"},"receipt_id":"rcp_01a11f9a-c8f0-7475-9d19-9f5cd9afd25c","recovery_policy_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","run_id":"run_01a11f9a-c8f0-746e-a8c7-3bf9c6e27e84","session_id":"ses_01a11f9a-c8f0-7497-adfe-3f2d514dcf77","task_id":"tsk_01a11f9a-c8f0-7468-8269-160443726790","thread_id":"thr_01a11f9a-c8f0-745e-a6bd-1c816412d607","trace_id":"trc_01a11f9a-c8f0-74a4-97bb-636532a21795","turn_id":"turn_01a11f9a-c8f0-749d-a398-01c601489f7e"}`
	return []byte(raw), nativeStartIdentity{runID: runID, taskID: taskID}
}

func validNativeRunResult(t *testing.T, start nativeStartIdentity, verification, revision string) ([]byte, string) {
	t.Helper()
	if verification == "passed" {
		const evidenceID = "evd_01a11f9a-c910-73ed-91f1-22e0a836893c"
		return []byte(`{"code":"FINAL_RESPONSE_ACCEPTED","evidence_ids":["` + evidenceID + `"],"final_message_id":null,"final_text":"done","last_checkpoint_id":null,"resumable":false,"run_id":"` + start.runID + `","status":"completed","task_id":"` + start.taskID + `","unresolved_action_ids":[],"verification":{"criteria_revision":"` + revision + `","evidence_ids":["` + evidenceID + `"],"status":"passed"}}`), evidenceID
	}
	return []byte(`{"code":"FINAL_RESPONSE_ACCEPTED","evidence_ids":[],"final_message_id":null,"final_text":"done","last_checkpoint_id":null,"resumable":false,"run_id":"` + start.runID + `","status":"completed","task_id":"` + start.taskID + `","unresolved_action_ids":[],"verification":{"criteria_revision":null,"evidence_ids":[],"status":"` + verification + `"}}`), ""
}

func nativeCriteriaProof(runResult []byte, revision, evidenceID string) *domainaction.NativeDelegationProof {
	hash := sha256.Sum256(runResult)
	return &domainaction.NativeDelegationProof{
		Version: domainaction.NativeDelegationProofVersion, ExpectedCriteriaRevision: revision,
		RunResultSHA256: hex.EncodeToString(hash[:]), TerminalEventID: "evt_terminal", TerminalEventSeq: 4,
		Evidence: []domainaction.NativeDelegationEvidenceProof{{EvidenceID: evidenceID, VerifierActionID: "act_verifier",
			VerifierAttemptID: "att_verifier", CompletionEventID: "evt_completed", RawHash: strings.Repeat("b", 64), TotalBytes: 0}},
	}
}

func TestRecoverActionRunsAfterRestartCancelsOpenStaleAction(t *testing.T) {
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTasks := taskmanager.New(firstStore, taskmanager.DefaultParallelLimits())
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	task, err := firstTasks.Create(context.Background(), domaintask.Task{
		Title: "interrupted action", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	action, _, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())

	recovered, err := recoverActionRunsAfterRestart(context.Background(), actions, secondTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	recoveredAction, err := actions.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredAction.Status != domainaction.StatusCancelled || recoveredTask.Status != domaintask.StatusCancelled {
		t.Fatalf("recovered Action=%#v Task=%#v", recoveredAction, recoveredTask)
	}
}
