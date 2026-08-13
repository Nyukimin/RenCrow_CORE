package l1sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestImportChatGPTL3RecordsIsIdempotentAndQueuesCurrentUserOnly(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	records := []ChatGPTL3ImportRecord{
		{Format: ChatGPTL3ArtifactFormat, ExportID: "export-hash", EvidenceID: "chatgpt_export:conv-1:user-1", ConversationID: "conv-1", MessageID: "user-1", MessageCreatedAt: now, Role: "user", Text: "私は映画が好き", ContentType: "text", Content: json.RawMessage(`{"parts":["私は映画が好き"]}`), OnCurrentBranch: true},
		{Format: ChatGPTL3ArtifactFormat, ExportID: "export-hash", EvidenceID: "chatgpt_export:conv-1:assistant-old", ConversationID: "conv-1", MessageID: "assistant-old", MessageCreatedAt: now.Add(time.Second), Role: "assistant", Text: "古い分岐", ContentType: "text", Content: json.RawMessage(`{"parts":["古い分岐"]}`), OnCurrentBranch: false},
	}
	first, err := store.ImportChatGPTL3Records(context.Background(), records, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 2 || first.Existing != 0 || first.Queued != 1 {
		t.Fatalf("first=%+v", first)
	}
	second, err := store.ImportChatGPTL3Records(context.Background(), records, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Existing != 2 || second.Queued != 0 {
		t.Fatalf("second=%+v", second)
	}
	events, err := store.RecentByNamespace(context.Background(), chatGPTConversationNamespace("conv-1"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Layer != "L3" || events[0].Source != "chatgpt_export" {
		t.Fatalf("events=%+v", events)
	}
	jobs, err := store.ListProfilePromotionJobs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].EvidenceEventID != "chatgpt_export:conv-1:user-1" {
		t.Fatalf("jobs=%+v", jobs)
	}
}

func TestConfirmChatGPTL3CandidatesRequiresExplicitApply(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evidence := ChatGPTL3ImportRecord{Format: ChatGPTL3ArtifactFormat, ExportID: "export-hash", EvidenceID: "chatgpt_export:conv-3:user-1", ConversationID: "conv-3", MessageID: "user-1", Role: "user", Text: "Renは映画が好き", OnCurrentBranch: true}
	if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{evidence}, true); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Renは映画が好き", State: MemoryStateCandidate, EvidenceEventIDs: []string{evidence.EvidenceID}, Confidence: 0.9, Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, evidence.EvidenceID); err != nil {
		t.Fatal(err)
	}
	dryRun, err := store.ConfirmChatGPTL3Candidates(context.Background(), "export-hash", "user authorized archive", false)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Matched != 1 || dryRun.Confirmed != 0 {
		t.Fatalf("dryRun=%+v", dryRun)
	}
	apply, err := store.ConfirmChatGPTL3Candidates(context.Background(), "export-hash", "user authorized archive", true)
	if err != nil {
		t.Fatal(err)
	}
	if apply.Matched != 1 || apply.Confirmed != 1 {
		t.Fatalf("apply=%+v", apply)
	}
	items, err := store.ListUserMemories(context.Background(), "ren", MemoryStateConfirmed, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != candidate.ID {
		t.Fatalf("items=%+v", items)
	}
}

func TestImportChatGPTL3RecordsDryRunDoesNotWrite(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := ChatGPTL3ImportRecord{Format: ChatGPTL3ArtifactFormat, ExportID: "export-hash", EvidenceID: "chatgpt_export:conv-2:user-1", ConversationID: "conv-2", MessageID: "user-1", Role: "user", Text: "記憶", OnCurrentBranch: true}
	result, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{record}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Validated != 1 || result.Imported != 0 {
		t.Fatalf("result=%+v", result)
	}
	events, err := store.RecentByNamespace(context.Background(), chatGPTConversationNamespace("conv-2"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("dry-run wrote %d events", len(events))
	}
}
