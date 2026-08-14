package dci

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
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

func TestJSONLStoreFindSearchTraceByIDReturnsLatestExactRecord(t *testing.T) {
	store := NewJSONLStore(filepath.Join(t.TempDir(), "dci_search_trace.jsonl"))
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	first := domaindci.SearchTrace{
		EventID:            "evt_1",
		StartedAt:          now,
		EndedAt:            now.Add(time.Second),
		Actor:              "Worker",
		Mode:               "dci",
		UserQuery:          "first",
		FinalEvidenceCount: 1,
		Status:             "completed",
	}
	prefix := first
	prefix.EventID = "evt_10"
	latest := first
	latest.EndedAt = now.Add(2 * time.Second)
	latest.UserQuery = "latest"

	for _, trace := range []domaindci.SearchTrace{first, prefix, latest} {
		if err := store.SaveSearchTrace(ctx, trace); err != nil {
			t.Fatalf("SaveSearchTrace(%s): %v", trace.EventID, err)
		}
	}

	got, found, err := store.FindSearchTraceByID(ctx, "evt_1")
	if err != nil {
		t.Fatalf("FindSearchTraceByID: %v", err)
	}
	if !found || got.EventID != "evt_1" || got.UserQuery != "latest" || !got.EndedAt.Equal(latest.EndedAt) {
		t.Fatalf("found=%v trace=%#v", found, got)
	}

	got, found, err = store.FindSearchTraceByID(ctx, "evt_1x")
	if err != nil {
		t.Fatalf("FindSearchTraceByID(prefix): %v", err)
	}
	if found || got.EventID != "" {
		t.Fatalf("prefix lookup found=%v trace=%#v", found, got)
	}

	got, found, err = store.FindSearchTraceByID(ctx, "missing")
	if err != nil {
		t.Fatalf("FindSearchTraceByID(missing): %v", err)
	}
	if found || got.EventID != "" {
		t.Fatalf("missing lookup found=%v trace=%#v", found, got)
	}
}

func TestJSONLStoreFindSearchTraceByIDRejectsMalformedOrInvalidRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	trace := domaindci.SearchTrace{
		EventID:   "evt_valid",
		StartedAt: now,
		EndedAt:   now.Add(time.Second),
		Actor:     "Worker",
		Mode:      "dci",
		UserQuery: "valid",
		Status:    "completed",
	}

	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dci_search_trace.jsonl")
		store := NewJSONLStore(path)
		if err := store.SaveSearchTrace(ctx, trace); err != nil {
			t.Fatalf("SaveSearchTrace: %v", err)
		}
		appendJSONLTestLine(t, path, "{not-json}")
		_, found, err := store.FindSearchTraceByID(ctx, trace.EventID)
		if err == nil || found {
			t.Fatalf("FindSearchTraceByID malformed: found=%v err=%v", found, err)
		}
	})

	t.Run("semantically invalid nonmatching record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dci_search_trace.jsonl")
		store := NewJSONLStore(path)
		if err := store.SaveSearchTrace(ctx, trace); err != nil {
			t.Fatalf("SaveSearchTrace: %v", err)
		}
		appendJSONLTestLine(t, path, `{"event_id":"evt_invalid"}`)
		_, found, err := store.FindSearchTraceByID(ctx, trace.EventID)
		if err == nil || found {
			t.Fatalf("FindSearchTraceByID invalid record: found=%v err=%v", found, err)
		}
	})
}

func appendJSONLTestLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open JSONL for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("append JSONL line: %v", err)
	}
}
