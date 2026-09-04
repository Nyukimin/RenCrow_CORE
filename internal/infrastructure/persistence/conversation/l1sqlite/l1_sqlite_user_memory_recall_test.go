package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOwnerRecallTraceIsOwnerScopedAndHidesLegacyRows(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "owner-recall.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	ownerA, err := store.OwnerProposeUserMemory(ctx, "owner-a-propose", "ren", "ren", domainmemory.UserMemoryTypePreference, "Ren prefers blue", "operator confirmed")
	if err != nil {
		t.Fatalf("owner A propose failed: %v", err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-a-confirm", "ren", "ren", ownerA.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "reviewed"); err != nil {
		t.Fatalf("owner A confirm failed: %v", err)
	}
	ownerCandidate, err := store.OwnerProposeUserMemory(ctx, "owner-a-candidate", "ren", "ren", domainmemory.UserMemoryTypePreference, "Ren may like blue", "operator candidate")
	if err != nil {
		t.Fatalf("owner A candidate failed: %v", err)
	}
	ownerInactive, err := store.OwnerProposeUserMemory(ctx, "owner-a-inactive", "ren", "ren", domainmemory.UserMemoryTypeProject, "Ren had inactive blue project", "operator candidate")
	if err != nil {
		t.Fatalf("owner A inactive failed: %v", err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-a-forget", "ren", "ren", ownerInactive.Item.ID, domainmemory.UserMemoryOwnerOperationForget, "", "remove inactive"); err != nil {
		t.Fatalf("owner A forget failed: %v", err)
	}
	sensitiveMeta, _ := json.Marshal(map[string]interface{}{
		"type": domainmemory.UserMemoryTypePreference, "user_id": "ren", "statement": "Ren has sensitive blue detail",
		"evidence_event_ids": []string{"sensitive-evidence"}, "confidence": 0.8, "sensitivity": "sensitive",
		"scope": "all_personas", "active": true,
	})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, '', '', 0, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		"owner-a-sensitive", "user:ren", string(domconv.SpeakerMemory), "Ren has sensitive blue detail", string(sensitiveMeta), MemoryStateConfirmed, MemoryLayerL1, "operator:ren", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("owner A sensitive insert failed: %v", err)
	}
	ownerB, err := store.OwnerProposeUserMemory(ctx, "owner-b-propose", "other", "other", domainmemory.UserMemoryTypePreference, "Other prefers red", "operator confirmed")
	if err != nil {
		t.Fatalf("owner B propose failed: %v", err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-b-confirm", "other", "other", ownerB.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "reviewed"); err != nil {
		t.Fatalf("owner B confirm failed: %v", err)
	}

	scope, err := domaintool.NewToolExecutionScope("owner-recall", domaintool.ActorKindUser, "ren", "ren", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	recall, err := store.OwnerRecallUserMemories(domaintool.WithToolExecutionScope(ctx, scope), "owner-recall", "ren", "blue", 12)
	if err != nil {
		t.Fatalf("owner recall failed: %v", err)
	}
	if len(recall.Items) != 1 || recall.Items[0].ID != ownerA.Item.ID || recall.Receipt.Operation != domainmemory.UserMemoryOwnerOperationRecall {
		t.Fatalf("owner recall=%+v", recall)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM prompt_injection_event WHERE trace_id = ?`, recall.Trace.ID); got != 1 {
		t.Fatalf("owner recall injection events=%d, want 1", got)
	}

	traces, err := store.OwnerListRecallTraces(domaintool.WithToolExecutionScope(ctx, scope), "ren", 20)
	if err != nil || len(traces) != 1 || traces[0].ID != recall.Trace.ID {
		t.Fatalf("owner traces=%+v err=%v", traces, err)
	}
	detail, err := store.OwnerFindRecallTrace(domaintool.WithToolExecutionScope(ctx, scope), "ren", recall.Trace.ID)
	if err != nil || len(detail.Items) != 4 || detail.Items[0].MemoryID == "" {
		t.Fatalf("owner trace detail=%+v err=%v", detail, err)
	}
	statuses := map[string]string{}
	for _, item := range detail.Items {
		statuses[item.MemoryID] = item.Status
	}
	if statuses[ownerA.Item.ID] != domconv.TraceStatusInjected || statuses[ownerCandidate.Item.ID] == domconv.TraceStatusInjected || statuses[ownerInactive.Item.ID] == domconv.TraceStatusInjected || statuses["owner-a-sensitive"] == domconv.TraceStatusInjected {
		t.Fatalf("owner trace decisions=%v", statuses)
	}

	redactTraceID := modulecore.NewTraceID()
	redactID := string(redactTraceID)
	if err := store.StartRecallTrace(ctx, domconv.RecallTraceRecord{TraceID: redactTraceID, OwnerID: "ren", TurnID: modulecore.NewTurnID(), RootTaskID: modulecore.NewTaskID(), ChatID: "ren", Status: "completed"}); err != nil {
		t.Fatalf("redaction trace start failed: %v", err)
	}
	long := strings.Repeat("x", 241)
	if err := store.AddRecallTraceItems(ctx, redactID, []domconv.RecallTraceItemRecord{
		{ItemID: redactID + ":normal", TraceID: redactID, Kind: "user_memory", Status: domconv.TraceStatusInjected, Summary: long, Sensitivity: "normal"},
		{ItemID: redactID + ":short", TraceID: redactID, Kind: "short_context", Status: domconv.TraceStatusInjected, Summary: long, Sensitivity: "normal"},
		{ItemID: redactID + ":rolling", TraceID: redactID, Kind: "rolling_summary", Status: domconv.TraceStatusInjected, Summary: long, Sensitivity: "normal"},
		{ItemID: redactID + ":sensitive", TraceID: redactID, Kind: "user_memory", Status: domconv.TraceStatusInjected, Summary: "secret", Sensitivity: "sensitive"},
	}); err != nil {
		t.Fatalf("redaction trace items failed: %v", err)
	}
	redacted, err := store.OwnerFindRecallTrace(domaintool.WithToolExecutionScope(ctx, scope), "ren", redactID)
	if err != nil {
		t.Fatalf("redaction trace show failed: %v", err)
	}
	for _, item := range redacted.Items {
		switch item.ItemID {
		case redactID + ":normal":
			if len([]rune(item.Summary)) != 240 {
				t.Fatalf("normal summary rune length=%d", len([]rune(item.Summary)))
			}
		case redactID + ":short", redactID + ":rolling", redactID + ":sensitive":
			if item.Summary != "" || item.TokenCount != 0 {
				t.Fatalf("summary redaction item=%+v", item)
			}
		}
	}

	legacyTraceID := modulecore.NewTraceID()
	legacyID := string(legacyTraceID)
	if err := store.StartRecallTrace(ctx, domconv.RecallTraceRecord{TraceID: legacyTraceID, TurnID: modulecore.NewTurnID(), RootTaskID: modulecore.NewTaskID(), ChatID: "ren", Status: "completed"}); err != nil {
		t.Fatalf("legacy trace insert failed: %v", err)
	}
	otherTraceID := modulecore.NewTraceID()
	otherID := string(otherTraceID)
	if err := store.StartRecallTrace(ctx, domconv.RecallTraceRecord{TraceID: otherTraceID, OwnerID: "other", TurnID: modulecore.NewTurnID(), RootTaskID: modulecore.NewTaskID(), ChatID: "other", Status: "completed"}); err != nil {
		t.Fatalf("other trace insert failed: %v", err)
	}
	if _, err := store.OwnerFindRecallTrace(domaintool.WithToolExecutionScope(ctx, scope), "ren", legacyID); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
		t.Fatalf("legacy trace error=%v, want owner not found", err)
	}
	if _, err := store.OwnerFindRecallTrace(domaintool.WithToolExecutionScope(ctx, scope), "ren", "trace:other-owner"); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
		t.Fatalf("other trace error=%v, want owner not found", err)
	}
	if _, err := store.OwnerFindRecallTrace(domaintool.WithToolExecutionScope(ctx, scope), "ren", otherID); !errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound) {
		t.Fatalf("cross-owner trace error=%v, want owner not found", err)
	}
}
