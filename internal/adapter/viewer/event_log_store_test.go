package viewer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestEventLogStoreQueryFiltersByAgentAndJob(t *testing.T) {
	store, err := NewEventLogStore(filepath.Join(t.TempDir(), "orchestrator_events.jsonl"))
	if err != nil {
		t.Fatalf("NewEventLogStore failed: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	for _, ev := range []orchestrator.OrchestratorEvent{
		{Type: "agent.note", From: "mio", To: "user", JobID: "job-1", Timestamp: now},
		{Type: "agent.response", From: "coder1", To: "shiro", JobID: "job-1", Timestamp: now},
		{Type: "agent.response", From: "coder2", To: "shiro", JobID: "job-2", Timestamp: now},
	} {
		if err := store.Append(ev); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	items, err := store.Query(context.Background(), LogFilter{Agent: "coder1", JobID: "job-1", Limit: 10})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].From != "coder1" {
		t.Fatalf("from = %q, want coder1", items[0].From)
	}
}

func TestEventLogStoreQueryWithoutFiltersReturnsRecentTail(t *testing.T) {
	store, err := NewEventLogStore(filepath.Join(t.TempDir(), "orchestrator_events.jsonl"))
	if err != nil {
		t.Fatalf("NewEventLogStore failed: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	for _, ev := range []orchestrator.OrchestratorEvent{
		{Type: "agent.note", Content: "old", Timestamp: now},
		{Type: "agent.note", Content: "middle", Timestamp: now},
		{Type: "agent.note", Content: "new", Timestamp: now},
	} {
		if err := store.Append(ev); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	items, err := store.Query(context.Background(), LogFilter{Limit: 2})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].Content != "new" || items[1].Content != "middle" {
		t.Fatalf("unexpected tail order: %+v", items)
	}
}
