package dci

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

func TestJSONLStoreSaveAndListRecent(t *testing.T) {
	store := NewJSONLStore(filepath.Join(t.TempDir(), "dci_search_trace.jsonl"))
	ctx := context.Background()
	for _, id := range []string{"evt_1", "evt_2"} {
		if err := store.SaveSearchTrace(ctx, domaindci.SearchTrace{
			EventID:            id,
			StartedAt:          time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
			EndedAt:            time.Date(2026, 5, 18, 12, 0, 1, 0, time.UTC),
			Actor:              "Worker",
			Mode:               "dci",
			UserQuery:          "DCI",
			Status:             "completed",
			FinalEvidenceCount: 1,
		}); err != nil {
			t.Fatalf("SaveSearchTrace: %v", err)
		}
	}

	recent, err := store.ListRecent(1)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(recent) != 1 || recent[0].EventID != "evt_2" {
		t.Fatalf("recent = %#v", recent)
	}
}
