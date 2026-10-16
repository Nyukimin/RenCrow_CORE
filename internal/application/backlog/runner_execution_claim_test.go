package backlog

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runnerExecutionClaimAPI interface {
	BeginRunnerExecution(context.Context, domainbacklog.Item) (string, error)
	EndRunnerExecution(context.Context, string) error
}

func runnerClaimAPI(t *testing.T, service *Service) runnerExecutionClaimAPI {
	t.Helper()
	api, ok := any(service).(runnerExecutionClaimAPI)
	if !ok {
		t.Fatal("backlog Service does not expose runner execution claims")
	}
	return api
}

func beginRunnerClaim(t *testing.T, service *Service, selected domainbacklog.Item) string {
	t.Helper()
	token, err := runnerClaimAPI(t, service).BeginRunnerExecution(backlogActionContext(t), selected)
	if err != nil {
		t.Fatalf("BeginRunnerExecution: %v", err)
	}
	if token == "" {
		t.Fatal("BeginRunnerExecution returned an empty claim token")
	}
	return token
}

func TestRunnerExecutionClaimCancelFirstRejectsStaleSelection(t *testing.T) {
	store := &blockedRejectedStageStore{prepared: make(chan struct{}), continueSave: make(chan struct{})}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	fixture.service.workstream = store
	selected := fixture.item
	released := false
	releaseCancellation := func() {
		if !released {
			close(store.continueSave)
			released = true
		}
	}
	defer releaseCancellation()

	reviseDone := make(chan error, 1)
	reviseCtx := backlogActionContext(t)
	go func() {
		_, err := fixture.service.Revise(reviseCtx, string(selected.BacklogItemID), rejectedCancellationRequest())
		reviseDone <- err
	}()
	select {
	case <-store.prepared:
	case <-time.After(3 * time.Second):
		t.Fatal("REJECTED Revise did not reach its prepared-stage barrier")
	}
	if fixture.service.lifecycleMu.TryLock() {
		fixture.service.lifecycleMu.Unlock()
		t.Fatal("REJECTED Revise did not hold lifecycleMu at the prepared-stage barrier")
	}

	beginStarted := make(chan struct{})
	beginDone := make(chan error, 1)
	beginCtx := backlogActionContext(t)
	api := runnerClaimAPI(t, fixture.service)
	go func() {
		close(beginStarted)
		_, err := api.BeginRunnerExecution(beginCtx, selected)
		beginDone <- err
	}()
	select {
	case <-beginStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("stale Begin goroutine did not start")
	}
	select {
	case err := <-beginDone:
		t.Fatalf("Begin crossed the prepared cancellation barrier before release: %v", err)
	default:
	}

	releaseCancellation()
	select {
	case err := <-reviseDone:
		if err != nil {
			t.Fatalf("cancel-first Revise: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("REJECTED Revise did not finish after releasing its barrier")
	}
	if got := fixture.items.items[0].DeliveryState; got != domainbacklog.DeliveryRejected {
		t.Fatalf("cancel-first state=%s, want REJECTED", got)
	}
	select {
	case err := <-beginDone:
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("BeginRunnerExecution after cancellation error=%v, want ErrLifecycleConflict", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BeginRunnerExecution did not finish after cancellation")
	}
	if got := fixture.items.items[0].DeliveryState; got != domainbacklog.DeliveryRejected {
		t.Fatalf("cancel-first state changed after stale Begin: %s", got)
	}
}

func TestRunnerExecutionClaimGuardsQueueFreezeReplacementAdmission(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	token := beginRunnerClaim(t, fixture.service, fixture.item)
	_, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), ReviseRequest{
		ExpectedRevision: fixture.item.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "blocked-while-claimed"}},
		Reason:       "worker stage failure",
	})
	if err != nil {
		t.Fatalf("worker BLOCKED revision while claim active: %v", err)
	}
	freeze := fixture.workstream.queueFreezes[len(fixture.workstream.queueFreezes)-1]
	replacement := fixture.items.items[1]
	replacement.SupersedesUnitID = freeze.BlockedUnitID
	fixture.items.items[1] = replacement

	itemsBefore := append([]domainbacklog.Item(nil), fixture.items.items...)
	var leaseBefore *domainworkstream.ImplementationLease
	if fixture.workstream.lease != nil {
		leaseCopy := *fixture.workstream.lease
		leaseBefore = &leaseCopy
	}
	freezesBefore := append([]domainworkstream.QueueFreeze(nil), fixture.workstream.queueFreezes...)
	stagesBefore := append([]domainworkstream.StageRunReceipt(nil), fixture.workstream.stageReceipts...)
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	saveCountBefore := fixture.items.saveCount
	_, _, _, err = fixture.service.ResolveQueueFreeze(backlogActionContext(t), freeze.FreezeID, ResolveQueueFreezeRequest{
		ActionID: modulecore.NewActionID(), ExpectedFreezeRevision: freeze.FreezeRevision,
		ReplacementUnitID: replacement.ImplementationUnit, SupersedesUnitID: freeze.BlockedUnitID,
		BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "fix", Ref: "replacement-evidence"}},
	})
	if !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("replacement admission during runner claim error=%v, want ErrLifecycleConflict", err)
	}
	if !reflect.DeepEqual(fixture.items.items, itemsBefore) || fixture.items.saveCount != saveCountBefore ||
		!reflect.DeepEqual(fixture.workstream.lease, leaseBefore) || !reflect.DeepEqual(fixture.workstream.queueFreezes, freezesBefore) ||
		!reflect.DeepEqual(fixture.workstream.stageReceipts, stagesBefore) || !reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) ||
		!reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) || !reflect.DeepEqual(fixture.workstream.goals, goalsBefore) ||
		!reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) {
		t.Fatal("replacement admission during runner claim changed freeze, lease, or lifecycle state")
	}
	if err := runnerClaimAPI(t, fixture.service).EndRunnerExecution(backlogActionContext(t), token); err != nil {
		t.Fatalf("EndRunnerExecution: %v", err)
	}
}

func TestRunnerExecutionClaimClaimFirstRejectsCancellationBeforeWritesAndAllowsWorkerRevision(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	token := beginRunnerClaim(t, fixture.service, fixture.item)
	itemsBefore := append([]domainbacklog.Item(nil), fixture.items.items...)
	leaseBefore := *fixture.workstream.lease
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	stagesBefore := append([]domainworkstream.StageRunReceipt(nil), fixture.workstream.stageReceipts...)
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	freezesBefore := append([]domainworkstream.QueueFreeze(nil), fixture.workstream.queueFreezes...)
	saveCountBefore := fixture.items.saveCount
	ctx := backlogActionContext(t)

	cancelled := make(chan error, 1)
	go func() {
		_, err := fixture.service.Revise(ctx, string(fixture.item.BacklogItemID), rejectedCancellationRequest())
		cancelled <- err
	}()
	select {
	case err := <-cancelled:
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("claim-first REJECTED Revise error=%v, want immediate ErrLifecycleConflict", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim-first REJECTED Revise waited for claim release")
	}
	if !reflect.DeepEqual(fixture.items.items, itemsBefore) || fixture.items.saveCount != saveCountBefore ||
		!reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) || !reflect.DeepEqual(fixture.workstream.goals, goalsBefore) ||
		!reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) || !reflect.DeepEqual(fixture.workstream.stageReceipts, stagesBefore) ||
		!reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) || !reflect.DeepEqual(fixture.workstream.queueFreezes, freezesBefore) ||
		fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Fatal("claim-first REJECTED Revise wrote cancellation or lease state before rejecting")
	}

	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), ReviseRequest{
		ExpectedRevision: fixture.item.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "blocked-while-claimed"}},
		Reason:       "worker stage failure",
	}); err != nil {
		t.Fatalf("non-REJECTED worker Revise while claim active: %v", err)
	}
	if got := fixture.items.items[0].DeliveryState; got != domainbacklog.DeliveryBlocked {
		t.Fatalf("non-REJECTED worker Revise state=%s, want BLOCKED", got)
	}
	if err := runnerClaimAPI(t, fixture.service).EndRunnerExecution(backlogActionContext(t), token); err != nil {
		t.Fatalf("EndRunnerExecution: %v", err)
	}
}

func TestRunnerExecutionClaimDuplicateAndStaleEndCannotClearNewerClaim(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	api := runnerClaimAPI(t, fixture.service)
	first, err := api.BeginRunnerExecution(backlogActionContext(t), fixture.item)
	if err != nil || first == "" {
		t.Fatalf("first BeginRunnerExecution token=%q err=%v", first, err)
	}
	if _, err := api.BeginRunnerExecution(backlogActionContext(t), fixture.item); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("duplicate BeginRunnerExecution error=%v, want ErrLifecycleConflict", err)
	}
	if err := api.EndRunnerExecution(backlogActionContext(t), first); err != nil {
		t.Fatalf("first EndRunnerExecution: %v", err)
	}
	second, err := api.BeginRunnerExecution(backlogActionContext(t), fixture.item)
	if err != nil || second == "" || second == first {
		t.Fatalf("second BeginRunnerExecution token=%q first=%q err=%v", second, first, err)
	}
	if err := api.EndRunnerExecution(backlogActionContext(t), first); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("stale EndRunnerExecution error=%v, want ErrLifecycleConflict", err)
	}
	if _, err := api.BeginRunnerExecution(backlogActionContext(t), fixture.item); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("stale EndRunnerExecution cleared the newer claim: Begin error=%v", err)
	}
	if err := api.EndRunnerExecution(backlogActionContext(t), second); err != nil {
		t.Fatalf("second EndRunnerExecution: %v", err)
	}
}

func TestRunnerExecutionClaimNonceOverflowFailsClosed(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.service.runnerExecutionNonce = ^uint64(0)

	token, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), fixture.item)
	if !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("BeginRunnerExecution at nonce overflow error=%v, want ErrLifecycleConflict", err)
	}
	if token != "" {
		t.Fatalf("BeginRunnerExecution at nonce overflow token=%q, want empty", token)
	}
	if fixture.service.runnerExecution != nil {
		t.Fatalf("BeginRunnerExecution at nonce overflow created claim: %+v", fixture.service.runnerExecution)
	}
	if fixture.service.runnerExecutionNonce != ^uint64(0) {
		t.Fatalf("BeginRunnerExecution at nonce overflow wrapped nonce to %d", fixture.service.runnerExecutionNonce)
	}
}

func TestRunnerExecutionClaimGuardsAdmissionAndRecoverCleanup(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	token := beginRunnerClaim(t, fixture.service, fixture.item)
	if _, err := fixture.service.AcquireRunnable(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("AcquireRunnable during runner execution error=%v, want ErrLifecycleConflict", err)
	}

	candidate := fixture.items.items[1]
	candidate.ConceptState = domainbacklog.ConceptCandidate
	candidate.DeliveryState = domainbacklog.DeliveryNone
	candidate.MaturationState = domainbacklog.MaturationStatePromoted
	fixture.items.items[1] = candidate
	itemsBefore := append([]domainbacklog.Item(nil), fixture.items.items...)
	leaseBefore := *fixture.workstream.lease
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	if _, err := fixture.service.Adopt(backlogActionContext(t), string(candidate.BacklogItemID), "adopt while runner claim active"); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Adopt during runner execution error=%v, want ErrLifecycleConflict", err)
	}
	if !reflect.DeepEqual(fixture.items.items, itemsBefore) || !reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) ||
		!reflect.DeepEqual(fixture.workstream.goals, goalsBefore) || !reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) ||
		fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Fatal("Adopt during runner execution changed queue or Workstream state")
	}

	// Model a worker that has durably reached DONE and released its lease while
	// Execute is still returning. Recover must not perform terminal cleanup yet.
	completed := fixture.items.items[0]
	completed.DeliveryState = domainbacklog.DeliveryDone
	fixture.items.items[0] = completed
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover while runner claim active error=%v, want ErrLifecycleConflict", err)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Fatalf("Recover cleaned up the active runner's terminal lease: %+v", fixture.workstream.lease)
	}
	if err := runnerClaimAPI(t, fixture.service).EndRunnerExecution(backlogActionContext(t), token); err != nil {
		t.Fatalf("EndRunnerExecution: %v", err)
	}
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover after claim release: %v", err)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("Recover after claim release did not clean terminal lease: %+v", fixture.workstream.lease)
	}
}

func TestBeginRunnerExecutionRejectsStaleSelectionLeaseFreezeAndPendingClosure(t *testing.T) {
	t.Run("stale item", func(t *testing.T) {
		fixture := newRejectedCancellationFixture(nil)
		selected := fixture.item
		fixture.items.items[0].Title += " changed after selection"
		if _, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), selected); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("stale selection error=%v, want ErrLifecycleConflict", err)
		}
	})
	t.Run("missing lease", func(t *testing.T) {
		fixture := newRejectedCancellationFixture(nil)
		fixture.workstream.lease = nil
		if _, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), fixture.item); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("missing lease error=%v, want ErrLifecycleConflict", err)
		}
	})
	t.Run("mismatched lease", func(t *testing.T) {
		fixture := newRejectedCancellationFixture(nil)
		fixture.workstream.lease.HolderWorkstreamID = "other-workstream"
		if _, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), fixture.item); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("mismatched lease error=%v, want ErrLifecycleConflict", err)
		}
	})
	t.Run("active freeze", func(t *testing.T) {
		fixture := newRejectedCancellationFixture(nil)
		fixture.workstream.queueFreezes = append(fixture.workstream.queueFreezes, domainworkstream.QueueFreeze{
			FreezeID: "test-active-freeze", Status: domainworkstream.QueueFreezeActive,
		})
		if _, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), fixture.item); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("active freeze error=%v, want ErrLifecycleConflict", err)
		}
	})
	t.Run("pending rejected closure", func(t *testing.T) {
		fixture, _, selected, _ := makeLegacyPreparedRejectedStage(t, false, 0)
		if _, err := runnerClaimAPI(t, fixture.service).BeginRunnerExecution(backlogActionContext(t), selected); !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("pending cancellation closure error=%v, want ErrLifecycleConflict", err)
		}
	})
}

func TestRunnerExecutionClaimRemainsActiveAfterDoneAndLeaseRelease(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	selected := fixture.items.items[0]
	selected.DeliveryState = domainbacklog.DeliveryPostDeployVerify
	selected.EvidenceRefs = revision2CumulativeEvidence()
	fixture.items.items[0] = selected
	fixture.item = selected
	fixture.workstream.lease.Stage = domainbacklog.DeliveryPostDeployVerify
	fixture.service.WithEvidenceVerifier(revision2Verifier{})
	token := beginRunnerClaim(t, fixture.service, selected)

	completed, err := fixture.service.Revise(backlogActionContext(t), string(selected.BacklogItemID), ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryLiveVerified,
		EvidenceRefs:        revision2CumulativeEvidence(),
	})
	if err != nil || completed.DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("worker terminal transition result=%+v err=%v, want durable DONE", completed, err)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("DONE closure did not release the lease before Execute returned: %+v", fixture.workstream.lease)
	}
	if _, err := fixture.service.AcquireRunnable(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("new admission after durable DONE but before End error=%v, want ErrLifecycleConflict", err)
	}
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover after durable DONE but before End error=%v, want ErrLifecycleConflict", err)
	}
	if err := runnerClaimAPI(t, fixture.service).EndRunnerExecution(backlogActionContext(t), token); err != nil {
		t.Fatalf("EndRunnerExecution after DONE: %v", err)
	}
}
