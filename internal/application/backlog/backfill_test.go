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
	item.OwnerModule = "RenCrow_CORE"
	item.Priority = "high"
	item.EvidenceRefs = []domainbacklog.EvidenceRef{{Stage: domainbacklog.DeliverySpec, Ref: "runtime-spec", Passed: true}}
	if err := store.Save(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	second, err := service.ReconcileBackfill(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Updated != 1 || second.Skipped != 113 || len(store.items) != 114 {
		t.Fatalf("second reconcile report=%+v items=%d", second, len(store.items))
	}
	item, err = service.Get(context.Background(), item.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.DeliveryState != domainbacklog.DeliverySpec || len(item.EvidenceRefs) != 1 || item.Owner != "ren" || item.OwnerModule != "RenCrow_CORE" || item.Priority != "high" {
		t.Fatalf("runtime overlay was overwritten: %+v", item)
	}
}
