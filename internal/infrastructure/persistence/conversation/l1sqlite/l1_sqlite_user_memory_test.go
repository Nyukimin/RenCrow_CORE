package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestL1SQLiteStore_UserMemoryCRUD(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	mem, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "短く論理的な説明を好む",
		State:            MemoryStateCandidate,
		EvidenceEventIDs: []string{"evt_1"},
		Confidence:       0.8,
		Sensitivity:      "normal",
		Source:           "test",
	})
	if err != nil {
		t.Fatalf("CreateUserMemory failed: %v", err)
	}
	if mem.Namespace != "user:ren" || mem.State != MemoryStateCandidate || !mem.Active {
		t.Fatalf("unexpected user memory: %+v", mem)
	}

	confirmed, err := store.UpdateUserMemoryState(ctx, mem.ID, MemoryStateConfirmed, "user_explicit")
	if err != nil {
		t.Fatalf("UpdateUserMemoryState failed: %v", err)
	}
	if confirmed.State != MemoryStateConfirmed {
		t.Fatalf("expected confirmed, got %+v", confirmed)
	}

	memories, err := store.ListUserMemories(ctx, "ren", "", false, 10)
	if err != nil {
		t.Fatalf("ListUserMemories failed: %v", err)
	}
	if len(memories) != 1 || memories[0].ID != mem.ID {
		t.Fatalf("unexpected user memories: %+v", memories)
	}

	forgotten, err := store.ForgetUserMemory(ctx, mem.ID, "user_requested")
	if err != nil {
		t.Fatalf("ForgetUserMemory failed: %v", err)
	}
	if forgotten.Active {
		t.Fatalf("forgotten memory should be inactive: %+v", forgotten)
	}
	memories, err = store.ListUserMemories(ctx, "ren", "", false, 10)
	if err != nil {
		t.Fatalf("ListUserMemories after forget failed: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("inactive memory should be filtered: %+v", memories)
	}
}

func TestL1SQLiteStore_ListPromptInjectableUserMemories(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	confirmed, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "短く答える",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-1"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create confirmed memory failed: %v", err)
	}
	if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:      "ren",
		Type:        domainmemory.UserMemoryTypePreference,
		Statement:   "candidate should stay out",
		State:       MemoryStateCandidate,
		Sensitivity: "normal",
		Scope:       "all_personas",
	}); err != nil {
		t.Fatalf("Create candidate memory failed: %v", err)
	}
	if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "sensitive should stay out",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-2"},
		Sensitivity:      "private",
		Scope:            "all_personas",
		Source:           "user_explicit",
	}); err != nil {
		t.Fatalf("Create private memory failed: %v", err)
	}

	items, err := store.ListPromptInjectableUserMemories(ctx, "ren", "mio", 10)
	if err != nil {
		t.Fatalf("ListPromptInjectableUserMemories failed: %v", err)
	}
	if len(items) != 1 || items[0].ID != confirmed.ID {
		t.Fatalf("unexpected injectable memories: %+v", items)
	}
}

func TestL1SQLiteStore_UserMemoryRejectsUnsafePromotion(t *testing.T) {
	store, err := NewL1SQLiteStore(l1TestTempDir(t) + "/l1.db")
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	_, err = store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
		UserID:      "ren",
		Type:        domainmemory.UserMemoryTypePreference,
		Statement:   "短く論理的な説明を好む",
		State:       MemoryStateConfirmed,
		Sensitivity: "normal",
		Source:      "test",
	})
	if err == nil {
		t.Fatal("confirmed user memory without evidence should fail")
	}
}
