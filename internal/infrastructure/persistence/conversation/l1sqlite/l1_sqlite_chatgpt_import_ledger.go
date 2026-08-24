package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const chatGPTImportLedgerTable = "l1_chatgpt_import_event"

func chatGPTImportInternalError(message string) error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInternal, message)
}

// applyChatGPTImportLedgerSchema is additive and independent from the Common
// Raw marker migration. It is called on every store open so an existing Common
// Raw database cannot hide a missing ledger table or immutable trigger.
func (s *L1SQLiteStore) applyChatGPTImportLedgerSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chatGPTImportInternalError("ChatGPT import ledger schema initialization failed")
	}
	rollback := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return chatGPTImportInternalError("ChatGPT import ledger schema rollback failed")
		}
		return cause
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS l1_chatgpt_import_event (
			event_id TEXT PRIMARY KEY CHECK(length(event_id) > 0),
			import_id TEXT NOT NULL CHECK(length(import_id) > 0),
			request_id TEXT NOT NULL CHECK(length(request_id) > 0),
			owner_id TEXT NOT NULL CHECK(length(owner_id) > 0),
			actor_id TEXT NOT NULL CHECK(length(actor_id) > 0),
			export_id TEXT NOT NULL CHECK(length(export_id) > 0),
			binding_sha256 TEXT NOT NULL CHECK(length(binding_sha256) = 64 AND lower(binding_sha256) = binding_sha256 AND binding_sha256 NOT GLOB '*[^0-9a-f]*'),
			manifest_sha256 TEXT NOT NULL CHECK(length(manifest_sha256) = 64 AND lower(manifest_sha256) = manifest_sha256 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'),
			artifact_sha256 TEXT NOT NULL CHECK(length(artifact_sha256) = 64 AND lower(artifact_sha256) = artifact_sha256 AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
			artifact_bytes INTEGER NOT NULL CHECK(artifact_bytes > 0 AND artifact_bytes <= 68719476736),
			format TEXT NOT NULL CHECK(length(format) > 0),
			schema_version TEXT NOT NULL CHECK(length(schema_version) > 0),
			converter_version TEXT NOT NULL CHECK(length(converter_version) > 0),
			source_file_count INTEGER NOT NULL CHECK(source_file_count >= 0),
			source_chunk_count INTEGER NOT NULL CHECK(source_chunk_count >= 0),
			source_object_count INTEGER NOT NULL CHECK(source_object_count >= 0),
			message_count INTEGER NOT NULL CHECK(message_count >= 0),
			apply INTEGER NOT NULL CHECK(apply IN (0, 1)),
			state TEXT NOT NULL CHECK(state IN ('validating', 'committing', 'completed', 'rejected', 'blocked')),
			source_count INTEGER NOT NULL CHECK(source_count >= 0),
			file_count INTEGER NOT NULL CHECK(file_count >= 0),
			chunk_count INTEGER NOT NULL CHECK(chunk_count >= 0),
			object_count INTEGER NOT NULL CHECK(object_count >= 0),
			count_message_count INTEGER NOT NULL CHECK(count_message_count >= 0),
			batch_count INTEGER NOT NULL CHECK(batch_count >= 0),
			raw_count INTEGER NOT NULL CHECK(raw_count >= 0),
			projection_count INTEGER NOT NULL CHECK(projection_count >= 0),
			job_count INTEGER NOT NULL CHECK(job_count >= 0),
			warnings_json TEXT NOT NULL CHECK(length(warnings_json) <= 8192),
			error_code TEXT NOT NULL CHECK(length(error_code) <= 512),
			failure_reason TEXT NOT NULL CHECK(length(failure_reason) <= 512),
			audit_reference TEXT NOT NULL CHECK(length(audit_reference) <= 256),
			created_at TIMESTAMP NOT NULL,
			UNIQUE(import_id, request_id, state)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_import_event_owner_export_created ON l1_chatgpt_import_event(owner_id, export_id, created_at DESC, event_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_import_event_import_created ON l1_chatgpt_import_event(import_id, created_at DESC, event_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_l1_chatgpt_import_event_owner_binding ON l1_chatgpt_import_event(owner_id, binding_sha256, created_at DESC)`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_import_event_immutable_update BEFORE UPDATE ON l1_chatgpt_import_event BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import event is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_l1_chatgpt_import_event_immutable_delete BEFORE DELETE ON l1_chatgpt_import_event BEGIN SELECT RAISE(ABORT, 'l1 ChatGPT import event is immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(chatGPTImportInternalError("ChatGPT import ledger schema update failed"))
		}
	}
	if err := applyChatGPTProfilePromotionBindingSchema(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := verifyChatGPTImportLedgerSchema(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return chatGPTImportInternalError("ChatGPT import ledger schema commit failed")
	}
	return nil
}

func verifyChatGPTImportLedgerSchema(ctx context.Context, tx *sql.Tx) error {
	objects := []struct {
		kind string
		name string
	}{
		{kind: "table", name: chatGPTImportLedgerTable},
		{kind: "index", name: "idx_l1_chatgpt_import_event_owner_export_created"},
		{kind: "index", name: "idx_l1_chatgpt_import_event_import_created"},
		{kind: "index", name: "idx_l1_chatgpt_import_event_owner_binding"},
		{kind: "trigger", name: "trg_l1_chatgpt_import_event_immutable_update"},
		{kind: "trigger", name: "trg_l1_chatgpt_import_event_immutable_delete"},
	}
	for _, object := range objects {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			return chatGPTImportInternalError("ChatGPT import ledger schema verification failed")
		}
		if count != 1 {
			return chatGPTImportInternalError("ChatGPT import ledger schema is incomplete")
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(l1_chatgpt_import_event)`)
	if err != nil {
		return chatGPTImportInternalError("ChatGPT import ledger schema columns cannot be read")
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return chatGPTImportInternalError("ChatGPT import ledger schema columns cannot be read")
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return chatGPTImportInternalError("ChatGPT import ledger schema columns cannot be read")
	}
	for _, name := range []string{
		"event_id", "import_id", "request_id", "owner_id", "actor_id", "export_id", "binding_sha256",
		"manifest_sha256", "artifact_sha256", "artifact_bytes", "format", "schema_version",
		"converter_version", "source_file_count", "source_chunk_count", "source_object_count",
		"message_count", "apply", "state", "source_count", "file_count", "chunk_count", "object_count",
		"count_message_count", "batch_count", "raw_count", "projection_count", "job_count", "warnings_json",
		"error_code", "failure_reason", "audit_reference", "created_at",
	} {
		if _, ok := columns[name]; !ok {
			return chatGPTImportInternalError("ChatGPT import ledger schema is incomplete")
		}
	}
	return nil
}

// AppendChatGPTImportEvent appends one owner-scoped state event. It derives
// all durable identifiers from the validated binding and trusted scope; the
// caller cannot choose an event, import, or row identity.
func (s *L1SQLiteStore) AppendChatGPTImportEvent(ctx context.Context, input domainmemory.ChatGPTImportEventInput) (domainmemory.ChatGPTImportEvent, error) {
	if s == nil || s.db == nil {
		return domainmemory.ChatGPTImportEvent{}, fmt.Errorf("ChatGPT import ledger store is unavailable: %w", domainmemory.ErrChatGPTImportUnavailable)
	}
	if err := input.Validate(); err != nil {
		return domainmemory.ChatGPTImportEvent{}, err
	}
	if err := authorizeChatGPTImportScope(ctx, input.RequestID, input.OwnerID, input.ActorID); err != nil {
		return domainmemory.ChatGPTImportEvent{}, err
	}
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(input.OwnerID, input.Binding)
	if err != nil {
		return domainmemory.ChatGPTImportEvent{}, err
	}
	importID := domainmemory.DeterministicChatGPTImportID(input.OwnerID, bindingSHA256)
	eventID := domainmemory.DeterministicChatGPTImportEventID(importID, input.RequestID, input.State)
	event := domainmemory.ChatGPTImportEvent{
		EventID: eventID, ImportID: importID, RequestID: input.RequestID,
		OwnerID: input.OwnerID, ActorID: input.ActorID, Binding: input.Binding,
		BindingSHA256: bindingSHA256, Apply: input.Apply, State: input.State,
		Counts: input.Counts, Warnings: append([]string(nil), input.Warnings...),
		ErrorCode: input.ErrorCode, FailureReason: input.FailureReason,
		AuditReference: input.AuditReference, CreatedAt: time.Now().UTC(),
	}
	if event.Warnings == nil {
		event.Warnings = []string{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("ChatGPT import ledger append could not begin")
	}
	rollback := func(cause error) (domainmemory.ChatGPTImportEvent, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("ChatGPT import ledger append rollback failed")
		}
		return domainmemory.ChatGPTImportEvent{}, cause
	}

	if existing, queryErr := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id = ?`, eventID); queryErr == nil {
		if chatGPTImportEventMatchesInput(existing, input, bindingSHA256, importID, eventID) {
			latestRequest, latestErr := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE import_id = ? AND request_id = ? ORDER BY rowid DESC LIMIT 1`, importID, input.RequestID)
			if latestErr != nil {
				return rollback(fmt.Errorf("read exact ChatGPT import replay history: %w", latestErr))
			}
			if (latestRequest.State == domainmemory.ChatGPTImportStateCompleted || latestRequest.State == domainmemory.ChatGPTImportStateRejected || latestRequest.State == domainmemory.ChatGPTImportStateBlocked) && latestRequest.EventID != existing.EventID {
				return rollback(fmt.Errorf("ChatGPT import request is already terminal: %w", domainmemory.ErrChatGPTImportInvalid))
			}
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("ChatGPT import replay rollback failed")
			}
			return existing, nil
		}
		return rollback(fmt.Errorf("ChatGPT import event %s has changed fields: %w", eventID, domainmemory.ErrChatGPTImportConflict))
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return rollback(fmt.Errorf("read ChatGPT import event %s: %w", eventID, queryErr))
	}

	var changedBinding string
	if err := tx.QueryRowContext(ctx, `SELECT binding_sha256 FROM l1_chatgpt_import_event WHERE owner_id = ? AND export_id = ? AND binding_sha256 <> ? LIMIT 1`, input.OwnerID, input.Binding.ExportID, bindingSHA256).Scan(&changedBinding); err == nil {
		return rollback(fmt.Errorf("ChatGPT import export %q binding changed: %w", input.Binding.ExportID, domainmemory.ErrChatGPTImportSourceChanged))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(chatGPTImportInternalError("ChatGPT import binding history could not be read"))
	}

	latestRequest, requestErr := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE import_id = ? AND request_id = ? ORDER BY rowid DESC LIMIT 1`, importID, input.RequestID)
	if requestErr != nil && !errors.Is(requestErr, sql.ErrNoRows) {
		return rollback(fmt.Errorf("read ChatGPT import request history: %w", requestErr))
	}
	if requestErr == nil {
		if !chatGPTImportTransitionAllowed(latestRequest.State, input.State) {
			return rollback(fmt.Errorf("illegal ChatGPT import transition %s -> %s: %w", latestRequest.State, input.State, domainmemory.ErrChatGPTImportInvalid))
		}
	} else {
		latestImport, importErr := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE import_id = ? ORDER BY rowid DESC LIMIT 1`, importID)
		if importErr != nil && !errors.Is(importErr, sql.ErrNoRows) {
			return rollback(fmt.Errorf("read ChatGPT import history: %w", importErr))
		}
		if importErr == nil {
			switch latestImport.State {
			case domainmemory.ChatGPTImportStateCompleted:
				if input.State != domainmemory.ChatGPTImportStateValidating {
					return rollback(fmt.Errorf("new ChatGPT import request must start validating: %w", domainmemory.ErrChatGPTImportInvalid))
				}
				if latestImport.Apply || !input.Apply {
					if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
						return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("ChatGPT import replay rollback failed")
					}
					return latestImport, nil
				}
			case domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportStateBlocked:
				if input.State != domainmemory.ChatGPTImportStateValidating {
					return rollback(fmt.Errorf("new ChatGPT import request must start validating: %w", domainmemory.ErrChatGPTImportInvalid))
				}
			default:
				return rollback(fmt.Errorf("ChatGPT import already has an active request: %w", domainmemory.ErrChatGPTImportConflict))
			}
		} else if input.State != domainmemory.ChatGPTImportStateValidating {
			return rollback(fmt.Errorf("new ChatGPT import request must start validating: %w", domainmemory.ErrChatGPTImportInvalid))
		}
	}

	warningsJSON, err := json.Marshal(event.Warnings)
	if err != nil {
		return rollback(fmt.Errorf("marshal ChatGPT import warnings: %w", err))
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
	if err != nil {
		return rollback(chatGPTImportInternalError("ChatGPT import ledger append failed"))
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("ChatGPT import ledger append commit failed")
	}
	return event, nil
}

// GetChatGPTImportStatus returns the latest bounded event for one owner and
// export. The owner predicate is part of the query, so another owner's import
// is indistinguishable from an unknown export.
func (s *L1SQLiteStore) GetChatGPTImportStatus(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportView, error) {
	if s == nil || s.db == nil {
		return domainmemory.ChatGPTImportView{}, fmt.Errorf("ChatGPT import ledger store is unavailable: %w", domainmemory.ErrChatGPTImportUnavailable)
	}
	if strings.TrimSpace(exportID) == "" || strings.ContainsAny(strings.TrimSpace(exportID), "/\\") {
		return domainmemory.ChatGPTImportView{}, fmt.Errorf("export_id is required: %w", domainmemory.ErrChatGPTImportInvalid)
	}
	if err := authorizeChatGPTImportScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.ChatGPTImportView{}, err
	}
	event, err := queryChatGPTImportEvent(ctx, s.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE owner_id = ? AND export_id = ? ORDER BY rowid DESC LIMIT 1`, ownerID, exportID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.ChatGPTImportView{}, fmt.Errorf("ChatGPT import export %q not found: %w", exportID, domainmemory.ErrChatGPTImportNotFound)
	}
	if err != nil {
		return domainmemory.ChatGPTImportView{}, chatGPTImportInternalError("ChatGPT import status could not be read")
	}
	return event.View(), nil
}

// ChatGPTImportStatus is a descriptive alias for status callers.
func (s *L1SQLiteStore) ChatGPTImportStatus(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportView, error) {
	return s.GetChatGPTImportStatus(ctx, requestID, ownerID, actorID, exportID)
}

func authorizeChatGPTImportScope(ctx context.Context, requestID, ownerID, actorID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.RequestID != strings.TrimSpace(requestID) || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != strings.TrimSpace(actorID) || scope.AuthenticatedUserID != strings.TrimSpace(ownerID) || !scope.Allows(domaintool.DataScopeUser) {
		return fmt.Errorf("ChatGPT import requires matching authenticated user ToolExecutionScope: %w", domainmemory.ErrChatGPTImportForbidden)
	}
	return nil
}

func chatGPTImportTransitionAllowed(previous, next domainmemory.ChatGPTImportState) bool {
	switch previous {
	case domainmemory.ChatGPTImportStateValidating:
		return next == domainmemory.ChatGPTImportStateCommitting || next == domainmemory.ChatGPTImportStateRejected || next == domainmemory.ChatGPTImportStateBlocked
	case domainmemory.ChatGPTImportStateCommitting:
		return next == domainmemory.ChatGPTImportStateCompleted || next == domainmemory.ChatGPTImportStateRejected || next == domainmemory.ChatGPTImportStateBlocked
	default:
		return false
	}
}

func chatGPTImportEventMatchesInput(event domainmemory.ChatGPTImportEvent, input domainmemory.ChatGPTImportEventInput, bindingSHA256, importID, eventID string) bool {
	return event.EventID == eventID && event.ImportID == importID && event.RequestID == input.RequestID && event.OwnerID == input.OwnerID && event.ActorID == input.ActorID && event.BindingSHA256 == bindingSHA256 && event.Binding == input.Binding && event.Apply == input.Apply && event.State == input.State && reflect.DeepEqual(event.Counts, input.Counts) && equalChatGPTImportWarnings(event.Warnings, input.Warnings) && event.ErrorCode == input.ErrorCode && event.FailureReason == input.FailureReason && event.AuditReference == input.AuditReference
}

func equalChatGPTImportWarnings(left, right []string) bool {
	if len(left) != len(right) {
		return len(left) == 0 && len(right) == 0
	}
	if len(left) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}

const chatGPTImportEventColumns = `event_id, import_id, request_id, owner_id, actor_id, export_id, binding_sha256, manifest_sha256, artifact_sha256, artifact_bytes, format, schema_version, converter_version, source_file_count, source_chunk_count, source_object_count, message_count, apply, state, source_count, file_count, chunk_count, object_count, count_message_count, batch_count, raw_count, projection_count, job_count, warnings_json, error_code, failure_reason, audit_reference, created_at`

func queryChatGPTImportEvent(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, args ...any) (domainmemory.ChatGPTImportEvent, error) {
	var event domainmemory.ChatGPTImportEvent
	var apply int
	var state string
	var warningsJSON string
	var sourceFileCount, sourceChunkCount, sourceObjectCount, messageCount int
	var sourceCount, fileCount, chunkCount, objectCount, countMessageCount, batchCount, rawCount, projectionCount, jobCount int
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(
		&event.EventID, &event.ImportID, &event.RequestID, &event.OwnerID, &event.ActorID,
		&event.Binding.ExportID, &event.BindingSHA256, &event.Binding.ManifestSHA256,
		&event.Binding.ArtifactSHA256, &event.Binding.ArtifactBytes, &event.Binding.Format,
		&event.Binding.SchemaVersion, &event.Binding.ConverterVersion, &sourceFileCount,
		&sourceChunkCount, &sourceObjectCount, &messageCount, &apply, &state, &sourceCount,
		&fileCount, &chunkCount, &objectCount, &countMessageCount, &batchCount, &rawCount,
		&projectionCount, &jobCount, &warningsJSON, &event.ErrorCode, &event.FailureReason,
		&event.AuditReference, &event.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainmemory.ChatGPTImportEvent{}, err
		}
		return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("stored ChatGPT import event cannot be read")
	}
	if err := json.Unmarshal([]byte(warningsJSON), &event.Warnings); err != nil {
		return domainmemory.ChatGPTImportEvent{}, chatGPTImportInternalError("stored ChatGPT import diagnostics are invalid")
	}
	if event.Warnings == nil {
		event.Warnings = []string{}
	}
	event.Binding.SourceFileCount = sourceFileCount
	event.Binding.SourceChunkCount = sourceChunkCount
	event.Binding.SourceObjectCount = sourceObjectCount
	event.Binding.MessageCount = messageCount
	event.Apply = apply != 0
	event.State = domainmemory.ChatGPTImportState(state)
	event.Counts = domainmemory.ChatGPTImportCounts{
		SourceCount: sourceCount, FileCount: fileCount, ChunkCount: chunkCount,
		ObjectCount: objectCount, MessageCount: countMessageCount, BatchCount: batchCount,
		RawCount: rawCount, ProjectionCount: projectionCount, JobCount: jobCount,
	}
	if err := validateStoredChatGPTImportEvent(event); err != nil {
		return domainmemory.ChatGPTImportEvent{}, err
	}
	return event, nil
}

func validateStoredChatGPTImportEvent(event domainmemory.ChatGPTImportEvent) error {
	if event.CreatedAt.IsZero() {
		return chatGPTImportInternalError("stored ChatGPT import event integrity check failed")
	}
	input := domainmemory.ChatGPTImportEventInput{
		RequestID: event.RequestID, OwnerID: event.OwnerID, ActorID: event.ActorID,
		Binding: event.Binding, Apply: event.Apply, State: event.State, Counts: event.Counts,
		Warnings: event.Warnings, ErrorCode: event.ErrorCode, FailureReason: event.FailureReason,
		AuditReference: event.AuditReference,
	}
	if err := input.Validate(); err != nil {
		return chatGPTImportInternalError("stored ChatGPT import event integrity check failed")
	}
	bindingSHA256, err := domainmemory.DeterministicChatGPTImportBindingSHA256(event.OwnerID, event.Binding)
	if err != nil {
		return chatGPTImportInternalError("stored ChatGPT import binding integrity check failed")
	}
	if event.BindingSHA256 != bindingSHA256 || event.ImportID != domainmemory.DeterministicChatGPTImportID(event.OwnerID, bindingSHA256) || event.EventID != domainmemory.DeterministicChatGPTImportEventID(event.ImportID, event.RequestID, event.State) {
		return chatGPTImportInternalError("stored ChatGPT import event identity does not match its binding")
	}
	return nil
}
