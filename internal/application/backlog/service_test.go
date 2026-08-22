package backlog

import (
	"context"
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
	workstreams []domainworkstream.Workstream
	goals       []domainworkstream.Goal
	artifacts   []domainworkstream.Artifact
	lease       *domainworkstream.ImplementationLease
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
