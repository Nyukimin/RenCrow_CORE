package backlog

import (
	"context"
	"errors"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestRevision2StageReplayIsIdempotent(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "revision2-replay",
		ImplementationUnit: "unit-revision2-replay",
		Title:              "stage replay",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryQueued,
	}}}
	service := NewService(store, &memoryWorkstreamStore{}).WithEvidenceVerifier(revision2Verifier{})
	request := ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage:  domainbacklog.DeliverySpec,
			Kind:   "spec",
			Ref:    "receipt/spec/replay",
			Passed: true,
		}},
	}

	first, err := service.Revise(context.Background(), "revision2-replay", request)
	if err != nil {
		t.Fatalf("first stage execution: %v", err)
	}
	second, err := service.Revise(context.Background(), "revision2-replay", request)
	if err != nil {
		t.Fatalf("same unit+revision+target replay must return the original receipt, got %v", err)
	}
	if second.DeliveryState != first.DeliveryState || len(store.items) != 1 {
		t.Fatalf("replay changed lifecycle state: first=%+v second=%+v items=%d", first, second, len(store.items))
	}
	ws := service.workstream.(*memoryWorkstreamStore)
	receipt, found, err := ws.FindStageRunReceipt(context.Background(), "unit-revision2-replay:1:SPEC")
	if err != nil || !found || receipt.Status != domainworkstream.StageRunCompleted || receipt.ResultJSON == "" {
		t.Fatalf("completed stage receipt=%+v found=%v err=%v", receipt, found, err)
	}
	// A later stage must not change the result returned by replaying the
	// original idempotency key.
	store.items[0].DeliveryState = domainbacklog.DeliveryTDDRed
	replayed, err := service.Revise(context.Background(), "revision2-replay", request)
	if err != nil || replayed.DeliveryState != domainbacklog.DeliverySpec {
		t.Fatalf("replay must return original stage result, item=%+v err=%v", replayed, err)
	}
}

func TestRevision2StageHashIgnoresCallerVerificationMetadata(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "revision2-hash-claims", ImplementationUnit: "unit-hash-claims",
		Title: "hash claims", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	workstream := &memoryWorkstreamStore{}
	verifier := &captureEvidenceVerifier{ok: true}
	service := NewService(store, workstream).WithEvidenceVerifier(verifier)

	firstRequest := ReviseRequest{
		RequestID: "hash-claims-first", TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliverySpec, Kind: "spec", Ref: "receipt/spec/hash-claims", Passed: true,
			Verified: true, VerificationResult: domainbacklog.EvidenceVerificationVerified,
			VerifiedAt: "forged-at", Verifier: "caller",
		}},
	}
	first, err := service.Revise(context.Background(), "revision2-hash-claims", firstRequest)
	if err != nil {
		t.Fatalf("first stage execution: %v", err)
	}
	if len(first.EvidenceRefs) != 1 || !first.EvidenceRefs[0].Verified || first.EvidenceRefs[0].Verifier != "" || first.EvidenceRefs[0].VerificationResult != domainbacklog.EvidenceVerificationVerified {
		t.Fatalf("persisted result was not produced by owner verifier: %+v", first.EvidenceRefs)
	}
	if len(verifier.requests) != 1 || verifier.requests[0].Ref.Verified || verifier.requests[0].Ref.VerificationResult != "" || verifier.requests[0].Ref.VerifiedAt != "" || verifier.requests[0].Ref.Verifier != "" {
		t.Fatalf("verifier received caller verification metadata: requests=%+v", verifier.requests)
	}

	replayRequest := ReviseRequest{
		RequestID: "hash-claims-replay", TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliverySpec, Kind: "spec", Ref: "receipt/spec/hash-claims", Passed: true,
		}},
	}
	second, err := service.Revise(context.Background(), "revision2-hash-claims", replayRequest)
	if err != nil {
		t.Fatalf("replay without caller verification metadata must be idempotent: %v", err)
	}
	if second.DeliveryState != first.DeliveryState || len(verifier.requests) != 1 || len(workstream.stageReceipts) != 1 {
		t.Fatalf("replay changed result or re-ran verification: first=%+v second=%+v verifier_calls=%d receipts=%d", first, second, len(verifier.requests), len(workstream.stageReceipts))
	}
}

func TestRevision2EvidenceVerificationUsesAuthoritativeContextAndClearsClaims(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "context-item", ImplementationUnit: "unit-authoritative",
		Title: "context", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	verifier := &captureEvidenceVerifier{ok: true}
	service := NewService(store, &memoryWorkstreamStore{}).WithEvidenceVerifier(verifier)
	result, err := service.Revise(context.Background(), "context-item", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Kind: "spec", Ref: "spec-context", Passed: true, Verified: true,
			VerificationResult: domainbacklog.EvidenceVerificationVerified, VerifiedAt: "forged", Verifier: "request",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("verifier calls=%d want=1", len(verifier.requests))
	}
	request := verifier.requests[0]
	if request.ItemID != "context-item" || request.ImplementationUnitID != "unit-authoritative" || request.ImplementationRevision != 1 || request.TargetDeliveryState != domainbacklog.DeliverySpec || request.Purpose != "delivery_stage" {
		t.Fatalf("authoritative evidence context=%+v", request)
	}
	if request.Ref.Stage != domainbacklog.DeliverySpec || request.Ref.Verified || request.Ref.VerificationResult != "" || request.Ref.VerifiedAt != "" || request.Ref.Verifier != "" {
		t.Fatalf("request-side verification claim was not cleared: %+v", request.Ref)
	}
	if len(result.EvidenceRefs) != 1 || !result.EvidenceRefs[0].Verified || result.EvidenceRefs[0].Stage != domainbacklog.DeliverySpec {
		t.Fatalf("owner verification result=%+v", result.EvidenceRefs)
	}
}

func TestRevision2EvidenceStageMismatchFailsBeforeVerifier(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "stage-bound-item", ImplementationUnit: "unit-stage-bound",
		Title: "stage bound", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	verifier := &captureEvidenceVerifier{ok: true}
	service := NewService(store, &memoryWorkstreamStore{}).WithEvidenceVerifier(verifier)
	_, err := service.Revise(context.Background(), "stage-bound-item", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryTDDRed, Kind: "spec", Ref: "wrong-stage"}},
	})
	if err == nil || len(verifier.requests) != 0 {
		t.Fatalf("mismatched stage must reject before verifier: err=%v calls=%d", err, len(verifier.requests))
	}
}

func TestRevision2FreezeEvidenceUsesBlockedUnitRevisionContext(t *testing.T) {
	blocked := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "blocked-context-item", ImplementationUnit: "unit-blocked-context",
		Title: "blocked context", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryBlocked,
		ImplementationRevision: 3,
	}
	replacement := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "replacement-context-item", ImplementationUnit: "unit-replacement-context",
		WorkstreamID: "ws-replacement-context", Title: "replacement context", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1, SupersedesUnitID: "unit-blocked-context",
	}
	items := &memoryItemStore{items: []domainbacklog.Item{blocked, replacement}}
	workstream := &memoryWorkstreamStore{}
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	if err := workstream.SaveQueueFreeze(context.Background(), domainworkstream.QueueFreeze{
		FreezeID: "freeze-context", BlockedUnitID: "unit-blocked-context", BlockedRevision: 3, FreezeRevision: 1,
		ReasonCode: "stage_failed", InvalidatedFromStage: domainbacklog.DeliveryBuild, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	verifier := &captureEvidenceVerifier{ok: true}
	service := NewService(items, workstream).WithEvidenceVerifier(verifier)
	_, _, acquired, err := service.ResolveQueueFreeze(context.Background(), "freeze-context", ResolveQueueFreezeRequest{
		RequestID: "resolve-context", ExpectedFreezeRevision: 1, ReplacementUnitID: replacement.ImplementationUnit,
		SupersedesUnitID:      blocked.ImplementationUnit,
		BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-context", Verified: true, Passed: true}},
	})
	if err != nil || !acquired {
		t.Fatalf("freeze resolution err=%v acquired=%v", err, acquired)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("verifier calls=%d want=1", len(verifier.requests))
	}
	request := verifier.requests[0]
	if request.ItemID != blocked.ItemID || request.ImplementationUnitID != blocked.ImplementationUnit || request.ImplementationRevision != blocked.ImplementationRevision || request.TargetDeliveryState != domainbacklog.DeliveryBlocked || request.Purpose != "blocker_resolution" {
		t.Fatalf("blocked evidence context=%+v", request)
	}
	if request.Ref.Stage != domainbacklog.DeliveryBlocked || request.Ref.Verified {
		t.Fatalf("blocked evidence normalization/claim clearing=%+v", request.Ref)
	}
}

func TestRevision2ReviseFailsClosedWithoutLifecycleStore(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "revision2-no-lifecycle", ImplementationUnit: "unit-no-lifecycle",
		Title: "no lifecycle", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	service := NewService(store, nil).WithEvidenceVerifier(revision2Verifier{})
	_, err := service.Revise(context.Background(), "revision2-no-lifecycle", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Kind: "spec", Ref: "spec-1", Passed: true}},
	})
	if !errors.Is(err, ErrLifecycleStoreUnavailable) {
		t.Fatalf("revision2 stage must fail closed without LifecycleStore, got %v", err)
	}
}

func TestRevision2StageReceiptPreparedBeforeItemMutation(t *testing.T) {
	events := []string{}
	store := &revision2OrderedItemStore{item: domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "revision2-order", ImplementationUnit: "unit-revision2-order",
		Title: "receipt order", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}, events: &events}
	workstream := &revision2OrderedWorkstreamStore{events: &events}
	service := NewService(store, workstream).WithEvidenceVerifier(revision2Verifier{})
	if _, err := service.Revise(context.Background(), "revision2-order", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Kind: "spec", Ref: "spec-order", Passed: true}},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"receipt:prepared", "item:save", "receipt:completed"}
	if len(events) != len(want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
}

func TestRevision2LiveVerifiedImmediatelyClosesToDone(t *testing.T) {
	refs := revision2CumulativeEvidence()
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "revision2-live-lease",
		ImplementationUnit: "unit-revision2-live-lease",
		WorkstreamID:       "ws-revision2-live-lease",
		Title:              "live lease",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryPostDeployVerify,
		EvidenceRefs:       refs,
	}}}
	workstream := &memoryWorkstreamStore{}
	_, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName:          domainbacklog.ImplementationLeaseName,
		HolderUnitID:       "unit-revision2-live-lease",
		HolderWorkstreamID: "ws-revision2-live-lease",
		Stage:              domainbacklog.DeliveryPostDeployVerify,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, workstream).WithEvidenceVerifier(revision2Verifier{})

	result, err := service.Revise(context.Background(), "revision2-live-lease", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryLiveVerified,
		EvidenceRefs:        refs,
	})
	if err != nil {
		t.Fatalf("LIVE_VERIFIED transition: %v", err)
	}
	if result.DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("LIVE_VERIFIED service operation must return DONE, got %+v", result)
	}
	if workstream.lease != nil {
		t.Fatalf("automatic DONE closure must release the implementation lease: %+v", workstream.lease)
	}
	closure, found, err := workstream.FindClosureReceipt(context.Background(), "unit-revision2-live-lease:1:DONE")
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("closure=%+v found=%v err=%v", closure, found, err)
	}
	projection, err := service.Projection(context.Background())
	if err != nil || len(projection.Current) != 1 || projection.Current[0].ItemID != "revision2-live-lease" {
		t.Fatalf("Current projection=%+v err=%v", projection.Current, err)
	}
}

type revision2ReleaseFailureStore struct {
	memoryWorkstreamStore
	releaseErr error
}

func (s *revision2ReleaseFailureStore) ReleaseImplementationLease(_ context.Context, _ string, _ string) error {
	return s.releaseErr
}

func TestRevision2DoneRejectsLeaseReleaseFailure(t *testing.T) {
	refs := revision2CumulativeEvidence()
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "revision2-done-release",
		ImplementationUnit: "unit-revision2-done-release",
		Title:              "done release",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryLiveVerified,
		EvidenceRefs:       refs,
	}}}
	workstream := &revision2ReleaseFailureStore{releaseErr: errors.New("lease release failed")}
	service := NewService(store, workstream).WithEvidenceVerifier(revision2Verifier{})

	if _, err := service.Revise(context.Background(), "revision2-done-release", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryDone,
		EvidenceRefs:        refs,
	}); err == nil {
		t.Fatal("DONE must not be recorded when closure cannot release the lease")
	}
}

func TestRevision2RecoverLiveWithoutClosureReceiptCompletesDone(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "revision2-recover-live", ImplementationUnit: "unit-revision2-recover-live",
		WorkstreamID: "ws-revision2-recover-live", Title: "recover live", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryLiveVerified, ImplementationRevision: 1,
	}}}
	ws := &memoryWorkstreamStore{}
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if _, err := ws.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-revision2-recover-live",
		HolderWorkstreamID: "ws-revision2-recover-live", Stage: domainbacklog.DeliveryLiveVerified, AcquiredAt: now, HeartbeatAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, ws)
	if err := service.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() failed: %v", err)
	}
	if store.items[0].DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("recovery state=%q, want DONE", store.items[0].DeliveryState)
	}
	if _, ok, err := ws.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || ok {
		t.Fatalf("recovery lease ok=%v err=%v", ok, err)
	}
	closure, found, err := ws.FindClosureReceipt(context.Background(), "unit-revision2-recover-live:1:DONE")
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("closure=%+v found=%v err=%v", closure, found, err)
	}
}

func TestRevision2QueueOrderingHonorsDependenciesBeforePriority(t *testing.T) {
	service := NewService(&memoryItemStore{}, nil)
	items := []domainbacklog.Item{
		{
			ItemID:        "dependency-blocker",
			Title:         "dependency blocker",
			ConceptState:  domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryQueued,
			Priority:      "low",
			AdoptedAt:     "2026-08-22T00:00:00Z",
		},
		{
			ItemID:        "dependency-dependent",
			Title:         "dependent item",
			ConceptState:  domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryQueued,
			Priority:      "urgent",
			DependsOn:     []string{"dependency-blocker"},
			AdoptedAt:     "2026-08-22T00:00:01Z",
		},
	}
	queue := service.queue(items)
	if len(queue) != 1 || queue[0].ItemID != "dependency-blocker" {
		t.Fatalf("dependency-unsatisfied item must be excluded and root retained: %+v", queue)
	}
}

func TestRevision2QueueExcludesMissingAndCyclicDependencies(t *testing.T) {
	service := NewService(&memoryItemStore{}, nil)
	items := []domainbacklog.Item{
		{ItemID: "missing", Title: "missing", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued, DependsOn: []string{"does-not-exist"}},
		{ItemID: "cycle-a", Title: "cycle a", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued, DependsOn: []string{"cycle-b"}},
		{ItemID: "cycle-b", Title: "cycle b", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued, DependsOn: []string{"cycle-a"}},
		{ItemID: "root", Title: "root", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued},
	}
	queue := service.queue(items)
	if len(queue) != 1 || queue[0].ItemID != "root" {
		t.Fatalf("missing/cyclic dependencies must be excluded: %+v", queue)
	}
}

func TestRevision2CurrentProjectionContainsDoneOnly(t *testing.T) {
	store := &memoryItemStore{items: []domainbacklog.Item{
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "live", Title: "live", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryLiveVerified},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-no-closure", ImplementationUnit: "unit-no-closure", ImplementationRevision: 1, Title: "done without closure", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-exact", ImplementationUnit: "unit-exact", ImplementationRevision: 2, Title: "done with closure", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-wrong-unit", ImplementationUnit: "unit-wrong-unit", ImplementationRevision: 3, Title: "done with wrong unit", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-wrong-revision", ImplementationUnit: "unit-wrong-revision", ImplementationRevision: 4, Title: "done with wrong revision", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-prepared", ImplementationUnit: "unit-prepared", ImplementationRevision: 5, Title: "done with prepared closure", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "done-no-unit", Title: "done without lifecycle unit", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryDone},
	}}
	workstream := &memoryWorkstreamStore{closureReceipts: []domainworkstream.ClosureReceipt{
		{ReceiptID: "closure-exact", UnitID: "unit-exact", ItemID: "done-exact", ImplementationRevision: 2, Status: domainworkstream.ClosureStatusCompleted, Phase: domainworkstream.ClosurePhaseDone},
		{ReceiptID: "closure-wrong-unit", UnitID: "different-unit", ItemID: "done-wrong-unit", ImplementationRevision: 3, Status: domainworkstream.ClosureStatusCompleted, Phase: domainworkstream.ClosurePhaseDone},
		{ReceiptID: "closure-wrong-revision", UnitID: "unit-wrong-revision", ItemID: "done-wrong-revision", ImplementationRevision: 99, Status: domainworkstream.ClosureStatusCompleted, Phase: domainworkstream.ClosurePhaseDone},
		{ReceiptID: "closure-prepared", UnitID: "unit-prepared", ItemID: "done-prepared", ImplementationRevision: 5, Status: domainworkstream.ClosureStatusPrepared, Phase: domainworkstream.ClosurePhasePrepared},
	}}
	service := NewService(store, workstream)
	projection, err := service.Projection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Current) != 2 {
		t.Fatalf("Current must expose only closure-complete v2 items plus records without lifecycle identity: %+v", projection.Current)
	}
	currentIDs := map[string]bool{}
	for _, item := range projection.Current {
		currentIDs[item.ItemID] = true
	}
	if !currentIDs["done-exact"] || !currentIDs["done-no-unit"] {
		t.Fatalf("Current missing exact closure or record without lifecycle identity: %+v", projection.Current)
	}
	for _, excluded := range []string{"done-no-closure", "done-wrong-unit", "done-wrong-revision", "done-prepared"} {
		if currentIDs[excluded] {
			t.Fatalf("Current included unproven DONE item %q: %+v", excluded, projection.Current)
		}
	}
}

func TestRevision2ResolveQueueFreezeValidatesReplacementAndIsIdempotent(t *testing.T) {
	blocked := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "blocked-item", ImplementationUnit: "unit-blocked",
		Title: "blocked", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryBlocked,
		ImplementationRevision: 2,
	}
	replacement := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "replacement-item", ImplementationUnit: "unit-replacement",
		WorkstreamID: "ws-replacement", Title: "replacement", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1, SupersedesUnitID: "unit-blocked",
	}
	items := &memoryItemStore{items: []domainbacklog.Item{blocked, replacement}}
	ws := &memoryWorkstreamStore{}
	now := time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC)
	if err := ws.SaveQueueFreeze(context.Background(), domainworkstream.QueueFreeze{
		FreezeID: "freeze-service", BlockedUnitID: "unit-blocked", BlockedRevision: 2, FreezeRevision: 4,
		ReasonCode: "stage_failed", InvalidatedFromStage: domainbacklog.DeliveryBuild, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(items, ws).WithEvidenceVerifier(revision2Verifier{})
	request := ResolveQueueFreezeRequest{
		RequestID: "resolve-service-1", ExpectedFreezeRevision: 4, ReplacementUnitID: "unit-replacement",
		SupersedesUnitID: "wrong-unit", BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-1"}},
	}
	if _, _, _, err := service.ResolveQueueFreeze(context.Background(), "freeze-service", request); err == nil {
		t.Fatal("replacement mismatch must fail closed")
	}
	request.SupersedesUnitID = "unit-blocked"
	freeze, lease, acquired, err := service.ResolveQueueFreeze(context.Background(), "freeze-service", request)
	if err != nil || !acquired || freeze.Status != domainworkstream.QueueFreezeResolved || lease.HolderUnitID != "unit-replacement" {
		t.Fatalf("resolved freeze=%+v lease=%+v acquired=%v err=%v", freeze, lease, acquired, err)
	}
	replayed, replayLease, replayAcquired, err := service.ResolveQueueFreeze(context.Background(), "freeze-service", request)
	if err != nil || !replayAcquired || replayed.ResolutionRequestID != freeze.ResolutionRequestID || replayLease.HolderUnitID != lease.HolderUnitID {
		t.Fatalf("resolution replay freeze=%+v lease=%+v acquired=%v err=%v", replayed, replayLease, replayAcquired, err)
	}
	request.BlockerResolutionRefs[0].Ref = "different-payload"
	if _, _, _, err := service.ResolveQueueFreeze(context.Background(), "freeze-service", request); !errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict) {
		t.Fatalf("same request with different payload err=%v", err)
	}
}

func TestRevision2AcquireRunnableReturnsNoneWhileQueueFrozen(t *testing.T) {
	items := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "queued-runnable", ImplementationUnit: "unit-runnable",
		Title: "queued", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	ws := &memoryWorkstreamStore{}
	if err := ws.SaveQueueFreeze(context.Background(), domainworkstream.QueueFreeze{
		FreezeID: "freeze-dispatch", BlockedUnitID: "unit-blocked", BlockedRevision: 1,
		ReasonCode: "blocked", InvalidatedFromStage: domainbacklog.DeliveryBuild, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(items, ws)
	result, err := service.AcquireRunnable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Acquired || result.Item.ItemID != "" || result.Reason != domainworkstream.ErrQueueFrozen.Error() {
		t.Fatalf("frozen dispatch result=%+v", result)
	}
	if ws.lease != nil {
		t.Fatalf("frozen dispatch acquired lease=%+v", ws.lease)
	}
}

func TestRevision2AcquireRunnableResumesExistingLeaseHolder(t *testing.T) {
	items := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "resume-item", ImplementationUnit: "unit-resume",
		WorkstreamID: "ws-resume", Title: "resume", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued,
	}}}
	ws := &memoryWorkstreamStore{}
	service := NewService(items, ws).WithEvidenceVerifier(revision2Verifier{})
	first, err := service.AcquireRunnable(context.Background())
	if err != nil || !first.Acquired || first.Item.ImplementationUnit != "unit-resume" {
		t.Fatalf("initial acquire=%+v err=%v", first, err)
	}
	if _, err := service.Revise(context.Background(), "resume-item", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliverySpec,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Kind: "spec", Ref: "spec-resume"}},
	}); err != nil {
		t.Fatalf("stage continuation=%v", err)
	}
	second, err := service.AcquireRunnable(context.Background())
	if err != nil || !second.Acquired || second.Item.ImplementationUnit != first.Item.ImplementationUnit || second.Item.DeliveryState != domainbacklog.DeliverySpec || second.Lease.HolderUnitID != first.Lease.HolderUnitID {
		t.Fatalf("resumed acquire=%+v err=%v", second, err)
	}
}

func revision2CumulativeEvidence() []domainbacklog.EvidenceRef {
	return []domainbacklog.EvidenceRef{
		{Kind: "spec", Ref: "spec", Passed: true},
		{Kind: "tdd_red", Ref: "red", Passed: true},
		{Kind: "tdd_green", Ref: "green", Passed: true},
		{Kind: "refactor", Ref: "refactor", Passed: true},
		{Kind: "e2e", Ref: "e2e", Passed: true},
		{Kind: "build", Ref: "build", Passed: true},
		{Kind: "deploy", Ref: "deploy", Passed: true},
		{Kind: "restart", Ref: "restart", Passed: true},
		{Kind: "readiness", Ref: "readiness", Passed: true},
		{Kind: "production_smoke", Ref: "live", Passed: true},
	}
}

type revision2Verifier struct{}

func (revision2Verifier) Verify(_ context.Context, request EvidenceVerificationRequest) (bool, error) {
	return request.Ref.Ref != "", nil
}

type captureEvidenceVerifier struct {
	requests []EvidenceVerificationRequest
	ok       bool
}

func (v *captureEvidenceVerifier) Verify(_ context.Context, request EvidenceVerificationRequest) (bool, error) {
	v.requests = append(v.requests, request)
	return v.ok, nil
}

type revision2OrderedItemStore struct {
	item   domainbacklog.Item
	events *[]string
}

func (s *revision2OrderedItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return []domainbacklog.Item{s.item}, nil
}

func (s *revision2OrderedItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	*s.events = append(*s.events, "item:save")
	s.item = item
	return nil
}

type revision2OrderedWorkstreamStore struct {
	memoryWorkstreamStore
	events *[]string
}

func (s *revision2OrderedWorkstreamStore) SaveStageRunReceipt(ctx context.Context, item domainworkstream.StageRunReceipt) error {
	*s.events = append(*s.events, "receipt:"+item.Status)
	return s.memoryWorkstreamStore.SaveStageRunReceipt(ctx, item)
}
