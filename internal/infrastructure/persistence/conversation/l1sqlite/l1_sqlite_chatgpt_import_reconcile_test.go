package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	reconcileInterruptedCode   = "process_interrupted"
	reconcileInterruptedReason = "CORE process interrupted the active import"
)

func TestReconcileActiveChatGPTImportsAfterReopen(t *testing.T) {
	dbPath := reconcileTestDBPath(t)
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}

	validating := ledgerTestInput("request-validating", "user-validating", "user-validating", reconcileTestBinding("export-validating", 'a', 'b'), domainmemory.ChatGPTImportStateValidating)
	validating.Apply = true
	validating.Warnings = []string{"validation progress retained"}
	validating.Counts.RawCount = 11
	validating.AuditReference = "audit-validating"
	validatingEvent, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, validating.RequestID, validating.OwnerID), validating)
	if err != nil {
		t.Fatalf("append validating: %v", err)
	}

	committing := ledgerTestInput("request-committing", "user-committing", "user-committing", reconcileTestBinding("export-committing", 'c', 'd'), domainmemory.ChatGPTImportStateValidating)
	committing.Apply = true
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, committing.RequestID, committing.OwnerID), committing); err != nil {
		t.Fatalf("append committing validation: %v", err)
	}
	committing.State = domainmemory.ChatGPTImportStateCommitting
	committing.Warnings = []string{"commit progress retained"}
	committing.Counts.RawCount = 23
	committing.Counts.ProjectionCount = 17
	committing.AuditReference = "audit-committing"
	committingEvent, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, committing.RequestID, committing.OwnerID), committing)
	if err != nil {
		t.Fatalf("append committing: %v", err)
	}

	completed := reconcileAppendTerminal(t, store, "request-completed", "user-completed", reconcileTestBinding("export-completed", 'e', 'f'), domainmemory.ChatGPTImportStateCompleted)
	rejected := reconcileAppendTerminal(t, store, "request-rejected", "user-rejected", reconcileTestBinding("export-rejected", '1', '2'), domainmemory.ChatGPTImportStateRejected)
	blocked := reconcileAppendTerminal(t, store, "request-blocked", "user-blocked", reconcileTestBinding("export-blocked", '3', '4'), domainmemory.ChatGPTImportStateBlocked)

	before := reconcileRowCount(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close before reconcile: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1SQLiteStore: %v", err)
	}
	defer reopened.Close()

	got, err := callReconcileActiveChatGPTImports(t, reopened)
	if err != nil {
		t.Fatalf("ReconcileActiveChatGPTImports: %v", err)
	}
	if got != 2 {
		t.Fatalf("reconciled count = %d, want 2", got)
	}
	if after := reconcileRowCount(t, reopened); after != before+2 {
		t.Fatalf("row count after reconcile = %d, want %d", after, before+2)
	}

	for _, active := range []struct {
		input    domainmemory.ChatGPTImportEventInput
		previous domainmemory.ChatGPTImportEvent
	}{
		{input: validating, previous: validatingEvent},
		{input: committing, previous: committingEvent},
	} {
		bindingSHA256, hashErr := domainmemory.DeterministicChatGPTImportBindingSHA256(active.input.OwnerID, active.input.Binding)
		if hashErr != nil {
			t.Fatalf("binding hash: %v", hashErr)
		}
		importID := domainmemory.DeterministicChatGPTImportID(active.input.OwnerID, bindingSHA256)
		blockedEventID := domainmemory.DeterministicChatGPTImportEventID(importID, active.input.RequestID, domainmemory.ChatGPTImportStateBlocked)
		blockedEvent, queryErr := queryChatGPTImportEvent(context.Background(), reopened.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id = ?`, blockedEventID)
		if queryErr != nil {
			t.Fatalf("read reconciled blocked event: %v", queryErr)
		}
		if blockedEvent.State != domainmemory.ChatGPTImportStateBlocked || blockedEvent.ErrorCode != reconcileInterruptedCode || blockedEvent.FailureReason != reconcileInterruptedReason {
			t.Fatalf("unexpected reconciled terminal event: %+v", blockedEvent)
		}
		if blockedEvent.ImportID != active.previous.ImportID || blockedEvent.RequestID != active.previous.RequestID || blockedEvent.OwnerID != active.previous.OwnerID || blockedEvent.ActorID != active.previous.ActorID || blockedEvent.Binding != active.previous.Binding || blockedEvent.BindingSHA256 != active.previous.BindingSHA256 || blockedEvent.Apply != active.previous.Apply || !reflect.DeepEqual(blockedEvent.Counts, active.previous.Counts) || !reflect.DeepEqual(blockedEvent.Warnings, active.previous.Warnings) || blockedEvent.AuditReference != active.previous.AuditReference {
			t.Fatalf("reconciled event did not preserve active binding and safe progress: previous=%+v blocked=%+v", active.previous, blockedEvent)
		}
		if !blockedEvent.CreatedAt.After(active.previous.CreatedAt) {
			t.Fatalf("blocked created_at %s is not after active event %s", blockedEvent.CreatedAt, active.previous.CreatedAt)
		}
		original, originalErr := queryChatGPTImportEvent(context.Background(), reopened.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id = ?`, active.previous.EventID)
		if originalErr != nil {
			t.Fatalf("read original active event: %v", originalErr)
		}
		originalCreatedAt := original.CreatedAt
		previousCreatedAt := active.previous.CreatedAt
		original.CreatedAt = time.Time{}
		active.previous.CreatedAt = time.Time{}
		if !originalCreatedAt.Equal(previousCreatedAt) || !reflect.DeepEqual(original, active.previous) {
			t.Fatalf("reconcile mutated the immutable active event: before=%+v after=%+v", active.previous, original)
		}

		status, statusErr := reopened.GetChatGPTImportStatus(ledgerTestContext(t, "status-"+active.input.RequestID, active.input.OwnerID), "status-"+active.input.RequestID, active.input.OwnerID, active.input.ActorID, active.input.Binding.ExportID)
		if statusErr != nil {
			t.Fatalf("status after reconcile: %v", statusErr)
		}
		if status.State != domainmemory.ChatGPTImportStateBlocked || status.EventID != blockedEventID {
			t.Fatalf("status after reconcile = %+v, want blocked event %q", status, blockedEventID)
		}
	}

	for _, terminal := range []domainmemory.ChatGPTImportEvent{completed, rejected, blocked} {
		var rows int
		if err := reopened.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event WHERE import_id = ? AND request_id = ?`, terminal.ImportID, terminal.RequestID).Scan(&rows); err != nil {
			t.Fatalf("count terminal request: %v", err)
		}
		wantRows := 3
		if terminal.State != domainmemory.ChatGPTImportStateCompleted {
			wantRows = 2
		}
		if rows != wantRows {
			t.Fatalf("terminal %s row count = %d, want %d", terminal.State, rows, wantRows)
		}
		status, statusErr := reopened.GetChatGPTImportStatus(ledgerTestContext(t, "status-"+terminal.RequestID, terminal.OwnerID), "status-"+terminal.RequestID, terminal.OwnerID, terminal.ActorID, terminal.Binding.ExportID)
		if statusErr != nil {
			t.Fatalf("read terminal status: %v", statusErr)
		}
		if status.EventID != terminal.EventID || status.State != terminal.State {
			t.Fatalf("terminal status changed: event=%+v status=%+v", terminal, status)
		}
	}

	rowsBeforeReplay := reconcileRowCount(t, reopened)
	replayed, err := callReconcileActiveChatGPTImports(t, reopened)
	if err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if replayed != 0 || reconcileRowCount(t, reopened) != rowsBeforeReplay {
		t.Fatalf("idempotent reconcile changed rows: count=%d before=%d after=%d", replayed, rowsBeforeReplay, reconcileRowCount(t, reopened))
	}
}

func TestReconcileActiveChatGPTImportsUsesDurableInsertionOrder(t *testing.T) {
	store, err := NewL1SQLiteStore(reconcileTestDBPath(t))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	binding := reconcileTestBinding("export-order", '5', '6')
	owner := "user-order"
	requestID := reconcileRequestWithMisleadingEventHash(t, owner, binding)
	createdAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	validating := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt)
	validating.Counts.RawCount = 1
	committing := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateCommitting, createdAt)
	committing.Counts.RawCount = 99
	committing.Warnings = []string{"latest by rowid"}
	committing.AuditReference = "audit-rowid"
	reconcileInsertEvent(t, store, validating, "")
	reconcileInsertEvent(t, store, committing, "")

	got, err := callReconcileActiveChatGPTImports(t, store)
	if err != nil {
		t.Fatalf("ReconcileActiveChatGPTImports: %v", err)
	}
	if got != 1 {
		t.Fatalf("reconciled count = %d, want 1", got)
	}
	blockedID := domainmemory.DeterministicChatGPTImportEventID(committing.ImportID, requestID, domainmemory.ChatGPTImportStateBlocked)
	blockedEvent, err := queryChatGPTImportEvent(context.Background(), store.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id = ?`, blockedID)
	if err != nil {
		t.Fatalf("read blocked event: %v", err)
	}
	if blockedEvent.Counts.RawCount != 99 || !reflect.DeepEqual(blockedEvent.Warnings, committing.Warnings) || blockedEvent.AuditReference != committing.AuditReference {
		t.Fatalf("reconcile chose hash ordering instead of latest rowid: %+v", blockedEvent)
	}
}

func TestReconcileActiveChatGPTImportsCorruptionRollsBackAll(t *testing.T) {
	store, err := NewL1SQLiteStore(reconcileTestDBPath(t))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	good := ledgerTestInput("request-good", "user-good", "user-good", reconcileTestBinding("export-good", '7', '8'), domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, good.RequestID, good.OwnerID), good); err != nil {
		t.Fatalf("append good active import: %v", err)
	}
	corrupt := reconcileTestEvent(t, "request-corrupt", "user-corrupt", reconcileTestBinding("export-corrupt", '9', 'a'), domainmemory.ChatGPTImportStateValidating, time.Now().UTC())
	reconcileInsertEvent(t, store, corrupt, `{"payload":"hidden"}`)

	before := reconcileRowCount(t, store)
	got, reconcileErr := callReconcileActiveChatGPTImports(t, store)
	if got != 0 || !errors.Is(reconcileErr, domainmemory.ErrChatGPTImportUnavailable) {
		t.Fatalf("corrupt reconcile = (%d, %v), want (0, unavailable)", got, reconcileErr)
	}
	if after := reconcileRowCount(t, store); after != before {
		t.Fatalf("corruption did not roll back all writes: before=%d after=%d", before, after)
	}
	for _, leaked := range []string{"payload", "hidden", "owner-corrupt"} {
		if strings.Contains(reconcileErr.Error(), leaked) {
			t.Fatalf("corruption error leaked %q: %v", leaked, reconcileErr)
		}
	}
}

func TestReconcileActiveChatGPTImportsRejectsConflictingBlockedIdentity(t *testing.T) {
	store, err := NewL1SQLiteStore(reconcileTestDBPath(t))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()

	binding := reconcileTestBinding("export-conflict", 'b', 'c')
	owner := "user-conflict"
	requestID := "request-conflict"
	createdAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	conflictingBlocked := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateBlocked, createdAt)
	conflictingBlocked.ErrorCode = "different_interruption"
	conflictingBlocked.FailureReason = "different sanitized reason"
	conflictingBlocked.Counts.RawCount = 77
	reconcileInsertEvent(t, store, conflictingBlocked, "")
	active := reconcileTestEvent(t, requestID, owner, binding, domainmemory.ChatGPTImportStateValidating, createdAt.Add(time.Second))
	active.Counts.RawCount = 2
	reconcileInsertEvent(t, store, active, "")

	before := reconcileRowCount(t, store)
	got, reconcileErr := callReconcileActiveChatGPTImports(t, store)
	if got != 0 || !errors.Is(reconcileErr, domainmemory.ErrChatGPTImportUnavailable) {
		t.Fatalf("conflicting blocked reconcile = (%d, %v), want (0, unavailable)", got, reconcileErr)
	}
	if after := reconcileRowCount(t, store); after != before {
		t.Fatalf("conflicting blocked row changed ledger: before=%d after=%d", before, after)
	}
}

func callReconcileActiveChatGPTImports(t *testing.T, store *L1SQLiteStore) (int, error) {
	t.Helper()
	reconciler, ok := any(store).(interface {
		ReconcileActiveChatGPTImports(context.Context) (int, error)
	})
	if !ok {
		t.Fatal("L1SQLiteStore does not implement ReconcileActiveChatGPTImports")
	}
	return reconciler.ReconcileActiveChatGPTImports(context.Background())
}

func reconcileAppendTerminal(t *testing.T, store *L1SQLiteStore, requestID, owner string, binding domainmemory.ChatGPTImportBinding, terminal domainmemory.ChatGPTImportState) domainmemory.ChatGPTImportEvent {
	t.Helper()
	input := ledgerTestInput(requestID, owner, owner, binding, domainmemory.ChatGPTImportStateValidating)
	if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), input); err != nil {
		t.Fatalf("append terminal validating: %v", err)
	}
	if terminal == domainmemory.ChatGPTImportStateCompleted {
		input.State = domainmemory.ChatGPTImportStateCommitting
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), input); err != nil {
			t.Fatalf("append terminal committing: %v", err)
		}
	}
	input.State = terminal
	if terminal == domainmemory.ChatGPTImportStateRejected || terminal == domainmemory.ChatGPTImportStateBlocked {
		input.ErrorCode = "terminal_test"
		input.FailureReason = "terminal test reason"
	}
	event, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, requestID, owner), input)
	if err != nil {
		t.Fatalf("append terminal %s: %v", terminal, err)
	}
	return event
}

func reconcileTestBinding(exportID string, manifestRune, artifactRune rune) domainmemory.ChatGPTImportBinding {
	binding := ledgerTestBinding()
	binding.ExportID = exportID
	binding.ManifestSHA256 = strings.Repeat(string(manifestRune), 64)
	binding.ArtifactSHA256 = strings.Repeat(string(artifactRune), 64)
	return binding
}

func reconcileTestEvent(t *testing.T, requestID, owner string, binding domainmemory.ChatGPTImportBinding, state domainmemory.ChatGPTImportState, createdAt time.Time) domainmemory.ChatGPTImportEvent {
	t.Helper()
	input := ledgerTestInput(requestID, owner, owner, binding, state)
	if state == domainmemory.ChatGPTImportStateRejected || state == domainmemory.ChatGPTImportStateBlocked {
		input.ErrorCode = "test_terminal"
		input.FailureReason = "test terminal reason"
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("validate test input: %v", err)
	}
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(owner, binding)
	if err != nil {
		t.Fatalf("binding hash: %v", err)
	}
	importID := domainmemory.DeterministicChatGPTImportID(owner, bindingSHA256)
	return domainmemory.ChatGPTImportEvent{
		EventID:  domainmemory.DeterministicChatGPTImportEventID(importID, requestID, state),
		ImportID: importID, RequestID: requestID, OwnerID: owner, ActorID: owner,
		Binding: binding, BindingSHA256: bindingSHA256, State: state,
		Counts: input.Counts, Warnings: []string{}, AuditReference: input.AuditReference,
		ErrorCode: input.ErrorCode, FailureReason: input.FailureReason, CreatedAt: createdAt.UTC(),
	}
}

func reconcileInsertEvent(t *testing.T, store *L1SQLiteStore, event domainmemory.ChatGPTImportEvent, warningsJSONOverride string) {
	t.Helper()
	warningsJSON := `[]`
	if len(event.Warnings) > 0 {
		encoded, err := json.Marshal(event.Warnings)
		if err != nil {
			t.Fatalf("marshal warnings: %v", err)
		}
		warningsJSON = string(encoded)
	}
	if warningsJSONOverride != "" {
		warningsJSON = warningsJSONOverride
	}
	if _, err := store.db.Exec(`INSERT INTO l1_chatgpt_import_event (
		event_id, import_id, request_id, owner_id, actor_id, export_id, binding_sha256,
		manifest_sha256, artifact_sha256, artifact_bytes, format, schema_version,
		converter_version, source_file_count, source_chunk_count, source_object_count,
		message_count, apply, state, source_count, file_count, chunk_count, object_count,
		count_message_count, batch_count, raw_count, projection_count, job_count,
		warnings_json, error_code, failure_reason, audit_reference, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.ImportID, event.RequestID, event.OwnerID, event.ActorID,
		event.Binding.ExportID, event.BindingSHA256, event.Binding.ManifestSHA256,
		event.Binding.ArtifactSHA256, event.Binding.ArtifactBytes, event.Binding.Format,
		event.Binding.SchemaVersion, event.Binding.ConverterVersion, event.Binding.SourceFileCount,
		event.Binding.SourceChunkCount, event.Binding.SourceObjectCount, event.Binding.MessageCount,
		boolInt(event.Apply), string(event.State), event.Counts.SourceCount, event.Counts.FileCount,
		event.Counts.ChunkCount, event.Counts.ObjectCount, event.Counts.MessageCount,
		event.Counts.BatchCount, event.Counts.RawCount, event.Counts.ProjectionCount,
		event.Counts.JobCount, warningsJSON, event.ErrorCode, event.FailureReason,
		event.AuditReference, event.CreatedAt); err != nil {
		t.Fatalf("insert test import event: %v", err)
	}
}

func reconcileRequestWithMisleadingEventHash(t *testing.T, owner string, binding domainmemory.ChatGPTImportBinding) string {
	t.Helper()
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(owner, binding)
	if err != nil {
		t.Fatalf("binding hash: %v", err)
	}
	importID := domainmemory.DeterministicChatGPTImportID(owner, bindingSHA256)
	for index := 0; index < 10_000; index++ {
		requestID := "request-order-" + strings.Repeat("x", index%7) + time.Unix(int64(index), 0).UTC().Format("150405")
		validatingID := domainmemory.DeterministicChatGPTImportEventID(importID, requestID, domainmemory.ChatGPTImportStateValidating)
		committingID := domainmemory.DeterministicChatGPTImportEventID(importID, requestID, domainmemory.ChatGPTImportStateCommitting)
		if validatingID > committingID {
			return requestID
		}
	}
	t.Fatal("could not find request ID with misleading deterministic event hash order")
	return ""
}

func reconcileRowCount(t *testing.T, store *L1SQLiteStore) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event`).Scan(&count); err != nil {
		t.Fatalf("count ChatGPT import ledger rows: %v", err)
	}
	return count
}

func reconcileTestDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "l1.db")
}
