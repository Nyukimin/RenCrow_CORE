package backlog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type atlasMigrationItemStore struct {
	items   []domainbacklog.Item
	listErr error
	saveErr error
	saves   int
}

func (s *atlasMigrationItemStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]domainbacklog.Item(nil), s.items...), nil
}

func (s *atlasMigrationItemStore) Save(_ context.Context, item domainbacklog.Item) error {
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	for index := range s.items {
		if s.items[index].ItemID == item.ItemID {
			s.items[index] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func legacyAtlasLifecycleItem() domainbacklog.Item {
	return domainbacklog.Item{
		SchemaVersion:          domainbacklog.SchemaVersion2,
		ItemID:                 "atlas:atlas.lifecycle",
		FeatureID:              "atlas.lifecycle",
		Kind:                   "idea",
		Title:                  "Atlas lifecycle",
		Purpose:                "preserve the lifecycle purpose",
		Problem:                "preserve the lifecycle problem",
		Idea:                   "preserve the lifecycle idea",
		Background:             "preserve the lifecycle background",
		ExpectedEffect:         []string{"effect-one", "effect-two"},
		RelationRefs:           []string{"atlas.backfill"},
		TargetModules:          []string{"RenCrow_CORE"},
		ConsumerModules:        []string{"RenCrow_CMD"},
		AffectedModules:        []string{"RenCrow_CORE", "RenCrow_CMD"},
		AcceptanceCriteria:     []string{"revalidate old completion"},
		Category:               "operations",
		Source:                 "legacy-atlas",
		Owner:                  "ren",
		OwnerModule:            domainbacklog.LifecycleOwnerModule,
		ConceptState:           domainbacklog.ConceptAdopted,
		DeliveryState:          domainbacklog.DeliveryLiveVerified,
		ImplementationUnit:     "atlas-lifecycle-v1",
		ImplementationRevision: 0,
		EvidenceRefs: []domainbacklog.EvidenceRef{{
			Stage: domainbacklog.DeliveryLiveVerified, Kind: "production_smoke", Ref: "legacy-smoke", Passed: true,
		}},
		Priority:       "high",
		QueueRank:      1,
		Tags:           []string{"legacy"},
		CreatedAt:      "2026-08-21T00:00:00Z",
		UpdatedAt:      "2026-08-21T00:01:00Z",
		Status:         "ok",
		Implementer:    "legacy-coder",
		Implementation: "legacy implementation note",
		TestResult:     "legacy test result",
		CheckOK:        true,
		CheckedBy:      "legacy-verifier",
	}
}

func TestMigrateLegacyAtlasLifecycleRepairsDoneOnlyWithExactClosureReceipt(t *testing.T) {
	legacy := legacyAtlasLifecycleItem()
	legacy.DeliveryState = domainbacklog.DeliveryDone
	legacy.ImplementationRevision = 1
	legacy.InvalidatedFromStage = domainbacklog.DeliveryBuild
	legacy.SupersedesUnitID = "atlas-lifecycle-v0"
	legacy.BlockerResolutionRefs = []domainbacklog.EvidenceRef{{
		Stage: domainbacklog.DeliveryBlocked, Kind: "blocker_resolution", Ref: "resolution-1", Verified: true,
	}}
	store := &atlasMigrationItemStore{items: []domainbacklog.Item{legacy}}
	workstream := &memoryWorkstreamStore{closureReceipts: []domainworkstream.ClosureReceipt{{
		ReceiptID:              "atlas-closure:atlas-lifecycle-v1:2",
		IdempotencyKey:         "atlas-lifecycle-v1:2:DONE",
		UnitID:                 "atlas-lifecycle-v1",
		ItemID:                 "atlas:atlas.lifecycle",
		ImplementationRevision: 2,
		Phase:                  domainworkstream.ClosurePhaseDone,
		Status:                 domainworkstream.ClosureStatusCompleted,
	}}}
	service := NewService(store, workstream)

	changed, err := service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if !changed || store.saves != 1 {
		t.Fatalf("changed=%t saves=%d, want one DONE repair", changed, store.saves)
	}
	want := legacy
	want.ImplementationRevision = 2
	if !reflect.DeepEqual(store.items[0], want) {
		t.Fatalf("DONE repair changed fields beyond revision: got=%+v want=%+v", store.items[0], want)
	}

	changed, err = service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	if changed || store.saves != 1 {
		t.Fatalf("second migration changed=%t saves=%d, want no-op", changed, store.saves)
	}
}

func TestMigrateLegacyAtlasLifecycleDoesNotRepairDoneWithoutExactClosure(t *testing.T) {
	legacy := legacyAtlasLifecycleItem()
	legacy.DeliveryState = domainbacklog.DeliveryDone
	legacy.ImplementationRevision = 1
	store := &atlasMigrationItemStore{items: []domainbacklog.Item{legacy}}
	service := NewService(store, &memoryWorkstreamStore{closureReceipts: []domainworkstream.ClosureReceipt{{
		UnitID:                 legacy.ImplementationUnit,
		ItemID:                 legacy.ItemID,
		ImplementationRevision: 1,
		Phase:                  domainworkstream.ClosurePhaseDone,
		Status:                 domainworkstream.ClosureStatusCompleted,
	}}})

	changed, err := service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if changed || store.saves != 0 || !reflect.DeepEqual(store.items[0], legacy) {
		t.Fatalf("DONE item without exact revision-2 closure changed=%t saves=%d item=%+v", changed, store.saves, store.items[0])
	}
}

func TestMigrateLegacyAtlasLifecycleQueuesUnverifiedRevisionAndPreservesClaims(t *testing.T) {
	legacy := legacyAtlasLifecycleItem()
	unrelated := legacy
	unrelated.ItemID = "atlas:unrelated"
	unrelated.FeatureID = "unrelated"
	store := &atlasMigrationItemStore{items: []domainbacklog.Item{legacy, unrelated}}
	service := NewService(store, nil)

	changed, err := service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if !changed || store.saves != 1 {
		t.Fatalf("changed=%t saves=%d, want one migration save", changed, store.saves)
	}
	got := store.items[0]
	want := legacy
	want.ImplementationRevision = 2
	want.InvalidatedFromStage = domainbacklog.DeliverySpec
	want.DeliveryState = domainbacklog.DeliveryQueued
	want.CheckOK = false
	want.Status = domainbacklog.StatusProposalReview
	want.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated item changed beyond the queued revalidation fields:\n got=%+v\nwant=%+v", got, want)
	}
	if !reflect.DeepEqual(got.EvidenceRefs, legacy.EvidenceRefs) || !reflect.DeepEqual(got.ExpectedEffect, legacy.ExpectedEffect) || !reflect.DeepEqual(got.RelationRefs, legacy.RelationRefs) {
		t.Fatalf("legacy claims were not preserved: got evidence=%+v effects=%+v relations=%+v", got.EvidenceRefs, got.ExpectedEffect, got.RelationRefs)
	}

	changed, err = service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	if changed || store.saves != 1 {
		t.Fatalf("second migration changed=%t saves=%d, want no-op", changed, store.saves)
	}
	if !reflect.DeepEqual(store.items[0], got) {
		t.Fatalf("second migration changed the record: first=%+v second=%+v", got, store.items[0])
	}
	if !reflect.DeepEqual(store.items[1], unrelated) {
		t.Fatalf("unrelated item was changed: got=%+v want=%+v", store.items[1], unrelated)
	}
}

func TestMigrateLegacyAtlasLifecycleIgnoresNonLegacyShapes(t *testing.T) {
	base := legacyAtlasLifecycleItem()
	cases := []domainbacklog.Item{
		func() domainbacklog.Item { item := base; item.ImplementationRevision = 2; return item }(),
		func() domainbacklog.Item { item := base; item.DeliveryState = domainbacklog.DeliveryDone; return item }(),
		func() domainbacklog.Item { item := base; item.DeliveryState = domainbacklog.DeliverySpec; return item }(),
		func() domainbacklog.Item { item := base; item.ImplementationUnit = "other-unit"; return item }(),
		func() domainbacklog.Item {
			item := base
			item.ConceptState = domainbacklog.ConceptCandidate
			return item
		}(),
		func() domainbacklog.Item { item := base; item.ItemID = "atlas:other"; return item }(),
	}
	store := &atlasMigrationItemStore{items: cases}
	service := NewService(store, nil)

	changed, err := service.MigrateLegacyAtlasLifecycle(context.Background())
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if changed || store.saves != 0 {
		t.Fatalf("non-legacy shapes changed=%t saves=%d, want deterministic no-op", changed, store.saves)
	}
	if !reflect.DeepEqual(store.items, cases) {
		t.Fatalf("non-legacy records changed: got=%+v want=%+v", store.items, cases)
	}
}

func TestMigrateLegacyAtlasLifecyclePropagatesStoreFailure(t *testing.T) {
	wantErr := errors.New("backlog unavailable")
	store := &atlasMigrationItemStore{listErr: wantErr}
	service := NewService(store, nil)

	changed, err := service.MigrateLegacyAtlasLifecycle(context.Background())
	if changed || !errors.Is(err, wantErr) {
		t.Fatalf("changed=%t err=%v, want unchanged store error", changed, err)
	}
}
