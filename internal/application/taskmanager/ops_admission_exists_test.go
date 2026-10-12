package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// AdmitAcceptedOPS decides inside its own Task transaction whether the Task
// exists: an existing Task is a replay (checked against the accepted receipt),
// a missing one is created. That first read is the authority, so a creation
// that the Task owner then refuses with ErrTaskExists means the two reads of
// one transaction disagreed. It must fail closed, not be taken for a replay and
// not overwrite the stored Task.

// blindFirstGetStore answers the first GetTask of one Task transaction with
// "not found" whatever is stored, as an index that missed the Task would.
type blindFirstGetStore struct {
	domaintask.Store
	blind *bool
}

func (s *blindFirstGetStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(tx domaintask.Store) error {
		return fn(&blindFirstGetStore{Store: tx, blind: s.blind})
	})
}

func (s *blindFirstGetStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if *s.blind {
		*s.blind = false
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	return s.Store.GetTask(ctx, taskID)
}

func TestAdmitAcceptedOPSFailsClosedWhenItsOwnReadsDisagreeAboutTheTask(t *testing.T) {
	clean, store := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := clean.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed {
		t.Fatalf("setup claim = %+v err=%v", claimed, err)
	}
	beforeTask, err := clean.Get(ctx, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := clean.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	blind := true
	manager := New(&blindFirstGetStore{Store: store, blind: &blind}, DefaultParallelLimits())
	result, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("AdmitAcceptedOPS = %+v err=%v, want errors.Is(ErrTaskExists)", result, err)
	}
	if result.MayExecute {
		t.Fatal("a disagreement about whether the Task exists granted the right to execute")
	}
	afterTask, err := clean.Get(ctx, receipt.TaskID)
	if err != nil || !reflect.DeepEqual(beforeTask, afterTask) {
		t.Fatalf("the refused claim changed the stored Task:\nbefore=%+v\nafter=%+v err=%v", beforeTask, afterTask, err)
	}
	afterRuns, err := clean.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || !reflect.DeepEqual(beforeRuns, afterRuns) {
		t.Fatalf("the refused claim changed the Runs:\nbefore=%+v\nafter=%+v err=%v", beforeRuns, afterRuns, err)
	}
}

// The ordinary replay is the idempotent success: the same receipt finds its
// Task, matches it, and gets the original Run back without a second creation.
// A receipt that reuses the TaskID for a different request is refused.
func TestAdmitAcceptedOPSReplayIsIdempotentAndAMismatchIsRefused(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	first, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || first.Status != AcceptedOPSClaimed {
		t.Fatalf("first claim = %+v err=%v", first, err)
	}

	replay, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replay.Status != AcceptedOPSAlreadyRunning || replay.MayExecute || replay.Run.RunID != first.Run.RunID {
		t.Fatalf("replay = %+v err=%v, want the original Run without the right to execute", replay, err)
	}

	other := receipt
	other.UserMessageID = modulecore.NewMessageID()
	otherCtx := acceptedOPSUserContext(t, other.RequestID, other.OwnerID)
	if _, err := manager.AdmitAcceptedOPS(otherCtx, other, domainconversation.BackendShiroNativeCodingV1); !errors.Is(err, ErrAcceptedOPSClaimRejected) {
		t.Fatalf("a different request reusing the TaskID: err=%v, want ErrAcceptedOPSClaimRejected", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after replay and mismatch = %v err=%v, want exactly the first Run", runs, err)
	}
}
