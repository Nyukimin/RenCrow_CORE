package knowledge

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"
	"testing"
	"time"
)

type fakeKnowledgeStagingStore struct {
	items []l1sqlite.L1StagingItem
}

func (s *fakeKnowledgeStagingStore) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	s.items = append(s.items, item)
	return &s.items[len(s.items)-1], nil
}

func TestImportKnowledgeCoreJSONLToStaging(t *testing.T) {
	store := &fakeKnowledgeStagingStore{}
	input := strings.NewReader(`{"id":"movie:interstellar","domain":"movie","title":"Interstellar","year":2014,"genres":["SF"],"keywords":["宇宙","重力"],"summary":"父と娘と時間の物語","themes":["家族"],"source_id":"manual:seed","source_url":"https://example.com/interstellar","license_note":"manual seed"}
{"id":"music:ambient","domain":"music","title":"Ambient Note","keywords":["音楽"],"summary":"静かな音楽メモ","source_id":"manual:seed"}`)

	result, err := ImportKnowledgeCoreJSONL(context.Background(), store, input, ImportOptions{
		Now: func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("ImportKnowledgeCoreJSONL failed: %v", err)
	}
	if result.Imported != 2 || len(store.items) != 2 {
		t.Fatalf("unexpected import result=%+v items=%+v", result, store.items)
	}
	first := store.items[0]
	if first.Kind != l1sqlite.L1StagingKindExternalFetch || first.Namespace != "kb:movie" || first.EventID != "movie:interstellar" {
		t.Fatalf("unexpected first staging identity: %+v", first)
	}
	if first.RawText != "Interstellar\n父と娘と時間の物語" || first.SummaryDraft != "父と娘と時間の物語" {
		t.Fatalf("unexpected first text fields: %+v", first)
	}
	if first.Meta["title"] != "Interstellar" || first.Meta["year"] != float64(2014) {
		t.Fatalf("common core metadata should be preserved: %+v", first.Meta)
	}
	if first.Keywords[0] != "宇宙" || first.LicenseNote != "manual seed" {
		t.Fatalf("unexpected first keywords/license: %+v", first)
	}
}
