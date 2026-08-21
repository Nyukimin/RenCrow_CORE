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
