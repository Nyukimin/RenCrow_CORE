package backlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	backlogstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
)

type memoryItemStore struct{ items []domainbacklog.Item }

func (s *memoryItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), s.items...), nil
}
func (s *memoryItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	for i := range s.items {
		if s.items[i].BacklogItemID == item.BacklogItemID {
			s.items[i] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

type memoryWorkstreamStore struct {
	workstreams     []domainworkstream.Workstream
	goals           []domainworkstream.Goal
	artifacts       []domainworkstream.Artifact
	queueFreezes    []domainworkstream.QueueFreeze
	stageReceipts   []domainworkstream.StageRunReceipt
	closureReceipts []domainworkstream.ClosureReceipt
	lease           *domainworkstream.ImplementationLease
}

func (s *memoryWorkstreamStore) AcquireImplementationLease(_ context.Context, lease domainworkstream.ImplementationLease) (bool, error) {
	if s.lease != nil && s.lease.HolderUnitID != lease.HolderUnitID {
		return false, nil
	}
	copyLease := lease
	s.lease = &copyLease
	return true, nil
}
func (s *memoryWorkstreamStore) ReleaseImplementationLease(_ context.Context, _ string, holder string) error {
	if s.lease != nil && (holder == "" || s.lease.HolderUnitID == holder) {
		s.lease = nil
	}
	return nil
}
func (s *memoryWorkstreamStore) GetImplementationLease(_ context.Context, name string) (domainworkstream.ImplementationLease, bool, error) {
	if s.lease == nil || s.lease.LeaseName != name {
		return domainworkstream.ImplementationLease{}, false, nil
	}
	return *s.lease, true, nil
}
func (s *memoryWorkstreamStore) HeartbeatImplementationLease(_ context.Context, lease domainworkstream.ImplementationLease) error {
	copyLease := lease
	s.lease = &copyLease
	return nil
}

func (s *memoryWorkstreamStore) SaveWorkstream(_ context.Context, item domainworkstream.Workstream) error {
	s.workstreams = append(s.workstreams, item)
	return nil
}
func (s *memoryWorkstreamStore) SaveGoal(_ context.Context, item domainworkstream.Goal) error {
	s.goals = append(s.goals, item)
	return nil
}
func (s *memoryWorkstreamStore) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	s.artifacts = append(s.artifacts, item)
	return nil
}
func (s *memoryWorkstreamStore) ListArtifacts(_ context.Context, limit int) ([]domainworkstream.Artifact, error) {
	out := make([]domainworkstream.Artifact, 0, len(s.artifacts))
	for index := len(s.artifacts) - 1; index >= 0; index-- {
		out = append(out, s.artifacts[index])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *memoryWorkstreamStore) ListWorkstreams(_ context.Context, limit int) ([]domainworkstream.Workstream, error) {
	out := make([]domainworkstream.Workstream, 0, len(s.workstreams))
	for index := len(s.workstreams) - 1; index >= 0; index-- {
		out = append(out, s.workstreams[index])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *memoryWorkstreamStore) FindGoalByID(_ context.Context, goalID string) (domainworkstream.Goal, bool, error) {
	for index := len(s.goals) - 1; index >= 0; index-- {
		if s.goals[index].GoalID == goalID {
			return s.goals[index], true, nil
		}
	}
	return domainworkstream.Goal{}, false, nil
}

func (s *memoryWorkstreamStore) SaveQueueFreeze(_ context.Context, item domainworkstream.QueueFreeze) error {
	for index := range s.queueFreezes {
		if s.queueFreezes[index].FreezeID == item.FreezeID {
			s.queueFreezes[index] = item
			return nil
		}
	}
	s.queueFreezes = append(s.queueFreezes, item)
	return nil
}

func (s *memoryWorkstreamStore) GetQueueFreeze(_ context.Context, id string) (domainworkstream.QueueFreeze, bool, error) {
	for index := len(s.queueFreezes) - 1; index >= 0; index-- {
		if s.queueFreezes[index].FreezeID == id {
			return s.queueFreezes[index], true, nil
		}
	}
	return domainworkstream.QueueFreeze{}, false, nil
}

func (s *memoryWorkstreamStore) ListQueueFreezes(_ context.Context, limit int) ([]domainworkstream.QueueFreeze, error) {
	out := append([]domainworkstream.QueueFreeze(nil), s.queueFreezes...)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *memoryWorkstreamStore) AcquireImplementationLeaseIfUnfrozen(_ context.Context, lease domainworkstream.ImplementationLease) (bool, string, error) {
	for index := len(s.queueFreezes) - 1; index >= 0; index-- {
		freeze := s.queueFreezes[index]
		if freeze.Status == domainworkstream.QueueFreezeActive || freeze.Status == "" {
			return false, domainworkstream.ErrQueueFrozen.Error(), nil
		}
	}
	if s.lease != nil && s.lease.HolderUnitID != lease.HolderUnitID {
		return false, domainworkstream.ErrImplementationLeaseHeld.Error(), nil
	}
	copyLease := lease
	s.lease = &copyLease
	return true, "", nil
}

func (s *memoryWorkstreamStore) ResolveQueueFreezeAndAcquireLease(_ context.Context, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	if err := domainworkstream.ValidateQueueFreezeResolution(resolution, replacement); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	freeze, found, err := s.GetQueueFreeze(context.Background(), freezeID)
	if err != nil || !found {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeNotFound
	}
	if freeze.Status == domainworkstream.QueueFreezeResolved {
		if !freeze.MatchesResolved(resolution) {
			return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		return freeze, freeze.ReplacementLease, freeze.ResolutionAcquired, nil
	}
	if freeze.Status != domainworkstream.QueueFreezeActive && freeze.Status != "" {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeResolutionConflict
	}
	if freeze.FreezeRevision != resolution.ExpectedFreezeRevision {
		return freeze, domainworkstream.ImplementationLease{}, false, domainworkstream.ErrQueueFreezeRevisionConflict
	}
	if s.lease != nil && s.lease.HolderUnitID != replacement.HolderUnitID {
		return freeze, domainworkstream.ImplementationLease{}, false, nil
	}
	now := time.Now().UTC()
	freeze.Status = domainworkstream.QueueFreezeResolved
	freeze.ActionID = resolution.ActionID
	freeze.ReplacementUnitID = resolution.ReplacementUnitID
	freeze.ReplacementLease = replacement
	freeze.ResolutionAcquired = true
	freeze.SupersedesUnitID = resolution.SupersedesUnitID
	freeze.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), resolution.BlockerResolutionRefs...)
	freeze.ResolutionPayloadHash = resolution.ResolutionPayloadHash
	freeze.UpdatedAt = now
	freeze.ResolvedAt = now
	if err := s.SaveQueueFreeze(context.Background(), freeze); err != nil {
		return freeze, domainworkstream.ImplementationLease{}, false, err
	}
	copyLease := replacement
	s.lease = &copyLease
	return freeze, replacement, true, nil
}

func (s *memoryWorkstreamStore) SaveStageRunReceipt(_ context.Context, item domainworkstream.StageRunReceipt) error {
	for index := range s.stageReceipts {
		if s.stageReceipts[index].IdempotencyKey == item.IdempotencyKey {
			s.stageReceipts[index] = item
			return nil
		}
	}
	s.stageReceipts = append(s.stageReceipts, item)
	return nil
}

func (s *memoryWorkstreamStore) FindStageRunReceipt(_ context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	for index := len(s.stageReceipts) - 1; index >= 0; index-- {
		if s.stageReceipts[index].IdempotencyKey == key || string(s.stageReceipts[index].ReceiptID) == key {
			return s.stageReceipts[index], true, nil
		}
	}
	return domainworkstream.StageRunReceipt{}, false, nil
}

func (s *memoryWorkstreamStore) ListStageRunReceipts(_ context.Context, limit int) ([]domainworkstream.StageRunReceipt, error) {
	out := append([]domainworkstream.StageRunReceipt(nil), s.stageReceipts...)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *memoryWorkstreamStore) SaveClosureReceipt(_ context.Context, item domainworkstream.ClosureReceipt) error {
	for index := range s.closureReceipts {
		if s.closureReceipts[index].IdempotencyKey == item.IdempotencyKey {
			s.closureReceipts[index] = item
			return nil
		}
	}
	s.closureReceipts = append(s.closureReceipts, item)
	return nil
}

func (s *memoryWorkstreamStore) FindClosureReceipt(_ context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	for index := len(s.closureReceipts) - 1; index >= 0; index-- {
		if s.closureReceipts[index].IdempotencyKey == key || string(s.closureReceipts[index].ReceiptID) == key {
			return s.closureReceipts[index], true, nil
		}
	}
	return domainworkstream.ClosureReceipt{}, false, nil
}

func (s *memoryWorkstreamStore) ListClosureReceipts(_ context.Context, limit int) ([]domainworkstream.ClosureReceipt, error) {
	out := append([]domainworkstream.ClosureReceipt(nil), s.closureReceipts...)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func TestServiceAdoptCreatesUnitWorkstreamAndSingletonQueue(t *testing.T) {
	store := &memoryItemStore{}
	ws := &memoryWorkstreamStore{}
	svc := NewService(store, ws).WithClock(func() time.Time { return time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC) })

	first, err := svc.Intake(context.Background(), IntakeRequest{BacklogItemID: modulecore.BacklogItemID("a"), Title: "A", Purpose: "test A", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	firstCandidate, err := svc.Candidate(context.Background(), string(first.BacklogItemID))
	if err != nil {
		t.Fatal(err)
	}
	// This fixture exercises the adoption/workstream behavior.  Maturation is
	// covered by the dedicated tests; seed the owner-approved decision here.
	firstCandidate.MaturationState = domainbacklog.MaturationStatePromoted
	store.items[0] = firstCandidate
	firstResult, err := svc.Adopt(context.Background(), string(first.BacklogItemID), "owner selected")
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Item.DeliveryState != domainbacklog.DeliveryQueued || !firstResult.LeaseAcquired {
		t.Fatalf("first adoption=%+v", firstResult)
	}
	second, err := svc.Intake(context.Background(), IntakeRequest{BacklogItemID: modulecore.BacklogItemID("b"), Title: "B", Purpose: "test B", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate, err := svc.Candidate(context.Background(), string(second.BacklogItemID))
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate.MaturationState = domainbacklog.MaturationStatePromoted
	store.items[1] = secondCandidate
	secondResult, err := svc.Adopt(context.Background(), string(second.BacklogItemID), "owner selected")
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.LeaseAcquired || secondResult.Item.DeliveryState != domainbacklog.DeliveryQueued {
		t.Fatalf("second adoption=%+v", secondResult)
	}
	if len(ws.workstreams) != 2 || len(ws.goals) != 2 || len(ws.artifacts) != 2 {
		t.Fatalf("created workstream records=%d goals=%d artifacts=%d", len(ws.workstreams), len(ws.goals), len(ws.artifacts))
	}
}

func TestServiceIntakeDeduplicatesExactSource(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	request := IntakeRequest{Title: "same", SourceRefs: []domainbacklog.SourceRef{{Type: "url", Locator: "https://example.test", ContentHash: "abc"}}}
	first, err := svc.Intake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Intake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Item.BacklogItemID != first.Item.BacklogItemID || len(store.items) != 1 {
		t.Fatalf("dedupe result first=%+v second=%+v items=%d", first, second, len(store.items))
	}
}

func TestServiceIntakeKeepsDistinctProposalsFromSharedEvidence(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	refs := []domainbacklog.SourceRef{{Type: "web", Locator: "https://example.test/paper", ContentHash: "abc"}}
	first, err := svc.Intake(context.Background(), IntakeRequest{Title: "Streaming support", SourceRefs: refs})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Intake(context.Background(), IntakeRequest{Title: "Memory compression", SourceRefs: refs})
	if err != nil || second.Duplicate || first.BacklogItemID == second.BacklogItemID || len(store.items) != 2 {
		t.Fatalf("distinct proposals collapsed: first=%+v second=%+v err=%v", first, second, err)
	}
	duplicate, err := svc.Intake(context.Background(), IntakeRequest{Title: " STREAMING   Support ", SourceRefs: refs})
	if err != nil || !duplicate.Duplicate || duplicate.BacklogItemID != first.BacklogItemID {
		t.Fatalf("normalized title replay: result=%+v err=%v", duplicate, err)
	}
}

func TestServiceIntakeReplayPreservesEditedCandidate(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	request := IntakeRequest{Title: "Original title", Body: "original body", SourceRefs: []domainbacklog.SourceRef{{Type: "web", Locator: "https://example.test/paper", ContentHash: "abc"}}}
	first, err := svc.Intake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	store.items[0].Title = "Owner edited title"
	store.items[0].Body = "owner edited body"
	store.items[0].ConceptState = domainbacklog.ConceptRejected
	replayed, err := svc.Intake(context.Background(), request)
	if err != nil || !replayed.Duplicate || replayed.BacklogItemID != first.BacklogItemID || replayed.Item.Title != "Owner edited title" || replayed.Item.Body != "owner edited body" || replayed.Item.ConceptState != domainbacklog.ConceptRejected {
		t.Fatalf("replay overwrote owner work: result=%+v err=%v", replayed, err)
	}
}

func TestServiceIntakeFixesLifecycleOwnerAndSeparatesModuleScope(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	result, err := svc.Intake(context.Background(), IntakeRequest{
		BacklogItemID:   modulecore.BacklogItemID("module-scope"),
		Title:           "Module scope",
		OwnerModule:     "RenCrow_STT",
		TargetModules:   []string{"RenCrow_STT"},
		ConsumerModules: []string{"RenCrow_CORE", "RenCrow_PORTAL"},
		AffectedModules: []string{"RenCrow_STT", "RenCrow_CORE", "RenCrow_PORTAL"},
		SourceRefs:      []domainbacklog.SourceRef{{Type: "manual", Locator: "module-scope"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.OwnerModule != domainbacklog.LifecycleOwnerModule {
		t.Fatalf("lifecycle owner=%q", result.Item.OwnerModule)
	}
	if len(result.Item.TargetModules) != 1 || result.Item.TargetModules[0] != "RenCrow_STT" {
		t.Fatalf("target modules=%v", result.Item.TargetModules)
	}
	if len(result.Item.ConsumerModules) != 2 || len(result.Item.AffectedModules) != 3 {
		t.Fatalf("consumer=%v affected=%v", result.Item.ConsumerModules, result.Item.AffectedModules)
	}
}

func TestServiceCandidateRequiresPurpose(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	intake, err := svc.Intake(context.Background(), IntakeRequest{
		BacklogItemID: modulecore.BacklogItemID("missing-purpose"), Title: "Missing purpose",
		SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "missing-purpose"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Candidate(context.Background(), string(intake.BacklogItemID)); err == nil {
		t.Fatal("candidate promotion accepted an item without purpose")
	}
}

func TestServiceRecoverFindsLeaseHolderByImplementationUnit(t *testing.T) {
	store := &memoryItemStore{}
	ws := &memoryWorkstreamStore{}
	svc := NewService(store, ws).WithClock(func() time.Time {
		return time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	})
	intake, err := svc.Intake(context.Background(), IntakeRequest{
		BacklogItemID: modulecore.BacklogItemID("recover-item"), Title: "recover", Purpose: "recover test", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "recover"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := svc.Candidate(context.Background(), string(intake.BacklogItemID))
	if err != nil {
		t.Fatal(err)
	}
	candidate.MaturationState = domainbacklog.MaturationStatePromoted
	store.items[0] = candidate
	adopted, err := svc.Adopt(context.Background(), string(intake.BacklogItemID), "recover test")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Lease.HolderUnitID == string(adopted.Item.BacklogItemID) {
		t.Fatalf("test must cover distinct item and unit IDs: %+v", adopted)
	}
	if err := svc.Recover(backlogActionContext(t)); err != nil {
		t.Fatal(err)
	}
	projection, err := svc.Projection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projection.Active == nil || projection.Active.ImplementationUnit != adopted.Lease.HolderUnitID {
		t.Fatalf("recovery dropped active lease: %+v", projection.Active)
	}
}

type recoveryFreezeFailureStore struct {
	memoryWorkstreamStore
	failFreezeSave bool
	freezeReadErr  error
	releaseErr     error
}

func (s *recoveryFreezeFailureStore) SaveQueueFreeze(ctx context.Context, item domainworkstream.QueueFreeze) error {
	if s.failFreezeSave {
		return errors.New("simulated queue freeze persistence failure")
	}
	return s.memoryWorkstreamStore.SaveQueueFreeze(ctx, item)
}

func (s *recoveryFreezeFailureStore) GetQueueFreeze(ctx context.Context, id string) (domainworkstream.QueueFreeze, bool, error) {
	if s.freezeReadErr != nil {
		return domainworkstream.QueueFreeze{}, false, s.freezeReadErr
	}
	return s.memoryWorkstreamStore.GetQueueFreeze(ctx, id)
}

func (s *recoveryFreezeFailureStore) ReleaseImplementationLease(ctx context.Context, leaseName, holder string) error {
	if s.releaseErr != nil {
		return s.releaseErr
	}
	return s.memoryWorkstreamStore.ReleaseImplementationLease(ctx, leaseName, holder)
}

func TestRecoverPersistsBlockedFreezeBeforeReleasingLease(t *testing.T) {
	now := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	items := &memoryItemStore{items: []domainbacklog.Item{
		{
			SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("recover-blocked"), ImplementationUnit: "unit-recover-blocked",
			WorkstreamID: "ws-recover-blocked", Title: "recover blocked", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
		{
			SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("recover-next"), ImplementationUnit: "unit-recover-next",
			WorkstreamID: "ws-recover-next", Title: "recover next", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
	}}
	workstream := &recoveryFreezeFailureStore{failFreezeSave: true}
	service := NewService(items, workstream).WithClock(func() time.Time { return now })
	if acquired, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-recover-blocked", HolderWorkstreamID: "ws-recover-blocked",
		Stage: domainbacklog.DeliverySpec, Revision: "1", AcquiredAt: now, HeartbeatAt: now,
	}); err != nil || !acquired {
		t.Fatalf("seed blocked lease acquired=%v err=%v", acquired, err)
	}

	if _, err := service.Revise(backlogActionContext(t), "recover-blocked", ReviseRequest{
		TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs:        []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "worker-failure-recover"}},
		Reason:              "worker_failure",
	}); err == nil {
		t.Fatal("initial freeze persistence failure must surface")
	}
	blocked, err := service.Get(context.Background(), "recover-blocked")
	if err != nil || blocked.DeliveryState != domainbacklog.DeliveryBlocked {
		t.Fatalf("item after partial BLOCKED write=%+v err=%v", blocked, err)
	}
	if _, found, err := workstream.GetQueueFreeze(context.Background(), "atlas-freeze:unit-recover-blocked:1"); err != nil || found {
		t.Fatalf("freeze must be absent after simulated failed append found=%v err=%v", found, err)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || !found {
		t.Fatalf("failed freeze write must retain lease found=%v err=%v", found, err)
	}

	if err := service.Recover(backlogActionContext(t)); err == nil {
		t.Fatal("recovery must fail closed while freeze persistence remains unavailable")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || !found {
		t.Fatalf("recovery freeze failure released lease found=%v err=%v", found, err)
	}

	workstream.failFreezeSave = false
	if err := service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("recovery after store restoration: %v", err)
	}
	freeze, found, err := workstream.GetQueueFreeze(context.Background(), "atlas-freeze:unit-recover-blocked:1")
	if err != nil || !found || freeze.Status != domainworkstream.QueueFreezeActive || freeze.BlockedUnitID != blocked.ImplementationUnit || freeze.BlockedRevision != blocked.ImplementationRevision || freeze.ReasonCode != "worker_failure" || freeze.InvalidatedFromStage != domainbacklog.DeliverySpec || len(freeze.EvidenceRefs) != 1 || freeze.EvidenceRefs[0].Stage != domainbacklog.DeliveryBlocked {
		t.Fatalf("recovered exact active freeze=%+v found=%v err=%v", freeze, found, err)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("lease must release only after freeze persistence found=%v err=%v", found, err)
	}
	result, err := service.AcquireRunnable(backlogActionContext(t))
	if err != nil || result.Acquired || result.Reason != domainworkstream.ErrQueueFrozen.Error() || result.Item.BacklogItemID != "" {
		t.Fatalf("recovered queue must remain frozen result=%+v err=%v", result, err)
	}
}

func TestRecoverBlockedExistingFreezeRetainsAttemptedStageEvidence(t *testing.T) {
	now := time.Date(2026, 8, 22, 4, 30, 0, 0, time.UTC)
	items := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("recover-blocked-existing"), ImplementationUnit: "unit-recover-blocked-existing",
		WorkstreamID: "ws-recover-blocked-existing", Title: "recover blocked existing", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryBlocked, ImplementationRevision: 1, InvalidatedFromStage: domainbacklog.DeliverySpec,
		Implementation: "worker_failure", EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Kind: "worker_failure", Ref: "item-failure", Passed: false}},
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}}}
	workstream := &recoveryFreezeFailureStore{releaseErr: errors.New("simulated lease release failure")}
	if acquired, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-recover-blocked-existing", HolderWorkstreamID: "ws-recover-blocked-existing",
		Stage: domainbacklog.DeliverySpec, Revision: "1", AcquiredAt: now, HeartbeatAt: now,
	}); err != nil || !acquired {
		t.Fatalf("seed blocked lease acquired=%v err=%v", acquired, err)
	}
	if err := workstream.memoryWorkstreamStore.SaveQueueFreeze(context.Background(), domainworkstream.QueueFreeze{
		FreezeID: "atlas-freeze:unit-recover-blocked-existing:1", BlockedUnitID: "unit-recover-blocked-existing", BlockedRevision: 1,
		FreezeRevision: 1, ReasonCode: "worker_failure", InvalidatedFromStage: domainbacklog.DeliverySpec,
		// Existing freeze evidence can use the attempted stage; recovery must
		// validate stable identity without rewriting or comparing this slice.
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Kind: "worker_failure", Ref: "persisted-failure", Passed: false}},
		Status:       domainworkstream.QueueFreezeActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(items, workstream).WithClock(func() time.Time { return now })
	if err := service.Recover(backlogActionContext(t)); err == nil {
		t.Fatal("release failure must be returned")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || !found {
		t.Fatalf("release failure must retain lease found=%v err=%v", found, err)
	}
	workstream.releaseErr = nil
	if err := service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("recovery with existing attempted-stage freeze: %v", err)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("lease remains after successful release found=%v err=%v", found, err)
	}
}

type recoveryDoneFailureItemStore struct {
	memoryItemStore
	failDoneSave bool
}

func (s *recoveryDoneFailureItemStore) Save(ctx context.Context, item domainbacklog.Item) error {
	if s.failDoneSave && item.DeliveryState == domainbacklog.DeliveryDone {
		return errors.New("simulated DONE persistence failure")
	}
	return s.memoryItemStore.Save(ctx, item)
}

func newLiveClosureRecoveryFixture(t *testing.T) (*Service, *recoveryDoneFailureItemStore, *memoryWorkstreamStore) {
	t.Helper()
	now := time.Date(2026, 8, 22, 5, 0, 0, 0, time.UTC)
	items := &recoveryDoneFailureItemStore{failDoneSave: true, memoryItemStore: memoryItemStore{items: []domainbacklog.Item{
		{
			SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("recover-live"), ImplementationUnit: "unit-recover-live",
			WorkstreamID: "ws-recover-live", Title: "recover live", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryLiveVerified, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
		{
			SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("recover-live-next"), ImplementationUnit: "unit-recover-live-next",
			WorkstreamID: "ws-recover-live-next", Title: "recover live next", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
	}}}
	workstream := &memoryWorkstreamStore{}
	if acquired, err := workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "unit-recover-live", HolderWorkstreamID: "ws-recover-live",
		Stage: domainbacklog.DeliveryLiveVerified, Revision: "1", AcquiredAt: now, HeartbeatAt: now,
	}); err != nil || !acquired {
		t.Fatalf("seed LIVE lease acquired=%v err=%v", acquired, err)
	}
	service := NewService(items, workstream).WithClock(func() time.Time { return now })
	live := items.items[0]
	done := live
	done.DeliveryState = domainbacklog.DeliveryDone
	if err := service.completeDone(backlogActionContext(t), live, done, ReviseRequest{TargetDeliveryState: domainbacklog.DeliveryDone}, stageRunKey(live.ImplementationUnit, 1, domainbacklog.DeliveryDone), ""); err == nil {
		t.Fatal("simulated DONE persistence failure must surface")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("fixture must reproduce released lease found=%v err=%v", found, err)
	}
	return service, items, workstream
}

func TestRecoverResumesLiveClosureWithoutLeaseBeforeQueue(t *testing.T) {
	service, items, workstream := newLiveClosureRecoveryFixture(t)
	before, found, err := workstream.FindClosureReceipt(context.Background(), "unit-recover-live:1:DONE")
	if err != nil || !found || before.ActionID.Validate() != nil {
		t.Fatalf("prepared closure must own a canonical action: %+v found=%v err=%v", before, found, err)
	}
	items.failDoneSave = false
	if err := service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover must resume LIVE closure without lease: %v", err)
	}
	if items.items[0].DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("recovered LIVE state=%q, want DONE", items.items[0].DeliveryState)
	}
	closure, found, err := workstream.FindClosureReceipt(context.Background(), "unit-recover-live:1:DONE")
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("recovered closure=%+v found=%v err=%v", closure, found, err)
	}
	doneStage, stageFound, stageErr := workstream.FindStageRunReceipt(context.Background(), "unit-recover-live:1:DONE")
	if closure.ActionID != before.ActionID || stageErr != nil || !stageFound || doneStage.ActionID != before.ActionID {
		t.Fatalf("recovery must preserve the prepared action %s: stage=%s closure=%s found=%v err=%v", before.ActionID, doneStage.ActionID, closure.ActionID, stageFound, stageErr)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("recovered closure retained lease found=%v err=%v", found, err)
	}
	if items.items[1].DeliveryState != domainbacklog.DeliveryQueued {
		t.Fatalf("next unit started during Recover: %+v", items.items[1])
	}
}

func TestAcquireRunnableResumesLiveClosureBeforeSelectingQueue(t *testing.T) {
	service, items, workstream := newLiveClosureRecoveryFixture(t)
	items.failDoneSave = false
	result, err := service.AcquireRunnable(backlogActionContext(t))
	if err != nil || result.Item.BacklogItemID != "recover-live" || result.Item.DeliveryState != domainbacklog.DeliveryDone || result.Reason != "LIVE_VERIFIED closure resumed" {
		t.Fatalf("AcquireRunnable closure recovery result=%+v err=%v", result, err)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("closure recovery acquired/retained lease found=%v err=%v", found, err)
	}
	if items.items[1].DeliveryState != domainbacklog.DeliveryQueued {
		t.Fatalf("next unit selected before LIVE closure: %+v", items.items[1])
	}
}

type intakeDesignCardItemStore struct {
	items     []domainbacklog.Item
	saveCount int
}

func (s *intakeDesignCardItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), s.items...), nil
}

func (s *intakeDesignCardItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	s.saveCount++
	for i := range s.items {
		if s.items[i].BacklogItemID == item.BacklogItemID {
			s.items[i] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func TestServiceIntakePreservesSchemaV2DesignCardAndRejectsUnknownSpecification(t *testing.T) {
	store := &intakeDesignCardItemStore{}
	service := NewService(store, nil)
	request := IntakeRequest{
		BacklogItemID:  modulecore.BacklogItemID("design-card"),
		FeatureID:      "atlas.design-card",
		Kind:           "idea",
		Title:          "Lossless Design Card",
		Purpose:        "retain unresolved Atlas design memory",
		Problem:        "owner intake currently drops design fields",
		Idea:           "map the complete card through CORE",
		Background:     "the HTTP owner route is the normal intake path",
		ExpectedEffect: []string{"lossless reconstruction", "auditable ownership"},
		RelationRefs:   []string{"atlas:lifecycle", "atlas:memory"},
		SpecificationRefs: []string{
			"spec_atlas_idea_recording_v1",
			"spec_l0v2_external",
		},
		SourceRefs: []domainbacklog.SourceRef{{Type: "test", Locator: "design-card"}},
	}
	result, err := service.Intake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.FeatureID != request.FeatureID || result.Item.Problem != request.Problem || result.Item.Idea != request.Idea || result.Item.Background != request.Background {
		t.Fatalf("design card scalars were not preserved: %+v", result.Item)
	}
	if !reflect.DeepEqual(result.Item.ExpectedEffect, request.ExpectedEffect) || !reflect.DeepEqual(result.Item.RelationRefs, request.RelationRefs) || !reflect.DeepEqual(result.Item.SpecificationRefs, request.SpecificationRefs) {
		t.Fatalf("design card slices were not preserved: %+v", result.Item)
	}
	request.ExpectedEffect[0] = "caller mutation"
	request.RelationRefs[0] = "caller mutation"
	request.SpecificationRefs[0] = "caller mutation"
	if result.Item.ExpectedEffect[0] == request.ExpectedEffect[0] || result.Item.RelationRefs[0] == request.RelationRefs[0] || result.Item.SpecificationRefs[0] == request.SpecificationRefs[0] {
		t.Fatal("intake retained caller-owned design card slices")
	}

	saveCount := store.saveCount
	_, err = service.Intake(context.Background(), IntakeRequest{
		BacklogItemID: modulecore.BacklogItemID("unknown-spec"), Title: "Unknown specification", Purpose: "partial input is allowed",
		SpecificationRefs: []string{"spec_not_embedded"},
		SourceRefs:        []domainbacklog.SourceRef{{Type: "test", Locator: "unknown-spec"}},
	})
	if err == nil {
		t.Fatal("unknown specification reference was accepted")
	}
	if store.saveCount != saveCount || len(store.items) != 1 {
		t.Fatalf("unknown specification reference reached Save: saves=%d items=%d", store.saveCount, len(store.items))
	}
}

type rejectedCancellationItemStore struct {
	memoryItemStore
	saveCount        int
	failRejectedSave bool
}

func (s *rejectedCancellationItemStore) Save(ctx context.Context, item domainbacklog.Item) error {
	s.saveCount++
	if s.failRejectedSave && item.DeliveryState == domainbacklog.DeliveryRejected {
		return errors.New("simulated rejected item persistence failure")
	}
	return s.memoryItemStore.Save(ctx, item)
}

type rejectedCancellationFixture struct {
	service       *Service
	items         *rejectedCancellationItemStore
	workstream    *memoryWorkstreamStore
	item          domainbacklog.Item
	workstreamRow domainworkstream.Workstream
	goal          domainworkstream.Goal
	artifact      domainworkstream.Artifact
	nextItemID    modulecore.BacklogItemID
	now           time.Time
}

func newRejectedCancellationFixture(workstream *memoryWorkstreamStore) *rejectedCancellationFixture {
	now := time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("cancel-item"),
		FeatureID: "atlas.cancel", Title: "Cancelable implementation", Body: "keep body metadata",
		Problem: "problem metadata", Idea: "idea metadata", Background: "background metadata",
		ExpectedEffect: []string{"effect one"}, RelationRefs: []string{"atlas:related"},
		Owner: "owner metadata", OwnerModule: domainbacklog.LifecycleOwnerModule, Priority: "high", Tags: []string{"keep-tag"},
		ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliverySpec,
		Implementation: "existing implementation detail", ImplementationUnit: "unit-cancel-item", WorkstreamID: "ws-cancel-item",
		ImplementationRevision: 7, CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}
	workstreamRow := domainworkstream.Workstream{
		WorkstreamID: item.WorkstreamID, Name: "custom workstream name", Description: "preserve description",
		Status: domainworkstream.StatusActive, PrimaryAgent: "Mio", VaultPath: "vault/custom.md",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	goal := domainworkstream.Goal{
		GoalID: "goal_atlas_cancel-item", TraceID: "trace-preserved", WorkstreamID: item.WorkstreamID,
		Title: "custom goal title", Description: "preserve goal description", SuccessCriteria: []string{"criterion"},
		Verification: []string{"verification"}, Status: domainworkstream.StatusWaiting,
		CreatedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute),
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: "artifact_atlas_cancel-item", TraceID: "artifact-trace", WorkstreamID: item.WorkstreamID,
		Type: "custom-artifact-type", FilePath: "artifacts/preserved.json", Title: "custom artifact title",
		Status: "pending", Payload: json.RawMessage(`{"nested":{"preserve":true},"list":[1,2]}`),
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	next := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("cancel-next"),
		Title: "Next queued implementation", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued, ImplementationUnit: "unit-cancel-next", WorkstreamID: "ws-cancel-next",
		ImplementationRevision: 1, CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}
	items := &rejectedCancellationItemStore{memoryItemStore: memoryItemStore{items: []domainbacklog.Item{item, next}}}
	if workstream == nil {
		workstream = &memoryWorkstreamStore{}
	}
	workstream.workstreams = append(workstream.workstreams, workstreamRow)
	workstream.goals = append(workstream.goals, goal)
	workstream.artifacts = append(workstream.artifacts, artifact)
	workstream.lease = &domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: item.ImplementationUnit,
		HolderWorkstreamID: item.WorkstreamID, Stage: item.DeliveryState, Revision: "7",
		AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute),
	}
	service := NewService(items, workstream).WithClock(func() time.Time { return now })
	return &rejectedCancellationFixture{
		service: service, items: items, workstream: workstream, item: item,
		workstreamRow: workstreamRow, goal: goal, artifact: artifact, nextItemID: next.BacklogItemID, now: now,
	}
}

func rejectedCancellationRequest() ReviseRequest {
	return ReviseRequest{
		ExpectedRevision: 7, TargetDeliveryState: domainbacklog.DeliveryRejected,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryRejected, Kind: "owner_cancellation", Ref: "owner-cancel-ref"}},
		Reason:       "  owner decided to cancel  ",
	}
}

func TestReviseRejectedCancellationRequiredFieldsFailBeforeWrites(t *testing.T) {
	tests := []struct {
		name    string
		request ReviseRequest
	}{
		{name: "reason", request: ReviseRequest{ExpectedRevision: 7, TargetDeliveryState: domainbacklog.DeliveryRejected, Reason: "  "}},
		{name: "expected revision", request: ReviseRequest{ExpectedRevision: 0, TargetDeliveryState: domainbacklog.DeliveryRejected, Reason: "owner cancellation"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRejectedCancellationFixture(nil)
			if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), test.request); err == nil {
				t.Fatal("required cancellation field was accepted")
			}
			if fixture.items.saveCount != 0 || len(fixture.workstream.stageReceipts) != 0 || len(fixture.workstream.closureReceipts) != 0 || len(fixture.workstream.workstreams) != 1 || len(fixture.workstream.goals) != 1 || len(fixture.workstream.artifacts) != 1 {
				t.Fatalf("invalid cancellation request wrote state: item saves=%d stages=%d closures=%d resources=%d/%d/%d", fixture.items.saveCount, len(fixture.workstream.stageReceipts), len(fixture.workstream.closureReceipts), len(fixture.workstream.workstreams), len(fixture.workstream.goals), len(fixture.workstream.artifacts))
			}
			if fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != fixture.item.ImplementationUnit {
				t.Fatalf("invalid cancellation request changed lease: %+v", fixture.workstream.lease)
			}
		})
	}
}

func TestReviseRejectedCancellationArchivesResourcesAndClosesBeforeStageCompletion(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	request := rejectedCancellationRequest()
	result, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), request)
	if err != nil {
		t.Fatalf("Revise(REJECTED): %v", err)
	}
	if result.DeliveryState != domainbacklog.DeliveryRejected || result.CheckOK || result.Implementation != "owner decided to cancel" {
		t.Fatalf("cancellation item=%+v", result)
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.IdempotencyKey != key || stage.Status != domainworkstream.StageRunCompleted || stage.DeliveryState != domainbacklog.DeliveryRejected || stage.ActionID.Validate() != nil {
		t.Fatalf("completed rejected stage receipt=%+v found=%v err=%v", stage, found, err)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.IdempotencyKey != key || closure.ActionID != stage.ActionID || closure.TargetDeliveryState != domainbacklog.DeliveryRejected || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone || !closure.LeaseReleased {
		t.Fatalf("completed rejected closure=%+v found=%v err=%v", closure, found, err)
	}
	workstreams, err := fixture.workstream.ListWorkstreams(context.Background(), 1<<20)
	if err != nil || len(workstreams) == 0 {
		t.Fatalf("read archived workstream: count=%d err=%v", len(workstreams), err)
	}
	wantWorkstream := fixture.workstreamRow
	wantWorkstream.Status = domainworkstream.StatusArchived
	wantWorkstream.UpdatedAt = fixture.now
	if !reflect.DeepEqual(workstreams[0], wantWorkstream) {
		t.Fatalf("archived workstream did not retain its fields: got=%+v want=%+v", workstreams[0], wantWorkstream)
	}
	goal, found, err := fixture.workstream.FindGoalByID(context.Background(), fixture.goal.GoalID)
	if err != nil || !found {
		t.Fatalf("read archived goal: found=%v err=%v", found, err)
	}
	wantGoal := fixture.goal
	wantGoal.Status = domainworkstream.StatusArchived
	if !reflect.DeepEqual(goal, wantGoal) {
		t.Fatalf("archived goal did not retain its fields: got=%+v want=%+v", goal, wantGoal)
	}
	artifacts, err := fixture.workstream.ListArtifacts(context.Background(), 1<<20)
	if err != nil || len(artifacts) == 0 {
		t.Fatalf("read archived artifact: count=%d err=%v", len(artifacts), err)
	}
	wantArtifact := fixture.artifact
	wantArtifact.Status = domainworkstream.StatusArchived
	wantArtifact.UpdatedAt = fixture.now
	if !reflect.DeepEqual(artifacts[0], wantArtifact) {
		t.Fatalf("archived artifact did not retain its payload and fields: got=%+v want=%+v", artifacts[0], wantArtifact)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("cancellation retained lease: %+v", fixture.workstream.lease)
	}
	projection, err := fixture.service.Projection(context.Background())
	if err != nil || len(projection.Current) != 0 {
		t.Fatalf("REJECTED cancellation projected as Current: current=%+v err=%v", projection.Current, err)
	}
}

func TestReviseRejectedCancellationReplayIsIdempotentAndPayloadBound(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	request := rejectedCancellationRequest()
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), request); err != nil {
		t.Fatalf("initial cancellation: %v", err)
	}
	foreignLease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "another-unit",
		HolderWorkstreamID: "another-workstream", Stage: domainbacklog.DeliverySpec, Revision: "1",
	}
	fixture.workstream.lease = &foreignLease
	itemSaveCount := fixture.items.saveCount
	workstreamCount, goalCount, artifactCount := len(fixture.workstream.workstreams), len(fixture.workstream.goals), len(fixture.workstream.artifacts)
	stageCount, closureCount := len(fixture.workstream.stageReceipts), len(fixture.workstream.closureReceipts)
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), request); err != nil {
		t.Fatalf("same-payload cancellation replay: %v", err)
	}
	if fixture.items.saveCount != itemSaveCount || len(fixture.workstream.workstreams) != workstreamCount || len(fixture.workstream.goals) != goalCount || len(fixture.workstream.artifacts) != artifactCount || len(fixture.workstream.stageReceipts) != stageCount || len(fixture.workstream.closureReceipts) != closureCount {
		t.Fatalf("same-payload replay added state: item saves %d->%d resources %d/%d/%d stages %d closures %d", itemSaveCount, fixture.items.saveCount, len(fixture.workstream.workstreams), len(fixture.workstream.goals), len(fixture.workstream.artifacts), len(fixture.workstream.stageReceipts), len(fixture.workstream.closureReceipts))
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, foreignLease) {
		t.Fatalf("completed replay changed an unrelated lease: %+v", fixture.workstream.lease)
	}
	request.Reason = "a changed cancellation reason"
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), request); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("changed cancellation payload error=%v, want ErrLifecycleConflict", err)
	}
}

func TestRecoverRejectedCancellationAfterLeaseReleaseWithoutLease(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure was hidden")
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("fixture must reach the post-release crash point: %+v", fixture.workstream.lease)
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Phase != domainworkstream.ClosurePhaseLease {
		t.Fatalf("fixture closure phase=%+v found=%v err=%v", closure, found, err)
	}
	fixture.items.failRejectedSave = false
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover rejected cancellation without lease: %v", err)
	}
	item := fixture.items.items[0]
	if item.DeliveryState != domainbacklog.DeliveryRejected || item.Implementation != "owner decided to cancel" {
		t.Fatalf("recovered item=%+v", item)
	}
	stage, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunCompleted {
		t.Fatalf("recovered stage=%+v found=%v err=%v", stage, found, err)
	}
	closure, found, err = fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone || closure.Error != "" || closure.ActionID != stage.ActionID {
		t.Fatalf("recovered closure=%+v found=%v err=%v", closure, found, err)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("recovery reacquired a lease: %+v", fixture.workstream.lease)
	}
}

type failRejectedCancellationDoneClosureStore struct {
	*memoryWorkstreamStore
	failedDoneSave bool
}

func (s *failRejectedCancellationDoneClosureStore) SaveClosureReceipt(ctx context.Context, receipt domainworkstream.ClosureReceipt) error {
	if receipt.TargetDeliveryState == domainbacklog.DeliveryRejected && receipt.Phase == domainworkstream.ClosurePhaseDone && !s.failedDoneSave {
		s.failedDoneSave = true
		return errors.New("simulated stop after rejected item persistence")
	}
	return s.memoryWorkstreamStore.SaveClosureReceipt(ctx, receipt)
}

func TestRecoverPreparedRejectedCancellationAfterJSONLStoreNormalization(t *testing.T) {
	ctx := context.Background()
	fixture := newRejectedCancellationFixture(nil)
	items := backlogstore.NewJSONLStore(filepath.Join(t.TempDir(), "backlog.jsonl"))
	preparedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var seedJSONL []byte
	for _, item := range fixture.items.items {
		item.CreatedAt = preparedAt.Add(-24 * time.Hour).Format(time.RFC3339)
		item = backlogstore.NormalizeForSave(item, preparedAt)
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("encode old canonical backlog fixture: %v", err)
		}
		seedJSONL = append(seedJSONL, encoded...)
		seedJSONL = append(seedJSONL, '\n')
	}
	if err := os.WriteFile(items.Path(), seedJSONL, 0o600); err != nil {
		t.Fatalf("seed old canonical backlog JSONL rows: %v", err)
	}
	fixture.service.items = items
	closureStore := &failRejectedCancellationDoneClosureStore{memoryWorkstreamStore: fixture.workstream}
	fixture.service.workstream = closureStore

	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated stop after rejected item persistence was hidden")
	} else if err.Error() != "simulated stop after rejected item persistence" {
		t.Fatalf("Revise stopped at an unexpected boundary: %v", err)
	}
	if !closureStore.failedDoneSave {
		t.Fatal("closure failure injection did not reach the done phase")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stageBefore, found, err := closureStore.FindStageRunReceipt(ctx, key)
	if err != nil || !found || stageBefore.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("crash-boundary stage=%+v found=%v err=%v", stageBefore, found, err)
	}
	var preparedItem domainbacklog.Item
	if err := json.Unmarshal([]byte(stageBefore.ResultJSON), &preparedItem); err != nil {
		t.Fatalf("decode prepared stage result: %v", err)
	}
	currentBefore, found, err := items.FindByID(ctx, string(fixture.item.BacklogItemID))
	if err != nil || !found || currentBefore.DeliveryState != domainbacklog.DeliveryRejected {
		t.Fatalf("persisted rejected item=%+v found=%v err=%v", currentBefore, found, err)
	}
	if currentBefore.UpdatedAt == preparedItem.UpdatedAt {
		t.Fatalf("fixture did not reproduce owner timestamp normalization: prepared=%q persisted=%q", preparedItem.UpdatedAt, currentBefore.UpdatedAt)
	}
	preparedWithPersistedTimestamp := preparedItem
	preparedWithPersistedTimestamp.UpdatedAt = currentBefore.UpdatedAt
	if !reflect.DeepEqual(currentBefore, preparedWithPersistedTimestamp) {
		t.Fatalf("JSONL persistence changed fields beyond UpdatedAt: persisted=%+v prepared=%+v", currentBefore, preparedItem)
	}
	closureBefore, found, err := closureStore.FindClosureReceipt(ctx, key)
	if err != nil || !found || closureBefore.Phase != domainworkstream.ClosurePhaseLease || closureBefore.Status != domainworkstream.ClosureStatusPrepared {
		t.Fatalf("crash-boundary closure=%+v found=%v err=%v", closureBefore, found, err)
	}
	if closureStore.lease != nil {
		t.Fatalf("crash boundary should follow lease release: %+v", closureStore.lease)
	}
	backlogBefore, err := os.ReadFile(items.Path())
	if err != nil {
		t.Fatalf("read backlog JSONL before recovery: %v", err)
	}

	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover prepared rejected cancellation after JSONL normalization: %v", err)
	}
	backlogAfter, err := os.ReadFile(items.Path())
	if err != nil {
		t.Fatalf("read backlog JSONL after recovery: %v", err)
	}
	if !reflect.DeepEqual(backlogAfter, backlogBefore) {
		t.Fatal("recovery rewrote the already persisted rejected backlog item")
	}
	currentAfter, found, err := items.FindByID(ctx, string(fixture.item.BacklogItemID))
	if err != nil || !found || !reflect.DeepEqual(currentAfter, currentBefore) {
		t.Fatalf("recovery changed the normalized terminal item: before=%+v after=%+v found=%v err=%v", currentBefore, currentAfter, found, err)
	}
	stageAfter, found, err := closureStore.FindStageRunReceipt(ctx, key)
	if err != nil || !found || stageAfter.Status != domainworkstream.StageRunCompleted {
		t.Fatalf("recovered stage=%+v found=%v err=%v", stageAfter, found, err)
	}
	closureAfter, found, err := closureStore.FindClosureReceipt(ctx, key)
	if err != nil || !found || closureAfter.Status != domainworkstream.ClosureStatusCompleted || closureAfter.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("recovered closure=%+v found=%v err=%v", closureAfter, found, err)
	}
}

func TestPreparedRejectedTerminalItemComparisonOnlyToleratesUpdatedAt(t *testing.T) {
	prepared := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("prepared-item"),
		Kind: "idea", Title: "Prepared title", Body: "Prepared body", Source: "prepared source",
		Owner: "prepared owner", OwnerModule: domainbacklog.LifecycleOwnerModule,
		ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryRejected,
		Status: "rejected", CheckOK: false, Implementation: "owner cancellation",
		ImplementationUnit: "unit-prepared-item", ImplementationRevision: 4, WorkstreamID: "ws-prepared-item",
		CreatedAt: "2026-10-15T00:00:00Z", UpdatedAt: "2026-10-15T01:00:00Z",
		SourceRefs:   []domainbacklog.SourceRef{{Type: "manual", Locator: "source-ref", CapturedAt: "2026-10-15T00:00:00Z", RawOrSummary: "source summary"}},
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryRejected, Kind: "owner_cancellation", Ref: "cancel-ref"}},
		Tags:         []string{"preserve"},
	}
	persisted := prepared
	persisted.UpdatedAt = "2026-10-16T01:00:00Z"
	if !preparedRejectedTerminalItemMatches(persisted, prepared) {
		t.Fatal("owner-normalized UpdatedAt should not invalidate an otherwise exact prepared terminal item")
	}

	tests := []struct {
		name   string
		mutate func(*domainbacklog.Item)
	}{
		{name: "title", mutate: func(item *domainbacklog.Item) { item.Title = "changed title" }},
		{name: "body", mutate: func(item *domainbacklog.Item) { item.Body = "changed body" }},
		{name: "source", mutate: func(item *domainbacklog.Item) { item.Source = "changed source" }},
		{name: "source refs", mutate: func(item *domainbacklog.Item) { item.SourceRefs[0].Locator = "changed-source-ref" }},
		{name: "evidence", mutate: func(item *domainbacklog.Item) { item.EvidenceRefs[0].Ref = "changed-evidence" }},
		{name: "backlog identity", mutate: func(item *domainbacklog.Item) { item.BacklogItemID = modulecore.BacklogItemID("other-item") }},
		{name: "implementation unit", mutate: func(item *domainbacklog.Item) { item.ImplementationUnit = "other-unit" }},
		{name: "implementation revision", mutate: func(item *domainbacklog.Item) { item.ImplementationRevision++ }},
		{name: "workstream identity", mutate: func(item *domainbacklog.Item) { item.WorkstreamID = "other-workstream" }},
		{name: "created at", mutate: func(item *domainbacklog.Item) { item.CreatedAt = "2026-10-15T00:00:01Z" }},
		{name: "owner", mutate: func(item *domainbacklog.Item) { item.Owner = "changed owner" }},
		{name: "implementation reason", mutate: func(item *domainbacklog.Item) { item.Implementation = "changed reason" }},
		{name: "legacy status", mutate: func(item *domainbacklog.Item) { item.Status = "blocked" }},
		{name: "legacy check", mutate: func(item *domainbacklog.Item) { item.CheckOK = true }},
		{name: "other metadata", mutate: func(item *domainbacklog.Item) { item.Tags[0] = "changed-tag" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := persisted
			changed.SourceRefs = append([]domainbacklog.SourceRef(nil), persisted.SourceRefs...)
			changed.EvidenceRefs = append([]domainbacklog.EvidenceRef(nil), persisted.EvidenceRefs...)
			changed.Tags = append([]string(nil), persisted.Tags...)
			test.mutate(&changed)
			if preparedRejectedTerminalItemMatches(changed, prepared) {
				t.Fatalf("prepared terminal comparison accepted changed %s", test.name)
			}
		})
	}
}

func TestRecoverRejectedCancellationRequiresMatchingLeaseWorkstream(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Phase != domainworkstream.ClosurePhaseLease {
		t.Fatalf("fixture closure phase=%+v found=%v err=%v", closure, found, err)
	}
	fixture.workstream.lease = &domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: fixture.item.ImplementationUnit,
		HolderWorkstreamID: "different-workstream", Stage: fixture.item.DeliveryState, Revision: "7",
	}
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	fixture.items.failRejectedSave = false
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover rejected cancellation with a same-unit, different-workstream lease error=%v, want ErrLifecycleConflict", err)
	}
	if fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != fixture.item.ImplementationUnit || fixture.workstream.lease.HolderWorkstreamID != "different-workstream" {
		t.Fatalf("recovery released or changed the nonmatching lease: %+v", fixture.workstream.lease)
	}
	if !reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) || !reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) ||
		!reflect.DeepEqual(fixture.workstream.goals, goalsBefore) || !reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) {
		t.Fatal("recovery with a foreign lease changed closure or resource state")
	}
}

func TestRecoverRejectedCancellationRejectsWorkstreamMismatchBeforeCompletion(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("fixture stage receipt=%+v found=%v err=%v", stage, found, err)
	}
	item := fixture.items.items[0]
	item.WorkstreamID = "rebound-workstream"
	fixture.items.items[0] = item
	saveCount := fixture.items.saveCount
	fixture.items.failRejectedSave = false
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover prepared rejected cancellation after WorkstreamID changed error=%v, want ErrLifecycleConflict", err)
	}
	if fixture.items.saveCount != saveCount || fixture.items.items[0].WorkstreamID != "rebound-workstream" || fixture.items.items[0].DeliveryState != domainbacklog.DeliverySpec {
		t.Fatalf("recovery changed mismatched live item: saves=%d->%d item=%+v", saveCount, fixture.items.saveCount, fixture.items.items[0])
	}
}

func TestRecoverRejectedCancellationRejectsWorkstreamMismatchAfterCompletion(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err != nil {
		t.Fatalf("initial cancellation: %v", err)
	}
	item := fixture.items.items[0]
	item.WorkstreamID = "rebound-workstream"
	fixture.items.items[0] = item
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover completed rejected cancellation after WorkstreamID changed error=%v, want ErrLifecycleConflict", err)
	}
	if fixture.items.items[0].WorkstreamID != "rebound-workstream" {
		t.Fatalf("recovery changed mismatched live item workstream: %+v", fixture.items.items[0])
	}
}

type failFirstRejectedClosureStore struct {
	memoryWorkstreamStore
	failedPreparedClosure bool
}

func (s *failFirstRejectedClosureStore) SaveClosureReceipt(ctx context.Context, receipt domainworkstream.ClosureReceipt) error {
	if receipt.TargetDeliveryState == domainbacklog.DeliveryRejected && !s.failedPreparedClosure {
		s.failedPreparedClosure = true
		return errors.New("simulated stop before rejected closure prepare")
	}
	return s.memoryWorkstreamStore.SaveClosureReceipt(ctx, receipt)
}

func TestRecoverRejectedCancellationFromPreparedStageWithoutClosure(t *testing.T) {
	store := &failFirstRejectedClosureStore{}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	fixture.service.workstream = store
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated stop before closure prepare was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := store.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("prepared stage receipt=%+v found=%v err=%v", stage, found, err)
	}
	if _, found, err := store.FindClosureReceipt(context.Background(), key); err != nil || found {
		t.Fatalf("closure should be missing at injected stop found=%v err=%v", found, err)
	}
	if store.lease == nil || store.lease.HolderUnitID != fixture.item.ImplementationUnit {
		t.Fatalf("lease must remain held before closure intent: %+v", store.lease)
	}
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover must create the closure from its prepared stage receipt: %v", err)
	}
	item := fixture.items.items[0]
	if item.DeliveryState != domainbacklog.DeliveryRejected {
		t.Fatalf("recovered item=%+v", item)
	}
	stage, found, err = store.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunCompleted {
		t.Fatalf("recovered stage=%+v found=%v err=%v", stage, found, err)
	}
	closure, found, err := store.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone || closure.ActionID != stage.ActionID {
		t.Fatalf("recovered closure=%+v found=%v err=%v", closure, found, err)
	}
}

func TestRecoverPreparedRejectedCancellationWithForeignLeaseHasNoWrites(t *testing.T) {
	store := &failFirstRejectedClosureStore{}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	fixture.service.workstream = store
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated stop before closure prepare was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stageBefore, found, err := store.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stageBefore.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("fixture stage=%+v found=%v err=%v", stageBefore, found, err)
	}
	if _, found, err := store.FindClosureReceipt(context.Background(), key); err != nil || found {
		t.Fatalf("closure should be absent before recovery: found=%v err=%v", found, err)
	}
	foreignLease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: "foreign-unit",
		HolderWorkstreamID: "foreign-workstream", Stage: domainbacklog.DeliverySpec, Revision: "1",
	}
	fixture.workstream.lease = &foreignLease
	itemBefore := fixture.items.items[0]
	stagesBefore := append([]domainworkstream.StageRunReceipt(nil), fixture.workstream.stageReceipts...)
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	saveCountBefore := fixture.items.saveCount
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover with a foreign lease error=%v, want ErrLifecycleConflict", err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) || fixture.items.saveCount != saveCountBefore ||
		!reflect.DeepEqual(fixture.workstream.stageReceipts, stagesBefore) || !reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) ||
		!reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) || !reflect.DeepEqual(fixture.workstream.goals, goalsBefore) ||
		!reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) {
		t.Fatal("recovery with a foreign lease changed item, receipt, or resource state")
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, foreignLease) {
		t.Fatalf("recovery changed the foreign lease: %+v", fixture.workstream.lease)
	}
}

func TestRejectedCancellationRejectsTerminalItemWithoutImplementationIdentity(t *testing.T) {
	newFixture := func() *rejectedCancellationFixture {
		fixture := newRejectedCancellationFixture(nil)
		item := fixture.items.items[0]
		item.DeliveryState = domainbacklog.DeliveryRejected
		item.ImplementationUnit = "  "
		fixture.items.items[0] = item
		fixture.workstream.lease = nil
		return fixture
	}
	t.Run("recover", func(t *testing.T) {
		fixture := newFixture()
		err := fixture.service.Recover(backlogActionContext(t))
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("Recover terminal REJECTED item without implementation identity error=%v, want ErrLifecycleConflict", err)
		}
		if fixture.workstream.lease != nil {
			t.Fatalf("Recover acquired a lease after rejecting malformed terminal item: %+v", fixture.workstream.lease)
		}
	})
	t.Run("acquire runnable", func(t *testing.T) {
		fixture := newFixture()
		result, err := fixture.service.AcquireRunnable(backlogActionContext(t))
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("AcquireRunnable terminal REJECTED item without implementation identity result=%+v error=%v, want ErrLifecycleConflict", result, err)
		}
		if fixture.workstream.lease != nil {
			t.Fatalf("AcquireRunnable acquired a lease after rejecting malformed terminal item: %+v", fixture.workstream.lease)
		}
	})
}

func TestRejectedCancellationRejectsStageBacklogItemCollision(t *testing.T) {
	newFixture := func(t *testing.T) *rejectedCancellationFixture {
		fixture := newRejectedCancellationFixture(nil)
		if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err != nil {
			t.Fatalf("seed completed cancellation: %v", err)
		}
		collision := domainbacklog.Item{
			SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("cancel-item-collision"),
			Title: "Colliding rejected item", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryRejected, Implementation: "different cancellation result",
			ImplementationUnit: fixture.item.ImplementationUnit, WorkstreamID: fixture.item.WorkstreamID,
			ImplementationRevision: fixture.item.ImplementationRevision,
		}
		fixture.items.items = append(fixture.items.items, collision)
		return fixture
	}
	t.Run("recover", func(t *testing.T) {
		fixture := newFixture(t)
		err := fixture.service.Recover(backlogActionContext(t))
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("Recover colliding rejected item error=%v, want ErrLifecycleConflict", err)
		}
		if fixture.workstream.lease != nil {
			t.Fatalf("Recover acquired a lease after rejecting colliding terminal item: %+v", fixture.workstream.lease)
		}
	})
	t.Run("acquire runnable", func(t *testing.T) {
		fixture := newFixture(t)
		result, err := fixture.service.AcquireRunnable(backlogActionContext(t))
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("AcquireRunnable colliding rejected item result=%+v error=%v, want ErrLifecycleConflict", result, err)
		}
		if fixture.workstream.lease != nil {
			t.Fatalf("AcquireRunnable acquired a lease after rejecting colliding terminal item: %+v", fixture.workstream.lease)
		}
	})
}

type blockedRejectedStageStore struct {
	memoryWorkstreamStore
	prepared       chan struct{}
	continueSave   chan struct{}
	preparedOnce   sync.Once
	acquireSawDone bool
}

func (s *blockedRejectedStageStore) SaveStageRunReceipt(ctx context.Context, item domainworkstream.StageRunReceipt) error {
	err := s.memoryWorkstreamStore.SaveStageRunReceipt(ctx, item)
	if err != nil {
		return err
	}
	if item.TargetStage == domainbacklog.DeliveryRejected && item.Status == domainworkstream.StageRunPrepared {
		s.preparedOnce.Do(func() { close(s.prepared) })
		<-s.continueSave
	}
	return nil
}

func (s *blockedRejectedStageStore) AcquireImplementationLeaseIfUnfrozen(ctx context.Context, lease domainworkstream.ImplementationLease) (bool, string, error) {
	for _, receipt := range s.closureReceipts {
		if receipt.TargetDeliveryState == domainbacklog.DeliveryRejected && (receipt.Status != domainworkstream.ClosureStatusCompleted || receipt.Phase != domainworkstream.ClosurePhaseDone) {
			return false, domainworkstream.ErrQueueFrozen.Error(), nil
		}
	}
	if len(s.closureReceipts) > 0 {
		for _, receipt := range s.closureReceipts {
			if receipt.TargetDeliveryState == domainbacklog.DeliveryRejected && receipt.Status == domainworkstream.ClosureStatusCompleted && receipt.Phase == domainworkstream.ClosurePhaseDone {
				s.acquireSawDone = true
			}
		}
	}
	return s.memoryWorkstreamStore.AcquireImplementationLeaseIfUnfrozen(ctx, lease)
}

func TestConcurrentAcquireRunnableWaitsForRejectedCancellationClosure(t *testing.T) {
	store := &blockedRejectedStageStore{prepared: make(chan struct{}), continueSave: make(chan struct{})}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	// Keep the same wrapper as the Service's store after the fixture has seeded it.
	fixture.service.workstream = store

	reviseDone := make(chan error, 1)
	reviseCtx := backlogActionContext(t)
	go func() {
		_, err := fixture.service.Revise(reviseCtx, string(fixture.item.BacklogItemID), rejectedCancellationRequest())
		reviseDone <- err
	}()
	<-store.prepared

	type acquireResult struct {
		result AcquireRunnableResult
		err    error
	}
	acquireDone := make(chan acquireResult, 1)
	acquireCtx := backlogActionContext(t)
	go func() {
		result, err := fixture.service.AcquireRunnable(acquireCtx)
		acquireDone <- acquireResult{result: result, err: err}
	}()
	select {
	case result := <-acquireDone:
		t.Fatalf("AcquireRunnable crossed the stage/closure gap: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(store.continueSave)
	if err := <-reviseDone; err != nil {
		t.Fatalf("Revise cancellation: %v", err)
	}
	result := <-acquireDone
	if result.err != nil || !result.result.Acquired || result.result.Item.BacklogItemID != fixture.nextItemID || !store.acquireSawDone {
		t.Fatalf("queued acquisition must happen only after closure completion: result=%+v err=%v closure-complete-at-acquire=%v", result.result, result.err, store.acquireSawDone)
	}
}

func TestConceptRejectedItemWithoutUnitDoesNotBlockRecoveryOrNextQueueItem(t *testing.T) {
	newFixture := func() *rejectedCancellationFixture {
		fixture := newRejectedCancellationFixture(nil)
		concept := fixture.items.items[0]
		concept.ConceptState = domainbacklog.ConceptRejected
		concept.DeliveryState = domainbacklog.DeliveryRejected
		concept.ImplementationUnit = ""
		concept.WorkstreamID = ""
		concept.ImplementationRevision = 0
		fixture.items.items[0] = concept
		fixture.workstream.lease = nil
		fixture.workstream.workstreams = nil
		fixture.workstream.goals = nil
		fixture.workstream.artifacts = nil
		return fixture
	}
	t.Run("recover", func(t *testing.T) {
		fixture := newFixture()
		if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
			t.Fatalf("Recover must ignore a concept rejection with no implementation unit: %v", err)
		}
	})
	t.Run("acquire next queued item", func(t *testing.T) {
		fixture := newFixture()
		result, err := fixture.service.AcquireRunnable(backlogActionContext(t))
		if err != nil {
			t.Fatalf("AcquireRunnable after concept rejection: %v", err)
		}
		if !result.Acquired || result.Item.BacklogItemID != fixture.nextItemID {
			t.Fatalf("concept rejection blocked the next queued item: result=%+v", result)
		}
	})
}

func seedLegacyCompletedRejectedStageWithoutClosure(t *testing.T, fixture *rejectedCancellationFixture) (domainworkstream.StageRunReceipt, domainbacklog.Item) {
	t.Helper()
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err != nil {
		t.Fatalf("seed completed rejected cancellation: %v", err)
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunCompleted {
		t.Fatalf("seed legacy completed stage: stage=%+v found=%v err=%v", stage, found, err)
	}
	item := fixture.items.items[0]
	fixture.workstream.closureReceipts = nil
	fixture.workstream.workstreams = []domainworkstream.Workstream{fixture.workstreamRow}
	fixture.workstream.goals = []domainworkstream.Goal{fixture.goal}
	fixture.workstream.artifacts = []domainworkstream.Artifact{fixture.artifact}
	return stage, item
}

func makeLegacyCompletedRejectedStageWithoutClosure(t *testing.T) (*rejectedCancellationFixture, domainworkstream.StageRunReceipt, domainbacklog.Item) {
	t.Helper()
	fixture := newRejectedCancellationFixture(nil)
	stage, item := seedLegacyCompletedRejectedStageWithoutClosure(t, fixture)
	return fixture, stage, item
}

func seedLegacyPreparedRejectedStage(t *testing.T, fixture *rejectedCancellationFixture, itemIndex int, terminalItemSaved bool, expectedRevision int) (domainworkstream.StageRunReceipt, domainbacklog.Item, domainbacklog.Item) {
	t.Helper()
	before := fixture.items.items[itemIndex]
	before.DeliveryState = domainbacklog.DeliveryQueued
	before.Implementation = ""
	before = domainbacklog.ProjectLegacy(before)
	fixture.items.items[itemIndex] = before
	if itemIndex == 0 {
		fixture.item = before
	}
	if fixture.workstream.lease != nil && fixture.workstream.lease.HolderUnitID == before.ImplementationUnit {
		fixture.workstream.lease.Stage = domainbacklog.DeliveryQueued
	}
	if itemIndex != 0 {
		fixture.workstream.workstreams = append(fixture.workstream.workstreams, domainworkstream.Workstream{
			WorkstreamID: before.WorkstreamID, Name: "legacy workstream", Status: domainworkstream.StatusActive,
			CreatedAt: fixture.now, UpdatedAt: fixture.now,
		})
		fixture.workstream.goals = append(fixture.workstream.goals, domainworkstream.Goal{
			GoalID: "goal_atlas_" + safeSegment(string(before.BacklogItemID)), WorkstreamID: before.WorkstreamID,
			Title: "legacy goal", Status: domainworkstream.StatusWaiting, CreatedAt: fixture.now,
		})
		fixture.workstream.artifacts = append(fixture.workstream.artifacts, domainworkstream.Artifact{
			ArtifactID: "artifact_atlas_" + safeSegment(string(before.BacklogItemID)), WorkstreamID: before.WorkstreamID,
			Title: "legacy artifact", Status: "pending", CreatedAt: fixture.now, UpdatedAt: fixture.now,
		})
	}

	refs := []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryRejected, Kind: "owner_cancellation", Ref: "legacy-empty-reason"}}
	next, err := domainbacklog.TransitionDelivery(before, domainbacklog.DeliveryRejected, refs)
	if err != nil {
		t.Fatalf("prepare legacy rejected result: %v", err)
	}
	next.Implementation = ""
	next.ImplementationRevision = before.ImplementationRevision
	resultJSON, err := json.Marshal(next)
	if err != nil {
		t.Fatalf("marshal legacy rejected result: %v", err)
	}
	request := ReviseRequest{
		ExpectedRevision: expectedRevision, TargetDeliveryState: domainbacklog.DeliveryRejected,
		EvidenceRefs: refs,
	}
	key := stageRunKey(before.ImplementationUnit, before.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage := domainworkstream.StageRunReceipt{
		ReceiptID: modulecore.NewReceiptID(), IdempotencyKey: key, ActionID: modulecore.NewActionID(),
		TransitionEventID: modulecore.NewEventID(), UnitID: before.ImplementationUnit, BacklogItemID: before.BacklogItemID,
		ImplementationRevision: before.ImplementationRevision, TargetStage: domainbacklog.DeliveryRejected,
		PayloadHash: stagePayloadHash(request), Status: domainworkstream.StageRunPrepared,
		DeliveryState: domainbacklog.DeliveryRejected, ResultJSON: string(resultJSON), CreatedAt: fixture.now,
	}
	fixture.workstream.stageReceipts = append(fixture.workstream.stageReceipts, stage)
	if terminalItemSaved {
		fixture.items.items[itemIndex] = next
		if itemIndex == 0 {
			fixture.item = next
		}
		active := fixture.items.items[1-itemIndex]
		active.DeliveryState = domainbacklog.DeliverySpec
		active = domainbacklog.ProjectLegacy(active)
		fixture.items.items[1-itemIndex] = active
		fixture.workstream.lease = &domainworkstream.ImplementationLease{
			LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: active.ImplementationUnit,
			HolderWorkstreamID: active.WorkstreamID, Stage: active.DeliveryState, Revision: "1",
			AcquiredAt: fixture.now, HeartbeatAt: fixture.now,
		}
	}
	return stage, before, next
}

func makeLegacyPreparedRejectedStage(t *testing.T, terminalItemSaved bool, expectedRevision int) (*rejectedCancellationFixture, domainworkstream.StageRunReceipt, domainbacklog.Item, domainbacklog.Item) {
	t.Helper()
	fixture := newRejectedCancellationFixture(nil)
	stage, before, next := seedLegacyPreparedRejectedStage(t, fixture, 0, terminalItemSaved, expectedRevision)
	return fixture, stage, before, next
}

func assertLegacyReconciliationEvent(t *testing.T, sink *developmentEventSinkStub, closure domainworkstream.ClosureReceipt, stage domainworkstream.StageRunReceipt, boundary string) {
	t.Helper()
	for _, event := range sink.events {
		if event.EventID != closure.TransitionEventID {
			continue
		}
		if event.Fields["legacy_reconciliation"] != true ||
			event.Fields["historical_stage_receipt_id"] != string(stage.ReceiptID) ||
			event.Fields["historical_stage_idempotency_key"] != stage.IdempotencyKey ||
			event.Fields["legacy_reconciliation_boundary"] != boundary {
			t.Fatalf("legacy reconciliation event lacks historical provenance: event=%+v", event)
		}
		return
	}
	t.Fatalf("legacy reconciliation event %q not found", closure.TransitionEventID)
}

func TestRecoverReconcilesPreparedLegacyRejectedStageBeforeTerminalItemSaveWithOmittedExpectedRevision(t *testing.T) {
	fixture, stageBefore, _, next := makeLegacyPreparedRejectedStage(t, false, 0)
	sink := &developmentEventSinkStub{}
	fixture.service.WithDevelopmentEventSink(sink)
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover prepared legacy rejection before item save: %v", err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], next) {
		t.Fatalf("recovery did not apply the exact prepared terminal result: got=%+v want=%+v", fixture.items.items[0], next)
	}
	key := stageBefore.IdempotencyKey
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	wantStage := stageBefore
	wantStage.Status = domainworkstream.StageRunCompleted
	wantStage.CompletedAt = stageAfter.CompletedAt
	if err != nil || !found || stageAfter.CompletedAt.IsZero() || !reflect.DeepEqual(stageAfter, wantStage) {
		t.Fatalf("legacy prepared receipt was not preserved except for completion: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("legacy prepared closure=%+v found=%v err=%v", closure, found, err)
	}
	assertLegacyReconciliationEvent(t, sink, closure, stageBefore, "prepared_before_item_save")
}

func TestRecoverReconcilesPreparedLegacyRejectedStageBeforeTerminalItemSavePreservingNewLease(t *testing.T) {
	fixture, stageBefore, _, next := makeLegacyPreparedRejectedStage(t, false, 7)
	active := fixture.items.items[1]
	active.DeliveryState = domainbacklog.DeliverySpec
	active = domainbacklog.ProjectLegacy(active)
	fixture.items.items[1] = active
	foreignLease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: active.ImplementationUnit,
		HolderWorkstreamID: active.WorkstreamID, Stage: active.DeliveryState, Revision: "1",
		AcquiredAt: fixture.now, HeartbeatAt: fixture.now,
	}
	fixture.workstream.lease = &foreignLease
	sink := &developmentEventSinkStub{}
	fixture.service.WithDevelopmentEventSink(sink)

	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover prepared legacy queued rejection with a newer lease: %v", err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], next) {
		t.Fatalf("recovery did not apply the exact prepared terminal result: got=%+v want=%+v", fixture.items.items[0], next)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, foreignLease) {
		t.Fatalf("recovery changed the independent newer lease: got=%+v want=%+v", fixture.workstream.lease, foreignLease)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), stageBefore.IdempotencyKey)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("legacy prepared closure=%+v found=%v err=%v", closure, found, err)
	}
	assertLegacyReconciliationEvent(t, sink, closure, stageBefore, "prepared_before_item_save")
}

func TestRecoverDrainsMultiplePreparedLegacyQueuedRejectedStagesInOneCall(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	stageA, _, resultA := seedLegacyPreparedRejectedStage(t, fixture, 0, false, 0)
	stageB, _, resultB := seedLegacyPreparedRejectedStage(t, fixture, 1, false, 0)
	sink := &developmentEventSinkStub{}
	fixture.service.WithDevelopmentEventSink(sink)

	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover multiple prepared legacy queued rejections: %v", err)
	}
	for index, expected := range []struct {
		stage  domainworkstream.StageRunReceipt
		result domainbacklog.Item
	}{{stageA, resultA}, {stageB, resultB}} {
		if !reflect.DeepEqual(fixture.items.items[index], expected.result) {
			t.Fatalf("recovered item %d=%+v, want %+v", index, fixture.items.items[index], expected.result)
		}
		stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), expected.stage.IdempotencyKey)
		wantStage := expected.stage
		wantStage.Status = domainworkstream.StageRunCompleted
		wantStage.CompletedAt = stageAfter.CompletedAt
		if err != nil || !found || stageAfter.CompletedAt.IsZero() || !reflect.DeepEqual(stageAfter, wantStage) {
			t.Fatalf("legacy prepared receipt %q was not completed: after=%+v found=%v err=%v", expected.stage.IdempotencyKey, stageAfter, found, err)
		}
		closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), expected.stage.IdempotencyKey)
		if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
			t.Fatalf("legacy prepared closure %q=%+v found=%v err=%v", expected.stage.IdempotencyKey, closure, found, err)
		}
		assertLegacyReconciliationEvent(t, sink, closure, expected.stage, "prepared_before_item_save")
	}
}

func TestRecoverReconcilesPreparedLegacyRejectedStageAfterTerminalItemSavePreservingNewLease(t *testing.T) {
	fixture, stageBefore, _, terminal := makeLegacyPreparedRejectedStage(t, true, 7)
	sink := &developmentEventSinkStub{}
	fixture.service.WithDevelopmentEventSink(sink)
	leaseBefore := *fixture.workstream.lease
	itemBefore := fixture.items.items[0]
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover prepared legacy rejection after item save: %v", err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) {
		t.Fatalf("recovery rewrote the already terminal legacy item: got=%+v want=%+v", fixture.items.items[0], itemBefore)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Fatalf("recovery changed the independent newer lease: got=%+v want=%+v", fixture.workstream.lease, leaseBefore)
	}
	if !reflect.DeepEqual(itemBefore, terminal) {
		t.Fatalf("prepared result fixture differs from terminal item: item=%+v result=%+v", itemBefore, terminal)
	}
	key := stageBefore.IdempotencyKey
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	wantStage := stageBefore
	wantStage.Status = domainworkstream.StageRunCompleted
	wantStage.CompletedAt = stageAfter.CompletedAt
	if err != nil || !found || stageAfter.CompletedAt.IsZero() || !reflect.DeepEqual(stageAfter, wantStage) {
		t.Fatalf("legacy prepared receipt was not preserved except for completion: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("legacy prepared closure=%+v found=%v err=%v", closure, found, err)
	}
	assertLegacyReconciliationEvent(t, sink, closure, stageBefore, "prepared_after_item_save")
}

type saturatedRejectedStageReceiptStore struct {
	*memoryWorkstreamStore
}

func (s *saturatedRejectedStageReceiptStore) ListStageRunReceipts(_ context.Context, limit int) ([]domainworkstream.StageRunReceipt, error) {
	return make([]domainworkstream.StageRunReceipt, limit), nil
}

func TestRecoverFailsClosedWhenRejectedStageReceiptScanMayBeTruncated(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.service.workstream = &saturatedRejectedStageReceiptStore{memoryWorkstreamStore: fixture.workstream}
	itemBefore := fixture.items.items[0]
	leaseBefore := *fixture.workstream.lease
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover saturated stage receipt scan error=%v, want ErrLifecycleConflict", err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) || fixture.workstream.lease == nil ||
		!reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) || len(fixture.workstream.closureReceipts) != 0 {
		t.Fatalf("saturated stage scan mutated lifecycle state: item=%+v lease=%+v closures=%+v", fixture.items.items[0], fixture.workstream.lease, fixture.workstream.closureReceipts)
	}
}

func TestRecoverLegacyCompletedRejectedStageWithoutClosureRepairsRejectedClosure(t *testing.T) {
	t.Run("reconcile without rewriting stage or item", func(t *testing.T) {
		fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
		sink := &developmentEventSinkStub{}
		fixture.service.WithDevelopmentEventSink(sink)
		if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
			t.Errorf("Recover legacy completed rejected stage without closure: %v", err)
		}
		key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
		closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
		if err != nil || !found || closure.TargetDeliveryState != domainbacklog.DeliveryRejected ||
			closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone || !closure.LeaseReleased {
			t.Fatalf("legacy rejected closure=%+v found=%v err=%v", closure, found, err)
		}
		stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
		if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) {
			t.Fatalf("legacy stage history changed: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
		}
		itemAfter := fixture.items.items[0]
		if !reflect.DeepEqual(itemAfter, itemBefore) || itemAfter.DeliveryState != domainbacklog.DeliveryRejected || itemAfter.CheckOK {
			t.Fatalf("legacy item must remain the same rejected, non-success item: before=%+v after=%+v", itemBefore, itemAfter)
		}
		var closureEvent *DevelopmentEvent
		for index := range sink.events {
			if sink.events[index].EventID == closure.TransitionEventID {
				closureEvent = &sink.events[index]
				break
			}
		}
		if closureEvent == nil || closureEvent.Fields["legacy_reconciliation"] != true ||
			closureEvent.Fields["historical_stage_receipt_id"] != string(stageBefore.ReceiptID) ||
			closureEvent.Fields["historical_stage_idempotency_key"] != stageBefore.IdempotencyKey {
			t.Fatalf("legacy cleanup event lacks historical provenance: event=%+v", closureEvent)
		}
		if fixture.items.saveCount != 1 {
			t.Fatalf("closure reconciliation rewrote the terminal item: saves=%d, want only the initial cancellation write", fixture.items.saveCount)
		}
		workstreams, err := fixture.workstream.ListWorkstreams(context.Background(), 1<<20)
		if err != nil || len(workstreams) < 1 || workstreams[0].Status != domainworkstream.StatusArchived {
			t.Fatalf("legacy cleanup did not archive the active workstream: workstreams=%+v err=%v", workstreams, err)
		}
		goal, found, err := fixture.workstream.FindGoalByID(context.Background(), fixture.goal.GoalID)
		if err != nil || !found || goal.Status != domainworkstream.StatusArchived {
			t.Fatalf("legacy cleanup did not archive the active goal: goal=%+v found=%v err=%v", goal, found, err)
		}
		artifacts, err := fixture.workstream.ListArtifacts(context.Background(), 1<<20)
		if err != nil || len(artifacts) < 1 || artifacts[0].Status != domainworkstream.StatusArchived {
			t.Fatalf("legacy cleanup did not archive the active artifact: artifacts=%+v err=%v", artifacts, err)
		}
	})
	for _, test := range []struct {
		name   string
		unitID string
		wsID   string
	}{
		{name: "same unit different workstream", unitID: "unit-cancel-item", wsID: "legacy-lease-other-workstream"},
		{name: "different unit same workstream", unitID: "legacy-lease-other-unit", wsID: "ws-cancel-item"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
			lease := domainworkstream.ImplementationLease{
				LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: test.unitID,
				HolderWorkstreamID: test.wsID, Stage: domainbacklog.DeliveryRejected, Revision: "7",
			}
			fixture.workstream.lease = &lease
			if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
				t.Errorf("Recover legacy rejected stage with mismatched lease error=%v, want ErrLifecycleConflict", err)
			}
			if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, lease) {
				t.Errorf("legacy closure reconciliation released or changed mismatched lease: %+v", fixture.workstream.lease)
			}
			key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
			if _, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key); err != nil || found {
				t.Errorf("mismatched lease must block closure intent: found=%v err=%v", found, err)
			}
			stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
			if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) || !reflect.DeepEqual(fixture.items.items[0], itemBefore) {
				t.Errorf("mismatched lease changed terminal history: stage=%+v item=%+v found=%v err=%v", stageAfter, fixture.items.items[0], found, err)
			}
		})
	}
}

func rekeyRejectedCancellationFixture(fixture *rejectedCancellationFixture, suffix string) {
	item := fixture.items.items[0]
	item.BacklogItemID = modulecore.BacklogItemID("cancel-item-" + suffix)
	item.ImplementationUnit = "unit-cancel-item-" + suffix
	item.WorkstreamID = "ws-cancel-item-" + suffix
	fixture.items.items[0] = item
	fixture.item = item

	workstream := fixture.workstream.workstreams[0]
	workstream.WorkstreamID = item.WorkstreamID
	fixture.workstream.workstreams[0] = workstream
	fixture.workstreamRow = workstream

	goal := fixture.workstream.goals[0]
	goal.GoalID = "goal_atlas_" + safeSegment(string(item.BacklogItemID))
	goal.WorkstreamID = item.WorkstreamID
	fixture.workstream.goals[0] = goal
	fixture.goal = goal

	artifact := fixture.workstream.artifacts[0]
	artifact.ArtifactID = "artifact_atlas_" + safeSegment(string(item.BacklogItemID))
	artifact.WorkstreamID = item.WorkstreamID
	fixture.workstream.artifacts[0] = artifact
	fixture.artifact = artifact

	if fixture.workstream.lease != nil {
		fixture.workstream.lease.HolderUnitID = item.ImplementationUnit
		fixture.workstream.lease.HolderWorkstreamID = item.WorkstreamID
	}
}

func TestRecoverReconcilesMultipleLegacyRejectedCancellationsInOneCall(t *testing.T) {
	fixtureA, stageA, _ := makeLegacyCompletedRejectedStageWithoutClosure(t)

	fixtureB := newRejectedCancellationFixture(nil)
	rekeyRejectedCancellationFixture(fixtureB, "second")
	stageB, itemB := seedLegacyCompletedRejectedStageWithoutClosure(t, fixtureB)

	fixtureA.items.items[1] = itemB
	fixtureA.workstream.stageReceipts = append(fixtureA.workstream.stageReceipts, stageB)
	fixtureA.workstream.workstreams = append(fixtureA.workstream.workstreams, fixtureB.workstreamRow)
	fixtureA.workstream.goals = append(fixtureA.workstream.goals, fixtureB.goal)
	fixtureA.workstream.artifacts = append(fixtureA.workstream.artifacts, fixtureB.artifact)
	fixtureA.workstream.lease = nil
	itemsBefore := append([]domainbacklog.Item(nil), fixtureA.items.items...)

	first, err := fixtureA.service.AcquireRunnable(backlogActionContext(t))
	if err != nil || first.Acquired || first.Item.DeliveryState != domainbacklog.DeliveryRejected {
		t.Fatalf("AcquireRunnable legacy reconciliation result=%+v err=%v", first, err)
	}
	for _, stageBefore := range []domainworkstream.StageRunReceipt{stageA, stageB} {
		closure, found, err := fixtureA.workstream.FindClosureReceipt(context.Background(), stageBefore.IdempotencyKey)
		if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
			t.Fatalf("AcquireRunnable returned before completing legacy closure %q: closure=%+v found=%v err=%v", stageBefore.IdempotencyKey, closure, found, err)
		}
	}
	if err := fixtureA.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover after idempotent legacy reconciliation: %v", err)
	}
	for _, stageBefore := range []domainworkstream.StageRunReceipt{stageA, stageB} {
		key := stageBefore.IdempotencyKey
		closure, found, err := fixtureA.workstream.FindClosureReceipt(context.Background(), key)
		if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
			t.Errorf("legacy closure %q was not completed: closure=%+v found=%v err=%v", key, closure, found, err)
		}
		stageAfter, found, err := fixtureA.workstream.FindStageRunReceipt(context.Background(), key)
		if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) {
			t.Errorf("legacy stage %q was rewritten: before=%+v after=%+v found=%v err=%v", key, stageBefore, stageAfter, found, err)
		}
	}
	if !reflect.DeepEqual(fixtureA.items.items, itemsBefore) || fixtureA.items.saveCount != 1 {
		t.Errorf("legacy reconciliation rewrote an item: before=%+v after=%+v saves=%d", itemsBefore, fixtureA.items.items, fixtureA.items.saveCount)
	}
}

func makeTwoLegacyRejectedCancellationsWithoutClosures(t *testing.T) (*rejectedCancellationFixture, []domainworkstream.StageRunReceipt, []domainbacklog.Item) {
	t.Helper()
	fixture, stageA, itemA := makeLegacyCompletedRejectedStageWithoutClosure(t)
	fixtureB := newRejectedCancellationFixture(nil)
	rekeyRejectedCancellationFixture(fixtureB, "second")
	stageB, itemB := seedLegacyCompletedRejectedStageWithoutClosure(t, fixtureB)

	fixture.items.items[1] = itemB
	fixture.workstream.stageReceipts = append(fixture.workstream.stageReceipts, stageB)
	fixture.workstream.workstreams = append(fixture.workstream.workstreams, fixtureB.workstreamRow)
	fixture.workstream.goals = append(fixture.workstream.goals, fixtureB.goal)
	fixture.workstream.artifacts = append(fixture.workstream.artifacts, fixtureB.artifact)
	fixture.workstream.lease = nil
	return fixture, []domainworkstream.StageRunReceipt{stageA, stageB}, []domainbacklog.Item{itemA, itemB}
}

func assertLegacyRejectedClosuresCompleted(t *testing.T, fixture *rejectedCancellationFixture, stages []domainworkstream.StageRunReceipt) {
	t.Helper()
	for _, stage := range stages {
		closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), stage.IdempotencyKey)
		if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
			t.Errorf("legacy closure %q was not completed before route returned: closure=%+v found=%v err=%v", stage.IdempotencyKey, closure, found, err)
		}
	}
}

func TestRecoverDrainsAllLegacyRejectedCancellationsInOneCall(t *testing.T) {
	fixture, stages, _ := makeTwoLegacyRejectedCancellationsWithoutClosures(t)
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover multiple legacy rejected cancellations: %v", err)
	}
	assertLegacyRejectedClosuresCompleted(t, fixture, stages)
}

func TestAdoptDrainsAllLegacyRejectedCancellationsBeforeLeaseAdmission(t *testing.T) {
	fixture, stages, _ := makeTwoLegacyRejectedCancellationsWithoutClosures(t)
	candidate := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("adopt-after-legacy-cancellations"),
		Title: "candidate after historical cancellations", ConceptState: domainbacklog.ConceptCandidate,
		MaturationState: domainbacklog.MaturationStatePromoted,
	}
	fixture.items.items = append(fixture.items.items, candidate)
	result, err := fixture.service.Adopt(backlogActionContext(t), string(candidate.BacklogItemID), "owner selected")
	if err != nil {
		t.Fatalf("Adopt after multiple legacy rejected cancellations: %v", err)
	}
	assertLegacyRejectedClosuresCompleted(t, fixture, stages)
	if !result.LeaseAcquired || fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != result.Item.ImplementationUnit {
		t.Fatalf("Adopt did not acquire its lease after all legacy closures: result=%+v lease=%+v", result, fixture.workstream.lease)
	}
}

func TestResolveQueueFreezeDrainsAllLegacyRejectedCancellationsBeforeReplacementLease(t *testing.T) {
	fixture, stages, _ := makeTwoLegacyRejectedCancellationsWithoutClosures(t)
	blocked := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("blocked-after-legacy-cancellations"),
		ImplementationUnit: "unit-blocked-after-legacy-cancellations", WorkstreamID: "ws-blocked-after-legacy-cancellations",
		Title: "blocked implementation", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryBlocked, ImplementationRevision: 1,
	}
	replacement := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("replacement-after-legacy-cancellations"),
		ImplementationUnit: "unit-replacement-after-legacy-cancellations", WorkstreamID: "ws-replacement-after-legacy-cancellations",
		Title: "replacement implementation", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1,
		SupersedesUnitID: blocked.ImplementationUnit,
	}
	fixture.items.items = append(fixture.items.items, blocked, replacement)
	freeze := domainworkstream.QueueFreeze{
		FreezeID:      queueFreezeID(blocked.ImplementationUnit, blocked.ImplementationRevision),
		BlockedUnitID: blocked.ImplementationUnit, BlockedRevision: blocked.ImplementationRevision,
		FreezeRevision: 1, ReasonCode: "worker_failure", InvalidatedFromStage: domainbacklog.DeliverySpec,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Kind: "worker_failure", Ref: "blocked-ref"}},
		Status:       domainworkstream.QueueFreezeActive, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.workstream.SaveQueueFreeze(context.Background(), freeze); err != nil {
		t.Fatalf("seed active freeze: %v", err)
	}
	fixture.service.WithEvidenceVerifier(revision2Verifier{})
	resolved, lease, acquired, err := fixture.service.ResolveQueueFreeze(backlogActionContext(t), freeze.FreezeID, ResolveQueueFreezeRequest{
		ActionID: modulecore.NewActionID(), ExpectedFreezeRevision: 1,
		ReplacementUnitID: replacement.ImplementationUnit, SupersedesUnitID: blocked.ImplementationUnit,
		BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-ref"}},
	})
	if err != nil {
		t.Fatalf("ResolveQueueFreeze after multiple legacy rejected cancellations: %v", err)
	}
	assertLegacyRejectedClosuresCompleted(t, fixture, stages)
	if !acquired || resolved.Status != domainworkstream.QueueFreezeResolved || lease.HolderUnitID != replacement.ImplementationUnit ||
		fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != replacement.ImplementationUnit {
		t.Fatalf("replacement lease was not acquired after all legacy closures: freeze=%+v returned lease=%+v acquired=%v stored=%+v", resolved, lease, acquired, fixture.workstream.lease)
	}
}

func TestAcquireRunnableDoesNotSelectQueuedWorkBeforeAllLegacyClosures(t *testing.T) {
	fixture, stages, _ := makeTwoLegacyRejectedCancellationsWithoutClosures(t)
	queued := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("queued-after-legacy-cancellations"),
		ImplementationUnit: "unit-queued-after-legacy-cancellations", WorkstreamID: "ws-queued-after-legacy-cancellations",
		Title: "queued after historical cancellations", ConceptState: domainbacklog.ConceptAdopted,
		DeliveryState: domainbacklog.DeliveryQueued, ImplementationRevision: 1,
	}
	fixture.items.items = append(fixture.items.items, queued)
	result, err := fixture.service.AcquireRunnable(backlogActionContext(t))
	if err != nil {
		t.Fatalf("AcquireRunnable with multiple legacy cancellations: %v", err)
	}
	assertLegacyRejectedClosuresCompleted(t, fixture, stages)
	if result.Acquired || result.Item.BacklogItemID == queued.BacklogItemID || fixture.workstream.lease != nil {
		t.Fatalf("AcquireRunnable selected or leased queued work while draining cancellations: result=%+v lease=%+v", result, fixture.workstream.lease)
	}
}

func TestLegacyRejectedCleanupPreservesIndependentlyBoundActiveLease(t *testing.T) {
	fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
	active := fixture.items.items[1]
	active.DeliveryState = domainbacklog.DeliverySpec
	fixture.items.items[1] = active
	addRejectedCancellationResourcesForItem(fixture, active)
	lease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: active.ImplementationUnit,
		HolderWorkstreamID: active.WorkstreamID, Stage: active.DeliveryState, Revision: "1",
	}
	fixture.workstream.lease = &lease

	first, err := fixture.service.AcquireRunnable(backlogActionContext(t))
	if err != nil || first.Item.BacklogItemID != itemBefore.BacklogItemID {
		t.Fatalf("legacy cleanup alongside active lease result=%+v err=%v", first, err)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, lease) {
		t.Fatalf("legacy cleanup released or changed the newer lease: %+v", fixture.workstream.lease)
	}
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover with newer active lease after legacy cleanup: %v", err)
	}
	second, err := fixture.service.AcquireRunnable(backlogActionContext(t))
	if err != nil || second.Item.BacklogItemID != active.BacklogItemID || !second.Acquired {
		t.Fatalf("active lease holder was not recoverable: result=%+v err=%v", second, err)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, lease) {
		t.Fatalf("recovery changed the newer active lease: %+v", fixture.workstream.lease)
	}
	key := stageBefore.IdempotencyKey
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("legacy cleanup did not complete beside active lease: closure=%+v found=%v err=%v", closure, found, err)
	}
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) || !reflect.DeepEqual(fixture.items.items[0], itemBefore) {
		t.Fatalf("legacy reconciliation rewrote historical item/stage: stage=%+v item=%+v", stageAfter, fixture.items.items[0])
	}
}

type blockingTransitionEventSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTransitionEventSink) AppendDevelopmentEvent(ctx context.Context, event DevelopmentEvent) error {
	if event.Type == "state_transition_requested" {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestAdoptSerializesWithPreparedRejectedCancellation(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.workstream.lease = nil
	candidate := fixture.items.items[1]
	candidate.ConceptState = domainbacklog.ConceptCandidate
	candidate.DeliveryState = ""
	candidate.MaturationState = domainbacklog.MaturationStatePromoted
	candidate.ImplementationUnit = ""
	candidate.WorkstreamID = ""
	fixture.items.items[1] = candidate

	sink := &blockingTransitionEventSink{entered: make(chan struct{}), release: make(chan struct{})}
	fixture.service.WithDevelopmentEventSink(sink)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(sink.release) })
	reviseCtx := backlogActionContext(t)
	adoptCtx := backlogActionContext(t)

	reviseDone := make(chan error, 1)
	go func() {
		_, err := fixture.service.Revise(reviseCtx, string(fixture.item.BacklogItemID), rejectedCancellationRequest())
		reviseDone <- err
	}()
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("REJECTED transition did not reach the prepared-stage gap")
	}

	type adoptionResult struct {
		result AdoptionResult
		err    error
	}
	adoptStarted := make(chan struct{})
	adoptDone := make(chan adoptionResult, 1)
	go func() {
		close(adoptStarted)
		result, err := fixture.service.Adopt(adoptCtx, string(candidate.BacklogItemID), "owner selected")
		adoptDone <- adoptionResult{result: result, err: err}
	}()
	<-adoptStarted
	select {
	case result := <-adoptDone:
		t.Fatalf("Adopt crossed the prepared cancellation gap: result=%+v err=%v", result.result, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(sink.release) })

	select {
	case err := <-reviseDone:
		if err != nil {
			t.Fatalf("REJECTED cancellation after serialized Adopt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("REJECTED cancellation did not finish after releasing the event sink")
	}
	select {
	case adopted := <-adoptDone:
		if adopted.err != nil || !adopted.result.LeaseAcquired {
			t.Fatalf("serialized Adopt result=%+v err=%v", adopted.result, adopted.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Adopt did not finish after REJECTED cancellation released lifecycleMu")
	}
	if fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != "unit_atlas_"+string(candidate.BacklogItemID) {
		t.Fatalf("Adopt's lease was not acquired after cancellation: %+v", fixture.workstream.lease)
	}
}

func TestAdoptRecoversPreparedRejectedCancellationBeforeAcquiringLease(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure after lease release was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stage, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stage.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("post-release cancellation stage=%+v found=%v err=%v", stage, found, err)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Phase != domainworkstream.ClosurePhaseLease || closure.Status != domainworkstream.ClosureStatusFailed {
		t.Fatalf("post-release cancellation closure=%+v found=%v err=%v", closure, found, err)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("post-release failed cancellation should have no lease: %+v", fixture.workstream.lease)
	}

	candidate := fixture.items.items[1]
	candidate.ConceptState = domainbacklog.ConceptCandidate
	candidate.DeliveryState = ""
	candidate.MaturationState = domainbacklog.MaturationStatePromoted
	candidate.ImplementationUnit = ""
	candidate.WorkstreamID = ""
	fixture.items.items[1] = candidate
	fixture.items.failRejectedSave = false
	adopted, adoptErr := fixture.service.Adopt(backlogActionContext(t), string(candidate.BacklogItemID), "owner selected")
	if adoptErr != nil {
		if fixture.workstream.lease != nil {
			t.Fatalf("fail-closed Adopt still acquired a lease: %+v (error %v)", fixture.workstream.lease, adoptErr)
		}
		return
	}
	if !adopted.LeaseAcquired {
		t.Fatalf("Adopt succeeded without acquiring the available lease: %+v", adopted)
	}
	closure, found, err = fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("Adopt acquired B's lease before repairing A: closure=%+v found=%v err=%v", closure, found, err)
	}
	if fixture.items.items[0].DeliveryState != domainbacklog.DeliveryRejected || fixture.items.items[0].CheckOK {
		t.Fatalf("prepared rejection A was not safely restored before Adopt: %+v", fixture.items.items[0])
	}
	if fixture.workstream.lease == nil || fixture.workstream.lease.HolderUnitID != adopted.Item.ImplementationUnit ||
		fixture.workstream.lease.HolderWorkstreamID != adopted.Item.WorkstreamID {
		t.Fatalf("Adopt did not acquire its own lease after cancellation repair: %+v", fixture.workstream.lease)
	}
}

type memoryDevelopmentEventStore struct {
	events map[modulecore.EventID]DevelopmentEvent
}

func (s *memoryDevelopmentEventStore) AppendDevelopmentEvent(_ context.Context, event DevelopmentEvent) error {
	if event.EventID == "" {
		event.EventID = modulecore.NewEventID()
	}
	if s.events == nil {
		s.events = make(map[modulecore.EventID]DevelopmentEvent)
	}
	s.events[event.EventID] = event
	return nil
}

func (s *memoryDevelopmentEventStore) GetDevelopmentEvent(_ context.Context, id modulecore.EventID) (DevelopmentEvent, bool, error) {
	event, found := s.events[id]
	return event, found, nil
}

func TestResolveQueueFreezeRecoversPreparedRejectedCancellationBeforeReplacementLease(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure after lease release was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	if fixture.workstream.lease != nil {
		t.Fatalf("post-release failed cancellation should have no lease: %+v", fixture.workstream.lease)
	}

	blocked := fixture.items.items[1]
	blocked.DeliveryState = domainbacklog.DeliveryQueued
	fixture.items.items[1] = blocked
	replacement := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("replacement-after-freeze"),
		ImplementationUnit: "unit-replacement-after-freeze", WorkstreamID: "ws-replacement-after-freeze",
		Title: "replacement", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued,
		ImplementationRevision: 1, SupersedesUnitID: blocked.ImplementationUnit,
	}
	fixture.items.items = append(fixture.items.items, replacement)
	events := &memoryDevelopmentEventStore{}
	fixture.service.WithDevelopmentEventSink(events).WithEvidenceVerifier(revision2Verifier{})
	if acquired, err := fixture.workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: blocked.ImplementationUnit,
		HolderWorkstreamID: blocked.WorkstreamID, Stage: domainbacklog.DeliveryQueued, Revision: "1",
	}); err != nil || !acquired {
		t.Fatalf("seed blocked implementation lease acquired=%v err=%v", acquired, err)
	}
	if _, err := fixture.service.Revise(backlogActionContext(t), string(blocked.BacklogItemID), ReviseRequest{
		ExpectedRevision: blocked.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "blocked-ref"}},
		Reason:       "worker_failure",
	}); err != nil {
		t.Fatalf("seed blocked freeze and stage receipt: %v", err)
	}
	fixture.items.failRejectedSave = false
	resolved, lease, acquired, err := fixture.service.ResolveQueueFreeze(backlogActionContext(t), queueFreezeID(blocked.ImplementationUnit, blocked.ImplementationRevision), ResolveQueueFreezeRequest{
		ActionID: modulecore.NewActionID(), ExpectedFreezeRevision: 1, ReplacementUnitID: replacement.ImplementationUnit,
		SupersedesUnitID: blocked.ImplementationUnit, BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-ref"}},
	})
	if err != nil {
		if fixture.workstream.lease != nil && fixture.workstream.lease.HolderUnitID == replacement.ImplementationUnit {
			t.Fatalf("ResolveQueueFreeze failed after acquiring replacement lease: lease=%+v err=%v", fixture.workstream.lease, err)
		}
		return
	}
	if !acquired || resolved.Status != domainworkstream.QueueFreezeResolved || lease.HolderUnitID != replacement.ImplementationUnit {
		t.Fatalf("resolved freeze=%+v lease=%+v acquired=%v", resolved, lease, acquired)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("ResolveQueueFreeze acquired replacement before repairing rejection A: closure=%+v found=%v err=%v", closure, found, err)
	}
}

func TestResolveQueueFreezeDoesNotResurrectPendingRejectedReplacement(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	pendingReplacement := fixture.items.items[0]
	pendingReplacement.DeliveryState = domainbacklog.DeliveryQueued
	pendingReplacement.SupersedesUnitID = "unit-blocked-before-replacement"
	fixture.items.items[0] = pendingReplacement
	fixture.item = pendingReplacement
	fixture.items.failRejectedSave = true
	if _, err := fixture.service.Revise(backlogActionContext(t), string(pendingReplacement.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated item persistence failure after lease release was hidden")
	}
	key := stageRunKey(pendingReplacement.ImplementationUnit, pendingReplacement.ImplementationRevision, domainbacklog.DeliveryRejected)
	if fixture.workstream.lease != nil {
		t.Fatalf("post-release failed cancellation should have no lease: %+v", fixture.workstream.lease)
	}

	blocked := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("blocked-before-replacement"),
		ImplementationUnit: "unit-blocked-before-replacement", WorkstreamID: "ws-blocked-before-replacement",
		Title: "blocked", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliverySpec,
		ImplementationRevision: 2,
	}
	fixture.items.items = append(fixture.items.items, blocked)
	events := &memoryDevelopmentEventStore{}
	fixture.service.WithDevelopmentEventSink(events).WithEvidenceVerifier(revision2Verifier{})
	if acquired, err := fixture.workstream.AcquireImplementationLease(context.Background(), domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: blocked.ImplementationUnit,
		HolderWorkstreamID: blocked.WorkstreamID, Stage: domainbacklog.DeliverySpec, Revision: "2",
	}); err != nil || !acquired {
		t.Fatalf("seed blocked implementation lease acquired=%v err=%v", acquired, err)
	}
	if _, err := fixture.service.Revise(backlogActionContext(t), string(blocked.BacklogItemID), ReviseRequest{
		ExpectedRevision: blocked.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "blocked-before-replacement-ref"}},
		Reason:       "worker_failure",
	}); err != nil {
		t.Fatalf("seed blocked freeze and stage receipt: %v", err)
	}
	fixture.items.failRejectedSave = false
	_, lease, acquired, err := fixture.service.ResolveQueueFreeze(backlogActionContext(t), queueFreezeID(blocked.ImplementationUnit, blocked.ImplementationRevision), ResolveQueueFreezeRequest{
		ActionID: modulecore.NewActionID(), ExpectedFreezeRevision: 1,
		ReplacementUnitID: pendingReplacement.ImplementationUnit, SupersedesUnitID: blocked.ImplementationUnit,
		BlockerResolutionRefs: []domainbacklog.EvidenceRef{{Kind: "fix", Ref: "fix-pending-replacement"}},
	})
	if err == nil || acquired || lease.HolderUnitID == pendingReplacement.ImplementationUnit {
		t.Fatalf("ResolveQueueFreeze acquired pending rejected replacement: lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	current, findErr := fixture.service.find(context.Background(), string(pendingReplacement.BacklogItemID))
	if findErr != nil || current.DeliveryState != domainbacklog.DeliveryRejected || current.CheckOK {
		t.Fatalf("pending rejected replacement was resurrected or marked successful: item=%+v err=%v", current, findErr)
	}
	if fixture.workstream.lease != nil {
		t.Fatalf("rejected replacement resolution acquired a lease: %+v", fixture.workstream.lease)
	}
	closure, found, findErr := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if findErr != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("pending replacement cancellation was not reconciled before resolution: closure=%+v found=%v err=%v", closure, found, findErr)
	}
}

func TestRecoverLegacyCompletedRejectedStageWithEmptyImplementation(t *testing.T) {
	fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
	itemBefore.Implementation = ""
	fixture.items.items[0] = itemBefore
	stageItem := domainbacklog.Item{}
	if err := json.Unmarshal([]byte(stageBefore.ResultJSON), &stageItem); err != nil {
		t.Fatalf("decode historical rejected result: %v", err)
	}
	stageItem.Implementation = ""
	resultJSON, err := json.Marshal(stageItem)
	if err != nil {
		t.Fatalf("encode historical rejected result: %v", err)
	}
	stageBefore.ResultJSON = string(resultJSON)
	fixture.workstream.stageReceipts[0] = stageBefore

	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover historical rejection with empty implementation reason: %v", err)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), stageBefore.IdempotencyKey)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted || closure.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("empty-reason legacy closure=%+v found=%v err=%v", closure, found, err)
	}
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), stageBefore.IdempotencyKey)
	if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) {
		t.Fatalf("empty-reason historical stage was rewritten: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) || fixture.items.items[0].CheckOK {
		t.Fatalf("empty-reason historical item changed or claims success: before=%+v after=%+v", itemBefore, fixture.items.items[0])
	}
}

type failFirstRejectedCancellationArchiveStore struct {
	*memoryWorkstreamStore
	failedArchive bool
}

func (s *failFirstRejectedCancellationArchiveStore) SaveWorkstream(ctx context.Context, item domainworkstream.Workstream) error {
	if item.Status == domainworkstream.StatusArchived && !s.failedArchive {
		s.failedArchive = true
		return errors.New("simulated interruption during legacy resource cleanup")
	}
	return s.memoryWorkstreamStore.SaveWorkstream(ctx, item)
}

func TestRecoverResumesLegacyRejectedCleanupAfterFailure(t *testing.T) {
	fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
	store := &failFirstRejectedCancellationArchiveStore{memoryWorkstreamStore: fixture.workstream}
	fixture.service.workstream = store
	if err := fixture.service.Recover(backlogActionContext(t)); err == nil {
		t.Fatal("simulated interruption during legacy cleanup was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	closureBefore, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closureBefore.Status != domainworkstream.ClosureStatusFailed || closureBefore.Phase != domainworkstream.ClosurePhasePrepared {
		t.Fatalf("interrupted cleanup receipt=%+v found=%v err=%v", closureBefore, found, err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) || fixture.items.saveCount != 1 {
		t.Fatalf("interrupted cleanup rewrote the terminal item: item=%+v saves=%d", fixture.items.items[0], fixture.items.saveCount)
	}
	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover legacy cleanup after interruption: %v", err)
	}
	closureAfter, found, err := fixture.workstream.FindClosureReceipt(context.Background(), key)
	if err != nil || !found || closureAfter.Status != domainworkstream.ClosureStatusCompleted || closureAfter.Phase != domainworkstream.ClosurePhaseDone {
		t.Fatalf("resumed cleanup receipt=%+v found=%v err=%v", closureAfter, found, err)
	}
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) {
		t.Fatalf("resumed cleanup rewrote historical stage: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
	}
	if !reflect.DeepEqual(fixture.items.items[0], itemBefore) || fixture.items.saveCount != 1 {
		t.Fatalf("resumed cleanup rewrote the terminal item: item=%+v saves=%d", fixture.items.items[0], fixture.items.saveCount)
	}
	workstreams, err := fixture.workstream.ListWorkstreams(context.Background(), 1<<20)
	if err != nil || len(workstreams) == 0 || workstreams[0].Status != domainworkstream.StatusArchived {
		t.Fatalf("resumed cleanup did not archive the workstream: workstreams=%+v err=%v", workstreams, err)
	}
}

func addRejectedCancellationResourcesForItem(fixture *rejectedCancellationFixture, item domainbacklog.Item) (domainworkstream.Workstream, domainworkstream.Goal, domainworkstream.Artifact) {
	workstream := domainworkstream.Workstream{
		WorkstreamID: item.WorkstreamID, Name: "competing workstream " + string(item.BacklogItemID),
		Description: "preserve competing workstream", Status: domainworkstream.StatusActive,
		PrimaryAgent: "Mio", VaultPath: "vault/" + string(item.BacklogItemID) + ".md",
		CreatedAt: fixture.now.Add(-time.Hour), UpdatedAt: fixture.now.Add(-time.Minute),
	}
	goal := domainworkstream.Goal{
		GoalID: "goal_atlas_" + safeSegment(string(item.BacklogItemID)), TraceID: "competing-trace-" + string(item.BacklogItemID),
		WorkstreamID: item.WorkstreamID, Title: "competing goal", Description: "preserve competing goal",
		SuccessCriteria: []string{"competing criterion"}, Verification: []string{"competing verification"},
		Status: domainworkstream.StatusWaiting, CreatedAt: fixture.now.Add(-time.Hour), CompletedAt: fixture.now.Add(-time.Minute),
	}
	artifact := domainworkstream.Artifact{
		ArtifactID: "artifact_atlas_" + safeSegment(string(item.BacklogItemID)), TraceID: "competing-artifact-trace",
		WorkstreamID: item.WorkstreamID, Type: "competing-artifact", FilePath: "artifacts/" + string(item.BacklogItemID) + ".json",
		Title: "competing artifact", Status: "pending", Payload: json.RawMessage(`{"preserve":true}`),
		CreatedAt: fixture.now.Add(-time.Hour), UpdatedAt: fixture.now.Add(-time.Minute),
	}
	fixture.workstream.workstreams = append(fixture.workstream.workstreams, workstream)
	fixture.workstream.goals = append(fixture.workstream.goals, goal)
	fixture.workstream.artifacts = append(fixture.workstream.artifacts, artifact)
	return workstream, goal, artifact
}

func assertRejectedCancellationResourcesUnchanged(t *testing.T, fixture *rejectedCancellationFixture, item domainbacklog.Item, wantWorkstream domainworkstream.Workstream, wantGoal domainworkstream.Goal, wantArtifact domainworkstream.Artifact) {
	t.Helper()
	workstreams, err := fixture.workstream.ListWorkstreams(context.Background(), 1<<20)
	if err != nil {
		t.Errorf("read competing workstreams: %v", err)
		return
	}
	var gotWorkstream domainworkstream.Workstream
	workstreamFound := false
	for _, candidate := range workstreams {
		if candidate.WorkstreamID == item.WorkstreamID {
			gotWorkstream, workstreamFound = candidate, true
			break
		}
	}
	goal, goalFound, err := fixture.workstream.FindGoalByID(context.Background(), wantGoal.GoalID)
	if err != nil {
		t.Errorf("read competing goal: %v", err)
		return
	}
	artifacts, err := fixture.workstream.ListArtifacts(context.Background(), 1<<20)
	if err != nil {
		t.Errorf("read competing artifacts: %v", err)
		return
	}
	var gotArtifact domainworkstream.Artifact
	artifactFound := false
	for _, candidate := range artifacts {
		if candidate.ArtifactID == wantArtifact.ArtifactID {
			gotArtifact, artifactFound = candidate, true
			break
		}
	}
	if !workstreamFound || !goalFound || !artifactFound || !reflect.DeepEqual(gotWorkstream, wantWorkstream) ||
		!reflect.DeepEqual(goal, wantGoal) || !reflect.DeepEqual(gotArtifact, wantArtifact) {
		t.Errorf("competing cancellation resources changed: workstream=%+v goal=%+v artifact=%+v", gotWorkstream, goal, gotArtifact)
	}
}

func TestRevisePendingRejectedCancellationFencesDifferentItemBeforeWrites(t *testing.T) {
	store := &failFirstRejectedClosureStore{}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	fixture.service.workstream = store
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated stop before rejected closure prepare was hidden")
	}
	keyA := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stageA, found, err := store.FindStageRunReceipt(context.Background(), keyA)
	if err != nil || !found || stageA.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("item A must have a prepared cancellation stage: stage=%+v found=%v err=%v", stageA, found, err)
	}

	itemB := fixture.items.items[1]
	itemB.DeliveryState = domainbacklog.DeliverySpec
	fixture.items.items[1] = itemB
	wantWorkstream, wantGoal, wantArtifact := addRejectedCancellationResourcesForItem(fixture, itemB)
	itemBefore := fixture.items.items[1]
	leaseBefore := *fixture.workstream.lease
	request := rejectedCancellationRequest()
	request.ExpectedRevision = itemB.ImplementationRevision
	if _, err := fixture.service.Revise(backlogActionContext(t), string(itemB.BacklogItemID), request); !errors.Is(err, ErrLifecycleConflict) {
		t.Errorf("Revise item B while item A cancellation is prepared error=%v, want ErrLifecycleConflict", err)
	}
	keyB := stageRunKey(itemB.ImplementationUnit, itemB.ImplementationRevision, domainbacklog.DeliveryRejected)
	if _, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), keyB); err != nil || found {
		t.Errorf("fenced item B must not persist a stage intent: found=%v err=%v", found, err)
	}
	if _, found, err := fixture.workstream.FindClosureReceipt(context.Background(), keyB); err != nil || found {
		t.Errorf("fenced item B must not persist a closure intent: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(fixture.items.items[1], itemBefore) {
		t.Errorf("fenced item B was mutated: before=%+v after=%+v", itemBefore, fixture.items.items[1])
	}
	assertRejectedCancellationResourcesUnchanged(t, fixture, itemB, wantWorkstream, wantGoal, wantArtifact)
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Errorf("fencing item B changed item A's lease: %+v", fixture.workstream.lease)
	}
}

func TestRevisePendingRejectedCancellationFencesDifferentTargetForSameItem(t *testing.T) {
	store := &failFirstRejectedClosureStore{}
	fixture := newRejectedCancellationFixture(&store.memoryWorkstreamStore)
	fixture.service.workstream = store
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err == nil {
		t.Fatal("simulated stop before rejected closure prepare was hidden")
	}
	key := stageRunKey(fixture.item.ImplementationUnit, fixture.item.ImplementationRevision, domainbacklog.DeliveryRejected)
	stageBefore, found, err := store.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || stageBefore.Status != domainworkstream.StageRunPrepared {
		t.Fatalf("item A must have a prepared cancellation stage: stage=%+v found=%v err=%v", stageBefore, found, err)
	}
	if _, found, err := store.FindClosureReceipt(context.Background(), key); err != nil || found {
		t.Fatalf("closure should be absent at the injected stop: found=%v err=%v", found, err)
	}

	// Remove the live lease so this assertion isolates the pending-revision
	// fence from the separate foreign-lease guard.
	fixture.workstream.lease = nil
	itemsBefore := append([]domainbacklog.Item(nil), fixture.items.items...)
	stagesBefore := append([]domainworkstream.StageRunReceipt(nil), fixture.workstream.stageReceipts...)
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	freezesBefore := append([]domainworkstream.QueueFreeze(nil), fixture.workstream.queueFreezes...)
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	itemSaveCountBefore := fixture.items.saveCount
	request := ReviseRequest{
		ExpectedRevision: fixture.item.ImplementationRevision, TargetDeliveryState: domainbacklog.DeliveryBlocked,
		EvidenceRefs: []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliveryBlocked, Kind: "worker_failure", Ref: "competing-block-ref"}},
		Reason:       "competing blocked target",
	}
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), request); !errors.Is(err, ErrLifecycleConflict) {
		t.Errorf("Revise item A to BLOCKED while its REJECTED cancellation is prepared error=%v, want ErrLifecycleConflict", err)
	}
	stageAfter, found, err := store.FindStageRunReceipt(context.Background(), key)
	if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) {
		t.Errorf("competing target changed the prepared REJECTED stage: before=%+v after=%+v found=%v err=%v", stageBefore, stageAfter, found, err)
	}
	if !reflect.DeepEqual(fixture.items.items, itemsBefore) || fixture.items.saveCount != itemSaveCountBefore {
		t.Errorf("competing target changed backlog items: before=%+v after=%+v saves=%d->%d", itemsBefore, fixture.items.items, itemSaveCountBefore, fixture.items.saveCount)
	}
	if !reflect.DeepEqual(fixture.workstream.stageReceipts, stagesBefore) || !reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) || !reflect.DeepEqual(fixture.workstream.queueFreezes, freezesBefore) ||
		!reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) || !reflect.DeepEqual(fixture.workstream.goals, goalsBefore) || !reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) {
		t.Error("competing target changed stage/closure/freeze/resource records")
	}
}

func TestReviseQueuedRejectedCancellationWithForeignLeaseHasNoIntentOrMutation(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	itemB := fixture.items.items[1]
	wantWorkstream, wantGoal, wantArtifact := addRejectedCancellationResourcesForItem(fixture, itemB)
	itemBefore := fixture.items.items[1]
	leaseBefore := *fixture.workstream.lease
	request := rejectedCancellationRequest()
	request.ExpectedRevision = itemB.ImplementationRevision
	if _, err := fixture.service.Revise(backlogActionContext(t), string(itemB.BacklogItemID), request); !errors.Is(err, ErrLifecycleConflict) {
		t.Errorf("cancel queued item B while item A holds the lease error=%v, want ErrLifecycleConflict", err)
	}
	keyB := stageRunKey(itemB.ImplementationUnit, itemB.ImplementationRevision, domainbacklog.DeliveryRejected)
	if _, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), keyB); err != nil || found {
		t.Errorf("queued item B cancellation must not persist a stage intent: found=%v err=%v", found, err)
	}
	if _, found, err := fixture.workstream.FindClosureReceipt(context.Background(), keyB); err != nil || found {
		t.Errorf("queued item B cancellation must not persist a closure intent: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(fixture.items.items[1], itemBefore) || fixture.items.saveCount != 0 {
		t.Errorf("foreign lease changed queued item B: before=%+v after=%+v saves=%d", itemBefore, fixture.items.items[1], fixture.items.saveCount)
	}
	assertRejectedCancellationResourcesUnchanged(t, fixture, itemB, wantWorkstream, wantGoal, wantArtifact)
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, leaseBefore) {
		t.Errorf("queued cancellation changed item A's lease: %+v", fixture.workstream.lease)
	}
}

func TestCompletedRejectedCancellationFastPathPreservesMismatchedLease(t *testing.T) {
	for _, route := range []string{"Recover", "AcquireRunnable"} {
		t.Run(route, func(t *testing.T) {
			fixture := newRejectedCancellationFixture(nil)
			if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err != nil {
				t.Fatalf("seed completed rejected closure: %v", err)
			}
			lease := domainworkstream.ImplementationLease{
				LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: fixture.item.ImplementationUnit,
				HolderWorkstreamID: "different-live-workstream", Stage: fixture.item.DeliveryState, Revision: "7",
			}
			fixture.workstream.lease = &lease
			var routeErr error
			if route == "Recover" {
				routeErr = fixture.service.Recover(backlogActionContext(t))
			} else {
				_, routeErr = fixture.service.AcquireRunnable(backlogActionContext(t))
			}
			if !errors.Is(routeErr, ErrLifecycleConflict) {
				t.Errorf("%s completed rejection with same-unit mismatched-workstream lease error=%v, want ErrLifecycleConflict", route, routeErr)
			}
			if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, lease) {
				t.Errorf("%s released or changed mismatched lease: %+v", route, fixture.workstream.lease)
			}
		})
	}
}

type findByIDLimitedItemStore struct {
	items []domainbacklog.Item
}

func (s *findByIDLimitedItemStore) List(_ context.Context, limit int) ([]domainbacklog.Item, error) {
	items := append([]domainbacklog.Item(nil), s.items...)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *findByIDLimitedItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	for index := range s.items {
		if s.items[index].BacklogItemID == item.BacklogItemID {
			s.items[index] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func (s *findByIDLimitedItemStore) FindByID(_ context.Context, id string) (domainbacklog.Item, bool, error) {
	for _, item := range s.items {
		if string(item.BacklogItemID) == id {
			return item, true, nil
		}
	}
	return domainbacklog.Item{}, false, nil
}

func TestFindReturnsItemBeyondListCap(t *testing.T) {
	items := make([]domainbacklog.Item, 5001)
	for index := range items {
		items[index] = domainbacklog.Item{BacklogItemID: modulecore.BacklogItemID("filler"), Title: "filler"}
	}
	targetID := modulecore.BacklogItemID("item-after-list-cap")
	items[len(items)-1] = domainbacklog.Item{BacklogItemID: targetID, Title: "item beyond list limit"}
	service := NewService(&findByIDLimitedItemStore{items: items}, nil)
	item, err := service.find(context.Background(), string(targetID))
	if err != nil {
		t.Fatalf("find exact item beyond List(ctx, 5000) cap: %v", err)
	}
	if item.BacklogItemID != targetID || item.Title != "item beyond list limit" {
		t.Fatalf("find returned the wrong item: %+v", item)
	}
}

type cappedRejectedCancellationItemStore struct {
	*rejectedCancellationItemStore
}

func (s *cappedRejectedCancellationItemStore) List(ctx context.Context, limit int) ([]domainbacklog.Item, error) {
	items, err := s.rejectedCancellationItemStore.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func appendBacklogHistoryFillers(items *rejectedCancellationItemStore, count int) {
	for index := 0; index < count; index++ {
		items.items = append(items.items, domainbacklog.Item{
			BacklogItemID: modulecore.BacklogItemID(fmt.Sprintf("history-filler-%05d", index)),
			ConceptState:  domainbacklog.ConceptCandidate,
			Title:         "history filler",
		})
	}
}

func TestLegacyRejectedCleanupValidatesForeignLeaseHolderBeyondListCap(t *testing.T) {
	fixture, stageBefore, itemBefore := makeLegacyCompletedRejectedStageWithoutClosure(t)
	active := fixture.items.items[1]
	active.DeliveryState = domainbacklog.DeliverySpec
	fixture.items.items = fixture.items.items[:1]
	appendBacklogHistoryFillers(fixture.items, 5000)
	fixture.items.items = append(fixture.items.items, active)
	addRejectedCancellationResourcesForItem(fixture, active)
	fixture.service.items = &cappedRejectedCancellationItemStore{rejectedCancellationItemStore: fixture.items}
	lease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: active.ImplementationUnit,
		HolderWorkstreamID: active.WorkstreamID, Stage: active.DeliveryState, Revision: "1",
	}
	fixture.workstream.lease = &lease

	if err := fixture.service.Recover(backlogActionContext(t)); err != nil {
		t.Fatalf("Recover legacy cancellation with holder beyond List(5000): %v", err)
	}
	if fixture.workstream.lease == nil || !reflect.DeepEqual(*fixture.workstream.lease, lease) {
		t.Fatalf("legacy cleanup released or changed later holder lease: %+v", fixture.workstream.lease)
	}
	closure, found, err := fixture.workstream.FindClosureReceipt(context.Background(), stageBefore.IdempotencyKey)
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("legacy cleanup did not complete with later holder: closure=%+v found=%v err=%v", closure, found, err)
	}
	stageAfter, found, err := fixture.workstream.FindStageRunReceipt(context.Background(), stageBefore.IdempotencyKey)
	if err != nil || !found || !reflect.DeepEqual(stageAfter, stageBefore) || !reflect.DeepEqual(fixture.items.items[0], itemBefore) {
		t.Fatalf("later holder lookup rewrote legacy history: stage=%+v item=%+v", stageAfter, fixture.items.items[0])
	}
}

func TestRecoverFindsMalformedRejectedItemBeyondListCap(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	fixture.workstream.lease = nil
	appendBacklogHistoryFillers(fixture.items, 5000)
	fixture.items.items = append(fixture.items.items, domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, BacklogItemID: modulecore.BacklogItemID("malformed-old-rejected-item"),
		ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryRejected,
		WorkstreamID: "ws-malformed-old-rejected-item",
	})
	fixture.service.items = &cappedRejectedCancellationItemStore{rejectedCancellationItemStore: fixture.items}
	if err := fixture.service.Recover(backlogActionContext(t)); !errors.Is(err, ErrLifecycleConflict) {
		t.Fatalf("Recover omitted malformed terminal row beyond List(5000): error=%v, want ErrLifecycleConflict", err)
	}
}

func TestRejectedCancellationReplayWithDifferentBacklogItemIDConflictsWithoutMutation(t *testing.T) {
	fixture := newRejectedCancellationFixture(nil)
	if _, err := fixture.service.Revise(backlogActionContext(t), string(fixture.item.BacklogItemID), rejectedCancellationRequest()); err != nil {
		t.Fatalf("seed completed rejected cancellation: %v", err)
	}
	replayItem := fixture.items.items[0]
	replayItem.BacklogItemID = modulecore.BacklogItemID("cancel-item-different-id")
	fixture.items.items = append(fixture.items.items, replayItem)
	itemsBefore := append([]domainbacklog.Item(nil), fixture.items.items...)
	workstreamsBefore := append([]domainworkstream.Workstream(nil), fixture.workstream.workstreams...)
	goalsBefore := append([]domainworkstream.Goal(nil), fixture.workstream.goals...)
	artifactsBefore := append([]domainworkstream.Artifact(nil), fixture.workstream.artifacts...)
	stagesBefore := append([]domainworkstream.StageRunReceipt(nil), fixture.workstream.stageReceipts...)
	closuresBefore := append([]domainworkstream.ClosureReceipt(nil), fixture.workstream.closureReceipts...)
	leaseBefore := fixture.workstream.lease
	saveCountBefore := fixture.items.saveCount
	if _, err := fixture.service.Revise(backlogActionContext(t), string(replayItem.BacklogItemID), rejectedCancellationRequest()); !errors.Is(err, ErrLifecycleConflict) {
		t.Errorf("same unit/revision cancellation replay with another BacklogItemID error=%v, want ErrLifecycleConflict", err)
	}
	if !reflect.DeepEqual(fixture.items.items, itemsBefore) || fixture.items.saveCount != saveCountBefore {
		t.Errorf("conflicting replay changed an item: before=%+v after=%+v saves=%d->%d", itemsBefore, fixture.items.items, saveCountBefore, fixture.items.saveCount)
	}
	if !reflect.DeepEqual(fixture.workstream.workstreams, workstreamsBefore) || !reflect.DeepEqual(fixture.workstream.goals, goalsBefore) ||
		!reflect.DeepEqual(fixture.workstream.artifacts, artifactsBefore) || !reflect.DeepEqual(fixture.workstream.stageReceipts, stagesBefore) ||
		!reflect.DeepEqual(fixture.workstream.closureReceipts, closuresBefore) || !reflect.DeepEqual(fixture.workstream.lease, leaseBefore) {
		t.Errorf("conflicting replay changed workstream, goal, artifact, receipt, or lease state")
	}
}
