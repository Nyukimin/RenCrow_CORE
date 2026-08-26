package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

func TestBacklogStoreSaveListLatest(t *testing.T) {
	store := NewBacklogStore(filepath.Join(t.TempDir(), "backlog.jsonl"))
	ctx := context.Background()

	if err := store.Save(ctx, BacklogItem{
		ItemID:   "item-1",
		Kind:     "idea",
		Title:    "面白い案",
		Source:   "mio",
		Status:   "open",
		Priority: "high",
	}); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := store.Save(ctx, BacklogItem{
		ItemID:     "item-1",
		Kind:       "idea",
		Title:      "面白い案",
		Source:     "mio",
		Status:     "ok",
		CheckOK:    true,
		CheckedBy:  "ren",
		TestResult: "passed",
	}); err != nil {
		t.Fatalf("save update: %v", err)
	}

	items, err := store.List(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d", len(items))
	}
	if !items[0].CheckOK || items[0].Status != "ok" || items[0].TestResult != "passed" {
		t.Fatalf("latest item not returned: %+v", items[0])
	}
}

func TestLegacyBacklogPostCannotBypassAtlasOwner(t *testing.T) {
	store := NewBacklogStore(filepath.Join(t.TempDir(), "backlog.jsonl"))
	handler := HandleBacklog(store)
	request := httptest.NewRequest(http.MethodPost, "/viewer/backlog", strings.NewReader(`{"item_id":"bypass","title":"bypass","status":"implementing"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("legacy mutation status=%d allow=%q body=%s", recorder.Code, recorder.Header().Get("Allow"), recorder.Body.String())
	}
	items, err := store.List(context.Background(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("legacy route mutated canonical store: items=%+v err=%v", items, err)
	}
}

func TestNormalizeBacklogItemPreservesProposalReview(t *testing.T) {
	item := normalizeBacklogItem(BacklogItem{ItemID: "proposal", Title: "候補", Status: domainbacklog.StatusProposalReview})
	if item.Status != domainbacklog.StatusProposalReview {
		t.Fatalf("proposal review must not become executable open state: %+v", item)
	}
}

func TestBacklogStoreListPreservesUpdatedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backlog.jsonl")
	if err := os.WriteFile(path, []byte(`{"item_id":"item-1","kind":"unimplemented","title":"固定時刻","source":"user","status":"open","priority":"normal","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`+"\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store := NewBacklogStore(path)

	items, err := store.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d", len(items))
	}
	if items[0].UpdatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("updated_at changed on read: %q", items[0].UpdatedAt)
	}
}
