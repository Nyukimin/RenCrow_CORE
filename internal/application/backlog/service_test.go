package backlog

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type memoryItemStore struct{ items []domainbacklog.Item }

func (s *memoryItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), s.items...), nil
}
func (s *memoryItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	for i := range s.items {
		if s.items[i].ItemID == item.ItemID {
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
	freeze.ResolutionRequestID = resolution.ResolutionRequestID
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
		if s.stageReceipts[index].IdempotencyKey == key || s.stageReceipts[index].ReceiptID == key {
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
		if s.closureReceipts[index].IdempotencyKey == key || s.closureReceipts[index].ReceiptID == key {
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

	first, err := svc.Intake(context.Background(), IntakeRequest{ItemID: "a", Title: "A", Purpose: "test A", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Candidate(context.Background(), first.ItemID); err != nil {
		t.Fatal(err)
	}
	firstResult, err := svc.Adopt(context.Background(), first.ItemID, "owner selected")
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Item.DeliveryState != domainbacklog.DeliveryQueued || !firstResult.LeaseAcquired {
		t.Fatalf("first adoption=%+v", firstResult)
	}
	second, err := svc.Intake(context.Background(), IntakeRequest{ItemID: "b", Title: "B", Purpose: "test B", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Candidate(context.Background(), second.ItemID); err != nil {
		t.Fatal(err)
	}
	secondResult, err := svc.Adopt(context.Background(), second.ItemID, "owner selected")
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
	if !second.Duplicate || second.Item.ItemID != first.Item.ItemID || len(store.items) != 1 {
		t.Fatalf("dedupe result first=%+v second=%+v items=%d", first, second, len(store.items))
	}
}

func TestServiceIntakeFixesLifecycleOwnerAndSeparatesModuleScope(t *testing.T) {
	store := &memoryItemStore{}
	svc := NewService(store, nil)
	result, err := svc.Intake(context.Background(), IntakeRequest{
		ItemID:          "module-scope",
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
		ItemID: "missing-purpose", Title: "Missing purpose",
		SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "missing-purpose"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Candidate(context.Background(), intake.ItemID); err == nil {
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
		ItemID: "recover-item", Title: "recover", Purpose: "recover test", SourceRefs: []domainbacklog.SourceRef{{Type: "manual", Locator: "recover"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Candidate(context.Background(), intake.ItemID); err != nil {
		t.Fatal(err)
	}
	adopted, err := svc.Adopt(context.Background(), intake.ItemID, "recover test")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Lease.HolderUnitID == adopted.Item.ItemID {
		t.Fatalf("test must cover distinct item and unit IDs: %+v", adopted)
	}
	if err := svc.Recover(context.Background()); err != nil {
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
			SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "recover-blocked", ImplementationUnit: "unit-recover-blocked",
			WorkstreamID: "ws-recover-blocked", Title: "recover blocked", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliverySpec, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
		{
			SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "recover-next", ImplementationUnit: "unit-recover-next",
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

	if _, err := service.Revise(context.Background(), "recover-blocked", ReviseRequest{
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

	if err := service.Recover(context.Background()); err == nil {
		t.Fatal("recovery must fail closed while freeze persistence remains unavailable")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || !found {
		t.Fatalf("recovery freeze failure released lease found=%v err=%v", found, err)
	}

	workstream.failFreezeSave = false
	if err := service.Recover(context.Background()); err != nil {
		t.Fatalf("recovery after store restoration: %v", err)
	}
	freeze, found, err := workstream.GetQueueFreeze(context.Background(), "atlas-freeze:unit-recover-blocked:1")
	if err != nil || !found || freeze.Status != domainworkstream.QueueFreezeActive || freeze.BlockedUnitID != blocked.ImplementationUnit || freeze.BlockedRevision != blocked.ImplementationRevision || freeze.ReasonCode != "worker_failure" || freeze.InvalidatedFromStage != domainbacklog.DeliverySpec || len(freeze.EvidenceRefs) != 1 || freeze.EvidenceRefs[0].Stage != domainbacklog.DeliveryBlocked {
		t.Fatalf("recovered exact active freeze=%+v found=%v err=%v", freeze, found, err)
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("lease must release only after freeze persistence found=%v err=%v", found, err)
	}
	result, err := service.AcquireRunnable(context.Background())
	if err != nil || result.Acquired || result.Reason != domainworkstream.ErrQueueFrozen.Error() || result.Item.ItemID != "" {
		t.Fatalf("recovered queue must remain frozen result=%+v err=%v", result, err)
	}
}

func TestRecoverBlockedExistingFreezeRetainsAttemptedStageEvidence(t *testing.T) {
	now := time.Date(2026, 8, 22, 4, 30, 0, 0, time.UTC)
	items := &memoryItemStore{items: []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "recover-blocked-existing", ImplementationUnit: "unit-recover-blocked-existing",
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
	if err := service.Recover(context.Background()); err == nil {
		t.Fatal("release failure must be returned")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || !found {
		t.Fatalf("release failure must retain lease found=%v err=%v", found, err)
	}
	workstream.releaseErr = nil
	if err := service.Recover(context.Background()); err != nil {
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
			SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "recover-live", ImplementationUnit: "unit-recover-live",
			WorkstreamID: "ws-recover-live", Title: "recover live", ConceptState: domainbacklog.ConceptAdopted,
			DeliveryState: domainbacklog.DeliveryLiveVerified, ImplementationRevision: 1,
			CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		},
		{
			SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "recover-live-next", ImplementationUnit: "unit-recover-live-next",
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
	if err := service.completeDone(context.Background(), live, done, ReviseRequest{TargetDeliveryState: domainbacklog.DeliveryDone}, stageRunKey(live.ImplementationUnit, 1, domainbacklog.DeliveryDone), ""); err == nil {
		t.Fatal("simulated DONE persistence failure must surface")
	}
	if _, found, err := workstream.GetImplementationLease(context.Background(), domainbacklog.ImplementationLeaseName); err != nil || found {
		t.Fatalf("fixture must reproduce released lease found=%v err=%v", found, err)
	}
	return service, items, workstream
}

func TestRecoverResumesLiveClosureWithoutLeaseBeforeQueue(t *testing.T) {
	service, items, workstream := newLiveClosureRecoveryFixture(t)
	items.failDoneSave = false
	if err := service.Recover(context.Background()); err != nil {
		t.Fatalf("Recover must resume LIVE closure without lease: %v", err)
	}
	if items.items[0].DeliveryState != domainbacklog.DeliveryDone {
		t.Fatalf("recovered LIVE state=%q, want DONE", items.items[0].DeliveryState)
	}
	closure, found, err := workstream.FindClosureReceipt(context.Background(), "unit-recover-live:1:DONE")
	if err != nil || !found || closure.Status != domainworkstream.ClosureStatusCompleted {
		t.Fatalf("recovered closure=%+v found=%v err=%v", closure, found, err)
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
	result, err := service.AcquireRunnable(context.Background())
	if err != nil || result.Item.ItemID != "recover-live" || result.Item.DeliveryState != domainbacklog.DeliveryDone || result.Reason != "LIVE_VERIFIED closure resumed" {
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
		if s.items[i].ItemID == item.ItemID {
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
		ItemID:         "design-card",
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
		ItemID: "unknown-spec", Title: "Unknown specification", Purpose: "partial input is allowed",
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
