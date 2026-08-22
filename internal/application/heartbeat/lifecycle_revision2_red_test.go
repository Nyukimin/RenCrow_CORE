package heartbeat

import (
	"context"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type revision2AtlasRunnerFake struct {
	result       appbacklog.AcquireRunnableResult
	acquireCalls int
	reviseCalls  []appbacklog.ReviseRequest
	reviseItemID []string
	reviseErr    error
}

func (f *revision2AtlasRunnerFake) AcquireRunnable(_ context.Context) (appbacklog.AcquireRunnableResult, error) {
	f.acquireCalls++
	return f.result, nil
}

func (f *revision2AtlasRunnerFake) Revise(_ context.Context, itemID string, request appbacklog.ReviseRequest) (domainbacklog.Item, error) {
	f.reviseItemID = append(f.reviseItemID, itemID)
	f.reviseCalls = append(f.reviseCalls, request)
	return domainbacklog.Item{}, f.reviseErr
}

type revision2HeartbeatLeaseStore struct {
	memoryWorkstreamHeartbeatStore
	lease *domainworkstream.ImplementationLease
}

func (s *revision2HeartbeatLeaseStore) AcquireImplementationLease(_ context.Context, lease domainworkstream.ImplementationLease) (bool, error) {
	if s.lease != nil && s.lease.HolderUnitID != lease.HolderUnitID {
		return false, nil
	}
	copyLease := lease
	s.lease = &copyLease
	return true, nil
}

func (s *revision2HeartbeatLeaseStore) ReleaseImplementationLease(_ context.Context, _ string, holder string) error {
	if s.lease != nil && (holder == "" || s.lease.HolderUnitID == holder) {
		s.lease = nil
	}
	return nil
}

func (s *revision2HeartbeatLeaseStore) GetImplementationLease(_ context.Context, name string) (domainworkstream.ImplementationLease, bool, error) {
	if s.lease == nil || s.lease.LeaseName != name {
		return domainworkstream.ImplementationLease{}, false, nil
	}
	return *s.lease, true, nil
}

func TestRevision2RunnerAdvancesPastUnitStartedMarker(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "runner-next-stage",
		ImplementationUnit: "unit-runner-next-stage",
		WorkstreamID:       "ws-runner-next-stage",
		Title:              "next stage",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliverySpec,
		Status:             "implementing",
		Implementation:     backlogRunnerStartedMarker + " item_id=runner-next-stage",
	}}}
	worker := &mockWorkerAgent{response: "stage accepted"}
	owner := &revision2AtlasRunnerFake{result: appbacklog.AcquireRunnableResult{
		Item: backlogStore.items[0], Acquired: true,
	}}
	service := NewHeartbeatService(worker, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithAtlasService(owner)

	report, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Started != 1 || !worker.called {
		t.Fatalf("unit-level started marker must not block the next stage: report=%+v called=%t", report, worker.called)
	}
	if len(backlogStore.saved) != 0 {
		t.Fatalf("v2 runner must not persist a legacy started marker: saved=%+v", backlogStore.saved)
	}
}

func TestRevision2RunnerFailurePersistsDeliveryBlocked(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "runner-blocked-v2",
		ImplementationUnit: "unit-runner-blocked-v2",
		WorkstreamID:       "ws-runner-blocked-v2",
		Title:              "blocked v2",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliverySpec,
		Status:             "implementing",
	}}}
	owner := &revision2AtlasRunnerFake{result: appbacklog.AcquireRunnableResult{
		Item: backlogStore.items[0], Acquired: true,
	}}
	service := NewHeartbeatService(&mockWorkerAgent{err: context.Canceled}, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithAtlasService(owner)

	if _, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("worker failure should be returned")
	}
	if len(backlogStore.saved) != 0 {
		t.Fatalf("worker failure must not mutate the v2 item through BacklogStore: saved=%+v", backlogStore.saved)
	}
	if len(owner.reviseCalls) != 1 || owner.reviseCalls[0].TargetDeliveryState != domainbacklog.DeliveryBlocked {
		t.Fatalf("worker failure must call owner Revise(BLOCKED): calls=%+v", owner.reviseCalls)
	}
	if owner.reviseCalls[0].ExpectedRevision != 1 || owner.reviseItemID[0] != "runner-blocked-v2" || owner.reviseCalls[0].EvidenceRefs[0].Passed {
		t.Fatalf("owner failure request must carry revision and non-success evidence: ids=%v calls=%+v", owner.reviseItemID, owner.reviseCalls)
	}
}

func TestRevision2BlockedUnitPreventsFollowingUnitSelection(t *testing.T) {
	items := []domainbacklog.Item{
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "blocked-unit", Title: "blocked", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryBlocked},
		{SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "following-unit", Title: "following", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryQueued},
	}
	owner := &revision2AtlasRunnerFake{result: appbacklog.AcquireRunnableResult{Reason: domainworkstream.ErrQueueFrozen.Error()}}
	backlogStore := &memoryBacklogStore{items: items}
	worker := &mockWorkerAgent{response: "must not run"}
	service := NewHeartbeatService(worker, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithAtlasService(owner)
	report, err := service.RunBacklogRunner(context.Background(), time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Started != 0 || worker.called || owner.acquireCalls != 1 {
		t.Fatalf("frozen owner decision must prevent following unit selection: report=%+v worker=%t acquire=%d", report, worker.called, owner.acquireCalls)
	}
}
