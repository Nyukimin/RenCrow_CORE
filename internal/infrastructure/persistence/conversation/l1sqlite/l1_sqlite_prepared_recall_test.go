package l1sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type preparedRecallUserMemoryReader interface {
	ReadPreparedRecallUserMemories(context.Context, string) ([]domainmemory.UserMemory, string, error)
}

func readPreparedRecallUserMemories(t *testing.T, store *L1SQLiteStore, ownerID string) ([]domainmemory.UserMemory, string, error) {
	t.Helper()
	reader, ok := any(store).(preparedRecallUserMemoryReader)
	if !ok {
		t.Fatal("L1 SQLite owner must expose ReadPreparedRecallUserMemories")
	}
	return reader.ReadPreparedRecallUserMemories(context.Background(), ownerID)
}

func TestPreparedRecallUserMemorySourceIsOwnerBoundAndEligible(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "prepared-recall-owner.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	confirmedID := addPreparedRecallOwnerMemory(t, store, "confirmed", "ren", "confirmed")
	pinnedID := addPreparedRecallOwnerMemory(t, store, "pinned", "ren", "pinned")
	addPreparedRecallOwnerMemory(t, store, "candidate", "ren", "candidate")
	inactiveID := addPreparedRecallOwnerMemory(t, store, "inactive", "ren", "confirmed")
	if _, err := store.OwnerTransitionUserMemory(ctx, "prepared-inactive-forget", "ren", "ren", inactiveID, domainmemory.UserMemoryOwnerOperationForget, "", "remove from recall"); err != nil {
		t.Fatalf("forget inactive memory failed: %v", err)
	}
	otherOwnerID := addPreparedRecallOwnerMemory(t, store, "other-owner", "other", "confirmed")
	insertPreparedRecallEvent(t, store, preparedRecallEvent{
		ID: "prepared-sensitive", OwnerID: "ren", Statement: "sensitive memory", Sensitivity: "sensitive",
		State: domainmemory.MemoryStateConfirmed,
	})

	memories, coverage, err := readPreparedRecallUserMemories(t, store, "ren")
	if err != nil {
		t.Fatalf("ReadPreparedRecallUserMemories failed: %v", err)
	}
	if coverage != domconv.PreparedRecallCoverageFull {
		t.Fatalf("coverage=%q, want full", coverage)
	}
	gotIDs := make([]string, 0, len(memories))
	for _, memory := range memories {
		gotIDs = append(gotIDs, memory.ID)
		if memory.UserID != "ren" || memory.Namespace != "user:ren" || !memory.Active || (memory.State != domainmemory.MemoryStateConfirmed && memory.State != domainmemory.MemoryStatePinned) || memory.Sensitivity != "normal" {
			t.Fatalf("source returned ineligible memory: %+v", memory)
		}
	}
	wantIDs := map[string]bool{confirmedID: true, pinnedID: true}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("memory IDs=%v, want only confirmed/pinned owner memories %v (inactive=%q other owner=%q)", gotIDs, wantIDs, inactiveID, otherOwnerID)
	}
	for _, id := range gotIDs {
		if !wantIDs[id] {
			t.Fatalf("memory IDs=%v, unexpected owner/state/sensitivity record %q", gotIDs, id)
		}
	}
	if len(memories) != 0 {
		memories[0].EvidenceEventIDs[0] = "caller-mutated"
		again, _, err := readPreparedRecallUserMemories(t, store, "ren")
		if err != nil {
			t.Fatalf("second prepared recall read failed: %v", err)
		}
		if again[0].EvidenceEventIDs[0] == "caller-mutated" {
			t.Fatal("source returned mutable evidence slice storage")
		}
	}
}

func TestPreparedRecallUserMemorySourceCapsAndReportsPartialCoverage(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "prepared-recall-cap.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	insertPreparedRecallMemoryBatch(t, store, domconv.PreparedRecallMaxRecordsPerSource+1)

	first, coverage, err := readPreparedRecallUserMemories(t, store, "ren")
	if err != nil {
		t.Fatalf("ReadPreparedRecallUserMemories failed: %v", err)
	}
	if len(first) != domconv.PreparedRecallMaxRecordsPerSource {
		t.Fatalf("returned %d memories, want cap %d", len(first), domconv.PreparedRecallMaxRecordsPerSource)
	}
	if coverage != domconv.PreparedRecallCoveragePartial {
		t.Fatalf("coverage=%q, want partial after truncation", coverage)
	}
	second, secondCoverage, err := readPreparedRecallUserMemories(t, store, "ren")
	if err != nil {
		t.Fatalf("repeat prepared recall read failed: %v", err)
	}
	if secondCoverage != coverage || len(second) != len(first) || first[0].ID != second[0].ID || first[len(first)-1].ID != second[len(second)-1].ID {
		t.Fatal("bounded source result is not deterministic across identical snapshots")
	}
}

func TestPreparedRecallUserMemorySourceRejectsMalformedEligibleRows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]interface{})
	}{
		{name: "unknown scope", mutate: func(meta map[string]interface{}) { meta["scope"] = "untrusted" }},
		{name: "unknown sensitivity", mutate: func(meta map[string]interface{}) { meta["sensitivity"] = "untrusted" }},
		{name: "missing evidence", mutate: func(meta map[string]interface{}) { meta["evidence_event_ids"] = []string{} }},
		{name: "missing active", mutate: func(meta map[string]interface{}) { delete(meta, "active") }},
		{name: "invalid active type", mutate: func(meta map[string]interface{}) { meta["active"] = "true" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "prepared-recall-invalid.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore failed: %v", err)
			}
			defer store.Close()
			meta := preparedRecallMeta("ren", "valid statement", "normal", "all_personas", true)
			tt.mutate(meta)
			insertPreparedRecallEvent(t, store, preparedRecallEvent{ID: "prepared-invalid", OwnerID: "ren", Statement: "valid statement", Metadata: meta})

			memories, coverage, err := readPreparedRecallUserMemories(t, store, "ren")
			if err == nil {
				t.Fatalf("malformed row was accepted: memories=%+v coverage=%q", memories, coverage)
			}
			if len(memories) != 0 {
				t.Fatalf("failed source returned partial memories: %+v", memories)
			}
		})
	}
}

func TestPreparedRecallUserMemorySourceRequiresExplicitOwnerAndReadHandle(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "prepared-recall-read-fail.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	if _, _, err := readPreparedRecallUserMemories(t, store, ""); err == nil {
		t.Fatal("empty authenticated owner must be rejected")
	}
	if _, _, err := readPreparedRecallUserMemories(t, store, " ren "); err == nil {
		t.Fatal("owner identity must not be normalized from caller input")
	}
	if err := store.readDB.Close(); err != nil {
		t.Fatalf("close exact L1 read handle failed: %v", err)
	}
	if err := store.db.PingContext(context.Background()); err != nil {
		t.Fatalf("writer handle should remain open for the no-fallback assertion: %v", err)
	}
	if memories, coverage, err := readPreparedRecallUserMemories(t, store, "ren"); err == nil || memories != nil || coverage != "" {
		t.Fatalf("failed exact read must return no data and no fallback: memories=%+v coverage=%q err=%v", memories, coverage, err)
	}
}

type preparedRecallEvent struct {
	ID          string
	OwnerID     string
	Statement   string
	Sensitivity string
	State       string
	Metadata    map[string]interface{}
}

func addPreparedRecallOwnerMemory(t *testing.T, store *L1SQLiteStore, key, ownerID, targetState string) string {
	t.Helper()
	ctx := context.Background()
	proposed, err := store.OwnerProposeUserMemory(ctx, "prepared-"+key+"-propose", ownerID, ownerID, domainmemory.UserMemoryTypePreference, "prepared recall "+key, "test owner review")
	if err != nil {
		t.Fatalf("propose %s memory failed: %v", key, err)
	}
	if targetState == "candidate" {
		return proposed.Item.ID
	}
	confirmed, err := store.OwnerTransitionUserMemory(ctx, "prepared-"+key+"-confirm", ownerID, ownerID, proposed.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "test confirmation")
	if err != nil {
		t.Fatalf("confirm %s memory failed: %v", key, err)
	}
	switch targetState {
	case "confirmed":
		return confirmed.Item.ID
	case "pinned":
		pinned, err := store.OwnerTransitionUserMemory(ctx, "prepared-"+key+"-pin", ownerID, ownerID, proposed.Item.ID, domainmemory.UserMemoryOwnerOperationPin, "", "test pin")
		if err != nil {
			t.Fatalf("pin %s memory failed: %v", key, err)
		}
		return pinned.Item.ID
	default:
		t.Fatalf("unsupported target state %q", targetState)
		return ""
	}
}

func insertPreparedRecallMemoryBatch(t *testing.T, store *L1SQLiteStore, count int) {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin prepared recall fixture transaction: %v", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("prepared-cap-%05d", i)
		statement := fmt.Sprintf("prepared memory %05d", i)
		meta, err := json.Marshal(preparedRecallMeta("ren", statement, "normal", "all_personas", true))
		if err != nil {
			t.Fatalf("marshal prepared recall fixture: %v", err)
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, 'user:ren', '', '', 0, '', ?, ?, ?, ?, ?, 'test-source', ?, ?)`,
			id, string(domconv.SpeakerMemory), statement, string(meta), domainmemory.MemoryStateConfirmed, MemoryLayerL1, now, now); err != nil {
			t.Fatalf("insert prepared recall fixture %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit prepared recall fixture: %v", err)
	}
}

func insertPreparedRecallEvent(t *testing.T, store *L1SQLiteStore, fixture preparedRecallEvent) {
	t.Helper()
	meta := fixture.Metadata
	if meta == nil {
		sensitivity := fixture.Sensitivity
		if sensitivity == "" {
			sensitivity = "normal"
		}
		meta = preparedRecallMeta(fixture.OwnerID, fixture.Statement, sensitivity, "all_personas", true)
	}
	if fixture.State == "" {
		fixture.State = domainmemory.MemoryStateConfirmed
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal prepared recall memory metadata: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, '', '', 0, '', ?, ?, ?, ?, ?, 'test-source', ?, ?)`,
		fixture.ID, "user:"+fixture.OwnerID, string(domconv.SpeakerMemory), fixture.Statement, string(encoded), fixture.State, MemoryLayerL1, now, now); err != nil {
		t.Fatalf("insert prepared recall memory %q: %v", fixture.ID, err)
	}
}

func preparedRecallMeta(ownerID, statement, sensitivity, scope string, active bool) map[string]interface{} {
	return map[string]interface{}{
		"type":                domainmemory.UserMemoryTypePreference,
		"user_id":             ownerID,
		"statement":           statement,
		"evidence_event_ids":  []string{"evidence-" + ownerID},
		"created_by_event_id": "evidence-" + ownerID,
		"confidence":          0.8,
		"sensitivity":         sensitivity,
		"scope":               scope,
		"active":              active,
	}
}
