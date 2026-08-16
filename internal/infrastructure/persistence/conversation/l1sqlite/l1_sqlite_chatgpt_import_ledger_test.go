package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestL1SQLiteChatGPTImportLedgerTransitionsReplayAndConflicts(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	owner := "user-a"
	actor := owner
	binding := ledgerTestBinding()
	appendEvent := func(requestID string, state domainmemory.ChatGPTImportState) (domainmemory.ChatGPTImportEvent, error) {
		input := ledgerTestInput(requestID, owner, actor, binding, state)
		return store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), input)
	}

	validating, err := appendEvent("request-1", domainmemory.ChatGPTImportStateValidating)
	if err != nil || validating.State != domainmemory.ChatGPTImportStateValidating {
		t.Fatalf("validating append: event=%+v err=%v", validating, err)
	}
	committing, err := appendEvent("request-1", domainmemory.ChatGPTImportStateCommitting)
	if err != nil || committing.State != domainmemory.ChatGPTImportStateCommitting {
		t.Fatalf("committing append: event=%+v err=%v", committing, err)
	}
	completed, err := appendEvent("request-1", domainmemory.ChatGPTImportStateCompleted)
	if err != nil || completed.State != domainmemory.ChatGPTImportStateCompleted {
		t.Fatalf("completed append: event=%+v err=%v", completed, err)
	}

	var before int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&before); err != nil {
		t.Fatalf("count before replay: %v", err)
	}
	replay, err := appendEvent("request-1", domainmemory.ChatGPTImportStateCompleted)
	if err != nil || replay.EventID != completed.EventID {
		t.Fatalf("exact terminal replay: event=%+v err=%v", replay, err)
	}
	var after int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&after); err != nil {
		t.Fatalf("count after replay: %v", err)
	}
	if after != before {
		t.Fatalf("exact replay appended a row: before=%d after=%d", before, after)
	}

	changed := ledgerTestInput("request-1", owner, actor, binding, domainmemory.ChatGPTImportStateCompleted)
	changed.Warnings = []string{"changed"}
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-1", owner), changed); !errors.Is(err, domainmemory.ErrChatGPTImportConflict) {
		t.Fatalf("changed duplicate error = %v, want conflict", err)
	}

	if _, err := appendEvent("request-2", domainmemory.ChatGPTImportStateCommitting); !errors.Is(err, domainmemory.ErrChatGPTImportInvalid) {
		t.Fatalf("skip validating error = %v, want invalid", err)
	}
	if _, err := appendEvent("request-1", domainmemory.ChatGPTImportStateValidating); !errors.Is(err, domainmemory.ErrChatGPTImportInvalid) {
		t.Fatalf("reopen terminal request error = %v, want invalid", err)
	}

	// A new request for a completed exact binding is a read-only completed replay.
	completedReplay, err := appendEvent("request-2", domainmemory.ChatGPTImportStateValidating)
	if err != nil || completedReplay.EventID != completed.EventID {
		t.Fatalf("completed binding replay: event=%+v err=%v", completedReplay, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&after); err != nil {
		t.Fatalf("count after completed binding replay: %v", err)
	}
	if after != before {
		t.Fatalf("completed binding replay appended a row: before=%d after=%d", before, after)
	}

	// A dry-run completion does not consume the binding for a later apply.
	applyInput := ledgerTestInput("request-apply", owner, actor, binding, domainmemory.ChatGPTImportStateValidating)
	applyInput.Apply = true
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-apply", owner), applyInput); err != nil {
		t.Fatalf("apply validating after dry-run: %v", err)
	}
	applyInput.State = domainmemory.ChatGPTImportStateCommitting
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-apply", owner), applyInput); err != nil {
		t.Fatalf("apply committing after dry-run: %v", err)
	}
	applyInput.State = domainmemory.ChatGPTImportStateCompleted
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-apply", owner), applyInput); err != nil {
		t.Fatalf("apply completed after dry-run: %v", err)
	}
	var applyReplayCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&applyReplayCount); err != nil {
		t.Fatalf("count before apply replay: %v", err)
	}
	noApplyReplay := ledgerTestInput("request-after-apply", owner, actor, binding, domainmemory.ChatGPTImportStateValidating)
	if replay, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-after-apply", owner), noApplyReplay); err != nil || !replay.Apply || replay.State != domainmemory.ChatGPTImportStateCompleted {
		t.Fatalf("completed apply replay: event=%+v err=%v", replay, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&after); err != nil {
		t.Fatalf("count after apply replay: %v", err)
	}
	if after != applyReplayCount {
		t.Fatalf("completed apply replay appended a row: before=%d after=%d", applyReplayCount, after)
	}
	terminalView, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "status-terminal", owner), "status-terminal", owner, actor, binding.ExportID)
	if err != nil {
		t.Fatalf("terminal status query: %v", err)
	}
	if terminalView.State != domainmemory.ChatGPTImportStateCompleted || !terminalView.Apply {
		t.Fatalf("unexpected terminal status: %+v", terminalView)
	}

	changedBinding := binding
	changedBinding.ArtifactSHA256 = strings.Repeat("c", 64)
	changedRequest := ledgerTestInput("request-3", owner, actor, changedBinding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-3", owner), changedRequest); !errors.Is(err, domainmemory.ErrChatGPTImportSourceChanged) {
		t.Fatalf("changed binding error = %v, want source_changed", err)
	}
}

func TestL1SQLiteChatGPTImportLedgerClosesValidationFailuresBeforeCommit(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	binding := ledgerTestBinding()
	owner := "user-a"
	input := ledgerTestInput("request-reject", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
		t.Fatalf("append validating: %v", err)
	}
	input.State = domainmemory.ChatGPTImportStateRejected
	input.ErrorCode = "artifact_invalid"
	input.FailureReason = "artifact semantic validation failed"
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
		t.Fatalf("validating to rejected: %v", err)
	}
	input = ledgerTestInput("request-block", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
		t.Fatalf("append second validating: %v", err)
	}
	input.State = domainmemory.ChatGPTImportStateBlocked
	input.ErrorCode = "storage_unavailable"
	input.FailureReason = "configured storage unavailable"
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
		t.Fatalf("validating to blocked: %v", err)
	}
}

func TestL1SQLiteChatGPTImportLedgerAllowsRetryAfterBlockedAndIsOwnerScoped(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	binding := ledgerTestBinding()
	owner := "user-a"
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-1", owner), ledgerTestInput("request-1", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)); err != nil {
		t.Fatalf("append validating: %v", err)
	}
	blocked := ledgerTestInput("request-1", owner, owner, binding, domainmemory.ChatGPTImportStateBlocked)
	// A terminal event must follow committing, so add that state first.
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-1", owner), ledgerTestInput("request-1", owner, owner, binding, domainmemory.ChatGPTImportStateCommitting)); err != nil {
		t.Fatalf("append committing: %v", err)
	}
	blocked.ErrorCode = "storage_unavailable"
	blocked.FailureReason = "storage unavailable"
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-1", owner), blocked); err != nil {
		t.Fatalf("append blocked: %v", err)
	}

	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, "request-2", owner), ledgerTestInput("request-2", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)); err != nil {
		t.Fatalf("retry after blocked: %v", err)
	}

	view, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "status-a", owner), "status-a", owner, owner, binding.ExportID)
	if err != nil {
		t.Fatalf("owner status: %v", err)
	}
	if view.State != domainmemory.ChatGPTImportStateValidating || view.ExportID != binding.ExportID {
		t.Fatalf("unexpected latest status: %+v", view)
	}

	otherOwner := "user-b"
	if _, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "status-b", otherOwner), "status-b", otherOwner, otherOwner, binding.ExportID); !errors.Is(err, domainmemory.ErrChatGPTImportNotFound) {
		t.Fatalf("other owner status error = %v, want not found", err)
	}
	if _, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "status-a-missing", owner), "status-a-missing", owner, owner, "missing-export"); !errors.Is(err, domainmemory.ErrChatGPTImportNotFound) {
		t.Fatalf("unknown export status error = %v, want not found", err)
	}
}

func TestL1SQLiteChatGPTImportLedgerScopeAndImmutableTriggers(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	binding := ledgerTestBinding()
	input := ledgerTestInput("request-1", "user-a", "user-a", binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(context.Background(), input); !errors.Is(err, domainmemory.ErrChatGPTImportForbidden) {
		t.Fatalf("missing scope error = %v, want forbidden", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&count); err != nil {
		t.Fatalf("count after scope rejection: %v", err)
	}
	if count != 0 {
		t.Fatalf("scope rejection wrote %d rows", count)
	}

	ctx := ledgerTestContext(t, input.RequestID, input.OwnerID)
	event, err := store.AppendChatGPTImportEvent(ctx, input)
	if err != nil {
		t.Fatalf("append valid event: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE l1_chatgpt_import_event SET state='committing' WHERE event_id=?`, event.EventID); err == nil {
		t.Fatal("immutable update unexpectedly succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM l1_chatgpt_import_event WHERE event_id=?`, event.EventID); err == nil {
		t.Fatal("immutable delete unexpectedly succeeded")
	}

	view, err := store.GetChatGPTImportStatus(ctx, input.RequestID, input.OwnerID, input.ActorID, input.Binding.ExportID)
	if err != nil {
		t.Fatalf("status after trigger checks: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	for _, forbidden := range []string{"owner_id", "actor_id", "raw_record_id", "content", "path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("status exposes forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestL1SQLiteChatGPTImportLedgerClosedStorePersistenceIsInternal(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	owner := "closed-owner"
	binding := ledgerTestBinding()
	input := ledgerTestInput("closed-append", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	_, appendErr := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input)
	_, statusErr := store.GetChatGPTImportStatus(ledgerTestContext(t, "closed-status", owner), "closed-status", owner, owner, binding.ExportID)
	if got := domainmemory.ChatGPTImportErrorCodeOf(appendErr); got != domainmemory.ChatGPTImportErrorInternal {
		t.Errorf("closed append code=%q err=%v, want internal", got, appendErr)
	}
	if got := domainmemory.ChatGPTImportErrorCodeOf(statusErr); got != domainmemory.ChatGPTImportErrorInternal {
		t.Errorf("closed status code=%q err=%v, want internal", got, statusErr)
	}
}

func TestL1SQLiteChatGPTImportLedgerNilStoreIsUnavailable(t *testing.T) {
	var store *L1SQLiteStore
	owner := "nil-owner"
	binding := ledgerTestBinding()
	input := ledgerTestInput("nil-append", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorUnavailable {
		t.Fatalf("nil append code=%q err=%v, want unavailable", domainmemory.ChatGPTImportErrorCodeOf(err), err)
	}
	if _, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "nil-status", owner), "nil-status", owner, owner, binding.ExportID); domainmemory.ChatGPTImportErrorCodeOf(err) != domainmemory.ChatGPTImportErrorUnavailable {
		t.Fatalf("nil status code=%q err=%v, want unavailable", domainmemory.ChatGPTImportErrorCodeOf(err), err)
	}
}

func TestL1SQLiteChatGPTImportLedgerSurvivesStoreReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "l1.db")
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	binding := ledgerTestBinding()
	owner := "user-a"
	input := ledgerTestInput("request-reopen", owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input); err != nil {
		t.Fatalf("append before reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}

	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1SQLiteStore: %v", err)
	}
	defer reopened.Close()
	view, err := reopened.GetChatGPTImportStatus(ledgerTestContext(t, "status-reopen", owner), "status-reopen", owner, owner, binding.ExportID)
	if err != nil {
		t.Fatalf("status after reopen: %v", err)
	}
	if view.State != domainmemory.ChatGPTImportStateValidating || view.BindingSHA256 == "" {
		t.Fatalf("unexpected durable status after reopen: %+v", view)
	}
}

func TestL1SQLiteChatGPTImportLedgerLatestStatusAndTransitionUseDurableInsertionOrder(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	owner := "user-order-transition"
	binding := ledgerTestBinding()
	binding.ExportID = "export-order-transition"
	requestID := ledgerRequestWithEarlierStateHashAfter(t, owner, binding, domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateCommitting)
	createdAt := time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC)
	validating := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt)
	validating.Counts.RawCount = 1
	committing := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateCommitting, createdAt)
	committing.Counts.RawCount = 22
	committing.Warnings = []string{"latest committing row"}
	reconcileInsertEvent(t, store, validating, "")
	reconcileInsertEvent(t, store, committing, "")

	status, err := store.GetChatGPTImportStatus(ledgerTestContext(t, "status-order-transition", owner), "status-order-transition", owner, owner, binding.ExportID)
	if err != nil {
		t.Fatalf("GetChatGPTImportStatus: %v", err)
	}
	if status.EventID != committing.EventID || status.State != domainmemory.ChatGPTImportStateCommitting || status.Counts.RawCount != committing.Counts.RawCount {
		t.Fatalf("status did not use last inserted row: got=%+v committing=%+v", status, committing)
	}

	completedInput := ledgerTestInput(requestID, owner, owner, binding, domainmemory.ChatGPTImportStateCompleted)
	completed, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), completedInput)
	if err != nil {
		t.Fatalf("append completed after last inserted committing: %v", err)
	}
	if completed.State != domainmemory.ChatGPTImportStateCompleted {
		t.Fatalf("completed transition returned %+v", completed)
	}
}

func TestL1SQLiteChatGPTImportLedgerExactReplayUsesDurableInsertionOrder(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	owner := "user-order-replay"
	binding := ledgerTestBinding()
	binding.ExportID = "export-order-replay"
	requestID := ledgerRequestWithEarlierStateHashAfter(t, owner, binding, domainmemory.ChatGPTImportStateValidating, domainmemory.ChatGPTImportStateBlocked)
	createdAt := time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC)
	validating := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt)
	blocked := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateBlocked, createdAt)
	reconcileInsertEvent(t, store, validating, "")
	reconcileInsertEvent(t, store, blocked, "")

	before := reconcileRowCount(t, store)
	replayInput := ledgerTestInput(requestID, owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), replayInput); !errors.Is(err, domainmemory.ErrChatGPTImportInvalid) {
		t.Fatalf("older exact replay error = %v, want invalid terminal request", err)
	}
	if after := reconcileRowCount(t, store); after != before {
		t.Fatalf("older exact replay changed rows: before=%d after=%d", before, after)
	}
}

func TestL1SQLiteChatGPTImportLedgerLatestImportUsesDurableInsertionOrder(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	owner := "user-order-import"
	binding := ledgerTestBinding()
	binding.ExportID = "export-order-import"
	terminalRequestID, activeRequestID := ledgerRequestsWithEarlierEventHashAfter(t, owner, binding, domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportStateValidating)
	createdAt := time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC)
	terminalValidating := reconcileTestEvent(t, terminalRequestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt)
	terminal := reconcileTestEvent(t, terminalRequestID, owner, binding, domainmemory.ChatGPTImportStateBlocked, createdAt)
	active := reconcileTestEvent(t, activeRequestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt)
	reconcileInsertEvent(t, store, terminalValidating, "")
	reconcileInsertEvent(t, store, terminal, "")
	reconcileInsertEvent(t, store, active, "")

	before := reconcileRowCount(t, store)
	newRequestID := "request-after-active"
	newInput := ledgerTestInput(newRequestID, owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, newRequestID, owner), newInput); !errors.Is(err, domainmemory.ErrChatGPTImportConflict) {
		t.Fatalf("new request while last inserted import is active error = %v, want conflict", err)
	}
	if after := reconcileRowCount(t, store); after != before {
		t.Fatalf("active import conflict changed rows: before=%d after=%d", before, after)
	}
}

func TestL1SQLiteChatGPTImportLedgerCorruptStoredRowsAreInternal(t *testing.T) {
	tests := []struct {
		name            string
		corruptImportID bool
		warningsJSON    string
	}{
		{name: "identity", corruptImportID: true, warningsJSON: "[]"},
		{name: "diagnostic_json", warningsJSON: `{"payload":"/secret/path"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			defer store.Close()

			owner := "user-corrupt"
			input := ledgerTestInput("request-corrupt", owner, owner, ledgerTestBinding(), domainmemory.ChatGPTImportStateValidating)
			bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(owner, input.Binding)
			if err != nil {
				t.Fatalf("binding hash: %v", err)
			}
			importID := domainmemory.DeterministicChatGPTImportID(owner, bindingSHA256)
			eventID := domainmemory.DeterministicChatGPTImportEventID(importID, input.RequestID, input.State)
			storedImportID := importID
			if tt.corruptImportID {
				storedImportID = "corrupt-import-id"
			}
			if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_import_event (
				event_id, import_id, request_id, owner_id, actor_id, export_id, binding_sha256,
				manifest_sha256, artifact_sha256, artifact_bytes, format, schema_version,
				converter_version, source_file_count, source_chunk_count, source_object_count,
				message_count, apply, state, source_count, file_count, chunk_count, object_count,
				count_message_count, batch_count, raw_count, projection_count, job_count,
				warnings_json, error_code, failure_reason, audit_reference, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				eventID, storedImportID, input.RequestID, input.OwnerID, input.ActorID, input.Binding.ExportID,
				bindingSHA256, input.Binding.ManifestSHA256, input.Binding.ArtifactSHA256, input.Binding.ArtifactBytes,
				input.Binding.Format, input.Binding.SchemaVersion, input.Binding.ConverterVersion,
				input.Binding.SourceFileCount, input.Binding.SourceChunkCount, input.Binding.SourceObjectCount,
				input.Binding.MessageCount, 0, string(input.State), input.Counts.SourceCount, input.Counts.FileCount,
				input.Counts.ChunkCount, input.Counts.ObjectCount, input.Counts.MessageCount, input.Counts.BatchCount,
				input.Counts.RawCount, input.Counts.ProjectionCount, input.Counts.JobCount, tt.warningsJSON,
				input.ErrorCode, input.FailureReason, input.AuditReference, time.Now().UTC()); err != nil {
				t.Fatalf("insert constrained corrupt row: %v", err)
			}

			var before int
			if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&before); err != nil {
				t.Fatalf("count before corruption reads: %v", err)
			}
			statusCtx := ledgerTestContext(t, "status-corrupt", owner)
			_, statusErr := store.GetChatGPTImportStatus(statusCtx, "status-corrupt", owner, owner, input.Binding.ExportID)
			assertStoredCorruptionInternal(t, statusErr)

			_, appendErr := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, owner), input)
			assertStoredCorruptionInternal(t, appendErr)
			var after int
			if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&after); err != nil {
				t.Fatalf("count after corruption reads: %v", err)
			}
			if after != before {
				t.Fatalf("corruption reads appended rows: before=%d after=%d", before, after)
			}
		})
	}
}

func assertStoredCorruptionInternal(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domainmemory.ErrChatGPTImportInternal) {
		t.Fatalf("corrupt stored row error = %v, want internal", err)
	}
	if errors.Is(err, domainmemory.ErrChatGPTImportInvalid) {
		t.Fatalf("corrupt stored row error = %v, must not be invalid", err)
	}
	for _, leaked := range []string{"/secret/path", "payload", "content", "owner_id", "actor_id"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("corrupt stored row error leaks %q: %v", leaked, err)
		}
	}
}

func ledgerTestBinding() domainmemory.ChatGPTImportBinding {
	return domainmemory.ChatGPTImportBinding{
		ExportID:          "export-1",
		ManifestSHA256:    strings.Repeat("a", 64),
		ArtifactSHA256:    strings.Repeat("b", 64),
		ArtifactBytes:     128,
		Format:            domainmemory.ChatGPTImportBundleFormat,
		SchemaVersion:     domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion:  domainmemory.ChatGPTImportConverterVersion,
		SourceFileCount:   3,
		SourceChunkCount:  4,
		SourceObjectCount: 2,
		MessageCount:      5,
	}
}

func ledgerTestInput(requestID, ownerID, actorID string, binding domainmemory.ChatGPTImportBinding, state domainmemory.ChatGPTImportState) domainmemory.ChatGPTImportEventInput {
	return domainmemory.ChatGPTImportEventInput{
		RequestID: requestID,
		OwnerID:   ownerID,
		ActorID:   actorID,
		Binding:   binding,
		State:     state,
		Counts: domainmemory.ChatGPTImportCounts{
			SourceCount:     3,
			FileCount:       3,
			ChunkCount:      4,
			ObjectCount:     2,
			MessageCount:    5,
			BatchCount:      1,
			RawCount:        5,
			ProjectionCount: 0,
			JobCount:        0,
		},
		AuditReference: "audit-1",
	}
}

func ledgerTestContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		ownerID,
		ownerID,
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func ledgerRequestWithEarlierStateHashAfter(t *testing.T, owner string, binding domainmemory.ChatGPTImportBinding, earlier, later domainmemory.ChatGPTImportState) string {
	t.Helper()
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(owner, binding)
	if err != nil {
		t.Fatalf("binding hash: %v", err)
	}
	importID := domainmemory.DeterministicChatGPTImportID(owner, bindingSHA256)
	for index := 0; index < 10_000; index++ {
		requestID := fmt.Sprintf("request-order-%d", index)
		earlierID := domainmemory.DeterministicChatGPTImportEventID(importID, requestID, earlier)
		laterID := domainmemory.DeterministicChatGPTImportEventID(importID, requestID, later)
		if earlierID > laterID {
			return requestID
		}
	}
	t.Fatal("could not find request ID with misleading state hash order")
	return ""
}

func ledgerRequestsWithEarlierEventHashAfter(t *testing.T, owner string, binding domainmemory.ChatGPTImportBinding, earlier, later domainmemory.ChatGPTImportState) (string, string) {
	t.Helper()
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(owner, binding)
	if err != nil {
		t.Fatalf("binding hash: %v", err)
	}
	importID := domainmemory.DeterministicChatGPTImportID(owner, bindingSHA256)
	for earlierIndex := 0; earlierIndex < 100; earlierIndex++ {
		earlierRequestID := fmt.Sprintf("request-earlier-%d", earlierIndex)
		earlierID := domainmemory.DeterministicChatGPTImportEventID(importID, earlierRequestID, earlier)
		earlierValidatingID := domainmemory.DeterministicChatGPTImportEventID(importID, earlierRequestID, domainmemory.ChatGPTImportStateValidating)
		if earlierID <= earlierValidatingID {
			continue
		}
		for laterIndex := 0; laterIndex < 100; laterIndex++ {
			laterRequestID := fmt.Sprintf("request-later-%d", laterIndex)
			laterID := domainmemory.DeterministicChatGPTImportEventID(importID, laterRequestID, later)
			if earlierID > laterID {
				return earlierRequestID, laterRequestID
			}
		}
	}
	t.Fatal("could not find request IDs with misleading event hash order")
	return "", ""
}
