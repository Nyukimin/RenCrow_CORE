package l1sqlite

import (
	"context"
	"path/filepath"
	"strings"
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

func TestL1SQLiteStore_CreateUserMemoryCandidateWithRequestIsStableAndReplays(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(l1TestTempDir(t), "l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}

	input := domainmemory.CreateUserMemoryInput{
		UserID:           " user-a ",
		Type:             " preference ",
		Statement:        "  Ren prefers concise explanations  ",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{" evt-1 ", "evt-2"},
		Confidence:       0.8,
		Sensitivity:      " normal ",
		Scope:            " mio_only ",
		Source:           "model-controlled",
	}
	created, replay, err := store.CreateUserMemoryCandidateWithRequest(ctx, " request-1 ", " shiro ", input)
	if err != nil {
		t.Fatalf("CreateUserMemoryCandidateWithRequest failed: %v", err)
	}
	if replay {
		t.Fatal("first candidate write must not be a replay")
	}
	if !strings.HasPrefix(created.ID, "user-memory-candidate/sha256:") || created.UserID != "user-a" || created.Namespace != "user:user-a" || created.Type != domainmemory.UserMemoryTypePreference || created.Statement != "Ren prefers concise explanations" || created.State != MemoryStateCandidate || created.Sensitivity != "normal" || created.Scope != "mio_only" || len(created.EvidenceEventIDs) != 2 || created.Confidence != 0.8 || !created.Active {
		t.Fatalf("created candidate = %#v", created)
	}
	if domainmemory.IsUserMemoryPromptInjectable(*created, "mio") {
		t.Fatal("candidate memory must not be prompt injectable")
	}

	events, err := store.RecentEvents(ctx, "user:user-a", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("creation events = %#v, err=%v", events, err)
	}
	if events[0].EventType != "memory.user_created" || events[0].Source != "agent:shiro" || events[0].Payload["request_id"] != "request-1" || events[0].Payload["actor_id"] != "shiro" || events[0].Payload["memory_id"] != created.ID {
		t.Fatalf("creation event = %#v", events[0])
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE id = ?`, created.ID); got != 1 {
		t.Fatalf("candidate rows = %d, want 1", got)
	}
	var source, state string
	if err := store.db.QueryRowContext(ctx, `SELECT source, memory_state FROM l1_memory_event WHERE id = ?`, created.ID).Scan(&source, &state); err != nil {
		t.Fatalf("candidate row metadata: %v", err)
	}
	if source != "agent:shiro" || state != MemoryStateCandidate {
		t.Fatalf("candidate source/state = %q/%q", source, state)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1SQLiteStore failed: %v", err)
	}
	defer reopened.Close()
	replayed, replay, err := reopened.CreateUserMemoryCandidateWithRequest(ctx, "request-1", "shiro", domainmemory.CreateUserMemoryInput{
		UserID:           "user-a",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "Ren prefers concise explanations",
		State:            MemoryStatePinned,
		EvidenceEventIDs: []string{"evt-1", "evt-2"},
		Confidence:       0.8,
		Sensitivity:      "normal",
		Scope:            "mio_only",
		Source:           "different-model-source",
	})
	if err != nil || !replay || replayed.ID != created.ID {
		t.Fatalf("replayed candidate = %#v replay=%v err=%v", replayed, replay, err)
	}
	if got := countL1Rows(t, ctx, reopened, `SELECT count(*) FROM l1_memory_event WHERE id = ?`, created.ID); got != 1 {
		t.Fatalf("replayed candidate rows = %d, want 1", got)
	}
	if events, err := reopened.RecentEvents(ctx, "user:user-a", 10); err != nil || len(events) != 1 {
		t.Fatalf("replayed creation events = %#v, err=%v", events, err)
	}

	conflicts := []struct {
		name  string
		actor string
		input domainmemory.CreateUserMemoryInput
	}{
		{name: "statement", actor: "shiro", input: domainmemory.CreateUserMemoryInput{UserID: "user-a", Type: domainmemory.UserMemoryTypePreference, Statement: "different statement", Confidence: 0.8, Sensitivity: "normal", Scope: "mio_only"}},
		{name: "actor", actor: "mio", input: domainmemory.CreateUserMemoryInput{UserID: "user-a", Type: domainmemory.UserMemoryTypePreference, Statement: "Ren prefers concise explanations", Confidence: 0.8, Sensitivity: "normal", Scope: "mio_only"}},
		{name: "user", actor: "shiro", input: domainmemory.CreateUserMemoryInput{UserID: "user-b", Type: domainmemory.UserMemoryTypePreference, Statement: "Ren prefers concise explanations", Confidence: 0.8, Sensitivity: "normal", Scope: "mio_only"}},
	}
	for _, test := range conflicts {
		t.Run(test.name, func(t *testing.T) {
			if _, replay, err := reopened.CreateUserMemoryCandidateWithRequest(ctx, "request-1", test.actor, test.input); err == nil || replay {
				t.Fatalf("conflict result replay=%v err=%v", replay, err)
			}
			if got := countL1Rows(t, ctx, reopened, `SELECT count(*) FROM l1_memory_event`); got != 1 {
				t.Fatalf("conflict mutated memory rows = %d", got)
			}
			if events, err := reopened.RecentEvents(ctx, "user:user-a", 10); err != nil || len(events) != 1 {
				t.Fatalf("conflict mutated audit events = %#v, err=%v", events, err)
			}
		})
	}
}

func TestL1SQLiteStore_CreateUserMemoryCandidateWithRequestRollsBackOnAuditFailure(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_user_memory_candidate_audit
BEFORE INSERT ON l1_event_log
WHEN NEW.event_type = 'memory.user_created'
BEGIN
  SELECT RAISE(ABORT, 'forced user memory audit failure');
END`); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}

	_, _, err = store.CreateUserMemoryCandidateWithRequest(ctx, "request-rollback", "shiro", domainmemory.CreateUserMemoryInput{
		UserID: "user-a", Type: domainmemory.UserMemoryTypePreference, Statement: "must roll back",
	})
	if err == nil {
		t.Fatal("candidate write must fail when audit insert fails")
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event`); got != 0 {
		t.Fatalf("memory row remained after audit failure: %d", got)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_event_log`); got != 0 {
		t.Fatalf("audit row remained after audit failure: %d", got)
	}
}

func TestL1SQLiteStore_FindUserMemoryByIDIsExactAndStrict(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	created, _, err := store.CreateUserMemoryCandidateWithRequest(ctx, "request-find", "shiro", domainmemory.CreateUserMemoryInput{
		UserID: "user-a", Type: domainmemory.UserMemoryTypePreference, Statement: "exact candidate",
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	found, ok, err := store.FindUserMemoryByID(ctx, created.ID)
	if err != nil || !ok || found.ID != created.ID || found.UserID != "user-a" || found.State != MemoryStateCandidate {
		t.Fatalf("exact find = %#v found=%v err=%v", found, ok, err)
	}
	missing, ok, err := store.FindUserMemoryByID(ctx, created.ID+"-suffix")
	if err != nil || ok || missing.ID != "" {
		t.Fatalf("suffix find = %#v found=%v err=%v", missing, ok, err)
	}

	var originalMeta string
	if err := store.db.QueryRowContext(ctx, `SELECT meta_json FROM l1_memory_event WHERE id = ?`, created.ID).Scan(&originalMeta); err != nil {
		t.Fatalf("read original metadata: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ? WHERE id = ?`, "{malformed", created.ID); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}
	if _, ok, err := store.FindUserMemoryByID(ctx, created.ID); err == nil || ok {
		t.Fatalf("malformed find = found=%v err=%v", ok, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ?, namespace = ? WHERE id = ?`, originalMeta, "conv:user-a", created.ID); err != nil {
		t.Fatalf("corrupt namespace: %v", err)
	}
	if _, ok, err := store.FindUserMemoryByID(ctx, created.ID); err == nil || ok {
		t.Fatalf("mismatched namespace find = found=%v err=%v", ok, err)
	}
}
