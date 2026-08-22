package backlog

import (
	"context"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
)

func TestReconcileBackfillIsIdempotentAndPreservesRuntimeOverlay(t *testing.T) {
	pkg, err := featurebacklog.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryItemStore{}
	service := NewService(store, nil)

	first, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 114 || first.Skipped != 0 || len(store.items) != 114 {
		t.Fatalf("first reconcile report=%+v items=%d", first, len(store.items))
	}
	for _, item := range store.items {
		if item.OwnerModule != "RenCrow_CORE" {
			t.Fatalf("canonical lifecycle owner=%q for %s", item.OwnerModule, item.ItemID)
		}
	}
	second, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Updated != 0 || second.Skipped != 114 || len(store.items) != 114 {
		t.Fatalf("second reconcile report=%+v items=%d", second, len(store.items))
	}
	item, err := service.Get(context.Background(), "atlas:agent.mio_chat")
	if err != nil {
		t.Fatal(err)
	}
	if item.FeatureID != "agent.mio_chat" || item.DeclaredDeliveryState != domainbacklog.DeliveryLiveVerified || item.DeliveryState != domainbacklog.DeliveryNone {
		t.Fatalf("declared/runtime state not separated: %+v", item)
	}
	if item.Purpose == "" || item.Problem == "" || item.Idea == "" || len(item.RelationRefs) == 0 {
		t.Fatalf("rich design fields missing: %+v", item)
	}
	if len(item.SourceRefs) == 0 || item.SourceRefs[0].Strength == "" {
		t.Fatalf("source strength missing: %+v", item.SourceRefs)
	}

	item.DeliveryState = domainbacklog.DeliverySpec
	item.Owner = "ren"
	item.OwnerModule = "RenCrow_Tools"
	item.Priority = "high"
	item.EvidenceRefs = []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Ref: "runtime-spec", Passed: true}}
	if err := store.Save(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	overlay, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if overlay.Imported != 0 || overlay.Updated != 1 || overlay.Skipped != 113 || len(store.items) != 114 {
		t.Fatalf("overlay reconcile report=%+v items=%d", overlay, len(store.items))
	}
	third, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if third.Imported != 0 || third.Updated != 0 || third.Skipped != 114 || len(store.items) != 114 {
		t.Fatalf("overlay reconcile report=%+v items=%d", third, len(store.items))
	}
	item, err = service.Get(context.Background(), item.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.DeliveryState != domainbacklog.DeliverySpec || len(item.EvidenceRefs) != 1 || item.Owner != "ren" || item.OwnerModule != "RenCrow_CORE" || item.Priority != "high" {
		t.Fatalf("runtime overlay was overwritten: %+v", item)
	}
}

func TestReconcileBackfillRepairsEmptyAndConflictingLifecycleOwners(t *testing.T) {
	pkg, err := featurebacklog.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryItemStore{}
	first := backfillItemToDomain(pkg.Items[0])
	first.OwnerModule = ""
	second := backfillItemToDomain(pkg.Items[1])
	second.OwnerModule = "RenCrow_Tools"
	store.items = append(store.items, first, second)
	service := NewService(store, nil)

	result, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 112 || result.Updated != 2 || result.Skipped != 0 || len(store.items) != 114 {
		t.Fatalf("repair reconcile report=%+v items=%d", result, len(store.items))
	}
	for _, item := range store.items {
		if item.OwnerModule != "RenCrow_CORE" {
			t.Fatalf("lifecycle owner=%q for %s", item.OwnerModule, item.ItemID)
		}
	}
}
