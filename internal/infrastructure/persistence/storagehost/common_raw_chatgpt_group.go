package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const (
	commonRawChatGPTImportOp    = "import_chatgpt_raw_batch"
	commonRawChatGPTEventOp     = "append_chatgpt_import_event"
	commonRawChatGPTStatusOp    = "get_chatgpt_import_status"
	commonRawChatGPTProgressOp  = "get_chatgpt_import_progress"
	commonRawChatGPTRetryOp     = "retry_chatgpt_import"
	commonRawChatGPTFinalizeOp  = "finalize_chatgpt_import"
	commonRawChatGPTReconcileOp = "reconcile_active_chatgpt_imports"
)

type commonRawChatGPTBatchPayload struct {
	RequestID string                             `json:"request_id"`
	Batch     domainmemory.ChatGPTRawImportBatch `json:"batch"`
	Apply     bool                               `json:"apply"`
}

type commonRawChatGPTEventPayload struct {
	RequestID      string                            `json:"request_id"`
	Binding        domainmemory.ChatGPTImportBinding `json:"binding"`
	Apply          bool                              `json:"apply"`
	State          domainmemory.ChatGPTImportState   `json:"state"`
	Counts         domainmemory.ChatGPTImportCounts  `json:"counts"`
	Warnings       []string                          `json:"warnings"`
	ErrorCode      string                            `json:"error_code"`
	FailureReason  string                            `json:"failure_reason"`
	AuditReference string                            `json:"audit_reference"`
}

type commonRawChatGPTExportPayload struct {
	RequestID string `json:"request_id"`
	ExportID  string `json:"export_id"`
}

type commonRawChatGPTFinalizePayload struct {
	RequestID string `json:"request_id"`
	ExportID  string `json:"export_id"`
	Apply     bool   `json:"apply"`
}

type commonRawChatGPTReconcilePayload struct{}

func registerCommonRawChatGPTOperations(handler *Handler, owner CommonRawGroupOwner, configuredUser string) error {
	registrations := []func() error{
		func() error {
			return registerCommonRawChatGPTMutation(handler, owner, commonRawChatGPTImportOp, l1sqlite.ChatGPTStorageHostImportBatch,
				func(p commonRawChatGPTBatchPayload) (l1sqlite.ChatGPTStorageHostMutation, bool) {
					return l1sqlite.ChatGPTStorageHostMutation{RequestID: p.RequestID, OwnerID: configuredUser, ActorID: configuredUser, Batch: &p.Batch, Apply: p.Apply}, validCommonRawChatGPTID(p.RequestID)
				}, func(r l1sqlite.ChatGPTStorageHostResult) (domainmemory.ChatGPTRawImportResult, bool) {
					if r.ImportBatch == nil {
						return domainmemory.ChatGPTRawImportResult{}, false
					}
					return *r.ImportBatch, validCommonRawChatGPTBatchResult(*r.ImportBatch)
				})
		},
		func() error {
			return registerCommonRawChatGPTMutation(handler, owner, commonRawChatGPTEventOp, l1sqlite.ChatGPTStorageHostAppendEvent,
				func(p commonRawChatGPTEventPayload) (l1sqlite.ChatGPTStorageHostMutation, bool) {
					input := domainmemory.ChatGPTImportEventInput{RequestID: p.RequestID, OwnerID: configuredUser, ActorID: configuredUser, Binding: p.Binding, Apply: p.Apply, State: p.State, Counts: p.Counts, Warnings: append([]string(nil), p.Warnings...), ErrorCode: p.ErrorCode, FailureReason: p.FailureReason, AuditReference: p.AuditReference}
					return l1sqlite.ChatGPTStorageHostMutation{Event: &input}, input.Validate() == nil
				}, func(r l1sqlite.ChatGPTStorageHostResult) (domainmemory.ChatGPTImportEvent, bool) {
					if r.Event == nil {
						return domainmemory.ChatGPTImportEvent{}, false
					}
					return *r.Event, r.Event.EventID != "" && !r.Event.CreatedAt.IsZero()
				})
		},
		func() error {
			return registerCommonRawChatGPTMutation(handler, owner, commonRawChatGPTRetryOp, l1sqlite.ChatGPTStorageHostRetry,
				func(p commonRawChatGPTExportPayload) (l1sqlite.ChatGPTStorageHostMutation, bool) {
					input := domainmemory.ChatGPTImportRetryInput{RequestID: p.RequestID, OwnerID: configuredUser, ActorID: configuredUser, ExportID: p.ExportID}
					return l1sqlite.ChatGPTStorageHostMutation{RequestID: input.RequestID, OwnerID: input.OwnerID, ActorID: input.ActorID, ExportID: input.ExportID}, input.Validate() == nil
				}, func(r l1sqlite.ChatGPTStorageHostResult) (domainmemory.ChatGPTImportRetryResult, bool) {
					if r.Retry == nil {
						return domainmemory.ChatGPTImportRetryResult{}, false
					}
					return *r.Retry, r.Retry.RequeuedCount >= 0 && r.Retry.MissingEvidenceCount >= 0
				})
		},
		func() error {
			return registerCommonRawChatGPTMutation(handler, owner, commonRawChatGPTFinalizeOp, l1sqlite.ChatGPTStorageHostFinalize,
				func(p commonRawChatGPTFinalizePayload) (l1sqlite.ChatGPTStorageHostMutation, bool) {
					input := domainmemory.ChatGPTImportFinalizeInput{RequestID: p.RequestID, OwnerID: configuredUser, ActorID: configuredUser, ExportID: p.ExportID, Apply: p.Apply}
					return l1sqlite.ChatGPTStorageHostMutation{Finalize: &input}, input.Validate() == nil
				}, func(r l1sqlite.ChatGPTStorageHostResult) (domainmemory.ChatGPTImportFinalizeResult, bool) {
					if r.Finalize == nil {
						return domainmemory.ChatGPTImportFinalizeResult{}, false
					}
					return *r.Finalize, r.Finalize.Status == domainmemory.ChatGPTImportFinalizeStatusCompleted
				})
		},
		func() error {
			return registerCommonRawChatGPTMutation(handler, owner, commonRawChatGPTReconcileOp, l1sqlite.ChatGPTStorageHostReconcile,
				func(commonRawChatGPTReconcilePayload) (l1sqlite.ChatGPTStorageHostMutation, bool) {
					return l1sqlite.ChatGPTStorageHostMutation{Reconcile: true}, true
				},
				func(r l1sqlite.ChatGPTStorageHostResult) (int, bool) {
					if r.Reconciled == nil {
						return 0, false
					}
					return *r.Reconciled, *r.Reconciled >= 0
				})
		},
		func() error { return registerCommonRawChatGPTStatus(handler, owner, configuredUser) },
		func() error { return registerCommonRawChatGPTProgress(handler, owner, configuredUser) },
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func registerCommonRawChatGPTMutation[P, R any](handler *Handler, owner CommonRawGroupOwner, op string, operation l1sqlite.ChatGPTStorageHostOperation, build func(P) (l1sqlite.ChatGPTStorageHostMutation, bool), extract func(l1sqlite.ChatGPTStorageHostResult) (R, bool)) error {
	return handler.RegisterRecoverable(GroupCommonRaw, op, func(ctx context.Context, metadata MutationMetadata) (any, error) {
		var payload P
		if decodeCommonRawChatGPTPayload(metadata.Payload, &payload) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "common raw ChatGPT request rejected"))
		}
		mutation, valid := build(payload)
		if !valid {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "common raw ChatGPT request rejected"))
		}
		ownerCtx, err := commonRawChatGPTOwnerContext(ctx, mutation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeUnauthorized, "common raw ChatGPT scope rejected"))
		}
		identity, err := newCommonRawChatGPTIdentity(metadata, op, operation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "common raw ChatGPT identity rejected"))
		}
		result, err := owner.ExecuteChatGPTStorageHostOperation(ownerCtx, identity, mutation)
		if err != nil {
			return nil, commonRawChatGPTStoreError(err)
		}
		value, ok := extract(result)
		if !ok || !boundedCommonRawChatGPTValue(value) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "common raw ChatGPT owner result is invalid")
		}
		return value, nil
	}, func(ctx context.Context, metadata MutationMetadata) (ReconcileDecision, error) {
		var payload P
		if decodeCommonRawChatGPTPayload(metadata.Payload, &payload) != nil {
			return UnknownOutcome(), nil
		}
		mutation, valid := build(payload)
		if !valid {
			return UnknownOutcome(), nil
		}
		ownerCtx, err := commonRawChatGPTOwnerContext(ctx, mutation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := newCommonRawChatGPTIdentity(metadata, op, operation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		state, result, err := owner.LookupChatGPTStorageHostOperationReceipt(ownerCtx, identity, mutation)
		if err != nil || state != l1sqlite.ChatGPTStorageHostReceiptCommitted {
			return UnknownOutcome(), nil
		}
		value, ok := extract(result)
		if !ok || !boundedCommonRawChatGPTValue(value) {
			return UnknownOutcome(), nil
		}
		return Committed(value), nil
	})
}

func registerCommonRawChatGPTStatus(handler *Handler, owner CommonRawGroupOwner, configuredUser string) error {
	return handler.Register(GroupCommonRaw, commonRawChatGPTStatusOp, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload commonRawChatGPTExportPayload
		if decodeCommonRawChatGPTPayload(raw, &payload) != nil || !validCommonRawChatGPTID(payload.RequestID) || !validCommonRawChatGPTID(payload.ExportID) {
			return nil, NewError(ErrorCodeSchemaRejected, "common raw ChatGPT status request rejected")
		}
		ownerCtx, err := commonRawOwnerContext(ctx, configuredUser, payload.RequestID)
		if err != nil {
			return nil, NewError(ErrorCodeUnauthorized, "common raw ChatGPT scope rejected")
		}
		result, err := owner.GetChatGPTImportStatus(ownerCtx, payload.RequestID, configuredUser, configuredUser, payload.ExportID)
		if err != nil || !validCommonRawChatGPTView(result, configuredUser, payload.ExportID) {
			return nil, commonRawChatGPTStoreError(err)
		}
		return result, nil
	})
}

func registerCommonRawChatGPTProgress(handler *Handler, owner CommonRawGroupOwner, configuredUser string) error {
	return handler.Register(GroupCommonRaw, commonRawChatGPTProgressOp, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload commonRawChatGPTExportPayload
		if decodeCommonRawChatGPTPayload(raw, &payload) != nil || !validCommonRawChatGPTID(payload.RequestID) || !validCommonRawChatGPTID(payload.ExportID) {
			return nil, NewError(ErrorCodeSchemaRejected, "common raw ChatGPT progress request rejected")
		}
		ownerCtx, err := commonRawOwnerContext(ctx, configuredUser, payload.RequestID)
		if err != nil {
			return nil, NewError(ErrorCodeUnauthorized, "common raw ChatGPT scope rejected")
		}
		result, err := owner.GetChatGPTImportProgress(ownerCtx, payload.RequestID, configuredUser, configuredUser, payload.ExportID)
		if err != nil || !validCommonRawChatGPTProgress(result, payload.RequestID, payload.ExportID) {
			return nil, commonRawChatGPTStoreError(err)
		}
		return result, nil
	})
}

func newCommonRawChatGPTIdentity(metadata MutationMetadata, op string, operation l1sqlite.ChatGPTStorageHostOperation) (l1sqlite.ChatGPTStorageHostOperationIdentity, error) {
	if metadata.OpID == "" || metadata.JournalGeneration <= 0 {
		return l1sqlite.ChatGPTStorageHostOperationIdentity{}, errors.New("incomplete ChatGPT storage-host identity")
	}
	return l1sqlite.NewChatGPTStorageHostOperationIdentity(metadata.OpID, operation, payloadHash(GroupCommonRaw, op, metadata.Payload), metadata.JournalGeneration)
}

func commonRawChatGPTOwnerContext(ctx context.Context, mutation l1sqlite.ChatGPTStorageHostMutation) (context.Context, error) {
	if mutation.Reconcile {
		return ctx, nil
	}
	requestID := mutation.RequestID
	ownerID := mutation.OwnerID
	if mutation.Event != nil {
		requestID = mutation.Event.RequestID
		ownerID = mutation.Event.OwnerID
	}
	if mutation.Finalize != nil {
		requestID = mutation.Finalize.RequestID
		ownerID = mutation.Finalize.OwnerID
	}
	return commonRawOwnerContext(ctx, ownerID, requestID)
}

func decodeCommonRawChatGPTPayload(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || len(raw) > maxCommonRawRequestBytes || !utf8.Valid(raw) {
		return errors.New("common raw ChatGPT payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return errors.New("common raw ChatGPT payload rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("common raw ChatGPT payload rejected")
	}
	return nil
}

func validCommonRawChatGPTID(value string) bool {
	return value != "" && len(value) <= commonRawMaxIDLength && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\x00")
}
func boundedCommonRawChatGPTValue(value any) bool {
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= maxCommonRawRequestBytes
}
func validCommonRawChatGPTBatchResult(v domainmemory.ChatGPTRawImportResult) bool {
	return v.Validated >= 0 && v.RawImported >= 0 && v.RawReplayed >= 0 && v.Projected >= 0 && v.Existing >= 0 && v.Queued >= 0 && len(v.RawRecordIDs) <= 100
}
func validCommonRawChatGPTView(v domainmemory.ChatGPTImportView, ownerID, exportID string) bool {
	if !validCommonRawChatGPTID(v.RequestID) || v.ExportID != exportID || v.Binding.ExportID != exportID || v.State.Validate() != nil || v.Binding.Validate() != nil || v.Counts.Validate() != nil || v.CreatedAt.IsZero() || !boundedCommonRawChatGPTValue(v) {
		return false
	}
	bindingSHA, err := domainmemory.DeterministicChatGPTImportBindingSHA256(ownerID, v.Binding)
	if err != nil || v.BindingSHA256 != bindingSHA {
		return false
	}
	importID := domainmemory.DeterministicChatGPTImportID(ownerID, bindingSHA)
	return v.ImportID == importID && v.EventID == domainmemory.DeterministicChatGPTImportEventID(importID, v.RequestID, v.State)
}
func validCommonRawChatGPTProgress(v domainmemory.ChatGPTImportProgress, requestID, exportID string) bool {
	return v.RequestID == requestID && v.ExportID == exportID && v.ImportID != "" && v.State.Validate() == nil && v.ExpectedRawCount >= 0 && v.RawCount >= 0 && v.JobCount >= 0 && boundedCommonRawChatGPTValue(v)
}
func commonRawChatGPTStoreError(err error) error {
	if err == nil {
		return NewError(ErrorCodeStoreUnavailable, "common raw ChatGPT owner result is invalid")
	}
	if errors.Is(err, l1sqlite.ErrChatGPTStorageHostConflict) {
		return ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "common raw ChatGPT operation conflicts with owner state"))
	}
	provedNotCommitted := errors.Is(err, l1sqlite.ErrChatGPTStorageHostNotCommitted)
	var mapped error
	var importErr *domainmemory.ChatGPTImportError
	if errors.As(err, &importErr) && importErr != nil {
		code := ErrorCodeStoreUnavailable
		switch importErr.Code {
		case domainmemory.ChatGPTImportErrorInvalid, domainmemory.ChatGPTImportErrorTooLarge, domainmemory.ChatGPTImportErrorArtifactInvalid:
			code = ErrorCodeSchemaRejected
		case domainmemory.ChatGPTImportErrorForbidden:
			code = ErrorCodeUnauthorized
		case domainmemory.ChatGPTImportErrorConflict, domainmemory.ChatGPTImportErrorSourceChanged:
			code = ErrorCodeDuplicateConflict
		}
		mapped = NewError(code, "chatgpt_import_code="+string(importErr.Code))
	} else {
		var rawErr *domainmemory.CommonRawError
		if errors.As(err, &rawErr) && rawErr != nil {
			mapped = commonRawStorageHostError(rawErr)
		} else {
			mapped = NewError(ErrorCodeStoreUnavailable, "common raw ChatGPT owner is unavailable")
		}
	}
	if provedNotCommitted {
		return ownerRolledBack(mapped)
	}
	return mapped
}

func commonRawChatGPTClientError(err error) error {
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr == nil {
		return err
	}
	if strings.HasPrefix(hostErr.Message, "common_raw_code=") {
		return commonRawClientError(err)
	}
	if !strings.HasPrefix(hostErr.Message, "chatgpt_import_code=") {
		return err
	}
	code := domainmemory.ChatGPTImportErrorCode(strings.TrimPrefix(hostErr.Message, "chatgpt_import_code="))
	switch code {
	case domainmemory.ChatGPTImportErrorInvalid, domainmemory.ChatGPTImportErrorTooLarge, domainmemory.ChatGPTImportErrorArtifactInvalid,
		domainmemory.ChatGPTImportErrorInternal, domainmemory.ChatGPTImportErrorForbidden, domainmemory.ChatGPTImportErrorNotFound,
		domainmemory.ChatGPTImportErrorConflict, domainmemory.ChatGPTImportErrorSourceChanged, domainmemory.ChatGPTImportErrorUnavailable,
		domainmemory.ChatGPTImportErrorBlocked:
		return domainmemory.NewChatGPTImportError(code, "storage host rejected ChatGPT import operation")
	default:
		return err
	}
}
