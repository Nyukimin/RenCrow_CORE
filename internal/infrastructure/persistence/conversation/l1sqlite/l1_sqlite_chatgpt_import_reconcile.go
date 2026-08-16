package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	chatGPTImportInterruptedErrorCode     = "process_interrupted"
	chatGPTImportInterruptedFailureReason = "CORE process interrupted the active import"
)

type observedChatGPTImportEvent struct {
	rowID int64
	event domainmemory.ChatGPTImportEvent
}

type chatGPTImportRequestKey struct {
	importID  string
	requestID string
}

// ReconcileActiveChatGPTImports closes imports left active by a previous CORE
// process. It is an internal startup repair and therefore does not require a
// caller ToolExecutionScope. It only appends blocked ledger events; it never
// changes a binding, owner, Raw record, or domain projection.
func (s *L1SQLiteStore) ReconcileActiveChatGPTImports(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, chatGPTImportReconcileUnavailable("store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, chatGPTImportReconcileUnavailable("transaction could not start")
	}
	rollback := func() {
		_ = tx.Rollback()
	}
	fail := func(reason string) (int, error) {
		rollback()
		return 0, chatGPTImportReconcileUnavailable(reason)
	}

	rowIDs, err := chatGPTImportLedgerRowIDs(ctx, tx)
	if err != nil {
		return fail("ledger order could not be read")
	}
	ordered := make([]observedChatGPTImportEvent, 0, len(rowIDs))
	latest := make(map[chatGPTImportRequestKey]observedChatGPTImportEvent, len(rowIDs))
	existingByEventID := make(map[string]domainmemory.ChatGPTImportEvent, len(rowIDs))
	var latestCreatedAt time.Time
	for _, rowID := range rowIDs {
		event, queryErr := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE rowid = ?`, rowID)
		if queryErr != nil {
			return fail("ledger integrity validation failed")
		}
		observed := observedChatGPTImportEvent{rowID: rowID, event: event}
		ordered = append(ordered, observed)
		latest[chatGPTImportRequestKey{importID: event.ImportID, requestID: event.RequestID}] = observed
		existingByEventID[event.EventID] = event
		if event.CreatedAt.After(latestCreatedAt) {
			latestCreatedAt = event.CreatedAt
		}
	}

	active := make([]observedChatGPTImportEvent, 0)
	for _, observed := range ordered {
		key := chatGPTImportRequestKey{importID: observed.event.ImportID, requestID: observed.event.RequestID}
		if latest[key].rowID != observed.rowID {
			continue
		}
		if observed.event.State == domainmemory.ChatGPTImportStateValidating || observed.event.State == domainmemory.ChatGPTImportStateCommitting {
			active = append(active, observed)
		}
	}
	if len(active) == 0 {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return 0, chatGPTImportReconcileUnavailable("read transaction could not close")
		}
		return 0, nil
	}

	blockedEvents := make([]domainmemory.ChatGPTImportEvent, 0, len(active))
	for _, observed := range active {
		blocked := observed.event
		blocked.State = domainmemory.ChatGPTImportStateBlocked
		blocked.ErrorCode = chatGPTImportInterruptedErrorCode
		blocked.FailureReason = chatGPTImportInterruptedFailureReason
		blocked.EventID = domainmemory.DeterministicChatGPTImportEventID(blocked.ImportID, blocked.RequestID, blocked.State)
		blocked.CreatedAt = time.Time{}
		if _, exists := existingByEventID[blocked.EventID]; exists {
			return fail("deterministic blocked event conflicts with active request")
		}
		blockedInput := domainmemory.ChatGPTImportEventInput{
			RequestID: blocked.RequestID, OwnerID: blocked.OwnerID, ActorID: blocked.ActorID,
			Binding: blocked.Binding, Apply: blocked.Apply, State: blocked.State, Counts: blocked.Counts,
			Warnings: blocked.Warnings, ErrorCode: blocked.ErrorCode, FailureReason: blocked.FailureReason,
			AuditReference: blocked.AuditReference,
		}
		if err := blockedInput.Validate(); err != nil {
			return fail("reconciled event validation failed")
		}
		blockedEvents = append(blockedEvents, blocked)
	}

	createdAt := time.Now().UTC()
	if !createdAt.After(latestCreatedAt) {
		createdAt = latestCreatedAt.Add(time.Nanosecond)
	}
	for index := range blockedEvents {
		blockedEvents[index].CreatedAt = createdAt.Add(time.Duration(index) * time.Nanosecond)
		if err := validateStoredChatGPTImportEvent(blockedEvents[index]); err != nil {
			return fail("reconciled event integrity validation failed")
		}
		if err := insertReconciledChatGPTImportEvent(ctx, tx, blockedEvents[index]); err != nil {
			return fail("blocked event could not be appended")
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, chatGPTImportReconcileUnavailable("transaction could not commit")
	}
	return len(blockedEvents), nil
}

func chatGPTImportLedgerRowIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM l1_chatgpt_import_event ORDER BY rowid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rowIDs := make([]int64, 0)
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			return nil, err
		}
		if rowID <= 0 {
			return nil, fmt.Errorf("invalid ledger row order")
		}
		rowIDs = append(rowIDs, rowID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rowIDs, nil
}

func insertReconciledChatGPTImportEvent(ctx context.Context, tx *sql.Tx, event domainmemory.ChatGPTImportEvent) error {
	warningsJSON, err := json.Marshal(event.Warnings)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO l1_chatgpt_import_event (
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
		event.Counts.JobCount, string(warningsJSON), event.ErrorCode, event.FailureReason,
		event.AuditReference, event.CreatedAt)
	return err
}

func chatGPTImportReconcileUnavailable(reason string) error {
	return fmt.Errorf("ChatGPT import startup reconciliation %s: %w", reason, domainmemory.ErrChatGPTImportUnavailable)
}
