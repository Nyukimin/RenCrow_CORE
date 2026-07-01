package conversation

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

func TestL1SQLiteStore_SaveMessageAndRecentByNamespace(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "覚えておく候補",
		Timestamp: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		Meta:      map[string]interface{}{"route": "chat"},
	}
	if err := store.SaveMessage(ctx, "session-1", 123, "conv:123", msg, MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	events, err := store.RecentByNamespace(ctx, "conv:123", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Namespace != "conv:123" || ev.SessionID != "session-1" || ev.ThreadID != 123 {
		t.Fatalf("unexpected identity fields: %+v", ev)
	}
	if ev.Speaker != domconv.SpeakerUser || ev.Message != "覚えておく候補" {
		t.Fatalf("unexpected message fields: %+v", ev)
	}
	if ev.MemoryState != MemoryStateObserved || ev.Layer != MemoryLayerL1 {
		t.Fatalf("unexpected memory fields: %+v", ev)
	}
	if ev.Meta["route"] != "chat" {
		t.Fatalf("unexpected meta: %+v", ev.Meta)
	}
}

func TestL1SQLiteStore_DefaultNamespaceAndState(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.NewMessage(domconv.SpeakerMio, "返答", nil)
	if err := store.SaveMessage(ctx, "session-1", 456, "", msg, ""); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	events, err := store.RecentBySession(ctx, "session-1", 10)
	if err != nil {
		t.Fatalf("RecentBySession failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Namespace != "conv:456" {
		t.Fatalf("unexpected namespace: %s", events[0].Namespace)
	}
	if events[0].MemoryState != MemoryStateObserved {
		t.Fatalf("unexpected state: %s", events[0].MemoryState)
	}
}

func TestL1SQLiteStore_RejectsInvalidNamespace(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.NewMessage(domconv.SpeakerUser, "bad namespace", nil)
	if err := store.SaveMessage(ctx, "session-1", 1, "misc:1", msg, MemoryStateObserved); err == nil {
		t.Fatal("expected invalid namespace to be rejected")
	}
	if _, err := store.RecentByNamespace(ctx, "misc:1", 10); err == nil {
		t.Fatal("expected invalid RecentByNamespace namespace to be rejected")
	}
	if _, err := store.AppendEvent(ctx, "test.event", "misc:1", "session-1", 1, nil, "test"); err == nil {
		t.Fatal("expected invalid event namespace to be rejected")
	}
}

func TestL1SQLiteStore_UpdateMemoryState(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "候補から確定へ",
		Timestamp: time.Date(2026, 5, 5, 13, 0, 0, 0, time.UTC),
	}
	if err := store.SaveMessage(ctx, "session-1", 789, "conv:789", msg, MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	events, err := store.RecentByNamespace(ctx, "conv:789", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if err := store.UpdateMemoryState(ctx, events[0].ID, MemoryStateCandidate); err != nil {
		t.Fatalf("UpdateMemoryState candidate failed: %v", err)
	}
	candidates, err := store.RecentByState(ctx, MemoryStateCandidate, 10)
	if err != nil {
		t.Fatalf("RecentByState candidate failed: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != events[0].ID {
		t.Fatalf("unexpected candidate events: %+v", candidates)
	}

	if err := store.UpdateMemoryState(ctx, events[0].ID, MemoryStateConfirmed); err != nil {
		t.Fatalf("UpdateMemoryState confirmed failed: %v", err)
	}
	confirmed, err := store.RecentByState(ctx, MemoryStateConfirmed, 10)
	if err != nil {
		t.Fatalf("RecentByState confirmed failed: %v", err)
	}
	if len(confirmed) != 1 || confirmed[0].MemoryState != MemoryStateConfirmed {
		t.Fatalf("unexpected confirmed events: %+v", confirmed)
	}
	candidates, err = store.RecentByState(ctx, MemoryStateCandidate, 10)
	if err != nil {
		t.Fatalf("RecentByState candidate after confirm failed: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate events, got %+v", candidates)
	}
}

func TestL1SQLiteStore_RejectsInvalidMemoryState(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.NewMessage(domconv.SpeakerUser, "bad state", nil)
	if err := store.SaveMessage(ctx, "session-1", 1, "", msg, "trusted"); err == nil {
		t.Fatal("expected SaveMessage to reject invalid memory state")
	}
	if _, err := store.RecentByState(ctx, "trusted", 10); err == nil {
		t.Fatal("expected RecentByState to reject invalid memory state")
	}
	if err := store.UpdateMemoryState(ctx, "missing", "trusted"); err == nil {
		t.Fatal("expected UpdateMemoryState to reject invalid memory state")
	}
}

func TestL1SQLiteStore_SaveMessageRejectsMalformedMemoryEvent(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	valid := domconv.NewMessage(domconv.SpeakerUser, "valid memory", nil)
	tests := []struct {
		name      string
		sessionID string
		threadID  int64
		msg       domconv.Message
		want      string
	}{
		{
			name:      "missing session",
			sessionID: "",
			threadID:  1,
			msg:       valid,
			want:      "session_id is required",
		},
		{
			name:      "missing thread",
			sessionID: "session-1",
			threadID:  0,
			msg:       valid,
			want:      "thread_id must be > 0",
		},
		{
			name:      "missing speaker",
			sessionID: "session-1",
			threadID:  1,
			msg:       domconv.Message{Msg: "valid memory", Timestamp: time.Now().UTC()},
			want:      "speaker is required",
		},
		{
			name:      "missing message",
			sessionID: "session-1",
			threadID:  1,
			msg:       domconv.NewMessage(domconv.SpeakerUser, "   ", nil),
			want:      "message is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.SaveMessage(ctx, tt.sessionID, tt.threadID, "", tt.msg, MemoryStateObserved)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SaveMessage() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestL1SQLiteStore_RecentMemoryRejectsMalformedRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 20, 8, 50, 0, 0, time.UTC)
	_, err = store.db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, "bad-memory-1", "conv:850", "session-850", int64(850), string(domconv.SpeakerUser), "", `{}`,
		MemoryStateObserved, MemoryLayerL1, "manual", now, now)
	if err != nil {
		t.Fatalf("insert malformed memory row: %v", err)
	}

	_, err = store.RecentByNamespace(ctx, "conv:850", 10)
	if err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("RecentByNamespace() error = %v, want message validation", err)
	}
}

func TestL1SQLiteStore_SearchCacheFreshHit(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, err := store.SaveSearchCache(
		ctx,
		"web",
		"  RenCrow   最新 仕様 ",
		`[{"title":"RenCrow memo"}]`,
		[]string{"https://example.com/rencrow"},
		time.Hour,
	)
	if err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	if entry.NormalizedQuery != "rencrow 最新 仕様" {
		t.Fatalf("unexpected normalized query: %s", entry.NormalizedQuery)
	}

	hit, err := store.GetFreshSearchCache(ctx, "web", "rencrow 最新 仕様", entry.RetrievedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("GetFreshSearchCache failed: %v", err)
	}
	if hit == nil {
		t.Fatal("expected fresh cache hit")
	}
	if hit.QueryHash != entry.QueryHash || hit.ResultsJSON != `[{"title":"RenCrow memo"}]` {
		t.Fatalf("unexpected cache hit: %+v", hit)
	}
	if len(hit.SourceURLs) != 1 || hit.SourceURLs[0] != "https://example.com/rencrow" {
		t.Fatalf("unexpected source urls: %+v", hit.SourceURLs)
	}
}

func TestL1SQLiteStore_RecentSearchCache(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.SaveSearchCache(ctx, "web", "first query", `[{"title":"first"}]`, nil, time.Hour); err != nil {
		t.Fatalf("SaveSearchCache first failed: %v", err)
	}
	if _, err := store.SaveSearchCache(ctx, "web", "second query", `[{"title":"second"}]`, []string{"https://example.com/second"}, time.Hour); err != nil {
		t.Fatalf("SaveSearchCache second failed: %v", err)
	}

	items, err := store.RecentSearchCache(ctx, 1)
	if err != nil {
		t.Fatalf("RecentSearchCache failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].NormalizedQuery != "second query" {
		t.Fatalf("expected newest cache first, got %+v", items[0])
	}
	if len(items[0].SourceURLs) != 1 || items[0].SourceURLs[0] != "https://example.com/second" {
		t.Fatalf("unexpected source urls: %+v", items[0].SourceURLs)
	}
}

func TestL1SQLiteStore_SearchCacheMissesAfterExpiry(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, err := store.SaveSearchCache(ctx, "web", "short lived", `[]`, nil, time.Second)
	if err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	hit, err := store.GetFreshSearchCache(ctx, "web", "short lived", entry.ExpiresAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("GetFreshSearchCache failed: %v", err)
	}
	if hit != nil {
		t.Fatalf("expected expired cache miss, got %+v", hit)
	}
}

func TestL1SQLiteStore_SearchCacheSimilarHitAndInvalidate(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, err := store.SaveSearchCache(ctx, "web", "RenCrow local LLM architecture", `[{"title":"arch"}]`, nil, time.Hour)
	if err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	hit, err := store.GetSimilarFreshSearchCache(ctx, "web", "local llm architecture rencrow", entry.RetrievedAt.Add(time.Minute), 0.75)
	if err != nil {
		t.Fatalf("GetSimilarFreshSearchCache failed: %v", err)
	}
	if hit == nil || hit.QueryHash != entry.QueryHash {
		t.Fatalf("expected similar cache hit, got %+v", hit)
	}
	invalidated, err := store.InvalidateSearchCache(ctx, "web", "RenCrow local LLM architecture")
	if err != nil {
		t.Fatalf("InvalidateSearchCache failed: %v", err)
	}
	if invalidated != 1 {
		t.Fatalf("expected one invalidated row, got %d", invalidated)
	}
	hit, err = store.GetSimilarFreshSearchCache(ctx, "web", "local llm architecture rencrow", entry.RetrievedAt.Add(time.Minute), 0.75)
	if err != nil {
		t.Fatalf("GetSimilarFreshSearchCache after invalidate failed: %v", err)
	}
	if hit != nil {
		t.Fatalf("expected invalidated similar cache miss, got %+v", hit)
	}
}

func TestL1SQLiteStore_SearchCacheRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.SaveSearchCache(ctx, "web", "query", `{bad`, nil, time.Hour); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
	if _, err := store.SaveSearchCache(ctx, "web", "   ", `[]`, nil, time.Hour); err == nil {
		t.Fatal("expected blank query to be rejected")
	}
	if _, err := store.GetFreshSearchCache(ctx, "web", "   ", time.Now()); err == nil {
		t.Fatal("expected blank query lookup to be rejected")
	}
}

func TestL1SQLiteStore_EventLogAppendAndRecent(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	first, err := store.AppendEvent(ctx, "search.cache_hit", "conv:123", "session-1", 123, map[string]interface{}{
		"query": "RenCrow 最新仕様",
	}, "search_cache")
	if err != nil {
		t.Fatalf("AppendEvent first failed: %v", err)
	}
	second, err := store.AppendEvent(ctx, "memory.promoted", "conv:123", "session-1", 123, map[string]interface{}{
		"memory_state": MemoryStateConfirmed,
	}, "memory")
	if err != nil {
		t.Fatalf("AppendEvent second failed: %v", err)
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatalf("unexpected event ids: first=%q second=%q", first.ID, second.ID)
	}

	events, err := store.RecentEvents(ctx, "conv:123", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != "memory.promoted" || events[1].EventType != "search.cache_hit" {
		t.Fatalf("unexpected event order: %+v", events)
	}
	if events[0].Payload["memory_state"] != MemoryStateConfirmed {
		t.Fatalf("unexpected payload: %+v", events[0].Payload)
	}
	if events[1].Source != "search_cache" || events[1].SessionID != "session-1" || events[1].ThreadID != 123 {
		t.Fatalf("unexpected event fields: %+v", events[1])
	}
}

func TestL1SQLiteStore_EventLogRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.AppendEvent(ctx, "", "conv:123", "session-1", 123, nil, "test"); err == nil {
		t.Fatal("expected blank event type to be rejected")
	}
	if _, err := store.AppendEvent(ctx, "test.event", "", "session-1", 123, nil, "test"); err == nil {
		t.Fatal("expected blank namespace to be rejected")
	}
}

func TestL1SQLiteStore_SaveMessageAppendsEventLog(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "イベントにも残す",
		Timestamp: time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC),
	}
	if err := store.SaveMessage(ctx, "session-1", 123, "conv:123", msg, MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	events, err := store.RecentEvents(ctx, "conv:123", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "memory.message_saved" || ev.Source != "conversation" {
		t.Fatalf("unexpected event identity: %+v", ev)
	}
	if ev.Payload["speaker"] != string(domconv.SpeakerUser) || ev.Payload["memory_state"] != MemoryStateObserved {
		t.Fatalf("unexpected event payload: %+v", ev.Payload)
	}
}

func TestL1SQLiteStore_SaveSearchCacheAppendsEventLog(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, err := store.SaveSearchCache(ctx, "web", "RenCrow 最新仕様", `[{"title":"memo"}]`, []string{"https://example.com"}, time.Hour)
	if err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	events, err := store.RecentEvents(ctx, "kb:web", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "search.cache_saved" || ev.Source != "search_cache" {
		t.Fatalf("unexpected event identity: %+v", ev)
	}
	if ev.Payload["query_hash"] != entry.QueryHash || ev.Payload["normalized_query"] != "rencrow 最新仕様" {
		t.Fatalf("unexpected event payload: %+v", ev.Payload)
	}
}

func TestL1SQLiteStore_UpdateMemoryStateAppendsEventLog(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "昇格ログ",
		Timestamp: time.Date(2026, 5, 5, 15, 0, 0, 0, time.UTC),
	}
	if err := store.SaveMessage(ctx, "session-1", 456, "conv:456", msg, MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	memories, err := store.RecentByNamespace(ctx, "conv:456", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace failed: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory event, got %d", len(memories))
	}
	if err := store.UpdateMemoryState(ctx, memories[0].ID, MemoryStateCandidate); err != nil {
		t.Fatalf("UpdateMemoryState failed: %v", err)
	}

	events, err := store.RecentEvents(ctx, "conv:456", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected message save and state update events, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "memory.state_updated" || ev.Source != "memory" {
		t.Fatalf("unexpected event identity: %+v", ev)
	}
	if ev.Payload["memory_id"] != memories[0].ID || ev.Payload["memory_state"] != MemoryStateCandidate {
		t.Fatalf("unexpected event payload: %+v", ev.Payload)
	}
}

func TestL1SQLiteStore_PromoteMemoryToNamespace(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	msg := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "ユーザーは短く要点を好む",
		Timestamp: time.Date(2026, 5, 5, 16, 0, 0, 0, time.UTC),
		Meta:      map[string]interface{}{"type": "preference"},
	}
	if err := store.SaveMessage(ctx, "session-1", 100, "conv:100", msg, MemoryStateCandidate); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	memories, err := store.RecentByNamespace(ctx, "conv:100", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace source failed: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 source memory, got %d", len(memories))
	}

	promoted, err := store.PromoteMemoryToNamespace(ctx, memories[0].ID, "user:U123", "explicit")
	if err != nil {
		t.Fatalf("PromoteMemoryToNamespace failed: %v", err)
	}
	if promoted.Namespace != "user:U123" || promoted.MemoryState != MemoryStateConfirmed {
		t.Fatalf("unexpected promoted memory: %+v", promoted)
	}
	if promoted.Message != msg.Msg || promoted.Meta["type"] != "preference" {
		t.Fatalf("promoted memory did not preserve content/meta: %+v", promoted)
	}
	if promoted.Meta["promoted_from"] != memories[0].ID || promoted.Meta["promoted_by"] != "explicit" {
		t.Fatalf("promoted meta missing source: %+v", promoted.Meta)
	}

	userMemories, err := store.RecentByNamespace(ctx, "user:U123", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace target failed: %v", err)
	}
	if len(userMemories) != 1 || userMemories[0].ID != promoted.ID {
		t.Fatalf("unexpected user memories: %+v", userMemories)
	}
	events, err := store.RecentEvents(ctx, "user:U123", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "memory.promoted" {
		t.Fatalf("expected memory.promoted event, got %+v", events)
	}
	if events[0].Payload["source_memory_id"] != memories[0].ID {
		t.Fatalf("unexpected promote event payload: %+v", events[0].Payload)
	}
}

func TestL1SQLiteStore_PromoteMemoryRejectsInvalidTargetNamespace(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.PromoteMemoryToNamespace(ctx, "missing", "misc:1", "explicit"); err == nil {
		t.Fatal("expected invalid target namespace to be rejected")
	}
}

func TestL1SQLiteStore_SaveStagingItemAndRecentByStatus(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "evt-20260505-001",
		SourceID:     "rss:example",
		SourceURL:    "https://example.com/news/1",
		FetchedAt:    time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 5, 8, 30, 0, 0, time.UTC),
		RawText:      "外部取得した本文",
		SummaryDraft: "LLMが作った要約案",
		Keywords:     []string{"RenCrow", "記憶OS"},
		LicenseNote:  "example license",
		Meta:         map[string]interface{}{"fetcher": "rss"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if item.ID == "" || item.RawHash == "" {
		t.Fatalf("expected id and raw hash: %+v", item)
	}
	if item.ValidationStatus != L1StagingStatusPending {
		t.Fatalf("unexpected validation status: %s", item.ValidationStatus)
	}
	if item.RawText == item.SummaryDraft {
		t.Fatalf("raw_text and summary_draft must remain separate: %+v", item)
	}

	items, err := store.RecentStagingItems(ctx, L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 staging item, got %d", len(items))
	}
	got := items[0]
	if got.ID != item.ID || got.Namespace != "kb:news" || got.SourceURL != "https://example.com/news/1" {
		t.Fatalf("unexpected staging identity: %+v", got)
	}
	if got.RawText != "外部取得した本文" || got.SummaryDraft != "LLMが作った要約案" {
		t.Fatalf("raw/summary fields were not preserved: %+v", got)
	}
	if len(got.Keywords) != 2 || got.Keywords[1] != "記憶OS" {
		t.Fatalf("unexpected keywords: %+v", got.Keywords)
	}
	if got.Meta["fetcher"] != "rss" {
		t.Fatalf("unexpected meta: %+v", got.Meta)
	}

	events, err := store.RecentEvents(ctx, "kb:news", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "staging.item_saved" {
		t.Fatalf("expected staging.item_saved event, got %+v", events)
	}
	if events[0].Payload["staging_id"] != item.ID || events[0].Payload["raw_hash"] != item.RawHash {
		t.Fatalf("unexpected staging event payload: %+v", events[0].Payload)
	}
}

func TestL1SQLiteStore_SaveStagingItemArchivesToDuckDB(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	archive, err := NewDuckDBStore(filepath.Join(t.TempDir(), "archive.duckdb"))
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer archive.Close()
	store.WithArchiveStore(archive)

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "stage-archive-1",
		SourceID:     "rss:archive",
		SourceURL:    "https://example.com/archive",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "staging raw",
		SummaryDraft: "staging summary",
		Keywords:     []string{"archive"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	var count int
	if err := archive.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_staging_item_archive WHERE id = ?`, item.ID).Scan(&count); err != nil {
		t.Fatalf("archive staging count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected staging archive row, got %d", count)
	}
}

func TestL1SQLiteStore_SaveStagingItemRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	valid := L1StagingItem{
		Kind:      L1StagingKindMemoryCandidate,
		Namespace: "user:U123",
		EventID:   "evt-1",
		SourceID:  "conversation",
		SourceURL: "https://example.com/source",
		RawText:   "覚えておく候補",
	}
	if _, err := store.SaveStagingItem(ctx, valid); err != nil {
		t.Fatalf("valid SaveStagingItem failed: %v", err)
	}
	invalidKind := valid
	invalidKind.EventID = "evt-2"
	invalidKind.Kind = "freeform"
	if _, err := store.SaveStagingItem(ctx, invalidKind); err == nil {
		t.Fatal("expected invalid staging kind to be rejected")
	}
	invalidNamespace := valid
	invalidNamespace.EventID = "evt-3"
	invalidNamespace.Namespace = "misc:U123"
	if _, err := store.SaveStagingItem(ctx, invalidNamespace); err == nil {
		t.Fatal("expected invalid namespace to be rejected")
	}
	invalidURL := valid
	invalidURL.EventID = "evt-4"
	invalidURL.SourceURL = "not a url"
	if _, err := store.SaveStagingItem(ctx, invalidURL); err == nil {
		t.Fatal("expected invalid source url to be rejected")
	}
	invalidStatus := valid
	invalidStatus.EventID = "evt-5"
	invalidStatus.ValidationStatus = "trusted"
	if _, err := store.SaveStagingItem(ctx, invalidStatus); err == nil {
		t.Fatal("expected invalid validation status to be rejected")
	}
	if _, err := store.RecentStagingItems(ctx, "trusted", 10); err == nil {
		t.Fatal("expected invalid RecentStagingItems status to be rejected")
	}
}

func TestL1SQLiteStore_ValidateStagingItemMarksValidated(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindExternalFetch,
		Namespace:   "kb:news",
		EventID:     "evt-valid",
		SourceID:    "rss:example",
		SourceURL:   "https://example.com/news/valid",
		FetchedAt:   time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		PublishedAt: time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC),
		RawText:     "検証を通過する外部取得本文",
		Keywords:    []string{"RenCrow"},
		LicenseNote: "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}

	result, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"rss:example": 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	if !result.Passed || result.Status != L1StagingStatusValidated || len(result.Issues) != 0 {
		t.Fatalf("expected validation pass, got %+v", result)
	}
	validated, err := store.RecentStagingItems(ctx, L1StagingStatusValidated, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems validated failed: %v", err)
	}
	if len(validated) != 1 || validated[0].ID != item.ID {
		t.Fatalf("unexpected validated staging items: %+v", validated)
	}
	events, err := store.RecentEvents(ctx, "kb:news", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 2 || events[0].EventType != "staging.item_validated" {
		t.Fatalf("expected staging.item_validated event, got %+v", events)
	}
	if events[0].Payload["passed"] != true || events[0].Payload["validation_status"] != L1StagingStatusValidated {
		t.Fatalf("unexpected validation event payload: %+v", events[0].Payload)
	}
}

func TestL1SQLiteStore_ValidateStagingItemRejectsUnsafeCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	first, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindMemoryCandidate,
		Namespace:   "user:U123",
		EventID:     "evt-dup-1",
		SourceID:    "conversation",
		SourceURL:   "https://example.com/source",
		FetchedAt:   time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		PublishedAt: time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC),
		RawText:     "api_key: secret は保存しない",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem first failed: %v", err)
	}
	if _, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindExternalFetch,
		Namespace:   "kb:news",
		EventID:     "evt-dup-2",
		SourceID:    "rss:example",
		SourceURL:   "https://example.com/news/dup",
		FetchedAt:   time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC),
		PublishedAt: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:     first.RawText,
		LicenseNote: "rss",
	}); err != nil {
		t.Fatalf("SaveStagingItem duplicate failed: %v", err)
	}

	result, err := store.ValidateStagingItem(ctx, first.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"conversation": 0.2},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	if result.Passed || result.Status != L1StagingStatusRejected {
		t.Fatalf("expected validation rejection, got %+v", result)
	}
	for _, code := range []string{
		"duplicate_raw_hash",
		"future_published_at",
		"low_source_trust",
		"missing_license_note",
		"missing_memory_type",
		"sensitive_raw_text",
	} {
		if !result.HasIssue(code) {
			t.Fatalf("expected issue %q in %+v", code, result.Issues)
		}
	}
	rejected, err := store.RecentStagingItems(ctx, L1StagingStatusRejected, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems rejected failed: %v", err)
	}
	if len(rejected) != 1 || rejected[0].ID != first.ID {
		t.Fatalf("unexpected rejected items: %+v", rejected)
	}
}

func TestL1SQLiteStore_PromoteValidatedStagingItemToDomainGraph(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "movie-edge-1",
		SourceID:     "web:eiga",
		SourceURL:    "https://example.com/movie/1",
		FetchedAt:    time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
		RawText:      "作品Aには人物Bが出演している",
		SummaryDraft: "作品A -> 人物B: performed_by",
		Keywords:     []string{"movie", "person"},
		LicenseNote:  "public catalog; review before promotion",
		Meta:         map[string]interface{}{"title": "作品A", "candidate_relation": "performed_by"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToDomainGraph(ctx, item.ID, "movie", "work", "movie:1", "performed_by", 0.8); err == nil {
		t.Fatal("expected pending staging promotion to domain graph to fail")
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"web:eiga": 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 6, 6, 10, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}

	assertion, err := store.PromoteValidatedStagingItemToDomainGraph(ctx, item.ID, "movie", "work", "movie:1", "performed-by", 0.8)
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToDomainGraph failed: %v", err)
	}
	if assertion.StagingID != item.ID || assertion.Domain != "movie" || assertion.EntityType != "work" || assertion.EntityID != "movie:1" {
		t.Fatalf("unexpected assertion identity: %+v", assertion)
	}
	if assertion.RelationType != "performed_by" || assertion.SourceURL != item.SourceURL || assertion.RawHash != item.RawHash {
		t.Fatalf("unexpected assertion relation/source: %+v", assertion)
	}
	if assertion.ValidationStatus != L1StagingStatusValidated || assertion.Confidence != 0.8 {
		t.Fatalf("unexpected assertion validation/confidence: %+v", assertion)
	}
	if assertion.Evidence["staging_id"] != item.ID || assertion.Evidence["source_url"] != item.SourceURL {
		t.Fatalf("unexpected assertion evidence: %+v", assertion.Evidence)
	}
	events, err := store.RecentEvents(ctx, "kb:domain_graph_movie", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "domain_graph.promoted_from_staging" {
		t.Fatalf("expected domain_graph.promoted_from_staging event, got %+v", events)
	}
}

func TestL1SQLiteStore_DomainGraphAssertionsFiltersAndPagination(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	first := saveValidatedDomainGraphAssertion(t, ctx, store, "evt-dg-1", "web:eiga", "https://example.com/movie/1", "movie", "work", "movie:1", "performed-by", 0.8)
	_ = saveValidatedDomainGraphAssertion(t, ctx, store, "evt-dg-2", "web:manga", "https://example.com/manga/1", "manga", "work", "manga:1", "created_by", 0.6)

	total, items, err := store.DomainGraphAssertions(ctx, DomainGraphAssertionQuery{Domain: "movie", Limit: 20})
	if err != nil {
		t.Fatalf("DomainGraphAssertions domain failed: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("unexpected domain query total=%d items=%+v", total, items)
	}
	if items[0].Evidence["staging_id"] != first.StagingID || items[0].Evidence["source_url"] != first.SourceURL {
		t.Fatalf("evidence did not roundtrip: %+v", items[0].Evidence)
	}

	cases := []DomainGraphAssertionQuery{
		{EntityType: "work"},
		{EntityID: "movie:1"},
		{RelationType: "performed-by"},
		{SourceID: "web:eiga"},
	}
	for _, q := range cases {
		q.Domain = "movie"
		total, items, err := store.DomainGraphAssertions(ctx, q)
		if err != nil {
			t.Fatalf("DomainGraphAssertions(%+v) failed: %v", q, err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != first.ID {
			t.Fatalf("unexpected query %+v total=%d items=%+v", q, total, items)
		}
	}

	total, items, err = store.DomainGraphAssertions(ctx, DomainGraphAssertionQuery{Domain: "movie", ValidationStatus: "", Limit: 999})
	if err != nil {
		t.Fatalf("DomainGraphAssertions default status failed: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected default validated status and capped limit, total=%d items=%+v", total, items)
	}
	_, pending, err := store.DomainGraphAssertions(ctx, DomainGraphAssertionQuery{ValidationStatus: L1StagingStatusPending})
	if err != nil {
		t.Fatalf("DomainGraphAssertions pending failed: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending domain graph assertions, got %+v", pending)
	}
}

func TestL1SQLiteStore_DomainGraphAssertionsRejectsNegativeOffset(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, _, err := store.DomainGraphAssertions(ctx, DomainGraphAssertionQuery{Offset: -1}); err == nil {
		t.Fatal("expected negative offset to be rejected")
	}
}

func saveValidatedDomainGraphAssertion(t *testing.T, ctx context.Context, store *L1SQLiteStore, eventID string, sourceID string, sourceURL string, domain string, entityType string, entityID string, relationType string, confidence float64) *L1DomainGraphAssertion {
	t.Helper()
	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:" + domain,
		EventID:      eventID,
		SourceID:     sourceID,
		SourceURL:    sourceURL,
		FetchedAt:    time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
		RawText:      "domain graph raw text " + eventID,
		SummaryDraft: "domain graph summary " + eventID,
		Keywords:     []string{domain},
		LicenseNote:  "public catalog; review before promotion",
		Meta:         map[string]interface{}{"title": eventID},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{sourceID: 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 6, 6, 10, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	assertion, err := store.PromoteValidatedStagingItemToDomainGraph(ctx, item.ID, domain, entityType, entityID, relationType, confidence)
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToDomainGraph failed: %v", err)
	}
	return assertion
}

func TestL1SQLiteStore_PromoteValidatedStagingItemToMemory(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindMemoryCandidate,
		Namespace:    "conv:500",
		EventID:      "evt-promote",
		SourceID:     "conversation",
		SourceURL:    "https://example.com/conversation/500",
		FetchedAt:    time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		RawText:      "ユーザーは短い要点を好む",
		SummaryDraft: "短く要点を好む",
		Keywords:     []string{"preference"},
		LicenseNote:  "user provided",
		Meta:         map[string]interface{}{"type": "preference", "session_id": "session-500", "thread_id": float64(500)},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"conversation": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 12, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}

	promoted, err := store.PromoteValidatedStagingItemToMemory(ctx, item.ID, "user:U123", "validator")
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToMemory failed: %v", err)
	}
	if promoted.Namespace != "user:U123" || promoted.MemoryState != MemoryStateConfirmed || promoted.Source != "promoter" {
		t.Fatalf("unexpected promoted memory: %+v", promoted)
	}
	if promoted.Message != "短く要点を好む" {
		t.Fatalf("summary_draft should be promoted as memory message, got %q", promoted.Message)
	}
	if promoted.Meta["staging_id"] != item.ID || promoted.Meta["raw_hash"] != item.RawHash || promoted.Meta["promoted_by"] != "validator" {
		t.Fatalf("promoted memory missing staging meta: %+v", promoted.Meta)
	}
	memories, err := store.RecentByNamespace(ctx, "user:U123", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace failed: %v", err)
	}
	if len(memories) != 1 || memories[0].ID != promoted.ID {
		t.Fatalf("unexpected promoted memories: %+v", memories)
	}
	events, err := store.RecentEvents(ctx, "user:U123", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "memory.promoted_from_staging" {
		t.Fatalf("expected memory.promoted_from_staging event, got %+v", events)
	}
}

func TestL1SQLiteStore_PromoterArchivesPromotedItemsToDuckDB(t *testing.T) {
	ctx := context.Background()
	l1, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1.Close()
	archive, err := NewDuckDBStore(filepath.Join(t.TempDir(), "archive.duckdb"))
	if err != nil {
		t.Fatalf("NewDuckDBStore failed: %v", err)
	}
	defer archive.Close()
	l1.WithArchiveStore(archive)

	memoryItem, err := l1.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindMemoryCandidate,
		Namespace:    "conv:archive",
		EventID:      "evt-archive-memory",
		SourceID:     "conversation",
		SourceURL:    "https://example.com/conversation/archive",
		FetchedAt:    time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		RawText:      "ユーザーは短い返答を好む",
		SummaryDraft: "短い返答を好む",
		Keywords:     []string{"preference"},
		LicenseNote:  "user provided",
		Meta:         map[string]interface{}{"type": "preference", "session_id": "sess-archive", "thread_id": float64(10)},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem memory failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, memoryItem.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"conversation": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 12, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem memory failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToMemory(ctx, memoryItem.ID, "user:archive", "validator"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToMemory failed: %v", err)
	}

	newsItem, err := l1.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "evt-archive-news",
		SourceID:     "rss:archive",
		SourceURL:    "https://example.com/news/archive",
		FetchedAt:    time.Date(2026, 5, 5, 8, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 5, 7, 0, 0, 0, time.UTC),
		RawText:      "ニュース本文",
		SummaryDraft: "ニュース要約",
		Keywords:     []string{"ai"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem news failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, newsItem.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"rss:archive": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 8, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem news failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToNews(ctx, newsItem.ID, "ai"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
	}

	kbItem, err := l1.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "evt-archive-kb",
		SourceID:     "api:archive",
		SourceURL:    "https://example.com/kb/archive",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "作品本文",
		SummaryDraft: "作品要約",
		Keywords:     []string{"movie"},
		LicenseNote:  "official api",
		Meta:         map[string]interface{}{"title": "Archive Movie"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem knowledge failed: %v", err)
	}
	if _, err := l1.ValidateStagingItem(ctx, kbItem.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"api:archive": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 10, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem knowledge failed: %v", err)
	}
	if _, err := l1.PromoteValidatedStagingItemToKnowledge(ctx, kbItem.ID, "movie"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
	}

	for table, want := range map[string]int{
		"l1_memory_event_archive":   1,
		"l1_news_item_archive":      1,
		"l1_knowledge_item_archive": 1,
	} {
		var got int
		if err := archive.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("archive count failed for %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("archive count for %s: want %d, got %d", table, want, got)
		}
	}
}

func TestL1SQLiteStore_ValidateStagingItemAutoPromotesMemoryCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindMemoryCandidate,
		Namespace:    "conv:501",
		EventID:      "evt-auto-promote",
		SourceID:     "conversation",
		SourceURL:    "https://example.com/conversation/501",
		FetchedAt:    time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		RawText:      "ユーザーは箇条書きを好む",
		SummaryDraft: "箇条書きを好む",
		Keywords:     []string{"preference"},
		LicenseNote:  "user provided",
		Meta: map[string]interface{}{
			"type":             "preference",
			"target_namespace": "user:U123",
			"session_id":       "session-501",
			"thread_id":        float64(501),
		},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}

	result, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores:          map[string]float64{"conversation": 1.0},
		MinimumTrustScore:          0.5,
		Now:                        time.Date(2026, 5, 5, 12, 10, 0, 0, time.UTC),
		AutoPromoteMemoryCandidate: true,
	})
	if err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	if !result.Passed || result.PromotedMemoryID == "" || result.PromotedNamespace != "user:U123" {
		t.Fatalf("expected auto promotion result, got %+v", result)
	}
	memories, err := store.RecentByNamespace(ctx, "user:U123", 10)
	if err != nil {
		t.Fatalf("RecentByNamespace failed: %v", err)
	}
	if len(memories) != 1 || memories[0].MemoryState != MemoryStateConfirmed || memories[0].Message != "箇条書きを好む" {
		t.Fatalf("unexpected promoted memories: %+v", memories)
	}
}

func TestL1SQLiteStore_PromoteStagingItemRequiresValidatedStatus(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindExternalFetch,
		Namespace:   "kb:news",
		EventID:     "evt-pending",
		SourceID:    "rss:example",
		SourceURL:   "https://example.com/news/pending",
		FetchedAt:   time.Date(2026, 5, 5, 13, 0, 0, 0, time.UTC),
		RawText:     "未検証の本文",
		LicenseNote: "rss",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToMemory(ctx, item.ID, "kb:news", "validator"); err == nil {
		t.Fatal("expected pending staging item promotion to be rejected")
	}
	if _, err := store.PromoteValidatedStagingItemToMemory(ctx, item.ID, "misc:news", "validator"); err == nil {
		t.Fatal("expected invalid target namespace to be rejected")
	}
}

func TestL1SQLiteStore_SourceRegistrySaveListAndTrustScores(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, err := store.SaveSourceRegistryEntry(ctx, L1SourceRegistryEntry{
		SourceID:      "rss:example",
		URL:           "https://example.com/feed.xml",
		Kind:          L1SourceKindRSS,
		TrustScore:    0.85,
		FetchInterval: 6 * time.Hour,
		LicenseNote:   "official rss feed",
		Enabled:       true,
		Meta:          map[string]interface{}{"domain": "news"},
	})
	if err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() {
		t.Fatalf("expected timestamps: %+v", entry)
	}

	entries, err := store.ListSourceRegistryEntries(ctx, true)
	if err != nil {
		t.Fatalf("ListSourceRegistryEntries failed: %v", err)
	}
	if len(entries) != 1 || entries[0].SourceID != "rss:example" {
		t.Fatalf("unexpected source registry entries: %+v", entries)
	}
	if entries[0].FetchInterval != 6*time.Hour || entries[0].Meta["domain"] != "news" {
		t.Fatalf("unexpected registry fields: %+v", entries[0])
	}
	scores, err := store.SourceTrustScores(ctx)
	if err != nil {
		t.Fatalf("SourceTrustScores failed: %v", err)
	}
	if scores["rss:example"] != 0.85 {
		t.Fatalf("unexpected trust scores: %+v", scores)
	}
	events, err := store.RecentEvents(ctx, "kb:source_registry", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "source_registry.saved" {
		t.Fatalf("expected source_registry.saved event, got %+v", events)
	}
}

func TestL1SQLiteStore_DueSourceRegistryEntriesAndFetchStatus(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveSourceRegistryEntry(ctx, L1SourceRegistryEntry{
		SourceID:      "rss:due",
		URL:           "https://example.com/feed.xml",
		Kind:          L1SourceKindRSS,
		TrustScore:    0.8,
		FetchInterval: time.Hour,
		LicenseNote:   "rss",
		Enabled:       true,
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}
	due, err := store.DueSourceRegistryEntries(ctx, now)
	if err != nil {
		t.Fatalf("DueSourceRegistryEntries failed: %v", err)
	}
	if len(due) != 1 || due[0].SourceID != "rss:due" {
		t.Fatalf("expected source to be due before first fetch, got %+v", due)
	}
	if err := store.MarkSourceRegistryFetched(ctx, "rss:due", now, "done", ""); err == nil {
		t.Fatal("expected invalid fetch status to fail")
	}
	if err := store.MarkSourceRegistryFetched(ctx, "rss:due", now, L1SourceFetchStatusError, ""); err == nil {
		t.Fatal("expected error status without last_error to fail")
	}
	if err := store.MarkSourceRegistryFetched(ctx, "rss:due", now, "ok", ""); err != nil {
		t.Fatalf("MarkSourceRegistryFetched failed: %v", err)
	}
	due, err = store.DueSourceRegistryEntries(ctx, now.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("DueSourceRegistryEntries failed: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("source should not be due inside interval: %+v", due)
	}
	due, err = store.DueSourceRegistryEntries(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("DueSourceRegistryEntries failed: %v", err)
	}
	if len(due) != 1 || !due[0].LastFetchedAt.Equal(now) || due[0].LastStatus != "ok" {
		t.Fatalf("source should be due after interval with status fields, got %+v", due)
	}
}

func TestL1SQLiteStore_SaveAndRecentRecallTraces(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	trace := domconv.RecallTrace{
		ResponseID: "job-1",
		SessionID:  "sess-1",
		Role:       "worker",
		Items: []domconv.RecallTraceItem{{
			Layer:   "L2",
			Kind:    "thread_summary",
			Summary: "summary",
		}},
	}
	if err := store.SaveRecallTrace(ctx, trace); err != nil {
		t.Fatalf("SaveRecallTrace: %v", err)
	}
	got, err := store.RecentRecallTraces(ctx, "sess-1", 5)
	if err != nil {
		t.Fatalf("RecentRecallTraces: %v", err)
	}
	if len(got) != 1 || got[0].ResponseID != "job-1" || got[0].Items[0].Summary != "summary" {
		t.Fatalf("unexpected traces: %+v", got)
	}
}

func TestL1SQLiteStore_StageSourceRegistryFetchToStaging(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.SaveSourceRegistryEntry(ctx, L1SourceRegistryEntry{
		SourceID:      "rss:ai-official",
		URL:           "https://example.com/feed.xml",
		Kind:          L1SourceKindRSS,
		TrustScore:    0.9,
		FetchInterval: time.Hour,
		LicenseNote:   "official rss feed",
		Enabled:       true,
		Meta: map[string]interface{}{
			"namespace": "kb:ai",
			"category":  "ai",
		},
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}

	fetchedAt := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	item, err := store.StageSourceRegistryFetch(ctx, "rss:ai-official", L1SourceFetchPayload{
		EventID:      "rss:ai-official:20260505:1",
		SourceURL:    "https://example.com/posts/1",
		FetchedAt:    fetchedAt,
		PublishedAt:  fetchedAt.Add(-time.Hour),
		RawText:      "AI official update body",
		SummaryDraft: "AI official update",
		Keywords:     []string{"AI", "official"},
		Meta:         map[string]interface{}{"fetcher": "rss"},
	})
	if err != nil {
		t.Fatalf("StageSourceRegistryFetch failed: %v", err)
	}
	if item.Kind != L1StagingKindExternalFetch || item.Namespace != "kb:ai" {
		t.Fatalf("unexpected staging identity: %+v", item)
	}
	if item.SourceID != "rss:ai-official" || item.SourceURL != "https://example.com/posts/1" {
		t.Fatalf("source fields should come from registry/payload: %+v", item)
	}
	if item.LicenseNote != "official rss feed" || item.ValidationStatus != L1StagingStatusPending {
		t.Fatalf("unexpected staging policy fields: %+v", item)
	}
	if item.Meta["source_kind"] != L1SourceKindRSS || item.Meta["source_registry_url"] != "https://example.com/feed.xml" || item.Meta["fetcher"] != "rss" {
		t.Fatalf("expected merged source/fetcher meta, got %+v", item.Meta)
	}
}

func TestL1SQLiteStore_SourceRegistryRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	valid := L1SourceRegistryEntry{
		SourceID:      "api:example",
		URL:           "https://api.example.com/items",
		Kind:          L1SourceKindOfficialAPI,
		TrustScore:    0.7,
		FetchInterval: time.Hour,
		LicenseNote:   "official api terms",
		Enabled:       true,
	}
	if _, err := store.SaveSourceRegistryEntry(ctx, valid); err != nil {
		t.Fatalf("valid SaveSourceRegistryEntry failed: %v", err)
	}
	invalidKind := valid
	invalidKind.SourceID = "bad:kind"
	invalidKind.Kind = "crawler"
	if _, err := store.SaveSourceRegistryEntry(ctx, invalidKind); err == nil {
		t.Fatal("expected invalid source kind to be rejected")
	}
	invalidURL := valid
	invalidURL.SourceID = "bad:url"
	invalidURL.URL = "file:///tmp/feed.xml"
	if _, err := store.SaveSourceRegistryEntry(ctx, invalidURL); err == nil {
		t.Fatal("expected invalid source url to be rejected")
	}
	invalidTrust := valid
	invalidTrust.SourceID = "bad:trust"
	invalidTrust.TrustScore = 1.5
	if _, err := store.SaveSourceRegistryEntry(ctx, invalidTrust); err == nil {
		t.Fatal("expected invalid trust score to be rejected")
	}
	invalidInterval := valid
	invalidInterval.SourceID = "bad:interval"
	invalidInterval.FetchInterval = 0
	if _, err := store.SaveSourceRegistryEntry(ctx, invalidInterval); err == nil {
		t.Fatal("expected invalid fetch interval to be rejected")
	}
	invalidLicense := valid
	invalidLicense.SourceID = "bad:license"
	invalidLicense.LicenseNote = ""
	if _, err := store.SaveSourceRegistryEntry(ctx, invalidLicense); err == nil {
		t.Fatal("expected missing license note to be rejected")
	}
}

func TestL1SQLiteStore_PromoteValidatedStagingItemToNews(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "news-evt-1",
		SourceID:     "rss:example",
		SourceURL:    "https://example.com/news/1",
		FetchedAt:    time.Date(2026, 5, 5, 8, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 5, 7, 0, 0, 0, time.UTC),
		RawText:      "ニュース本文そのもの",
		SummaryDraft: "ニュース要約案",
		Keywords:     []string{"AI", "RenCrow"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"rss:example": 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}

	news, err := store.PromoteValidatedStagingItemToNews(ctx, item.ID, "ai")
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
	}
	if news.ID == "" || news.StagingID != item.ID || news.Category != "ai" {
		t.Fatalf("unexpected news identity: %+v", news)
	}
	if news.RawText != "ニュース本文そのもの" || news.SummaryDraft != "ニュース要約案" {
		t.Fatalf("raw/summary fields were not preserved: %+v", news)
	}
	if news.RawHash != item.RawHash || news.SourceID != "rss:example" || news.SourceURL != "https://example.com/news/1" {
		t.Fatalf("source metadata not preserved: %+v", news)
	}
	recent, err := store.RecentNewsItems(ctx, "ai", 10)
	if err != nil {
		t.Fatalf("RecentNewsItems failed: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != news.ID {
		t.Fatalf("unexpected recent news: %+v", recent)
	}
	events, err := store.RecentEvents(ctx, "kb:news", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 3 || events[0].EventType != "news.promoted_from_staging" {
		t.Fatalf("expected news.promoted_from_staging event, got %+v", events)
	}
}

func TestL1SQLiteStore_PromoteNewsRequiresValidatedExternalItem(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	pending, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindExternalFetch,
		Namespace:   "kb:news",
		EventID:     "news-pending",
		SourceID:    "rss:example",
		SourceURL:   "https://example.com/news/pending",
		FetchedAt:   time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC),
		RawText:     "未検証ニュース",
		LicenseNote: "rss",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem pending failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToNews(ctx, pending.ID, "general"); err == nil {
		t.Fatal("expected pending news promotion to be rejected")
	}

	memory, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindMemoryCandidate,
		Namespace:   "conv:1",
		EventID:     "memory-not-news",
		SourceID:    "conversation",
		SourceURL:   "https://example.com/conversation/1",
		FetchedAt:   time.Date(2026, 5, 5, 9, 10, 0, 0, time.UTC),
		RawText:     "記憶候補",
		LicenseNote: "user provided",
		Meta:        map[string]interface{}{"type": "preference"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem memory failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, memory.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"conversation": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem memory failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToNews(ctx, memory.ID, "general"); err == nil {
		t.Fatal("expected memory candidate news promotion to be rejected")
	}
}

func TestL1SQLiteStore_BuildDailyDigestFromNews(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	for i, summary := range []string{"AIニュース要約1", "AIニュース要約2"} {
		eventID := fmt.Sprintf("digest-news-%d", i+1)
		item, err := store.SaveStagingItem(ctx, L1StagingItem{
			Kind:         L1StagingKindExternalFetch,
			Namespace:    "kb:news",
			EventID:      eventID,
			SourceID:     "rss:example",
			SourceURL:    "https://example.com/news/" + eventID,
			FetchedAt:    time.Date(2026, 5, 5, 8+i, 0, 0, 0, time.UTC),
			PublishedAt:  time.Date(2026, 5, 5, 7+i, 0, 0, 0, time.UTC),
			RawText:      "ニュース本文 " + eventID,
			SummaryDraft: summary,
			LicenseNote:  "rss",
		})
		if err != nil {
			t.Fatalf("SaveStagingItem failed: %v", err)
		}
		if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
			SourceTrustScores: map[string]float64{"rss:example": 0.9},
			MinimumTrustScore: 0.5,
			Now:               time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("ValidateStagingItem failed: %v", err)
		}
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, item.ID, "ai"); err != nil {
			t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
		}
	}

	digest, err := store.BuildDailyDigest(ctx, time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), "ai", 10)
	if err != nil {
		t.Fatalf("BuildDailyDigest failed: %v", err)
	}
	if digest.DigestDate != "2026-05-05" || digest.Category != "ai" || len(digest.NewsIDs) != 2 {
		t.Fatalf("unexpected digest identity: %+v", digest)
	}
	if !strings.Contains(digest.DigestText, "AIニュース要約1") || !strings.Contains(digest.DigestText, "AIニュース要約2") {
		t.Fatalf("digest text missing summaries: %q", digest.DigestText)
	}
	recent, err := store.RecentDailyDigests(ctx, "ai", 10)
	if err != nil {
		t.Fatalf("RecentDailyDigests failed: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != digest.ID {
		t.Fatalf("unexpected recent digests: %+v", recent)
	}
	events, err := store.RecentEvents(ctx, "kb:news", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if events[0].EventType != "news.daily_digest_built" {
		t.Fatalf("expected news.daily_digest_built event, got %+v", events[0])
	}
}

type stubDailyDigestSummarizer struct {
	gotCategory string
	gotSlot     string
	gotNews     []L1NewsItem
	text        string
	err         error
}

func (s *stubDailyDigestSummarizer) SummarizeDailyDigest(_ context.Context, _ time.Time, category string, slot string, news []L1NewsItem) (string, error) {
	s.gotCategory = category
	s.gotSlot = slot
	s.gotNews = append([]L1NewsItem(nil), news...)
	return s.text, s.err
}

type stubKnowledgeVectorSink struct {
	items []L1KnowledgeItem
	err   error
}

func (s *stubKnowledgeVectorSink) SaveL1KnowledgeItem(_ context.Context, item L1KnowledgeItem) error {
	s.items = append(s.items, item)
	return s.err
}

func TestL1SQLiteStore_PromoteKnowledgeSyncsVectorSink(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	vectorSink := &stubKnowledgeVectorSink{}
	store.WithKnowledgeVectorSink(vectorSink)

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "movie-vector-001",
		SourceID:     "api:movie",
		SourceURL:    "https://example.com/movie/vector",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "映画本文",
		SummaryDraft: "映画要約",
		Keywords:     []string{"SF"},
		LicenseNote:  "official api",
		Meta:         map[string]interface{}{"title": "Vector Movie"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"api:movie": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 10, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}

	kb, err := store.PromoteValidatedStagingItemToKnowledge(ctx, item.ID, "movie")
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
	}
	if len(vectorSink.items) != 1 || vectorSink.items[0].ID != kb.ID || vectorSink.items[0].Title != "Vector Movie" {
		t.Fatalf("knowledge vector sink was not called with promoted item: %+v", vectorSink.items)
	}
}

func TestL1SQLiteStore_BuildDailyDigestUsesSummarizer(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	summarizer := &stubDailyDigestSummarizer{text: "LLMが整理した朝の要約"}
	store.WithDailyDigestSummarizer(summarizer)

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "digest-llm-news",
		SourceID:     "rss:llm",
		SourceURL:    "https://example.com/news/llm",
		FetchedAt:    time.Date(2026, 5, 5, 8, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 5, 7, 30, 0, 0, time.UTC),
		RawText:      "ニュース本文",
		SummaryDraft: "決定論的な要約",
		Keywords:     []string{"AI"},
		LicenseNote:  "official rss excerpt",
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"rss:llm": 1.0},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 8, 10, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToNews(ctx, item.ID, "ai"); err != nil {
		t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
	}

	digest, err := store.BuildDailyDigestForSlot(ctx, time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), "ai", L1DailyDigestSlotMorning, 10)
	if err != nil {
		t.Fatalf("BuildDailyDigestForSlot failed: %v", err)
	}
	if digest.DigestText != "LLMが整理した朝の要約" {
		t.Fatalf("digest should use summarizer output, got %q", digest.DigestText)
	}
	if summarizer.gotCategory != "ai" || summarizer.gotSlot != L1DailyDigestSlotMorning || len(summarizer.gotNews) != 1 {
		t.Fatalf("summarizer received unexpected inputs: %+v", summarizer)
	}
}

func TestL1SQLiteStore_BuildDailyDigestForSlots(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	newsTimes := []struct {
		eventID string
		hour    int
		summary string
	}{
		{"morning-news", 8, "朝のニュース"},
		{"noon-news", 13, "昼のニュース"},
		{"evening-news", 20, "夜のニュース"},
	}
	for _, nt := range newsTimes {
		item, err := store.SaveStagingItem(ctx, L1StagingItem{
			Kind:         L1StagingKindExternalFetch,
			Namespace:    "kb:news",
			EventID:      nt.eventID,
			SourceID:     "rss:example",
			SourceURL:    "https://example.com/news/" + nt.eventID,
			FetchedAt:    time.Date(2026, 5, 5, nt.hour, 0, 0, 0, time.UTC),
			PublishedAt:  time.Date(2026, 5, 5, nt.hour, 0, 0, 0, time.UTC),
			RawText:      "本文 " + nt.eventID,
			SummaryDraft: nt.summary,
			LicenseNote:  "rss",
		})
		if err != nil {
			t.Fatalf("SaveStagingItem failed: %v", err)
		}
		if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
			SourceTrustScores: map[string]float64{"rss:example": 0.9},
			MinimumTrustScore: 0.5,
			Now:               time.Date(2026, 5, 5, 21, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("ValidateStagingItem failed: %v", err)
		}
		if _, err := store.PromoteValidatedStagingItemToNews(ctx, item.ID, "ai"); err != nil {
			t.Fatalf("PromoteValidatedStagingItemToNews failed: %v", err)
		}
	}

	digestDate := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	morning, err := store.BuildDailyDigestForSlot(ctx, digestDate, "ai", L1DailyDigestSlotMorning, 10)
	if err != nil {
		t.Fatalf("BuildDailyDigestForSlot morning failed: %v", err)
	}
	evening, err := store.BuildDailyDigestForSlot(ctx, digestDate, "ai", L1DailyDigestSlotEvening, 10)
	if err != nil {
		t.Fatalf("BuildDailyDigestForSlot evening failed: %v", err)
	}
	if morning.DigestSlot != L1DailyDigestSlotMorning || !strings.Contains(morning.DigestText, "朝のニュース") || strings.Contains(morning.DigestText, "昼のニュース") {
		t.Fatalf("unexpected morning digest: %+v", morning)
	}
	if evening.DigestSlot != L1DailyDigestSlotEvening || !strings.Contains(evening.DigestText, "夜のニュース") || strings.Contains(evening.DigestText, "朝のニュース") {
		t.Fatalf("unexpected evening digest: %+v", evening)
	}
	if morning.ID == evening.ID {
		t.Fatalf("slot digests should have distinct IDs: %s", morning.ID)
	}
}

func TestL1SQLiteStore_BuildDailyDigestRequiresNews(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.BuildDailyDigest(ctx, time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), "ai", 10); err == nil {
		t.Fatal("expected empty digest build to be rejected")
	}
	if _, err := store.RecentDailyDigests(ctx, "ai", 10); err != nil {
		t.Fatalf("RecentDailyDigests empty failed: %v", err)
	}
}

func TestL1SQLiteStore_PromoteValidatedStagingItemToKnowledge(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:movie",
		EventID:      "movie-001",
		SourceID:     "api:movie",
		SourceURL:    "https://example.com/movie/1",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "映画の本文情報",
		SummaryDraft: "映画の要約",
		Keywords:     []string{"SF", "宇宙"},
		LicenseNote:  "official api",
		Meta:         map[string]interface{}{"title": "Example Movie", "year": float64(2026)},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
		SourceTrustScores: map[string]float64{"api:movie": 0.9},
		MinimumTrustScore: 0.5,
		Now:               time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ValidateStagingItem failed: %v", err)
	}

	kb, err := store.PromoteValidatedStagingItemToKnowledge(ctx, item.ID, "movie")
	if err != nil {
		t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
	}
	if kb.Domain != "movie" || kb.Title != "Example Movie" || kb.RawText != item.RawText || kb.SummaryDraft != item.SummaryDraft {
		t.Fatalf("unexpected knowledge item: %+v", kb)
	}
	recent, err := store.RecentKnowledgeItems(ctx, "movie", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != kb.ID {
		t.Fatalf("unexpected recent knowledge: %+v", recent)
	}
	events, err := store.RecentEvents(ctx, "kb:movie", 10)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if events[0].EventType != "knowledge.promoted_from_staging" {
		t.Fatalf("expected knowledge.promoted_from_staging event, got %+v", events[0])
	}
}

func TestL1SQLiteStore_SearchKnowledgeItemsFTS(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	items := []struct {
		eventID string
		title   string
		raw     string
		summary string
	}{
		{"movie-space", "Space Film", "宇宙船と重力の映画", "父と娘のSF"},
		{"movie-cooking", "Cooking Film", "料理人の映画", "厨房の物語"},
	}
	for _, it := range items {
		item, err := store.SaveStagingItem(ctx, L1StagingItem{
			Kind:         L1StagingKindExternalFetch,
			Namespace:    "kb:movie",
			EventID:      it.eventID,
			SourceID:     "api:movie",
			SourceURL:    "https://example.com/movie/" + it.eventID,
			FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
			RawText:      it.raw,
			SummaryDraft: it.summary,
			Keywords:     []string{"映画"},
			LicenseNote:  "official api",
			Meta:         map[string]interface{}{"title": it.title},
		})
		if err != nil {
			t.Fatalf("SaveStagingItem failed: %v", err)
		}
		if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
			SourceTrustScores: map[string]float64{"api:movie": 0.9},
			MinimumTrustScore: 0.5,
			Now:               time.Date(2026, 5, 5, 11, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("ValidateStagingItem failed: %v", err)
		}
		if _, err := store.PromoteValidatedStagingItemToKnowledge(ctx, item.ID, "movie"); err != nil {
			t.Fatalf("PromoteValidatedStagingItemToKnowledge failed: %v", err)
		}
	}

	results, err := store.SearchKnowledgeItemsFTS(ctx, "movie", "重力", 10)
	if err != nil {
		t.Fatalf("SearchKnowledgeItemsFTS failed: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Space Film" {
		t.Fatalf("unexpected FTS results: %+v", results)
	}
}

func TestL1SQLiteStore_SearchWikiPageIndex(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	updated := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.SaveWikiPageIndex(ctx, WikiPageIndexItem{
		PageID:          "concept:recall-pack",
		Path:            "docs/wiki/concepts/recall-pack.md",
		Title:           "RecallPack",
		Type:            "concept",
		Status:          WikiPageStatusActive,
		Owner:           "core",
		CanonicalSource: "docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md",
		SourcePaths: []string{
			"docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md",
			"internal/domain/conversation/recall_pack.go",
		},
		Related:     []string{"docs/wiki/concepts/memory-lifecycle.md"},
		Summary:     "RecallPack は Mio に渡す文脈を選別済みにする prompt 注入用フォーマット。",
		ContentHash: "hash-recall-pack",
		UpdatedAt:   updated,
	}); err != nil {
		t.Fatalf("SaveWikiPageIndex active failed: %v", err)
	}
	if _, err := store.SaveWikiPageIndex(ctx, WikiPageIndexItem{
		PageID:          "concept:old-wiki",
		Path:            "docs/wiki/concepts/old-wiki.md",
		Title:           "Old Wiki",
		Type:            "concept",
		Status:          WikiPageStatusArchived,
		Owner:           "core",
		CanonicalSource: "docs/wiki/log.md",
		SourcePaths:     []string{"docs/wiki/log.md"},
		Summary:         "archived page should not be returned",
		UpdatedAt:       updated.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveWikiPageIndex archived failed: %v", err)
	}

	results, err := store.SearchWikiPageIndex(ctx, "Mio prompt RecallPack", 10)
	if err != nil {
		t.Fatalf("SearchWikiPageIndex failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 active wiki page, got %+v", results)
	}
	if results[0].PageID != "concept:recall-pack" || results[0].Path != "docs/wiki/concepts/recall-pack.md" {
		t.Fatalf("unexpected wiki page result: %+v", results[0])
	}
	if len(results[0].SourcePaths) != 2 || results[0].SourcePaths[1] != "internal/domain/conversation/recall_pack.go" {
		t.Fatalf("source paths were not preserved: %+v", results[0].SourcePaths)
	}
}

func TestL1SQLiteStore_SaveWikiPageIndexValidatesPathAndSource(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.SaveWikiPageIndex(ctx, WikiPageIndexItem{
		PageID:          "bad:path",
		Path:            "../secret.md",
		Title:           "Bad",
		Type:            "concept",
		Status:          WikiPageStatusActive,
		CanonicalSource: "docs/wiki/index.md",
		SourcePaths:     []string{"docs/wiki/index.md"},
	}); err == nil {
		t.Fatal("expected invalid wiki path to be rejected")
	}
	if _, err := store.SaveWikiPageIndex(ctx, WikiPageIndexItem{
		PageID:          "missing:source",
		Path:            "docs/wiki/concepts/missing-source.md",
		Title:           "Missing Source",
		Type:            "concept",
		Status:          WikiPageStatusActive,
		CanonicalSource: "docs/wiki/index.md",
	}); err == nil {
		t.Fatal("expected missing source paths to be rejected")
	}
}

func TestL1SQLiteStore_PromoteKnowledgeRequiresValidatedItem(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	item, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind:        L1StagingKindExternalFetch,
		Namespace:   "kb:movie",
		EventID:     "movie-pending",
		SourceID:    "api:movie",
		SourceURL:   "https://example.com/movie/pending",
		FetchedAt:   time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		RawText:     "未検証KB",
		LicenseNote: "api",
		Meta:        map[string]interface{}{"title": "Pending"},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}
	if _, err := store.PromoteValidatedStagingItemToKnowledge(ctx, item.ID, "movie"); err == nil {
		t.Fatal("expected pending knowledge promotion to be rejected")
	}
	if _, err := store.RecentKnowledgeItems(ctx, "bad domain", 10); err == nil {
		t.Fatal("expected invalid knowledge domain to be rejected")
	}
}

func TestL1SQLiteStore_ExportAndImportStagingItemsJSONL(t *testing.T) {
	ctx := context.Background()
	source, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore source failed: %v", err)
	}
	defer source.Close()

	if _, err := source.SaveStagingItem(ctx, L1StagingItem{
		Kind:         L1StagingKindExternalFetch,
		Namespace:    "kb:news",
		EventID:      "jsonl-1",
		SourceID:     "rss:example",
		SourceURL:    "https://example.com/jsonl/1",
		FetchedAt:    time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		RawText:      "JSONL raw",
		SummaryDraft: "JSONL summary",
		Keywords:     []string{"jsonl"},
		LicenseNote:  "rss",
	}); err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}

	var buf bytes.Buffer
	if err := source.ExportStagingItemsJSONL(ctx, L1StagingStatusPending, &buf); err != nil {
		t.Fatalf("ExportStagingItemsJSONL failed: %v", err)
	}
	if !strings.Contains(buf.String(), "JSONL raw") || !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("unexpected JSONL output: %q", buf.String())
	}

	target, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore target failed: %v", err)
	}
	defer target.Close()
	imported, err := target.ImportStagingItemsJSONL(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ImportStagingItemsJSONL failed: %v", err)
	}
	if imported != 1 {
		t.Fatalf("expected 1 imported item, got %d", imported)
	}
	items, err := target.RecentStagingItems(ctx, L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 || items[0].RawText != "JSONL raw" || items[0].SummaryDraft != "JSONL summary" {
		t.Fatalf("unexpected imported items: %+v", items)
	}
}
