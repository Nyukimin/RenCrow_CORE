package l1sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	opsAcceptanceRawSourceType    = "conversation"
	opsAcceptanceRawSchemaVersion = domconv.AcceptedOPSInputCanonicalVersion
	opsAcceptanceRawConverter     = "core-native/accepted-ops-input-v1"
	opsAcceptanceRawProvenance    = "authenticated-core-ops-intake"
	opsAcceptanceRawRights        = "user-provided"
	opsAcceptanceRawLicense       = "private"
	opsAcceptanceAgentReadActor   = "shiro"
	opsAcceptanceAgentReadRole    = "worker"
	opsAcceptanceAgentReadPurpose = "ops"
	opsAcceptanceMaxWriteAttempts = 3
	opsAcceptanceRollbackTimeout  = 5 * time.Second
)

type opsAcceptanceRetryableError struct{ cause error }

func (e *opsAcceptanceRetryableError) Error() string { return "accepted OPS input write contention" }
func (e *opsAcceptanceRetryableError) Unwrap() error { return e.cause }

type acceptedOPSInputQuery interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

type acceptedOPSInputTx interface {
	acceptedOPSInputQuery
	l1SQLExecer
}

func (s *L1SQLiteStore) AcceptOPSInput(ctx context.Context, request domconv.AcceptedOPSInputRequest) (domconv.AcceptedOPSInputReceipt, error) {
	if s == nil || s.db == nil {
		return domconv.AcceptedOPSInputReceipt{}, domconv.ErrAcceptedOPSInputUnavailable
	}
	normalized, err := domconv.NormalizeAcceptedOPSInputRequest(request)
	if err != nil {
		return domconv.AcceptedOPSInputReceipt{}, err
	}
	if err := validateCommonRawOwnerScope(ctx, normalized.RequestID, normalized.OwnerID, normalized.ActorID); err != nil {
		return domconv.AcceptedOPSInputReceipt{}, domconv.ErrAcceptedOPSInputForbidden
	}
	payloadHash, err := domconv.AcceptedOPSInputPayloadSHA256(normalized)
	if err != nil {
		return domconv.AcceptedOPSInputReceipt{}, err
	}

	s.rawMu.Lock()
	defer s.rawMu.Unlock()

	for attempt := 0; attempt < opsAcceptanceMaxWriteAttempts; attempt++ {
		receipt, err := s.acceptOPSInputTransaction(ctx, normalized, payloadHash)
		if err == nil {
			return receipt, nil
		}
		if existing, found, lookupErr := s.readAcceptedOPSInputForRetry(ctx, normalized.OwnerID, normalized.RequestID); lookupErr == nil && found {
			if existing.Receipt.PayloadSHA256 != payloadHash {
				return domconv.AcceptedOPSInputReceipt{}, domconv.ErrAcceptedOPSInputConflict
			}
			existing.Receipt.IdempotentReplay = true
			return existing.Receipt, nil
		}
		var contention *opsAcceptanceRetryableError
		if !errors.As(err, &contention) {
			return domconv.AcceptedOPSInputReceipt{}, err
		}
	}
	return domconv.AcceptedOPSInputReceipt{}, domconv.ErrAcceptedOPSInputUnavailable
}

func (s *L1SQLiteStore) acceptOPSInputTransaction(
	ctx context.Context,
	request domconv.AcceptedOPSInputRequest,
	payloadHash string,
) (domconv.AcceptedOPSInputReceipt, error) {
	base := domconv.AcceptedOPSInputReceipt{}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return base, classifyOPSInputWriteError(err)
	}
	defer func() { _ = conn.Close() }()
	// Reserve SQLite's writer before any receipt or thread reads. A deferred
	// read-to-write upgrade on independent handles can fail with SQLITE_BUSY.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return base, classifyOPSInputWriteError(err)
	}
	transactionFinished := false
	finishWithRollback := func() error {
		if transactionFinished {
			return nil
		}
		// Mark the one cleanup attempt before I/O so explicit returns and panic
		// unwinding can never issue a second ROLLBACK on the same connection.
		transactionFinished = true
		if rollbackErr := s.rollbackAcceptedOPSInput(conn); rollbackErr != nil {
			return errors.Join(rollbackErr, discardAcceptedOPSInputConnection(conn))
		}
		return nil
	}
	defer func() {
		if !transactionFinished {
			_ = finishWithRollback()
		}
	}()
	tx := acceptedOPSInputTx(conn)
	rollback := func(cause error) (domconv.AcceptedOPSInputReceipt, error) {
		if rollbackErr := finishWithRollback(); rollbackErr != nil {
			return base, domconv.ErrAcceptedOPSInputUnavailable
		}
		return base, cause
	}

	existing, found, err := loadAcceptedOPSInput(ctx, tx, request.OwnerID, request.RequestID)
	if err != nil {
		return rollback(domconv.ErrAcceptedOPSInputUnavailable)
	}
	if found {
		if existing.Receipt.PayloadSHA256 != payloadHash {
			return rollback(domconv.ErrAcceptedOPSInputConflict)
		}
		if _, err := verifyAcceptedOPSInput(ctx, tx, existing.Receipt); err != nil {
			return rollback(domconv.ErrAcceptedOPSInputUnavailable)
		}
		if err := finishWithRollback(); err != nil {
			return base, domconv.ErrAcceptedOPSInputUnavailable
		}
		existing.Receipt.IdempotentReplay = true
		return existing.Receipt, nil
	}

	now := time.Now().UTC()
	prepared, receiptJSON, checkpointJSON, manifestID, rawRecordID, rawHash, err := prepareAcceptedOPSCommonRaw(request, now)
	if err != nil {
		return rollback(domconv.ErrAcceptedOPSInputInvalid)
	}
	if err := bindAcceptedOPSFirstThread(ctx, tx, request, now); err != nil {
		return rollback(classifyOPSInputWriteError(err))
	}
	if err := insertPreparedCommonRawRows(ctx, tx, request.RequestID, request.OwnerID, request.ActorID, manifestID, prepared, receiptJSON, checkpointJSON, now); err != nil {
		return rollback(classifyOPSInputWriteError(err))
	}

	base = domconv.AcceptedOPSInputReceipt{
		AcceptanceSequence: 1,
		RequestID:          request.RequestID, OwnerID: request.OwnerID, ActorID: request.ActorID,
		SessionID: request.SessionID, ThreadID: request.FirstThreadID, ThreadSeq: modulecore.ThreadSeq(1),
		ThreadKind: modulecore.ThreadKindUserConversation, TaskID: request.TaskID, TurnID: request.TurnID,
		TraceID: request.TraceID, UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
		DeclaredOrigin: request.DeclaredOrigin, PayloadSHA256: payloadHash, RawRecordID: rawRecordID,
		ManifestID: manifestID, RawSHA256: rawHash, ManifestSHA256: prepared.manifestSHA256, AcceptedAt: now,
	}
	if err := base.Validate(); err != nil {
		return rollback(domconv.ErrAcceptedOPSInputInvalid)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO conversation_ops_input_acceptance (
	owner_id, actor_id, request_id, payload_sha256, declared_origin, session_id, thread_id,
	thread_seq, thread_kind, task_id, turn_id, trace_id, user_message_id, agent_message_id,
	raw_record_id, manifest_id, raw_sha256, manifest_sha256, accepted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		base.OwnerID, base.ActorID, base.RequestID, base.PayloadSHA256, base.DeclaredOrigin, base.SessionID, base.ThreadID,
		base.ThreadSeq, base.ThreadKind, base.TaskID, base.TurnID, base.TraceID, base.UserMessageID, base.AgentMessageID,
		base.RawRecordID, base.ManifestID, base.RawSHA256, base.ManifestSHA256, base.AcceptedAt)
	if err != nil {
		return rollback(classifyOPSInputWriteError(err))
	}
	sequence, err := result.LastInsertId()
	if err != nil || sequence <= 0 {
		return rollback(domconv.ErrAcceptedOPSInputUnavailable)
	}
	base.AcceptanceSequence = sequence
	if _, err := verifyAcceptedOPSInput(ctx, tx, base); err != nil {
		return rollback(domconv.ErrAcceptedOPSInputUnavailable)
	}
	if err := s.commitAcceptedOPSInput(ctx, conn); err != nil {
		if rollbackErr := finishWithRollback(); rollbackErr != nil {
			return base, domconv.ErrAcceptedOPSInputUnavailable
		}
		return base, domconv.ErrAcceptedOPSInputUnavailable
	}
	transactionFinished = true
	return base, nil
}

func prepareAcceptedOPSCommonRaw(
	request domconv.AcceptedOPSInputRequest,
	now time.Time,
) (preparedCommonRawIntake, []byte, []byte, string, string, string, error) {
	content := []byte(request.RawMessage)
	rawHash := domainmemory.SHA256Hex(content)
	manifest := domainmemory.CommonRawManifest{
		ContractVersion:  domainmemory.CommonRawContractVersion,
		SourceType:       opsAcceptanceRawSourceType,
		SourceIdentity:   acceptedOPSRawSourceIdentity(request.RequestID),
		SourceCount:      1,
		SchemaVersion:    opsAcceptanceRawSchemaVersion,
		ConverterVersion: opsAcceptanceRawConverter,
		Sensitivity:      domainmemory.CommonRawPrivateSensitivity,
		Rights:           opsAcceptanceRawRights,
		License:          opsAcceptanceRawLicense,
		Provenance:       opsAcceptanceRawProvenance,
	}
	record := domainmemory.CommonRawRecord{
		SourceRecordID: string(request.UserMessageID),
		ThreadID:       string(request.FirstThreadID),
		Sensitivity:    domainmemory.CommonRawPrivateSensitivity,
		Role:           "user",
		ContentType:    "text/plain; charset=utf-8",
		OccurredAt:     now,
		Content:        content,
		ContentSHA256:  rawHash,
		Provenance:     opsAcceptanceRawProvenance,
		Rights:         opsAcceptanceRawRights,
		License:        opsAcceptanceRawLicense,
	}
	manifestHash, err := domainmemory.CommonRawInputHash(manifest, []domainmemory.CommonRawRecord{record}, nil)
	if err != nil {
		return preparedCommonRawIntake{}, nil, nil, "", "", "", err
	}
	manifest.ManifestSHA256 = manifestHash
	prepared, err := prepareCommonRawIntake(request.OwnerID, domainmemory.CommonRawIntakeRequest{
		Manifest: manifest,
		Records:  []domainmemory.CommonRawRecord{record},
	})
	if err != nil {
		return preparedCommonRawIntake{}, nil, nil, "", "", "", err
	}
	if prepared.requiresObject || len(prepared.records) != 1 || prepared.records[0].storage != domainmemory.CommonRawStorageInline {
		return preparedCommonRawIntake{}, nil, nil, "", "", "", fmt.Errorf("accepted OPS input must remain one inline CommonRaw record")
	}
	manifestID := domainmemory.DeterministicCommonRawManifestID(
		request.OwnerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, prepared.manifestSHA256,
	)
	rawRecordID := domainmemory.DeterministicCommonRawRecordID(
		request.OwnerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity,
		prepared.records[0].input.SourceRecordID, prepared.records[0].hash,
	)
	commonReceipt := commonRawReceiptFromPrepared(request.RequestID, request.OwnerID, prepared.manifest.Scope, manifestID, prepared, now)
	receiptJSON, err := json.Marshal(commonReceipt)
	if err != nil {
		return preparedCommonRawIntake{}, nil, nil, "", "", "", err
	}
	checkpointJSON, err := json.Marshal(struct {
		ManifestID  string                      `json:"manifest_id"`
		SourceCount int                         `json:"source_count"`
		AssetCount  int                         `json:"asset_count"`
		Status      domainmemory.CommonRawState `json:"status"`
	}{manifestID, len(prepared.records), len(prepared.assets), domainmemory.CommonRawStateCompleted})
	if err != nil {
		return preparedCommonRawIntake{}, nil, nil, "", "", "", err
	}
	return prepared, receiptJSON, checkpointJSON, manifestID, rawRecordID, rawHash, nil
}

func bindAcceptedOPSFirstThread(
	ctx context.Context,
	tx acceptedOPSInputTx,
	request domconv.AcceptedOPSInputRequest,
	now time.Time,
) error {
	var existingThreadID, existingKind, existingDomain string
	var existingSeq int64
	var existingCount int
	err := tx.QueryRowContext(ctx, `
SELECT thread_id, thread_seq, thread_kind, domain, message_count
FROM conversation_active_thread
WHERE session_id = ?`, request.SessionID).Scan(&existingThreadID, &existingSeq, &existingKind, &existingDomain, &existingCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	var sessionEventCount, threadEventCount, turnReceiptCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event WHERE session_id = ?`, request.SessionID).Scan(&sessionEventCount); err != nil {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event WHERE thread_id = ?`, request.FirstThreadID).Scan(&threadEventCount); err != nil {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_turn_receipt WHERE session_id = ?`, request.SessionID).Scan(&turnReceiptCount); err != nil {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	if sessionEventCount != 0 || threadEventCount != 0 || turnReceiptCount != 0 {
		return domconv.ErrAcceptedOPSInputConflict
	}
	if err == nil {
		if existingThreadID != string(request.FirstThreadID) || existingSeq != 1 ||
			existingKind != string(modulecore.ThreadKindUserConversation) || existingCount != 0 ||
			existingDomain == "" || !utf8.ValidString(existingDomain) || strings.IndexByte(existingDomain, 0) >= 0 {
			return domconv.ErrAcceptedOPSInputConflict
		}
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO conversation_active_thread (session_id, thread_id, thread_seq, thread_kind, domain, message_count, updated_at)
VALUES (?, ?, 1, 'user_conversation', 'ops', 0, ?)`, request.SessionID, request.FirstThreadID, now)
	if err != nil {
		return err
	}
	return nil
}

func (s *L1SQLiteStore) ReadAcceptedOPSInput(
	ctx context.Context,
	request domconv.AcceptedOPSInputReadRequest,
) (domconv.AcceptedOPSInput, error) {
	if s == nil || s.readDB == nil {
		return domconv.AcceptedOPSInput{}, domconv.ErrAcceptedOPSInputUnavailable
	}
	normalized, err := domconv.NormalizeAcceptedOPSInputReadRequest(request)
	if err != nil {
		return domconv.AcceptedOPSInput{}, err
	}
	if err := validateAcceptedOPSInputReadScope(ctx, normalized); err != nil {
		return domconv.AcceptedOPSInput{}, err
	}
	input, found, err := readAcceptedOPSInputSnapshot(ctx, s.readDB, normalized.OwnerID, normalized.RequestID, nil)
	if err != nil || !found {
		return domconv.AcceptedOPSInput{}, domconv.ErrAcceptedOPSInputUnavailable
	}
	return input, nil
}

func validateAcceptedOPSInputReadScope(ctx context.Context, request domconv.AcceptedOPSInputReadRequest) error {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.RequestID != request.RequestID ||
		scope.AuthenticatedUserID != request.OwnerID || !scope.Allows(domaintool.DataScopeUser) {
		return domconv.ErrAcceptedOPSInputForbidden
	}
	if scope.ActorKind == domaintool.ActorKindUser {
		if err := validateCommonRawOwnerScope(ctx, request.RequestID, request.OwnerID, request.OwnerID); err != nil {
			return domconv.ErrAcceptedOPSInputForbidden
		}
		return nil
	}
	if scope.ActorKind == domaintool.ActorKindAgent &&
		scope.ActorID == opsAcceptanceAgentReadActor &&
		scope.AgentRole == opsAcceptanceAgentReadRole &&
		scope.Purpose == opsAcceptanceAgentReadPurpose &&
		scope.AuthenticationSource == domaintool.AuthenticationSourceAgentOrchestrator {
		return nil
	}
	return domconv.ErrAcceptedOPSInputForbidden
}

func (s *L1SQLiteStore) readAcceptedOPSInputForRetry(
	ctx context.Context,
	ownerID string,
	requestID string,
) (domconv.AcceptedOPSInput, bool, error) {
	if s == nil || s.readDB == nil {
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	return readAcceptedOPSInputSnapshot(ctx, s.readDB, ownerID, requestID, nil)
}

func (s *L1SQLiteStore) commitAcceptedOPSInput(ctx context.Context, conn *sql.Conn) error {
	if s.opsAcceptanceCommitHook != nil {
		return s.opsAcceptanceCommitHook(ctx, conn)
	}
	_, err := conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (s *L1SQLiteStore) rollbackAcceptedOPSInput(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), opsAcceptanceRollbackTimeout)
	defer cancel()
	if s.opsAcceptanceRollbackHook != nil {
		return s.opsAcceptanceRollbackHook(ctx, conn)
	}
	_, err := conn.ExecContext(ctx, `ROLLBACK`)
	return err
}

func discardAcceptedOPSInputConnection(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if err == nil || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}

// readAcceptedOPSInputSnapshot keeps receipt, CommonRaw metadata, payload, and
// lifecycle checks on one SQLite snapshot. The configured read pool enforces
// query_only for every connection.
func readAcceptedOPSInputSnapshot(
	ctx context.Context,
	db *sql.DB,
	ownerID string,
	requestID string,
	afterReceiptLoaded func(),
) (domconv.AcceptedOPSInput, bool, error) {
	if db == nil {
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	receipt, found, err := loadAcceptedOPSInput(ctx, tx, ownerID, requestID)
	if err != nil || !found {
		_ = tx.Rollback()
		return domconv.AcceptedOPSInput{}, found, err
	}
	if afterReceiptLoaded != nil {
		afterReceiptLoaded()
	}
	message, err := verifyAcceptedOPSInput(ctx, tx, receipt.Receipt)
	if err != nil {
		_ = tx.Rollback()
		return domconv.AcceptedOPSInput{}, false, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	receipt.RawMessage = message
	return receipt, true, nil
}

func classifyOPSInputWriteError(err error) error {
	if errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		return domconv.ErrAcceptedOPSInputConflict
	}
	if errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return domconv.ErrAcceptedOPSInputUnavailable
	}
	code := sqliteErr.Code()
	switch code {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return domconv.ErrAcceptedOPSInputConflict
	}
	primaryCode := code & 0xff
	if primaryCode == sqlite3.SQLITE_BUSY || primaryCode == sqlite3.SQLITE_LOCKED {
		return &opsAcceptanceRetryableError{cause: err}
	}
	return domconv.ErrAcceptedOPSInputUnavailable
}

func loadAcceptedOPSInput(
	ctx context.Context,
	query acceptedOPSInputQuery,
	ownerID string,
	requestID string,
) (domconv.AcceptedOPSInput, bool, error) {
	var receipt domconv.AcceptedOPSInputReceipt
	var sessionID, threadID, threadKind, taskID, turnID, traceID, userMessageID, agentMessageID string
	var origin string
	err := query.QueryRowContext(ctx, `
SELECT acceptance_sequence, request_id, owner_id, actor_id, session_id, thread_id, thread_seq,
	thread_kind, task_id, turn_id, trace_id, user_message_id, agent_message_id, declared_origin,
	payload_sha256, raw_record_id, manifest_id, raw_sha256, manifest_sha256, accepted_at
FROM conversation_ops_input_acceptance
WHERE owner_id = ? AND request_id = ?`, ownerID, requestID).Scan(
		&receipt.AcceptanceSequence, &receipt.RequestID, &receipt.OwnerID, &receipt.ActorID, &sessionID, &threadID,
		&receipt.ThreadSeq, &threadKind, &taskID, &turnID, &traceID, &userMessageID, &agentMessageID, &origin,
		&receipt.PayloadSHA256, &receipt.RawRecordID, &receipt.ManifestID, &receipt.RawSHA256, &receipt.ManifestSHA256, &receipt.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domconv.AcceptedOPSInput{}, false, nil
	}
	if err != nil {
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	receipt.SessionID = modulecore.SessionID(sessionID)
	receipt.ThreadID = modulecore.ThreadID(threadID)
	receipt.ThreadKind = modulecore.ThreadKind(threadKind)
	receipt.TaskID = modulecore.TaskID(taskID)
	receipt.TurnID = modulecore.TurnID(turnID)
	receipt.TraceID = modulecore.TraceID(traceID)
	receipt.UserMessageID = modulecore.MessageID(userMessageID)
	receipt.AgentMessageID = modulecore.MessageID(agentMessageID)
	receipt.DeclaredOrigin = domconv.AcceptedOPSInputOrigin(origin)
	if receipt.OwnerID != ownerID || receipt.RequestID != requestID || receipt.Validate() != nil {
		return domconv.AcceptedOPSInput{}, false, domconv.ErrAcceptedOPSInputUnavailable
	}
	return domconv.AcceptedOPSInput{Receipt: receipt}, true, nil
}

func verifyAcceptedOPSInput(
	ctx context.Context,
	query acceptedOPSInputQuery,
	receipt domconv.AcceptedOPSInputReceipt,
) (string, error) {
	if receipt.Validate() != nil {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	var manifestContract, manifestType, manifestIdentity, manifestHash, manifestSchema, manifestConverter string
	var manifestOwner, manifestScope, manifestSensitivity, manifestRights, manifestLicense, manifestProvenance string
	var manifestRequestID, manifestActorID, manifestStatus, checkpointJSON, commonReceiptJSON string
	var manifestSourceCount, manifestAssetCount, manifestAllowEmpty int
	var manifestCreatedAt, manifestUpdatedAt time.Time
	var recordID, recordManifestID, recordContract, recordType, recordIdentity, recordSourceID, recordParentID, recordThreadID string
	var recordOwner, recordScope, recordSensitivity, recordRole, recordContentType, recordStorage, recordObjectRef string
	var recordHash, recordAssetJSON, recordProvenance, recordRights, recordLicense string
	var recordContent []byte
	var recordSize int64
	var recordOccurredAt, recordIngestedAt, recordCreatedAt time.Time
	err := query.QueryRowContext(ctx, `
SELECT
	m.contract_version, m.source_type, m.source_identity, m.manifest_sha256, m.schema_version, m.converter_version,
	m.owner_id, m.scope, m.sensitivity, m.rights, m.license, m.provenance, m.source_count, m.asset_count,
	m.allow_empty, m.request_id, m.actor_id, m.intake_status, m.checkpoint_json, m.receipt_json, m.created_at, m.updated_at,
	r.raw_record_id, r.manifest_id, r.contract_version, r.source_type, r.source_identity, r.source_record_id, r.parent_id,
	r.thread_id, r.owner_id, r.scope, r.sensitivity, r.role, r.content_type, r.occurred_at, r.ingested_at, r.storage_kind,
	r.inline_payload, r.object_ref, r.content_sha256, r.content_size, r.asset_refs_json, r.provenance, r.rights, r.license, r.created_at
FROM l1_raw_source_manifest m
JOIN l1_raw_record r ON r.manifest_id = m.manifest_id
WHERE m.manifest_id = ? AND r.raw_record_id = ?`, receipt.ManifestID, receipt.RawRecordID).Scan(
		&manifestContract, &manifestType, &manifestIdentity, &manifestHash, &manifestSchema, &manifestConverter,
		&manifestOwner, &manifestScope, &manifestSensitivity, &manifestRights, &manifestLicense, &manifestProvenance,
		&manifestSourceCount, &manifestAssetCount, &manifestAllowEmpty, &manifestRequestID, &manifestActorID,
		&manifestStatus, &checkpointJSON, &commonReceiptJSON, &manifestCreatedAt, &manifestUpdatedAt,
		&recordID, &recordManifestID, &recordContract, &recordType, &recordIdentity, &recordSourceID, &recordParentID,
		&recordThreadID, &recordOwner, &recordScope, &recordSensitivity, &recordRole, &recordContentType,
		&recordOccurredAt, &recordIngestedAt, &recordStorage, &recordContent, &recordObjectRef, &recordHash, &recordSize,
		&recordAssetJSON, &recordProvenance, &recordRights, &recordLicense, &recordCreatedAt)
	if err != nil {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	sourceIdentity := acceptedOPSRawSourceIdentity(receipt.RequestID)
	scope := "user:" + receipt.OwnerID
	if recordID != receipt.RawRecordID || recordManifestID != receipt.ManifestID ||
		recordContract != domainmemory.CommonRawContractVersion || recordType != opsAcceptanceRawSourceType ||
		recordIdentity != sourceIdentity || recordSourceID != string(receipt.UserMessageID) || recordParentID != "" ||
		recordThreadID != string(receipt.ThreadID) || recordOwner != receipt.OwnerID || recordScope != scope ||
		recordSensitivity != domainmemory.CommonRawPrivateSensitivity || recordRole != "user" ||
		recordContentType != "text/plain; charset=utf-8" || recordStorage != domainmemory.CommonRawStorageInline ||
		recordObjectRef != "" || recordHash != receipt.RawSHA256 || recordSize < 1 ||
		recordSize > domconv.AcceptedOPSInputMaxMessageBytes || int64(len(recordContent)) != recordSize ||
		domainmemory.SHA256Hex(recordContent) != receipt.RawSHA256 || recordAssetJSON != "[]" ||
		recordProvenance != opsAcceptanceRawProvenance || recordRights != opsAcceptanceRawRights ||
		recordLicense != opsAcceptanceRawLicense || recordOccurredAt.IsZero() || recordIngestedAt.IsZero() ||
		recordCreatedAt.IsZero() || !recordOccurredAt.Equal(receipt.AcceptedAt) ||
		!recordIngestedAt.Equal(receipt.AcceptedAt) || !recordCreatedAt.Equal(receipt.AcceptedAt) {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	if manifestContract != domainmemory.CommonRawContractVersion || manifestType != opsAcceptanceRawSourceType ||
		manifestIdentity != sourceIdentity || manifestHash != receipt.ManifestSHA256 ||
		manifestSchema != opsAcceptanceRawSchemaVersion || manifestConverter != opsAcceptanceRawConverter ||
		manifestOwner != receipt.OwnerID || manifestScope != scope || manifestSensitivity != domainmemory.CommonRawPrivateSensitivity ||
		manifestRights != opsAcceptanceRawRights || manifestLicense != opsAcceptanceRawLicense ||
		manifestProvenance != opsAcceptanceRawProvenance || manifestSourceCount != 1 || manifestAssetCount != 0 ||
		manifestAllowEmpty != 0 || manifestRequestID != receipt.RequestID || manifestActorID != receipt.ActorID ||
		manifestStatus != string(domainmemory.CommonRawStateCompleted) ||
		!manifestCreatedAt.Equal(receipt.AcceptedAt) || !manifestUpdatedAt.Equal(receipt.AcceptedAt) {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	var checkpoint struct {
		ManifestID  string                      `json:"manifest_id"`
		SourceCount int                         `json:"source_count"`
		AssetCount  int                         `json:"asset_count"`
		Status      domainmemory.CommonRawState `json:"status"`
	}
	if json.Unmarshal([]byte(checkpointJSON), &checkpoint) != nil ||
		checkpoint.ManifestID != receipt.ManifestID || checkpoint.SourceCount != 1 ||
		checkpoint.AssetCount != 0 || checkpoint.Status != domainmemory.CommonRawStateCompleted {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	var commonReceipt domainmemory.CommonRawIntakeReceipt
	if json.Unmarshal([]byte(commonReceiptJSON), &commonReceipt) != nil ||
		commonReceipt.RequestID != receipt.RequestID || commonReceipt.ManifestID != receipt.ManifestID ||
		commonReceipt.Status != domainmemory.CommonRawStateCompleted || commonReceipt.ManifestSHA256 != receipt.ManifestSHA256 ||
		commonReceipt.SourceCount != 1 || commonReceipt.AssetCount != 0 || commonReceipt.Checkpoint != "completed" ||
		commonReceipt.IdempotentReplay || !commonReceipt.CreatedAt.Equal(receipt.AcceptedAt) || len(commonReceipt.Records) != 1 {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	commonRecord := commonReceipt.Records[0]
	if commonRecord.RawRecordID != receipt.RawRecordID || commonRecord.SourceRecordID != string(receipt.UserMessageID) ||
		commonRecord.ContentSHA256 != receipt.RawSHA256 || commonRecord.ContentSize != recordSize ||
		commonRecord.StorageKind != domainmemory.CommonRawStorageInline || commonRecord.ObjectRef != "" ||
		len(commonRecord.AssetRefs) != 0 {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	// The internal OPS reader currently supports the initial completed manifest
	// with exactly one ingested event. Any later lifecycle event, including
	// forget/restrict, is unsupported and fails closed until its disclosure
	// semantics are specified by the CommonRaw owner.
	var stateCount int
	if err := query.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_state_event WHERE raw_record_id = ?`, receipt.RawRecordID).Scan(&stateCount); err != nil || stateCount != 1 {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	var stateID, stateType, stateHash, stateOwner, stateScope, stateRequest, stateActor, stateReason, statePayload string
	var stateCreatedAt time.Time
	if err := query.QueryRowContext(ctx, `
SELECT state_event_id, event_type, event_hash, owner_id, scope, request_id, actor_id, reason_code, payload_json, created_at
FROM l1_raw_state_event
WHERE raw_record_id = ?`, receipt.RawRecordID).Scan(
		&stateID, &stateType, &stateHash, &stateOwner, &stateScope, &stateRequest, &stateActor, &stateReason, &statePayload, &stateCreatedAt); err != nil {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	expectedStateID := domainmemory.DeterministicCommonRawStateEventID(receipt.RawRecordID, "ingested", receipt.RawSHA256)
	if stateID != expectedStateID || stateType != "ingested" || stateHash != receipt.RawSHA256 ||
		stateOwner != receipt.OwnerID || stateScope != scope || stateRequest != receipt.RequestID ||
		stateActor != receipt.ActorID || stateReason != "ingested" || statePayload != "{}" ||
		!stateCreatedAt.Equal(receipt.AcceptedAt) {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	message := string(recordContent)
	request := domconv.AcceptedOPSInputRequest{
		RequestID: receipt.RequestID, OwnerID: receipt.OwnerID, ActorID: receipt.ActorID,
		SessionID: receipt.SessionID, FirstThreadID: receipt.ThreadID, TaskID: receipt.TaskID, TurnID: receipt.TurnID,
		TraceID: receipt.TraceID, UserMessageID: receipt.UserMessageID, AgentMessageID: receipt.AgentMessageID,
		RawMessage: message, DeclaredOrigin: receipt.DeclaredOrigin,
	}
	payloadHash, err := domconv.AcceptedOPSInputPayloadSHA256(request)
	if err != nil || payloadHash != receipt.PayloadSHA256 {
		return "", domconv.ErrAcceptedOPSInputUnavailable
	}
	return message, nil
}

func acceptedOPSRawSourceIdentity(requestID string) string {
	return "accepted-ops:" + requestID
}

var _ domconv.AcceptedOPSInputStore = (*L1SQLiteStore)(nil)
