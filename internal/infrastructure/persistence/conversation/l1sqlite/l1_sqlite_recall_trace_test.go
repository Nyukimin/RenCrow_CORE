package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestL1SQLiteStore_RecallTraceTables(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	createdAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	trace := domconv.RecallTraceRecord{
		TraceID:             "trace:test",
		TurnID:              "turn:test",
		ChatID:              "chat-1",
		Persona:             "mio",
		Route:               "chat",
		UserMessageHash:     "abc",
		QueryTextRedacted:   "hello",
		CreatedAt:           createdAt,
		RecallPolicyVersion: "memory-lifecycle-v1",
		TotalCandidates:     2,
		Status:              "started",
	}
	if err := store.StartRecallTrace(ctx, trace); err != nil {
		t.Fatalf("StartRecallTrace failed: %v", err)
	}
	items := []domconv.RecallTraceItemRecord{
		{
			ItemID:        "item-1",
			TraceID:       trace.TraceID,
			Layer:         "L3",
			MemoryID:      "user:ren:user_memory:1",
			SourceType:    "user_memory",
			Status:        domconv.TraceStatusInjected,
			Score:         0.9,
			Reason:        "confirmed user memory",
			Injected:      true,
			PromptSection: domconv.PromptSectionUserMemory,
			TokenCount:    8,
			Summary:       "短く答える",
			Kind:          "user_memory",
		},
		{
			ItemID:        "item-2",
			TraceID:       trace.TraceID,
			Layer:         "L2",
			Status:        domconv.TraceStatusFilteredStatus,
			Reason:        "candidate",
			PromptSection: domconv.PromptSectionUserMemory,
			Kind:          "user_memory",
		},
	}
	if err := store.AddRecallTraceItems(ctx, trace.TraceID, items); err != nil {
		t.Fatalf("AddRecallTraceItems failed: %v", err)
	}
	if err := store.AddPromptInjectionEvents(ctx, trace.TraceID, []domconv.PromptInjectionEventRecord{{
		InjectionID:   "inj-1",
		TraceID:       trace.TraceID,
		PromptSection: domconv.PromptSectionUserMemory,
		OrderIndex:    0,
		ItemIDs:       []string{"item-1"},
		TokenCount:    8,
		CreatedAt:     createdAt,
	}}); err != nil {
		t.Fatalf("AddPromptInjectionEvents failed: %v", err)
	}
	if err := store.FinishRecallTrace(ctx, trace.TraceID, "completed", 1, 8); err != nil {
		t.Fatalf("FinishRecallTrace failed: %v", err)
	}

	var status string
	var injectedCount int
	var totalTokens int
	if err := store.db.QueryRowContext(ctx, `SELECT status, injected_count, total_injected_tokens FROM recall_trace WHERE trace_id = ?`, trace.TraceID).Scan(&status, &injectedCount, &totalTokens); err != nil {
		t.Fatalf("query recall_trace failed: %v", err)
	}
	if status != "completed" || injectedCount != 1 || totalTokens != 8 {
		t.Fatalf("unexpected trace summary: status=%s injected=%d tokens=%d", status, injectedCount, totalTokens)
	}
	var itemCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recall_trace_item WHERE trace_id = ?`, trace.TraceID).Scan(&itemCount); err != nil {
		t.Fatalf("query trace items failed: %v", err)
	}
	if itemCount != 2 {
		t.Fatalf("trace item count = %d, want 2", itemCount)
	}
}
