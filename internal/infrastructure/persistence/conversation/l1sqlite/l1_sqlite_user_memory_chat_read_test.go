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

func TestReadUserMemoriesForChatReturnsBoundedSelectionAndExactTurnProvenance(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-read.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	proposed, err := store.OwnerProposeUserMemory(ctx, "chat-memory-propose", "ren", "ren", domainmemory.UserMemoryTypePreference, "Ren prefers blue", "operator confirmed")
	if err != nil {
		t.Fatalf("propose memory: %v", err)
	}
	confirmed, err := store.OwnerTransitionUserMemory(ctx, "chat-memory-confirm", "ren", "ren", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "reviewed")
	if err != nil {
		t.Fatalf("confirm memory: %v", err)
	}

	request := chatReadRequest("ren")
	request.Limit = 1
	userCtx := authenticatedChatReadContext(t, "ren")
	ctx, err = domaintool.DeriveAgentToolExecutionScope(userCtx, "mio-chat-read", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatalf("derive Mio scope: %v", err)
	}
	beforeTraces := countL1Rows(t, ctx, store, `SELECT count(*) FROM recall_trace`)
	beforeInjections := countL1Rows(t, ctx, store, `SELECT count(*) FROM prompt_injection_event`)

	result, err := store.ReadUserMemoriesForChat(ctx, request)
	if err != nil {
		t.Fatalf("ReadUserMemoriesForChat failed: %v", err)
	}
	if result.TurnID != request.TurnID || result.TraceID != request.TraceID || result.RootTaskID != request.RootTaskID || result.SessionID != request.SessionID || result.OwnerID != request.OwnerID {
		t.Fatalf("read identity=%+v, want request identity %+v", result, request)
	}
	if result.TotalCandidates != 1 || len(result.Items) != 1 || result.Items[0].Item.ID != confirmed.Item.ID {
		t.Fatalf("chat recall result=%+v", result)
	}
	ref := result.Items[0].Reference
	if ref.OwnerID != request.OwnerID || ref.SessionID != request.SessionID || ref.MemoryID != confirmed.Item.ID || ref.Namespace != "user:ren" || ref.SourceType != "user_memory" || ref.Source == "" || ref.PersonaScope != "all_personas" || ref.State != MemoryStateConfirmed || ref.Sensitivity != "normal" || !ref.Active || len(ref.EvidenceEventIDs) != 1 || ref.EvidenceEventIDs[0] != string(confirmed.Item.EvidenceEventIDs[0]) || ref.UpdatedAt.IsZero() || ref.OwnerVersion == "" {
		t.Fatalf("chat recall source reference=%+v", ref)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM recall_trace`); got != beforeTraces {
		t.Fatalf("chat recall wrote %d recall traces; before=%d", got-beforeTraces, beforeTraces)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM prompt_injection_event`); got != beforeInjections {
		t.Fatalf("chat recall wrote %d injection events; before=%d", got-beforeInjections, beforeInjections)
	}
}

func TestReadUserMemoriesForChatRequiresAuthenticatedOwnerAndMioChatScope(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-auth.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	request := chatReadRequest("ren")
	otherUser := authenticatedChatReadContext(t, "other")
	if _, err := store.ReadUserMemoriesForChat(otherUser, request); !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
		t.Fatalf("owner mismatch error=%v, want forbidden", err)
	}

	workerCtx, err := domaintool.DeriveAgentToolExecutionScope(authenticatedChatReadContext(t, "ren"), "worker-read", "shiro", "worker", "ops", true)
	if err != nil {
		t.Fatalf("derive worker scope: %v", err)
	}
	if _, err := store.ReadUserMemoriesForChat(workerCtx, request); !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
		t.Fatalf("worker scope error=%v, want forbidden", err)
	}

	mioOpsCtx, err := domaintool.DeriveAgentToolExecutionScope(authenticatedChatReadContext(t, "ren"), "mio-ops-read", "mio", "worker", "ops", true)
	if err != nil {
		t.Fatalf("derive Mio ops scope: %v", err)
	}
	if _, err := store.ReadUserMemoriesForChat(mioOpsCtx, request); !errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden) {
		t.Fatalf("Mio ops scope error=%v, want forbidden", err)
	}
}

func TestReadUserMemoriesForChatPropagatesCancellationWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-cancel.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	request := chatReadRequest("ren")
	readCtx := authenticatedChatReadContext(t, "ren")
	readCtx, cancel := context.WithCancel(readCtx)
	cancel()
	beforeTraces := countL1Rows(t, ctx, store, `SELECT count(*) FROM recall_trace`)
	beforeInjections := countL1Rows(t, ctx, store, `SELECT count(*) FROM prompt_injection_event`)
	if _, err := store.ReadUserMemoriesForChat(readCtx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error=%v, want context.Canceled", err)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM recall_trace`); got != beforeTraces {
		t.Fatalf("cancelled read wrote recall traces: before=%d after=%d", beforeTraces, got)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM prompt_injection_event`); got != beforeInjections {
		t.Fatalf("cancelled read wrote injection events: before=%d after=%d", beforeInjections, got)
	}
}

func TestReadUserMemoriesForChatEnforcesQueryLimitAndReferences(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-bounds.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	ctx := authenticatedChatReadContext(t, "ren")
	base := chatReadRequest("ren")
	maxRuneQuery := base
	maxRuneQuery.Query = strings.Repeat("界", 512)
	if _, err := store.ReadUserMemoriesForChat(ctx, maxRuneQuery); err != nil {
		t.Fatalf("512-rune query was rejected: %v", err)
	}

	cases := []struct {
		name   string
		update func(*domainmemory.UserMemoryChatReadRequest)
		want   error
	}{
		{name: "empty query", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.Query = " " }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "query too long", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.Query = strings.Repeat("界", 513) }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "invalid session ID", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.SessionID = "" }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "invalid turn ID", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.TurnID = "" }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "invalid trace ID", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.TraceID = "" }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "invalid root task ID", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.RootTaskID = "" }, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "limit too large", update: func(request *domainmemory.UserMemoryChatReadRequest) {
			request.Limit = domainmemory.UserMemoryRecallMaxLimit + 1
		}, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "too many references", update: func(request *domainmemory.UserMemoryChatReadRequest) {
			request.References = make([]string, domainmemory.UserMemoryRecallMaxReferences+1)
		}, want: domainmemory.ErrUserMemoryOwnerInvalid},
		{name: "unsupported reference", update: func(request *domainmemory.UserMemoryChatReadRequest) { request.References = []string{"mem:exact"} }, want: domainmemory.ErrUserMemoryRecallReferencesUnsupported},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.update(&request)
			if _, err := store.ReadUserMemoriesForChat(ctx, request); !errors.Is(err, test.want) {
				t.Fatalf("read error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestReadUserMemoriesForChatFiltersIneligibleMemoriesBeforeBudget(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-eligibility.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	insertChatReadMemory(t, store, "ren", "eligible", "Ren prefers blue", MemoryStateConfirmed, "normal", "all_personas", true, now)
	insertChatReadMemory(t, store, "ren", "candidate", "Ren candidate blue", MemoryStateCandidate, "normal", "all_personas", true, now.Add(time.Second))
	insertChatReadMemory(t, store, "ren", "sensitive", "Ren sensitive blue", MemoryStateConfirmed, "sensitive", "all_personas", true, now.Add(2*time.Second))
	insertChatReadMemory(t, store, "ren", "inactive", "Ren inactive blue", MemoryStateConfirmed, "normal", "all_personas", false, now.Add(3*time.Second))
	insertChatReadMemory(t, store, "ren", "other-persona", "Ren Shiro blue", MemoryStateConfirmed, "normal", "shiro_only", true, now.Add(4*time.Second))

	result, err := store.ReadUserMemoriesForChat(authenticatedChatReadContext(t, "ren"), chatReadRequest("ren"))
	if err != nil {
		t.Fatalf("ReadUserMemoriesForChat failed: %v", err)
	}
	if result.TotalCandidates != 1 || result.HasMore || len(result.Items) != 1 || result.Items[0].Item.ID != "eligible" {
		t.Fatalf("chat recall eligibility result=%+v", result)
	}
}

func TestReadUserMemoriesForChatFindsEligibleMemoryBelowNewerIneligibleRows(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-ineligible-window.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	insertChatReadMemory(t, store, "ren", "eligible-old", "Ren prefers blue tea", MemoryStateConfirmed, "normal", "mio", true, now)
	for i := 0; i < domainmemory.UserMemoryRecallMaxScan; i++ {
		id := "newer-ineligible-" + time.Unix(int64(i), 0).UTC().Format("150405")
		state, sensitivity, scope, active := MemoryStateConfirmed, "normal", "mio", true
		switch i % 4 {
		case 0:
			state = MemoryStateCandidate
		case 1:
			active = false
		case 2:
			sensitivity = "sensitive"
		case 3:
			scope = "shiro_only"
		}
		insertChatReadMemory(t, store, "ren", id, "ineligible blue memory", state, sensitivity, scope, active, now.Add(time.Duration(i+1)*time.Second))
	}

	result, err := store.ReadUserMemoriesForChat(authenticatedChatReadContext(t, "ren"), chatReadRequest("ren"))
	if err != nil {
		t.Fatalf("ReadUserMemoriesForChat failed: %v", err)
	}
	if result.TotalCandidates != 1 || result.HasMore || len(result.Items) != 1 || result.Items[0].Item.ID != "eligible-old" {
		t.Fatalf("chat recall lost an eligible memory below %d newer ineligible rows: candidates=%d items=%+v", domainmemory.UserMemoryRecallMaxScan, result.TotalCandidates, result.Items)
	}
}

func TestReadUserMemoriesForChatCapsScanSelectionAndEvidenceReferences(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "chat-user-memory-scan-bound.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	for i := 0; i < domainmemory.UserMemoryRecallMaxScan+5; i++ {
		insertChatReadMemory(t, store, "ren", "bounded-"+time.Unix(int64(i), 0).UTC().Format("150405"), "bounded blue memory", MemoryStateConfirmed, "normal", "all_personas", true, now.Add(time.Duration(i)*time.Second))
	}
	request := chatReadRequest("ren")
	request.Limit = 2
	result, err := store.ReadUserMemoriesForChat(authenticatedChatReadContext(t, "ren"), request)
	if err != nil {
		t.Fatalf("ReadUserMemoriesForChat failed: %v", err)
	}
	if result.TotalCandidates != domainmemory.UserMemoryRecallMaxScan || !result.HasMore || len(result.Items) != request.Limit {
		t.Fatalf("chat recall bounds: candidates=%d has_more=%t selected=%d", result.TotalCandidates, result.HasMore, len(result.Items))
	}
	for _, item := range result.Items {
		if len(item.Reference.EvidenceEventIDs) > domainmemory.UserMemoryRecallMaxEvidenceReferences {
			t.Fatalf("unbounded evidence references: %+v", item.Reference)
		}
	}

	tooManyEvidence := make([]string, domainmemory.UserMemoryRecallMaxEvidenceReferences+1)
	for i := range tooManyEvidence {
		tooManyEvidence[i] = "event-extra-" + time.Unix(int64(i), 0).UTC().Format("150405")
	}
	insertChatReadMemory(t, store, "other", "many-evidence", "many evidence blue", MemoryStateConfirmed, "normal", "all_personas", true, now, tooManyEvidence...)
	tooManyEvidenceRequest := chatReadRequest("other")
	if _, err := store.ReadUserMemoriesForChat(authenticatedChatReadContext(t, "other"), tooManyEvidenceRequest); !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("excess evidence references error=%v, want unavailable", err)
	}
}

func chatReadRequest(ownerID string) domainmemory.UserMemoryChatReadRequest {
	return domainmemory.UserMemoryChatReadRequest{
		OwnerID: ownerID, SessionID: modulecore.NewSessionID(), TurnID: modulecore.NewTurnID(),
		TraceID: modulecore.NewTraceID(), RootTaskID: modulecore.NewTaskID(), Persona: "mio", Query: "blue", Limit: 12,
	}
}

func authenticatedChatReadContext(t *testing.T, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope("chat-read-"+ownerID, domaintool.ActorKindUser, ownerID, ownerID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatalf("create user scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func insertChatReadMemory(t *testing.T, store *L1SQLiteStore, ownerID, id, statement, state, sensitivity, scope string, active bool, createdAt time.Time, evidenceIDs ...string) {
	t.Helper()
	if len(evidenceIDs) == 0 {
		evidenceIDs = []string{"event-" + id}
	}
	meta, err := json.Marshal(map[string]any{
		"type": domainmemory.UserMemoryTypePreference, "user_id": ownerID, "statement": statement,
		"evidence_event_ids": evidenceIDs, "created_by_event_id": evidenceIDs[0],
		"confidence": 0.9, "sensitivity": sensitivity, "scope": scope, "active": active,
	})
	if err != nil {
		t.Fatalf("marshal memory metadata: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO l1_memory_event (id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at) VALUES (?, ?, '', '', 0, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, NamespaceKindUser+":"+ownerID, string(domconv.SpeakerMemory), statement, string(meta), state, MemoryLayerL1, "profile_extractor", createdAt, createdAt); err != nil {
		t.Fatalf("insert UserMemory %q: %v", id, err)
	}
}
