package l1sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

var chatGPTStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var ErrChatGPTStorageHostConflict = errors.New("ChatGPT storage-host operation conflict")
var ErrChatGPTStorageHostNotCommitted = errors.New("ChatGPT storage-host operation was not begun")

type ChatGPTStorageHostOperation string

const (
	ChatGPTStorageHostImportBatch ChatGPTStorageHostOperation = "import_chatgpt_raw_batch"
	ChatGPTStorageHostAppendEvent ChatGPTStorageHostOperation = "append_chatgpt_import_event"
	ChatGPTStorageHostRetry       ChatGPTStorageHostOperation = "retry_chatgpt_import"
	ChatGPTStorageHostFinalize    ChatGPTStorageHostOperation = "finalize_chatgpt_import"
	ChatGPTStorageHostReconcile   ChatGPTStorageHostOperation = "reconcile_active_chatgpt_imports"
)

type ChatGPTStorageHostOperationIdentity struct {
	OpID             string
	Operation        ChatGPTStorageHostOperation
	PayloadSHA256    string
	WriterGeneration int64
}

func NewChatGPTStorageHostOperationIdentity(opID string, operation ChatGPTStorageHostOperation, payloadSHA256 string, generation int64) (ChatGPTStorageHostOperationIdentity, error) {
	v := ChatGPTStorageHostOperationIdentity{OpID: opID, Operation: operation, PayloadSHA256: payloadSHA256, WriterGeneration: generation}
	if err := v.validate(); err != nil {
		return ChatGPTStorageHostOperationIdentity{}, err
	}
	return v, nil
}

func (v ChatGPTStorageHostOperationIdentity) validate() error {
	switch v.Operation {
	case ChatGPTStorageHostImportBatch, ChatGPTStorageHostAppendEvent, ChatGPTStorageHostRetry, ChatGPTStorageHostFinalize, ChatGPTStorageHostReconcile:
	default:
		return errors.New("ChatGPT storage-host operation is invalid")
	}
	if !chatGPTStorageHostOpIDPattern.MatchString(v.OpID) || v.WriterGeneration <= 0 || len(v.PayloadSHA256) != sha256.Size*2 || v.PayloadSHA256 != strings.ToLower(v.PayloadSHA256) {
		return errors.New("ChatGPT storage-host operation identity is invalid")
	}
	if _, err := hex.DecodeString(v.PayloadSHA256); err != nil {
		return errors.New("ChatGPT storage-host payload hash is invalid")
	}
	return nil
}

type ChatGPTStorageHostMutation struct {
	RequestID string
	OwnerID   string
	ActorID   string
	Batch     *ChatGPTRawImportBatch
	Apply     bool
	Event     *domainmemory.ChatGPTImportEventInput
	ExportID  string
	Finalize  *domainmemory.ChatGPTImportFinalizeInput
	Reconcile bool
}

type ChatGPTStorageHostResult struct {
	ImportBatch *ChatGPTRawImportResult                   `json:"import_batch,omitempty"`
	Event       *domainmemory.ChatGPTImportEvent          `json:"event,omitempty"`
	Retry       *domainmemory.ChatGPTImportRetryResult    `json:"retry,omitempty"`
	Finalize    *domainmemory.ChatGPTImportFinalizeResult `json:"finalize,omitempty"`
	Reconciled  *int                                      `json:"reconciled,omitempty"`
}

type ChatGPTStorageHostReceiptState uint8

const (
	ChatGPTStorageHostReceiptAbsent ChatGPTStorageHostReceiptState = iota
	ChatGPTStorageHostReceiptPending
	ChatGPTStorageHostReceiptCommitted
)

func (s *L1SQLiteStore) ExecuteChatGPTStorageHostOperation(ctx context.Context, identity ChatGPTStorageHostOperationIdentity, mutation ChatGPTStorageHostMutation) (ChatGPTStorageHostResult, error) {
	if s == nil || s.db == nil {
		return ChatGPTStorageHostResult{}, chatGPTImportReconcileUnavailable("store is unavailable")
	}
	if err := identity.validate(); err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	if err := validateChatGPTStorageHostMutation(identity.Operation, mutation); err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	if err := s.preflightChatGPTStorageHostMutation(ctx, identity.Operation, mutation); err != nil {
		return ChatGPTStorageHostResult{}, errors.Join(ErrChatGPTStorageHostNotCommitted, err)
	}
	state, prior, err := s.LookupChatGPTStorageHostOperationReceipt(ctx, identity, mutation)
	if err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	if state == ChatGPTStorageHostReceiptCommitted {
		return prior, nil
	}
	if state == ChatGPTStorageHostReceiptPending {
		return ChatGPTStorageHostResult{}, fmt.Errorf("ChatGPT storage-host outcome is unresolved")
	}
	if err := s.beginChatGPTStorageHostOperation(ctx, identity); err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	result, err := s.executeChatGPTStorageHostMutation(ctx, identity.Operation, mutation)
	if err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	effectJSON, err := s.captureChatGPTStorageHostEffect(ctx, identity.Operation, mutation, result)
	if err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	if err := s.commitChatGPTStorageHostOperation(ctx, identity, result, effectJSON); err != nil {
		return ChatGPTStorageHostResult{}, err
	}
	return result, nil
}

func (s *L1SQLiteStore) LookupChatGPTStorageHostOperationReceipt(ctx context.Context, identity ChatGPTStorageHostOperationIdentity, mutation ChatGPTStorageHostMutation) (ChatGPTStorageHostReceiptState, ChatGPTStorageHostResult, error) {
	if s == nil || s.db == nil {
		return ChatGPTStorageHostReceiptAbsent, ChatGPTStorageHostResult{}, chatGPTImportReconcileUnavailable("store is unavailable")
	}
	if err := identity.validate(); err != nil {
		return ChatGPTStorageHostReceiptAbsent, ChatGPTStorageHostResult{}, err
	}
	if err := validateChatGPTStorageHostMutation(identity.Operation, mutation); err != nil {
		return ChatGPTStorageHostReceiptAbsent, ChatGPTStorageHostResult{}, err
	}
	if err := s.ensureChatGPTStorageHostReceiptSchema(ctx); err != nil {
		return ChatGPTStorageHostReceiptAbsent, ChatGPTStorageHostResult{}, err
	}
	row, found, err := readChatGPTStorageHostReceipt(ctx, s.db, identity.OpID)
	if err != nil || !found {
		return ChatGPTStorageHostReceiptAbsent, ChatGPTStorageHostResult{}, err
	}
	if row.operation != identity.Operation || row.payloadSHA256 != identity.PayloadSHA256 || row.writerGeneration != identity.WriterGeneration {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, fmt.Errorf("%w: op_id %q", ErrChatGPTStorageHostConflict, identity.OpID)
	}
	if row.status == "pending" {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, nil
	}
	if row.status != "committed" {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, errors.New("ChatGPT storage-host receipt status is invalid")
	}
	resultHash := sha256.Sum256([]byte(row.resultJSON))
	effectHash := sha256.Sum256([]byte(row.effectJSON))
	if row.resultSHA256 != hex.EncodeToString(resultHash[:]) || row.effectSHA256 != hex.EncodeToString(effectHash[:]) || row.proofSHA256 != chatGPTStorageHostProofSHA(row.operation, row.payloadSHA256, row.writerGeneration, row.resultSHA256, row.effectSHA256) {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, errors.New("ChatGPT storage-host receipt proof is invalid")
	}
	var result ChatGPTStorageHostResult
	decoder := json.NewDecoder(bytes.NewBufferString(row.resultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || !jsonDecoderAtEOF(decoder) || !validChatGPTStorageHostResult(identity.Operation, mutation, result) {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, errors.New("ChatGPT storage-host result is invalid")
	}
	if err := s.verifyChatGPTStorageHostEffect(ctx, identity.Operation, mutation, result, []byte(row.effectJSON)); err != nil {
		return ChatGPTStorageHostReceiptPending, ChatGPTStorageHostResult{}, err
	}
	return ChatGPTStorageHostReceiptCommitted, result, nil
}

func (s *L1SQLiteStore) preflightChatGPTStorageHostMutation(ctx context.Context, operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation) error {
	switch operation {
	case ChatGPTStorageHostImportBatch:
		if err := validateCommonRawOwnerScope(ctx, mutation.RequestID, mutation.OwnerID, mutation.ActorID); err != nil {
			return err
		}
		_, err := prepareChatGPTRawPlan(mutation.OwnerID, *mutation.Batch)
		return err
	case ChatGPTStorageHostAppendEvent:
		return validateCommonRawOwnerScope(ctx, mutation.Event.RequestID, mutation.Event.OwnerID, mutation.Event.ActorID)
	case ChatGPTStorageHostRetry:
		return validateCommonRawOwnerScope(ctx, mutation.RequestID, mutation.OwnerID, mutation.ActorID)
	case ChatGPTStorageHostFinalize:
		return validateCommonRawOwnerScope(ctx, mutation.Finalize.RequestID, mutation.Finalize.OwnerID, mutation.Finalize.ActorID)
	case ChatGPTStorageHostReconcile:
		return nil
	default:
		return errors.New("unsupported ChatGPT storage-host operation")
	}
}

func (s *L1SQLiteStore) executeChatGPTStorageHostMutation(ctx context.Context, operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation) (ChatGPTStorageHostResult, error) {
	switch operation {
	case ChatGPTStorageHostImportBatch:
		result, err := s.ImportChatGPTRawBatch(ctx, mutation.RequestID, mutation.OwnerID, mutation.ActorID, *mutation.Batch, mutation.Apply)
		return ChatGPTStorageHostResult{ImportBatch: &result}, err
	case ChatGPTStorageHostAppendEvent:
		result, err := s.AppendChatGPTImportEvent(ctx, *mutation.Event)
		return ChatGPTStorageHostResult{Event: &result}, err
	case ChatGPTStorageHostRetry:
		result, err := s.RetryFailedChatGPTImportJobsForExport(ctx, mutation.RequestID, mutation.OwnerID, mutation.ActorID, mutation.ExportID)
		return ChatGPTStorageHostResult{Retry: &result}, err
	case ChatGPTStorageHostFinalize:
		result, err := s.FinalizeChatGPTImport(ctx, *mutation.Finalize)
		return ChatGPTStorageHostResult{Finalize: &result}, err
	case ChatGPTStorageHostReconcile:
		count, err := s.ReconcileActiveChatGPTImports(ctx)
		return ChatGPTStorageHostResult{Reconciled: &count}, err
	default:
		return ChatGPTStorageHostResult{}, errors.New("unsupported ChatGPT storage-host operation")
	}
}

func validateChatGPTStorageHostMutation(operation ChatGPTStorageHostOperation, m ChatGPTStorageHostMutation) error {
	count := 0
	for _, present := range []bool{m.Batch != nil, m.Event != nil, m.Finalize != nil, m.ExportID != "", m.Reconcile} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("ChatGPT storage-host mutation must contain exactly one command")
	}
	switch operation {
	case ChatGPTStorageHostImportBatch:
		if m.Batch == nil || strings.TrimSpace(m.RequestID) == "" || strings.TrimSpace(m.OwnerID) == "" || strings.TrimSpace(m.ActorID) == "" {
			return errors.New("ChatGPT raw batch command is invalid")
		}
	case ChatGPTStorageHostAppendEvent:
		if m.Event == nil || m.Event.Validate() != nil {
			return errors.New("ChatGPT event command is invalid")
		}
	case ChatGPTStorageHostRetry:
		if (&domainmemory.ChatGPTImportRetryInput{RequestID: m.RequestID, OwnerID: m.OwnerID, ActorID: m.ActorID, ExportID: m.ExportID}).Validate() != nil {
			return errors.New("ChatGPT retry command is invalid")
		}
	case ChatGPTStorageHostFinalize:
		if m.Finalize == nil || m.Finalize.Validate() != nil {
			return errors.New("ChatGPT finalize command is invalid")
		}
	case ChatGPTStorageHostReconcile:
		if !m.Reconcile {
			return errors.New("ChatGPT reconciliation command is invalid")
		}
	default:
		return errors.New("unsupported ChatGPT storage-host operation")
	}
	return nil
}

func validChatGPTStorageHostResult(operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation, result ChatGPTStorageHostResult) bool {
	count := 0
	for _, present := range []bool{result.ImportBatch != nil, result.Event != nil, result.Retry != nil, result.Finalize != nil, result.Reconciled != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false
	}
	switch operation {
	case ChatGPTStorageHostImportBatch:
		return result.ImportBatch != nil && result.ImportBatch.BatchIndex == mutation.Batch.BatchIndex && result.ImportBatch.BatchCount == mutation.Batch.BatchCount && result.ImportBatch.ExternalManifestSHA256 == mutation.Batch.ManifestSHA256
	case ChatGPTStorageHostAppendEvent:
		return result.Event != nil && result.Event.RequestID == mutation.Event.RequestID && result.Event.OwnerID == mutation.Event.OwnerID && result.Event.ActorID == mutation.Event.ActorID && result.Event.State == mutation.Event.State
	case ChatGPTStorageHostRetry:
		return result.Retry != nil && result.Retry.RequestID == mutation.RequestID && result.Retry.ExportID == mutation.ExportID && result.Retry.RequeuedCount >= 0 && result.Retry.MissingEvidenceCount >= 0
	case ChatGPTStorageHostFinalize:
		return result.Finalize != nil && result.Finalize.RequestID == mutation.Finalize.RequestID && result.Finalize.ExportID == mutation.Finalize.ExportID && result.Finalize.Apply == mutation.Finalize.Apply
	case ChatGPTStorageHostReconcile:
		return result.Reconciled != nil && *result.Reconciled >= 0
	default:
		return false
	}
}

func (s *L1SQLiteStore) ensureChatGPTStorageHostReceiptSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS l1_chatgpt_storagehost_operation_receipt (
		op_id TEXT PRIMARY KEY NOT NULL, operation TEXT NOT NULL, payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','committed')),
		result_json TEXT NOT NULL DEFAULT '', result_sha256 TEXT NOT NULL DEFAULT '',
		effect_json TEXT NOT NULL DEFAULT '', effect_sha256 TEXT NOT NULL DEFAULT '', proof_sha256 TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create ChatGPT storage-host receipt schema: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) beginChatGPTStorageHostOperation(ctx context.Context, identity ChatGPTStorageHostOperationIdentity) error {
	if err := s.ensureChatGPTStorageHostReceiptSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO l1_chatgpt_storagehost_operation_receipt
		(op_id, operation, payload_sha256, writer_generation, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?) ON CONFLICT(op_id) DO NOTHING`, identity.OpID, string(identity.Operation), identity.PayloadSHA256, identity.WriterGeneration, now, now)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != 1 {
		return fmt.Errorf("%w: op_id %q", ErrChatGPTStorageHostConflict, identity.OpID)
	}
	return nil
}

func (s *L1SQLiteStore) commitChatGPTStorageHostOperation(ctx context.Context, identity ChatGPTStorageHostOperationIdentity, result ChatGPTStorageHostResult, effectJSON []byte) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	resultHash := sha256.Sum256(resultJSON)
	effectHash := sha256.Sum256(effectJSON)
	resultSHA, effectSHA := hex.EncodeToString(resultHash[:]), hex.EncodeToString(effectHash[:])
	proof := chatGPTStorageHostProofSHA(identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, resultSHA, effectSHA)
	updated, err := s.db.ExecContext(ctx, `UPDATE l1_chatgpt_storagehost_operation_receipt SET status='committed', result_json=?, result_sha256=?, effect_json=?, effect_sha256=?, proof_sha256=?, updated_at=?
		WHERE op_id=? AND operation=? AND payload_sha256=? AND writer_generation=? AND status='pending'`, string(resultJSON), resultSHA, string(effectJSON), effectSHA, proof, time.Now().UTC(), identity.OpID, string(identity.Operation), identity.PayloadSHA256, identity.WriterGeneration)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("ChatGPT storage-host receipt could not be committed")
	}
	return nil
}

type chatGPTStorageHostReceipt struct {
	operation                                                                              ChatGPTStorageHostOperation
	payloadSHA256, status, resultJSON, resultSHA256, effectJSON, effectSHA256, proofSHA256 string
	writerGeneration                                                                       int64
}

func readChatGPTStorageHostReceipt(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (chatGPTStorageHostReceipt, bool, error) {
	var v chatGPTStorageHostReceipt
	var createdAt, updatedAt time.Time
	err := q.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, status, result_json, result_sha256, effect_json, effect_sha256, proof_sha256, created_at, updated_at FROM l1_chatgpt_storagehost_operation_receipt WHERE op_id=?`, opID).Scan(&v.operation, &v.payloadSHA256, &v.writerGeneration, &v.status, &v.resultJSON, &v.resultSHA256, &v.effectJSON, &v.effectSHA256, &v.proofSHA256, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if createdAt.IsZero() || updatedAt.Before(createdAt) {
		return v, false, errors.New("ChatGPT storage-host receipt timestamp is invalid")
	}
	return v, true, nil
}

func chatGPTStorageHostProofSHA(operation ChatGPTStorageHostOperation, payloadSHA string, generation int64, resultSHA, effectSHA string) string {
	h := sha256.New()
	for _, value := range []string{string(operation), payloadSHA, fmt.Sprint(generation), resultSHA, effectSHA} {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type chatGPTStorageHostAuditEffect struct {
	ID, EventType, Namespace, PayloadJSON, Source string
}

type chatGPTStorageHostEffect struct {
	Result ChatGPTStorageHostResult          `json:"result"`
	Audit  *chatGPTStorageHostAuditEffect    `json:"audit,omitempty"`
	Events []domainmemory.ChatGPTImportEvent `json:"events,omitempty"`
}

func (s *L1SQLiteStore) captureChatGPTStorageHostEffect(ctx context.Context, operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation, result ChatGPTStorageHostResult) ([]byte, error) {
	effect := chatGPTStorageHostEffect{Result: result}
	switch operation {
	case ChatGPTStorageHostImportBatch:
		if err := s.verifyChatGPTRawStorageHostResult(ctx, mutation, *result.ImportBatch); err != nil {
			return nil, err
		}
	case ChatGPTStorageHostAppendEvent:
		stored, err := queryChatGPTImportEvent(ctx, s.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id=?`, result.Event.EventID)
		if err != nil || !reflectChatGPTEventEqual(stored, *result.Event) {
			return nil, errors.New("ChatGPT ledger effect is missing or altered")
		}
	case ChatGPTStorageHostRetry:
		if result.Retry.RequeuedCount > 0 || result.Retry.MissingEvidenceCount > 0 {
			audit, err := s.latestChatGPTStorageHostAudit(ctx, chatGPTMachineRetryAuditEventType, mutation.OwnerID, mutation.ExportID, result.Retry.RequeuedCount, result.Retry.MissingEvidenceCount)
			if err != nil {
				return nil, err
			}
			effect.Audit = &audit
		}
	case ChatGPTStorageHostFinalize:
		if mutation.Finalize.Apply {
			if err := s.verifyChatGPTFinalizeStorageHostResult(ctx, *mutation.Finalize, *result.Finalize); err != nil {
				return nil, err
			}
		}
	case ChatGPTStorageHostReconcile:
		events, err := s.latestInterruptedChatGPTEvents(ctx, *result.Reconciled)
		if err != nil {
			return nil, err
		}
		effect.Events = events
	}
	return json.Marshal(effect)
}

func (s *L1SQLiteStore) verifyChatGPTStorageHostEffect(ctx context.Context, operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation, result ChatGPTStorageHostResult, effectJSON []byte) error {
	var effect chatGPTStorageHostEffect
	decoder := json.NewDecoder(bytes.NewReader(effectJSON))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&effect) != nil || !jsonDecoderAtEOF(decoder) || !validChatGPTStorageHostResult(operation, mutation, effect.Result) || !chatGPTStorageHostResultsEqual(effect.Result, result) {
		return errors.New("ChatGPT storage-host effect proof is invalid")
	}
	switch operation {
	case ChatGPTStorageHostImportBatch:
		if !mutation.Apply {
			recomputed, err := s.ImportChatGPTRawBatch(ctx, mutation.RequestID, mutation.OwnerID, mutation.ActorID, *mutation.Batch, false)
			if err != nil || !chatGPTStorageHostResultsEqual(ChatGPTStorageHostResult{ImportBatch: &recomputed}, result) {
				return errors.New("ChatGPT Raw dry-run effect result is altered")
			}
			return nil
		}
		return s.verifyChatGPTRawStorageHostResult(ctx, mutation, resultValue(result.ImportBatch))
	case ChatGPTStorageHostAppendEvent:
		stored, err := queryChatGPTImportEvent(ctx, s.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id=?`, result.Event.EventID)
		if err != nil || !reflectChatGPTEventEqual(stored, *result.Event) {
			return errors.New("ChatGPT ledger effect is missing or altered")
		}
	case ChatGPTStorageHostRetry:
		if (result.Retry.RequeuedCount > 0 || result.Retry.MissingEvidenceCount > 0) != (effect.Audit != nil) {
			return errors.New("ChatGPT retry audit effect proof is incomplete")
		}
		if effect.Audit != nil {
			var eventType, namespace, payloadJSON, source string
			if err := s.db.QueryRowContext(ctx, `SELECT event_type, namespace, payload_json, source FROM l1_event_log WHERE id=?`, effect.Audit.ID).Scan(&eventType, &namespace, &payloadJSON, &source); err != nil || eventType != effect.Audit.EventType || namespace != effect.Audit.Namespace || payloadJSON != effect.Audit.PayloadJSON || source != effect.Audit.Source {
				return errors.New("ChatGPT retry audit effect is missing or altered")
			}
		}
	case ChatGPTStorageHostFinalize:
		if mutation.Finalize.Apply {
			return s.verifyChatGPTFinalizeStorageHostResult(ctx, *mutation.Finalize, *result.Finalize)
		}
		recomputed, err := s.FinalizeChatGPTImport(ctx, *mutation.Finalize)
		if err != nil || !chatGPTStorageHostResultsEqual(ChatGPTStorageHostResult{Finalize: &recomputed}, result) {
			return errors.New("ChatGPT finalize dry-run effect result is altered")
		}
	case ChatGPTStorageHostReconcile:
		if len(effect.Events) != *result.Reconciled {
			return errors.New("ChatGPT reconciliation effect proof is incomplete")
		}
		for _, expected := range effect.Events {
			stored, err := queryChatGPTImportEvent(ctx, s.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE event_id=?`, expected.EventID)
			if err != nil || !reflectChatGPTEventEqual(stored, expected) {
				return errors.New("ChatGPT reconciliation effect is missing or altered")
			}
		}
	}
	return nil
}

func jsonDecoderAtEOF(decoder *json.Decoder) bool {
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func (s *L1SQLiteStore) verifyChatGPTRawStorageHostResult(ctx context.Context, mutation ChatGPTStorageHostMutation, result ChatGPTRawImportResult) error {
	if !mutation.Apply {
		return nil
	}
	plan, err := prepareChatGPTRawPlan(mutation.OwnerID, *mutation.Batch)
	if err != nil {
		return err
	}
	receipt, found, err := s.findCommonRawManifestReplay(ctx, mutation.OwnerID, plan.Prepared)
	if err != nil || !found {
		return errors.New("ChatGPT Raw effect is missing")
	}
	receipt.IdempotentReplay = result.RawReceipt.IdempotentReplay
	left, _ := json.Marshal(receipt)
	right, _ := json.Marshal(result.RawReceipt)
	if !bytes.Equal(left, right) {
		return errors.New("ChatGPT Raw effect is altered")
	}
	if _, err := s.readChatGPTRawProjectionRecords(ctx, mutation.OwnerID, plan, receipt); err != nil {
		return err
	}
	return nil
}

func (s *L1SQLiteStore) verifyChatGPTFinalizeStorageHostResult(ctx context.Context, input domainmemory.ChatGPTImportFinalizeInput, result domainmemory.ChatGPTImportFinalizeResult) error {
	receiptID := chatGPTMachineReceiptID(input.OwnerID, input.ExportID, true)
	var ownerID, actorID, exportID, payloadHash, resultJSON string
	var apply int
	if err := s.db.QueryRowContext(ctx, `SELECT owner_id, actor_id, export_id, apply, payload_hash, result_json FROM l1_chatgpt_import_finalize_receipt WHERE receipt_id=?`, receiptID).Scan(&ownerID, &actorID, &exportID, &apply, &payloadHash, &resultJSON); err != nil {
		return errors.New("ChatGPT finalize effect is missing")
	}
	if ownerID != input.OwnerID || actorID != input.ActorID || exportID != input.ExportID || apply != 1 || payloadHash != chatGPTMachinePayloadHash(input.OwnerID, input.ActorID, input.ExportID, true) {
		return errors.New("ChatGPT finalize effect is altered")
	}
	var stored domainmemory.ChatGPTImportFinalizeResult
	if json.Unmarshal([]byte(resultJSON), &stored) != nil {
		return errors.New("ChatGPT finalize effect is invalid")
	}
	result.IdempotentReplay = false
	stored.IdempotentReplay = false
	left, _ := json.Marshal(stored)
	right, _ := json.Marshal(result)
	if !bytes.Equal(left, right) {
		return errors.New("ChatGPT finalize effect result is altered")
	}
	return nil
}

func (s *L1SQLiteStore) latestChatGPTStorageHostAudit(ctx context.Context, eventType, ownerID, exportID string, requeued, missing int) (chatGPTStorageHostAuditEffect, error) {
	var audit chatGPTStorageHostAuditEffect
	err := s.db.QueryRowContext(ctx, `SELECT id, event_type, namespace, payload_json, source FROM l1_event_log WHERE event_type=? AND namespace=? ORDER BY rowid DESC LIMIT 1`, eventType, "user:"+ownerID).Scan(&audit.ID, &audit.EventType, &audit.Namespace, &audit.PayloadJSON, &audit.Source)
	if err != nil {
		return audit, errors.New("ChatGPT retry audit effect is missing")
	}
	var payload struct {
		ExportID string `json:"export_id"`
		Requeued int    `json:"requeued_count"`
		Missing  int    `json:"missing_evidence_count"`
	}
	if json.Unmarshal([]byte(audit.PayloadJSON), &payload) != nil || payload.ExportID != exportID || payload.Requeued != requeued || payload.Missing != missing || audit.Source != "chatgpt_import_machine" {
		return audit, errors.New("ChatGPT retry audit effect is altered")
	}
	return audit, nil
}

func (s *L1SQLiteStore) latestInterruptedChatGPTEvents(ctx context.Context, count int) ([]domainmemory.ChatGPTImportEvent, error) {
	if count == 0 {
		return []domainmemory.ChatGPTImportEvent{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rowid FROM l1_chatgpt_import_event WHERE error_code=? AND failure_reason=? ORDER BY rowid DESC LIMIT ?`, chatGPTImportInterruptedErrorCode, chatGPTImportInterruptedFailureReason, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, count)
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			return nil, errors.New("ChatGPT reconciliation effect cannot be read")
		}
		ids = append(ids, id)
	}
	if len(ids) != count {
		return nil, errors.New("ChatGPT reconciliation effect is incomplete")
	}
	events := make([]domainmemory.ChatGPTImportEvent, 0, count)
	for i := len(ids) - 1; i >= 0; i-- {
		event, err := queryChatGPTImportEvent(ctx, s.db, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE rowid=?`, ids[i])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func chatGPTStorageHostResultsEqual(left, right ChatGPTStorageHostResult) bool {
	a, e1 := json.Marshal(left)
	b, e2 := json.Marshal(right)
	return e1 == nil && e2 == nil && bytes.Equal(a, b)
}
func reflectChatGPTEventEqual(left, right domainmemory.ChatGPTImportEvent) bool {
	a, e1 := json.Marshal(left)
	b, e2 := json.Marshal(right)
	return e1 == nil && e2 == nil && bytes.Equal(a, b)
}
func resultValue[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
