package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	filememory "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/memory"
)

const (
	GroupOperationMemory = "operation_memory"

	maxOperationMemoryInputBytes   = 1 << 20
	maxOperationMemoryPayloadBytes = (6 << 20) + 1024
	maxOperationMemoryResultBytes  = 8 << 20
	maxOperationMemoryRecentDays   = 31
)

type OperationMemoryGroupOwner interface {
	domainmemory.Store
	ReadStorageHostMemoryLongTerm() (string, error)
	ReadStorageHostMemoryToday() (string, error)
	ReadStorageHostMemoryRecentDailyNotes(days int) (string, error)
	ReadStorageHostMemoryContext() (string, error)
	ApplyStorageHostMemoryOperation(filememory.StorageHostMemoryOperation) (filememory.StorageHostMemoryReceipt, error)
	LookupStorageHostMemoryOperation(opID, operation, payloadHash string) (filememory.StorageHostMemoryLookup, error)
}

type operationMemoryEmptyPayload struct{}

type operationMemoryContentPayload struct {
	Content string `json:"content"`
}

type operationMemoryDatePayload struct {
	Date    string `json:"date"`
	Content string `json:"content"`
}

type operationMemoryRecentPayload struct {
	Days int `json:"days"`
}

type operationMemoryContentResult struct {
	Content string `json:"content"`
}

// RegisterOperationMemoryGroup exposes only the existing operation-memory
// Store methods. Mutation identity and recovery remain owned by FileStore.
func RegisterOperationMemoryGroup(h *Handler, store OperationMemoryGroupOwner) error {
	if h == nil || store == nil {
		return errors.New("storagehost: operation memory group needs a handler and an owner store")
	}
	if err := h.Register(GroupOperationMemory, "read_long_term", false, func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := decodeOperationMemoryPayload(raw, &operationMemoryEmptyPayload{}); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "operation memory read payload rejected")
		}
		return operationMemoryStoreResult(store.ReadStorageHostMemoryLongTerm())
	}); err != nil {
		return err
	}
	if err := h.RegisterRecoverable(GroupOperationMemory, "write_long_term", operationMemoryMutation(store, filememory.StorageHostMemoryWriteLongTerm), operationMemoryReconcile(store, filememory.StorageHostMemoryWriteLongTerm)); err != nil {
		return err
	}
	if err := h.Register(GroupOperationMemory, "read_today", false, func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := decodeOperationMemoryPayload(raw, &operationMemoryEmptyPayload{}); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "operation memory read payload rejected")
		}
		return operationMemoryStoreResult(store.ReadStorageHostMemoryToday())
	}); err != nil {
		return err
	}
	if err := h.RegisterRecoverable(GroupOperationMemory, "append_today", operationMemoryMutation(store, filememory.StorageHostMemoryAppendToday), operationMemoryReconcile(store, filememory.StorageHostMemoryAppendToday)); err != nil {
		return err
	}
	if err := h.Register(GroupOperationMemory, "recent_daily_notes", false, func(_ context.Context, raw json.RawMessage) (any, error) {
		var p operationMemoryRecentPayload
		if err := decodeOperationMemoryPayload(raw, &p); err != nil || p.Days < 0 || p.Days > maxOperationMemoryRecentDays {
			return nil, NewError(ErrorCodeSchemaRejected, "operation memory recent-notes request rejected")
		}
		return operationMemoryStoreResult(store.ReadStorageHostMemoryRecentDailyNotes(p.Days))
	}); err != nil {
		return err
	}
	if err := h.RegisterRecoverable(GroupOperationMemory, "save_daily_note_for_date", operationMemoryMutation(store, filememory.StorageHostMemorySaveForDate), operationMemoryReconcile(store, filememory.StorageHostMemorySaveForDate)); err != nil {
		return err
	}
	return h.Register(GroupOperationMemory, "memory_context", false, func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := decodeOperationMemoryPayload(raw, &operationMemoryEmptyPayload{}); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "operation memory context payload rejected")
		}
		return operationMemoryStoreResult(store.ReadStorageHostMemoryContext())
	})
}

func operationMemoryMutation(store OperationMemoryGroupOwner, operation string) OwnerMutationFunc {
	return func(_ context.Context, mutation MutationMetadata) (any, error) {
		var content operationMemoryContentPayload
		request := filememory.StorageHostMemoryOperation{OpID: mutation.OpID, Operation: operation, PayloadHash: payloadHash(GroupOperationMemory, operation, mutation.Payload)}
		if operation == filememory.StorageHostMemorySaveForDate {
			var save operationMemoryDatePayload
			if err := decodeOperationMemoryPayload(mutation.Payload, &save); err != nil || !canonicalMemoryDate(save.Date) || validateOperationMemoryContent(save.Content) != nil {
				return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "operation memory dated note rejected"))
			}
			request.Date = save.Date
			request.Content = save.Content
		} else {
			if err := decodeOperationMemoryPayload(mutation.Payload, &content); err != nil || validateOperationMemoryContent(content.Content) != nil {
				return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "operation memory content rejected"))
			}
			request.Content = content.Content
		}
		receipt, err := store.ApplyStorageHostMemoryOperation(request)
		if err != nil {
			if errors.Is(err, filememory.ErrStorageHostOperationConflict) {
				return nil, NewError(ErrorCodeDuplicateConflict, "operation memory op_id is already bound to another payload")
			}
			return nil, NewError(ErrorCodeStoreUnavailable, "operation memory write failed")
		}
		if !validOperationMemoryReceipt(receipt, request.OpID, operation, request.PayloadHash) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "operation memory owner receipt rejected")
		}
		return nil, nil
	}
}

func operationMemoryReconcile(store OperationMemoryGroupOwner, operation string) OwnerReconcileFunc {
	return func(_ context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		if !validOperationMemoryMutationPayload(operation, mutation.Payload) {
			return UnknownOutcome(), nil
		}
		hash := payloadHash(GroupOperationMemory, operation, mutation.Payload)
		lookup, err := store.LookupStorageHostMemoryOperation(mutation.OpID, operation, hash)
		if err != nil {
			return UnknownOutcome(), nil
		}
		switch lookup.Resolution {
		case filememory.StorageHostMemoryCommitted:
			if !validOperationMemoryReceipt(lookup.Receipt, mutation.OpID, operation, hash) {
				return UnknownOutcome(), nil
			}
			return Committed(nil), nil
		case filememory.StorageHostMemoryNotCommitted:
			return ConfirmedNotCommitted(), nil
		default:
			return UnknownOutcome(), nil
		}
	}
}

func validOperationMemoryMutationPayload(operation string, raw json.RawMessage) bool {
	if operation == filememory.StorageHostMemorySaveForDate {
		var request operationMemoryDatePayload
		return decodeOperationMemoryPayload(raw, &request) == nil && canonicalMemoryDate(request.Date) && validateOperationMemoryContent(request.Content) == nil
	}
	var request operationMemoryContentPayload
	return decodeOperationMemoryPayload(raw, &request) == nil && validateOperationMemoryContent(request.Content) == nil
}

func operationMemoryResult(content string) (any, error) {
	if !utf8.ValidString(content) || len(content) > maxOperationMemoryResultBytes {
		return nil, NewError(ErrorCodeStoreUnavailable, "operation memory result is invalid or exceeds size limit")
	}
	encoded, err := json.Marshal(operationMemoryContentResult{Content: content})
	if err != nil || len(encoded) > maxOperationMemoryResultBytes {
		return nil, NewError(ErrorCodeStoreUnavailable, "operation memory result exceeds size limit")
	}
	return operationMemoryContentResult{Content: content}, nil
}

func operationMemoryStoreResult(content string, err error) (any, error) {
	if err != nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "operation memory read failed")
	}
	return operationMemoryResult(content)
}

func validateOperationMemoryContent(content string) error {
	if !utf8.ValidString(content) || len(content) > maxOperationMemoryInputBytes {
		return errors.New("operation memory content rejected")
	}
	return nil
}

func canonicalMemoryDate(raw string) bool {
	if len(raw) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", raw)
	return err == nil && parsed.Year() > 0 && parsed.Format("2006-01-02") == raw
}

func decodeOperationMemoryPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !utf8.Valid(trimmed) || trimmed[0] != '{' || len(trimmed) > maxOperationMemoryPayloadBytes {
		return errors.New("operation memory payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("operation memory payload rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("operation memory payload trailing data rejected")
	}
	return nil
}

func validOperationMemoryReceipt(receipt filememory.StorageHostMemoryReceipt, opID, operation, payloadHash string) bool {
	return receipt.OpID == opID && receipt.Operation == operation && receipt.PayloadHash == payloadHash && bytes.Equal(receipt.ResultJSON, []byte("null"))
}
