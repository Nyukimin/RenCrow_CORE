package backlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

func TestJSONLStoreReadsLegacyAndProjectsWithoutAdopting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backlog.jsonl")
	if err := os.WriteFile(path, []byte(`{"item_id":"legacy","title":"old","source":"user","status":"proposal_review","priority":"normal"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewJSONLStore(path)
	items, err := store.List(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].ConceptState != domainbacklog.ConceptCandidate || items[0].DeliveryState == domainbacklog.DeliveryLiveVerified {
		t.Fatalf("legacy item forged state: %+v", items[0])
	}
}

func TestJSONLStoreReconcilesBackfillWithoutDuplicateItems(t *testing.T) {
	store := NewJSONLStore(filepath.Join(t.TempDir(), "backlog.jsonl"))
	items := []domainbacklog.Item{{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: "atlas:one", FeatureID: "one", Title: "one",
		ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliveryNone,
	}}
	receipt := domainbacklog.BackfillImportReceipt{
		RecordType:    "atlas_backfill_import",
		ImportID:      domainbacklog.BackfillImportID("sha", 1),
		DatasetID:     "dataset",
		PackageSHA256: "sha", Revision: 1, ImportedAt: "2026-08-22T00:00:00Z",
	}
	first, err := store.ReconcileBackfill(context.Background(), items, receipt)
	if err != nil || first.Imported != 1 {
		t.Fatalf("first reconcile result=%+v err=%v", first, err)
	}
	second, err := store.ReconcileBackfill(context.Background(), items, receipt)
	if err != nil || second.Imported != 0 || second.Skipped != 1 {
		t.Fatalf("second reconcile result=%+v err=%v", second, err)
	}
	got, err := store.List(context.Background(), 10)
	if err != nil || len(got) != 1 || got[0].ItemID != "atlas:one" {
		t.Fatalf("items=%+v err=%v", got, err)
	}
}
