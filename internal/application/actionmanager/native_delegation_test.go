package actionmanager_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEnsureNativeDelegationAcrossManagersCreatesOneActionAndAttempt(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "actions")
	firstStore, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	managers := []*actionmanager.Manager{actionmanager.New(firstStore), actionmanager.New(secondStore)}
	input := actionmanager.EnsureNativeDelegationInput{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: strings.Repeat("a", 64)}
	const callers = 16
	results := make(chan actionPair, callers)
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			action, attempt, ensureErr := managers[index%len(managers)].EnsureNativeDelegation(context.Background(), input)
			if ensureErr != nil {
				errorsByCaller <- ensureErr
				return
			}
			results <- actionPair{action: action, attempt: attempt}
		}(index)
	}
	wait.Wait()
	close(results)
	close(errorsByCaller)
	for ensureErr := range errorsByCaller {
		t.Errorf("EnsureNativeDelegation() error = %v", ensureErr)
	}
	var first actionPair
	count := 0
	for pair := range results {
		if count == 0 {
			first = pair
		} else if pair.action.ActionID != first.action.ActionID || pair.attempt.AttemptID != first.attempt.AttemptID {
			t.Errorf("EnsureNativeDelegation() returned multiple identities: %s/%s and %s/%s", first.action.ActionID, first.attempt.AttemptID, pair.action.ActionID, pair.attempt.AttemptID)
		}
		count++
	}
	if count != callers {
		t.Fatalf("successful ensures = %d, want %d", count, callers)
	}
	actions, err := firstStore.ListActions(context.Background(), domainaction.Filter{TaskID: input.TaskID, RunID: input.RunID})
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := firstStore.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: first.action.ActionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || len(attempts) != 1 || attempts[0].StartReason != domainaction.AttemptStartReasonFirst {
		t.Fatalf("native action/attempt counts = %d/%d, want one first attempt", len(actions), len(attempts))
	}
}

func TestNativeDelegationPersistsExactMutationsAndFailsClosedWithoutProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store, manager, pair := newNativeManagerPair(t, root, nil)
	openPayload := []byte("{ \"idempotency_key\":\"open-key\",\"workspace\":\"/workspace\" }")
	callerPayload := append([]byte(nil), openPayload...)
	_, preparedAttempt, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen, Key: "open-key", Payload: callerPayload,
	})
	if err != nil {
		t.Fatalf("prepare open: %v", err)
	}
	if string(preparedAttempt.NativeDelegation.Open.Payload) != string(openPayload) {
		t.Fatalf("open payload changed: %q", preparedAttempt.NativeDelegation.Open.Payload)
	}
	callerPayload[0] ^= 0x01
	preparedAttempt.NativeDelegation.Open.Payload[0] ^= 0x01
	if _, ensured, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after prepared open: %v", err)
	} else if ensured.AttemptID != pair.attempt.AttemptID || string(ensured.NativeDelegation.Open.Payload) != string(openPayload) {
		t.Fatal("prepare input or return mutation changed the persisted open payload or attempt identity")
	}
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart, Key: "start-key", Payload: []byte(`{"idempotency_key":"start-key"}`),
	}); err == nil {
		t.Fatal("start preparation without accepted open must fail")
	}
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen, Key: "open-key", Payload: []byte(`{"idempotency_key":"open-key","changed":true}`),
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("different open payload error = %v, want conflict", err)
	}
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen, Key: "different-key", Payload: openPayload,
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("different open key error = %v, want conflict", err)
	}
	// An identical prepare is a read of the existing immutable slot.
	_, preparedAttempt, err = manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen, Key: "open-key", Payload: openPayload,
	})
	if err != nil {
		t.Fatalf("reuse open prepare: %v", err)
	}
	if string(preparedAttempt.NativeDelegation.Open.Payload) != string(openPayload) {
		t.Fatalf("reused open payload changed: %q", preparedAttempt.NativeDelegation.Open.Payload)
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotOpen); err != nil {
		t.Fatalf("mark open delivery unknown: %v", err)
	}
	if _, ensured, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after unknown open: %v", err)
	} else if ensured.AttemptID != pair.attempt.AttemptID || ensured.NativeDelegation.Open.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatal("ensure after unknown open issued a fresh attempt or lost unknown state")
	}
	if _, _, err := manager.StartAttempt(ctx, pair.action.ActionID, domainaction.AttemptStartReasonRetry); !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("generic retry error = %v, want conflict", err)
	}
	if _, _, err := manager.CompleteAttempt(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "transport ended"); !errors.Is(err, actionmanager.ErrAttemptConflict) {
		t.Fatalf("generic completion during unknown delivery = %v, want conflict", err)
	}
	if _, observed, err := manager.RecordNativeObservation(ctx, actionmanager.RecordNativeObservationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID,
		Observation: domainaction.NativeObservation{Kind: "transport", Code: "response_lost"},
	}); err != nil {
		t.Fatalf("record bounded diagnostic observation: %v", err)
	} else if observed.NativeDelegation.LastObservation == nil || observed.NativeDelegation.LastObservation.Code != "response_lost" || observed.Status != domainaction.AttemptStatusRunning {
		t.Fatal("diagnostic observation changed immutable execution state or terminalized the unknown delivery")
	}
	if action, err := store.GetAction(ctx, pair.action.ActionID); err != nil {
		t.Fatal(err)
	} else if action.Status != domainaction.StatusOpen {
		t.Fatalf("unknown delivery closed action: %+v", action)
	}
	if attempt, err := store.GetAttempt(ctx, pair.attempt.AttemptID); err != nil {
		t.Fatal(err)
	} else if attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("unknown delivery closed attempt: %+v", attempt)
	}

	reopened, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen after unknown open: %v", err)
	}
	manager = actionmanager.New(reopened)
	pair.action, pair.attempt, err = manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID})
	if err != nil {
		t.Fatalf("ensure after unknown open: %v", err)
	}
	if pair.attempt.AttemptID == "" || string(pair.attempt.NativeDelegation.Open.Payload) != string(openPayload) || pair.attempt.NativeDelegation.Open.PayloadSHA256 != sha256Hex(openPayload) || pair.attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("reopened open mutation did not preserve exact unknown request: %+v", pair.attempt.NativeDelegation.Open)
	}
	openResult := []byte(`{"session":{"thread_id":"thread-1"}}`)
	if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Status: domainaction.NativeMutationStatusAccepted, Result: openResult,
	}); err != nil {
		t.Fatalf("record accepted open: %v", err)
	}
	openResult[0] ^= 0x01
	if _, ensured, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after accepted open: %v", err)
	} else if ensured.AttemptID != pair.attempt.AttemptID || string(ensured.NativeDelegation.Open.Result) != `{"session":{"thread_id":"thread-1"}}` {
		t.Fatal("accepted open result input mutation changed stored state or attempt identity")
	}
	startPayload := []byte(`{"idempotency_key":"start-key","thread_id":"thread-1"}`)
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart, Key: "start-key", Payload: startPayload,
	}); err != nil {
		t.Fatalf("prepare start: %v", err)
	}
	if _, ensured, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after prepared start: %v", err)
	} else if ensured.AttemptID != pair.attempt.AttemptID || ensured.NativeDelegation.Start == nil || ensured.NativeDelegation.Start.Status != domainaction.NativeMutationStatusPrepared {
		t.Fatal("ensure after prepared start issued a fresh attempt or lost start state")
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotStart); err != nil {
		t.Fatalf("mark start delivery unknown: %v", err)
	}
	if _, ensured, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after unknown start: %v", err)
	} else if ensured.AttemptID != pair.attempt.AttemptID || ensured.NativeDelegation.Start.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatal("ensure after unknown start issued a fresh attempt or lost unknown state")
	}
	if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart,
		Status: domainaction.NativeMutationStatusAccepted, Result: []byte(`{"run_id":"harness-run"}`),
	}); err != nil {
		t.Fatalf("record accepted start: %v", err)
	}
	runResult := []byte(`{"status":"completed","verification":{"status":"passed"}}`)
	if _, _, err := manager.CompleteNativeDelegation(ctx, actionmanager.CompleteNativeDelegationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, RunResult: runResult,
		AttemptStatus: domainaction.AttemptStatusSucceeded, ActionStatus: domainaction.StatusSucceeded, Summary: "run result recorded",
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("success without proof error = %v, want conflict", err)
	}
	completeAction, completeAttempt, err := manager.CompleteNativeDelegation(ctx, actionmanager.CompleteNativeDelegationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, RunResult: runResult,
		AttemptStatus: domainaction.AttemptStatusFailed, ActionStatus: domainaction.StatusFailed, Summary: "criteria proof unavailable",
	})
	if err != nil {
		t.Fatalf("complete native delegation: %v", err)
	}
	if completeAction.Status != domainaction.StatusFailed || completeAttempt.Status != domainaction.AttemptStatusFailed || string(completeAttempt.NativeDelegation.RunResult) != string(runResult) {
		t.Fatalf("completion did not atomically persist terminal state and run result: %+v %+v", completeAction, completeAttempt)
	}
	completeAttempt.NativeDelegation.RunResult[0] ^= 0x01
	loaded, err := reopened.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.NativeDelegation.RunResult) != string(runResult) || loaded.Status != domainaction.AttemptStatusFailed {
		t.Fatal("mutating a returned attempt changed persisted native state")
	}
	recovered, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("recover and reopen completed delegation: %v", err)
	}
	recoveredAction, err := recovered.GetAction(ctx, pair.action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredAttempt, err := recovered.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredAction.Status != domainaction.StatusFailed || recoveredAttempt.Status != domainaction.AttemptStatusFailed || recoveredAttempt.NativeDelegation.Proof != nil || recoveredAttempt.NativeDelegation.Open.PayloadSHA256 != sha256Hex(openPayload) ||
		string(recoveredAttempt.NativeDelegation.Open.Payload) != string(openPayload) || recoveredAttempt.NativeDelegation.Start.PayloadSHA256 != sha256Hex(startPayload) ||
		string(recoveredAttempt.NativeDelegation.Start.Payload) != string(startPayload) || string(recoveredAttempt.NativeDelegation.Open.Result) != `{"session":{"thread_id":"thread-1"}}` ||
		string(recoveredAttempt.NativeDelegation.Start.Result) != `{"run_id":"harness-run"}` || string(recoveredAttempt.NativeDelegation.RunResult) != string(runResult) {
		t.Fatal("WAL recovery and reopen did not preserve exact mutation bytes, hashes, results, and terminal state")
	}
	if _, _, err := manager.CompleteNativeDelegation(ctx, actionmanager.CompleteNativeDelegationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, RunResult: runResult,
		AttemptStatus: domainaction.AttemptStatusFailed, ActionStatus: domainaction.StatusFailed, Summary: "criteria proof unavailable",
	}); err != nil {
		t.Fatalf("identical terminal replay: %v", err)
	}
	if _, _, err := manager.CompleteNativeDelegation(ctx, actionmanager.CompleteNativeDelegationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, RunResult: []byte(`{"status":"failed"}`),
		AttemptStatus: domainaction.AttemptStatusFailed, ActionStatus: domainaction.StatusFailed, Summary: "changed",
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("changed terminal replay error = %v, want conflict", err)
	}
	if ensuredAction, ensuredAttempt, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatalf("ensure after terminal completion: %v", err)
	} else if ensuredAction.ActionID != pair.action.ActionID || ensuredAttempt.AttemptID != pair.attempt.AttemptID || ensuredAttempt.Status != domainaction.AttemptStatusFailed {
		t.Fatal("ensure after terminal completion issued a fresh attempt")
	}
}

func TestNativeDelegationSuccessPersistsCriteriaProofAtomically(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	revision := strings.Repeat("a", 64)
	_, manager, pair := newNativeManagerPair(t, root, nil, revision)
	prepareAccepted := func(slot domainaction.NativeMutationSlot, key string, payload, result []byte) {
		t.Helper()
		if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
			ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: slot, Key: key, Payload: payload,
		}); err != nil {
			t.Fatalf("prepare %s: %v", slot, err)
		}
		if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, slot); err != nil {
			t.Fatalf("mark %s delivery unknown: %v", slot, err)
		}
		if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
			ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: slot,
			Status: domainaction.NativeMutationStatusAccepted, Result: result,
		}); err != nil {
			t.Fatalf("record accepted %s: %v", slot, err)
		}
	}
	prepareAccepted(domainaction.NativeMutationSlotOpen, "open-key", []byte(`{"open":true}`), []byte(`{"thread_id":"thr_1"}`))
	prepareAccepted(domainaction.NativeMutationSlotStart, "start-key", []byte(`{"start":true}`), []byte(`{"run_id":"run_1"}`))
	runResult := []byte(`{"run_result":"canonical"}`)
	hash := sha256.Sum256(runResult)
	proof := &domainaction.NativeDelegationProof{
		Version: domainaction.NativeDelegationProofVersion, ExpectedCriteriaRevision: revision,
		RunResultSHA256: fmt.Sprintf("%x", hash[:]), TerminalEventID: "evt_terminal", TerminalEventSeq: 4,
		Evidence: []domainaction.NativeDelegationEvidenceProof{{EvidenceID: "evd_verifier", VerifierActionID: "act_verifier",
			VerifierAttemptID: "att_verifier", CompletionEventID: "evt_completed", RawHash: strings.Repeat("b", 64), TotalBytes: 0}},
	}
	action, attempt, err := manager.CompleteNativeDelegation(ctx, actionmanager.CompleteNativeDelegationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, RunResult: runResult, Proof: proof,
		AttemptStatus: domainaction.AttemptStatusSucceeded, ActionStatus: domainaction.StatusSucceeded, Summary: "criteria proof recorded",
	})
	if err != nil {
		t.Fatalf("complete with criteria proof: %v", err)
	}
	if action.Status != domainaction.StatusSucceeded || attempt.Status != domainaction.AttemptStatusSucceeded || attempt.NativeDelegation.Proof == nil ||
		attempt.NativeDelegation.Proof.RunResultSHA256 != fmt.Sprintf("%x", hash[:]) {
		t.Fatalf("success did not atomically persist its proof: action=%+v attempt=%+v", action, attempt)
	}
	reopened, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil || loaded.NativeDelegation.Proof == nil || loaded.NativeDelegation.Proof.TerminalEventID != "evt_terminal" {
		t.Fatalf("reopened proof = %+v err=%v", loaded.NativeDelegation, err)
	}
}

func TestEnsureNativeDelegationRejectsReferenceMismatchAndCopiesInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store, manager, pair := newNativeManagerPair(t, root, validReference("owner-a", "request-a"))
	pair.attempt.NativeDelegation.InputReference.OwnerID = "caller-mutated"
	loaded, err := store.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NativeDelegation.InputReference.OwnerID != "owner-a" {
		t.Fatal("mutating EnsureNativeDelegation return changed the stored reference")
	}
	if _, _, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{
		TaskID: pair.action.TaskID, RunID: pair.action.RunID, InputReference: validReference("owner-b", "request-b"),
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("reference mismatch error = %v, want conflict", err)
	}
	if _, _, err := manager.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: pair.action.TaskID, RunID: pair.action.RunID, Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName,
	}); err == nil {
		t.Fatal("a second native delegation for one Task/Run must fail")
	}
	if err := store.Transaction(ctx, func(tx actionmanager.Store) error {
		action, err := tx.GetAction(ctx, pair.action.ActionID)
		if err != nil {
			return err
		}
		action.TaskID = modulecore.NewTaskID()
		action.RunID = modulecore.NewRunID()
		return tx.SaveAction(ctx, action)
	}); err == nil {
		t.Fatal("generic SaveAction moved the native action away from its uniqueness identity")
	}
}

func TestEnsureNativeDelegationRequiresAValidCriteriaRevisionForNewAction(t *testing.T) {
	for _, revision := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		t.Run(revision, func(t *testing.T) {
			store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
			if err != nil {
				t.Fatal(err)
			}
			manager := actionmanager.New(store)
			_, _, err = manager.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
				TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: revision,
			})
			if !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
				t.Fatalf("new Action without one lowercase SHA256 criteria pin error=%v", err)
			}
			actions, listErr := store.ListActions(context.Background(), domainaction.Filter{})
			if listErr != nil || len(actions) != 0 {
				t.Fatalf("invalid criteria pin must not create an Action: actions=%+v err=%v", actions, listErr)
			}
		})
	}
}

func TestNativeMutationRecordLimitFailsBeforePersisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, manager, pair := newNativeManagerPair(t, filepath.Join(t.TempDir(), "actions"), nil)
	payload := bytes.Repeat([]byte{'x'}, (jsonlbatch.MaxJSONLRecordBytes*3/4)+1)
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Key: "oversized", Payload: payload,
	}); err == nil {
		t.Fatal("oversized native mutation record unexpectedly committed")
	}
	loaded, err := store.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NativeDelegation.Open != nil || loaded.Status != domainaction.AttemptStatusRunning {
		t.Fatal("oversized record failure left a partial mutation or changed attempt state")
	}
}

func TestNativeDefiniteOpenRejectionFailsAtomicallyWithoutRunResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	_, manager, pair := newNativeManagerPair(t, root, nil)
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Key: "open-key", Payload: []byte(`{"open":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotOpen); err != nil {
		t.Fatal(err)
	}
	rejectedAction, rejectedAttempt, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Status: domainaction.NativeMutationStatusRejected, ErrorCode: "workspace_denied",
	})
	if err != nil {
		t.Fatalf("record definite Open rejection: %v", err)
	}
	if rejectedAction.Status != domainaction.StatusFailed || rejectedAttempt.Status != domainaction.AttemptStatusFailed ||
		rejectedAttempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusRejected ||
		rejectedAttempt.NativeDelegation.Open.ErrorCode != "workspace_denied" || len(rejectedAttempt.NativeDelegation.RunResult) != 0 {
		t.Fatalf("rejection was not atomically terminalized without a fabricated RunResult: %+v %+v", rejectedAction, rejectedAttempt)
	}
	reopened, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen definite rejection: %v", err)
	}
	manager = actionmanager.New(reopened)
	if recovered, err := reopened.GetAttempt(ctx, pair.attempt.AttemptID); err != nil {
		t.Fatal(err)
	} else if recovered.Status != domainaction.AttemptStatusFailed || recovered.NativeDelegation.Open.Status != domainaction.NativeMutationStatusRejected || len(recovered.NativeDelegation.RunResult) != 0 {
		t.Fatal("reopen lost or changed the terminal rejection state")
	}
	if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Status: domainaction.NativeMutationStatusRejected, ErrorCode: "workspace_denied",
	}); err != nil {
		t.Fatalf("identical definite rejection replay: %v", err)
	}
	if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Status: domainaction.NativeMutationStatusRejected, ErrorCode: "different_reason",
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("changed definite rejection replay error = %v, want conflict", err)
	}
	if action, attempt, err := manager.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{TaskID: pair.action.TaskID, RunID: pair.action.RunID}); err != nil {
		t.Fatal(err)
	} else if action.ActionID != pair.action.ActionID || attempt.AttemptID != pair.attempt.AttemptID || attempt.Status != domainaction.AttemptStatusFailed {
		t.Fatal("Ensure after definite rejection issued a fresh Attempt")
	}
}

func TestNativeDefiniteStartRejectionRequiresAcceptedOpenAndFailsAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, manager, pair := newNativeManagerPair(t, filepath.Join(t.TempDir(), "actions"), nil)
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart,
		Key: "start-key", Payload: []byte(`{"start":true}`),
	}); !errors.Is(err, actionmanager.ErrNativeDelegationConflict) {
		t.Fatalf("Start before Open accepted error = %v, want conflict", err)
	}
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Key: "open-key", Payload: []byte(`{"open":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotOpen); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Status: domainaction.NativeMutationStatusAccepted, Result: []byte(`{"thread_id":"thread-a"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart,
		Key: "start-key", Payload: []byte(`{"start":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotStart); err != nil {
		t.Fatal(err)
	}
	action, attempt, err := manager.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotStart,
		Status: domainaction.NativeMutationStatusRejected, ErrorCode: "invalid_turn",
	})
	if err != nil {
		t.Fatalf("record definite Start rejection: %v", err)
	}
	if action.Status != domainaction.StatusFailed || attempt.Status != domainaction.AttemptStatusFailed ||
		attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusAccepted ||
		attempt.NativeDelegation.Start.Status != domainaction.NativeMutationStatusRejected || attempt.NativeDelegation.Start.ErrorCode != "invalid_turn" ||
		len(attempt.NativeDelegation.RunResult) != 0 {
		t.Fatalf("Start rejection did not preserve accepted Open and terminal failure without RunResult: %+v %+v", action, attempt)
	}
}

func TestNativeTransportErrorAndInterruptObservationRemainUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, manager, pair := newNativeManagerPair(t, filepath.Join(t.TempDir(), "actions"), nil)
	if _, _, err := manager.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID, Slot: domainaction.NativeMutationSlotOpen,
		Key: "open-key", Payload: []byte(`{"open":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.MarkNativeMutationDeliveryUnknown(ctx, pair.action.ActionID, pair.attempt.AttemptID, domainaction.NativeMutationSlotOpen); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"transport_error", "interrupted"} {
		if _, _, err := manager.RecordNativeObservation(ctx, actionmanager.RecordNativeObservationInput{
			ActionID: pair.action.ActionID, AttemptID: pair.attempt.AttemptID,
			Observation: domainaction.NativeObservation{Kind: "transport", Code: code},
		}); err != nil {
			t.Fatal(err)
		}
	}
	action, err := store.GetAction(ctx, pair.action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetAttempt(ctx, pair.attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domainaction.StatusOpen || attempt.Status != domainaction.AttemptStatusRunning ||
		attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusDeliveryUnknown || len(attempt.NativeDelegation.RunResult) != 0 {
		t.Fatal("transport error or interruption was treated as a definite rejection or terminal outcome")
	}
}

func newNativeManagerPair(t *testing.T, root string, reference *conversation.AcceptedOPSInputReference, expectedCriteriaRevision ...string) (*actionstore.JSONLStore, *actionmanager.Manager, actionPair) {
	t.Helper()
	store, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := actionmanager.New(store)
	revision := strings.Repeat("a", 64)
	if len(expectedCriteriaRevision) > 0 {
		revision = expectedCriteriaRevision[0]
	}
	action, attempt, err := manager.EnsureNativeDelegation(context.Background(), actionmanager.EnsureNativeDelegationInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ExpectedCriteriaRevision: revision, InputReference: reference,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, manager, actionPair{action: action, attempt: attempt}
}

type actionPair struct {
	action  domainaction.Action
	attempt domainaction.Attempt
}

func validReference(owner, request string) *conversation.AcceptedOPSInputReference {
	return &conversation.AcceptedOPSInputReference{OwnerID: owner, RequestID: request, PayloadSHA256: sha256Hex([]byte("accepted input"))}
}

func sha256Hex(value []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(value))
}
