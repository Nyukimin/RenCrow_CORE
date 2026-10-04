package storagehost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const (
	GroupCommonRaw       = "common_raw"
	commonRawIntakeOp    = "intake"
	commonRawMaxIDLength = 256
	// The intake request bound conservatively limits the number of referenced
	// asset IDs to maxRequestBytes (each JSON reference occupies at least one
	// byte). Each receipt reference repeats both an asset ID and media type;
	// JSON can expand each metadata byte to at most six escaped bytes. The
	// record term bounds source IDs and the fixed envelope margin bounds the
	// manifest and request identity fields. This is deliberately derived from
	// the owner contract rather than a lower operational guess.
	maxCommonRawIntakeResultBytes int64 = int64(maxRequestBytes)*(12*int64(domainmemory.CommonRawMaxMetadataSize)+512) + int64(domainmemory.CommonRawMaxRecords)*(6*int64(domainmemory.CommonRawMaxMetadataSize)+1024) + 1<<20
	maxCommonRawIntakeResultPages       = int((maxCommonRawIntakeResultBytes + int64(resultPageBytes) - 1) / int64(resultPageBytes))
)

type CommonRawGroupOwner interface {
	IntakeCommonRawStorageHost(context.Context, l1sqlite.CommonRawStorageHostOperationIdentity, string, string, string, domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error)
	LookupCommonRawStorageHostOperationReceipt(context.Context, l1sqlite.CommonRawStorageHostOperationIdentity, string, string, string, domainmemory.CommonRawIntakeRequest) (l1sqlite.CommonRawStorageHostReceiptState, domainmemory.CommonRawIntakeReceipt, error)
	ExecuteChatGPTStorageHostOperation(context.Context, l1sqlite.ChatGPTStorageHostOperationIdentity, l1sqlite.ChatGPTStorageHostMutation) (l1sqlite.ChatGPTStorageHostResult, error)
	LookupChatGPTStorageHostOperationReceipt(context.Context, l1sqlite.ChatGPTStorageHostOperationIdentity, l1sqlite.ChatGPTStorageHostMutation) (l1sqlite.ChatGPTStorageHostReceiptState, l1sqlite.ChatGPTStorageHostResult, error)
	GetChatGPTImportStatus(context.Context, string, string, string, string) (domainmemory.ChatGPTImportView, error)
	GetChatGPTImportProgress(context.Context, string, string, string, string) (domainmemory.ChatGPTImportProgress, error)
}

type commonRawManifestPayload struct {
	ContractVersion  string `json:"contract_version"`
	SourceType       string `json:"source_type"`
	SourceIdentity   string `json:"source_identity"`
	ManifestSHA256   string `json:"manifest_sha256"`
	SourceCount      int    `json:"source_count"`
	AssetCount       int    `json:"asset_count"`
	SchemaVersion    string `json:"schema_version"`
	ConverterVersion string `json:"converter_version"`
	Sensitivity      string `json:"sensitivity"`
	Rights           string `json:"rights"`
	License          string `json:"license"`
	Provenance       string `json:"provenance"`
	AllowEmpty       bool   `json:"allow_empty"`
}

type commonRawRecordPayload struct {
	SourceRecordID string    `json:"source_record_id"`
	ParentID       string    `json:"parent_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	Sensitivity    string    `json:"sensitivity"`
	Role           string    `json:"role"`
	ContentType    string    `json:"content_type"`
	OccurredAt     time.Time `json:"occurred_at"`
	Content        []byte    `json:"content"`
	ContentSHA256  string    `json:"content_sha256"`
	Provenance     string    `json:"provenance,omitempty"`
	Rights         string    `json:"rights,omitempty"`
	License        string    `json:"license,omitempty"`
	AssetRefs      []string  `json:"asset_refs,omitempty"`
}

type commonRawAssetPayload struct {
	SourceAssetID string `json:"source_asset_id"`
	MediaType     string `json:"media_type"`
	Content       []byte `json:"content"`
	ContentSHA256 string `json:"content_sha256"`
	Provenance    string `json:"provenance,omitempty"`
	Rights        string `json:"rights,omitempty"`
	License       string `json:"license,omitempty"`
}

type commonRawIntakePayload struct {
	RequestID string                   `json:"request_id"`
	Manifest  commonRawManifestPayload `json:"manifest"`
	Records   []commonRawRecordPayload `json:"records"`
	Assets    []commonRawAssetPayload  `json:"assets"`
}

type commonRawIntakeResult struct {
	Receipt domainmemory.CommonRawIntakeReceipt `json:"receipt"`
}

// RegisterCommonRawGroup exposes only immutable raw intake. The physical
// object root is configured on the owner and is never represented in a
// request or operation DTO.
func RegisterCommonRawGroup(handler *Handler, owner CommonRawGroupOwner, configuredUser string) error {
	if handler == nil || owner == nil || isNilCommonRawOwner(owner) || strings.TrimSpace(configuredUser) == "" {
		return errors.New("storagehost: common raw group needs a handler, owner and configured user")
	}
	configuredUser = strings.TrimSpace(configuredUser)
	if err := handler.RegisterRecoverable(GroupCommonRaw, commonRawIntakeOp, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeCommonRawIntakePayload(mutation.Payload)
		if err != nil || !validCommonRawIntakePayload(payload) {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "common raw intake request rejected"))
		}
		ownerCtx, err := commonRawOwnerContext(ctx, configuredUser, payload.RequestID)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeUnauthorized, "common raw owner scope rejected"))
		}
		input := payload.domainInput()
		identity, err := commonRawStorageHostIdentity(mutation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "common raw operation identity rejected"))
		}
		receipt, err := owner.IntakeCommonRawStorageHost(ownerCtx, identity, payload.RequestID, configuredUser, configuredUser, input)
		if err != nil {
			state, recovered, lookupErr := owner.LookupCommonRawStorageHostOperationReceipt(ownerCtx, identity, payload.RequestID, configuredUser, configuredUser, input)
			if lookupErr == nil && state == l1sqlite.CommonRawStorageHostReceiptCommitted && validCommonRawReceipt(recovered, payload.RequestID) {
				return commonRawIntakeResult{Receipt: recovered}, nil
			}
			if lookupErr == nil && state == l1sqlite.CommonRawStorageHostReceiptAbsent {
				return nil, ownerRolledBack(commonRawStorageHostError(err))
			}
			return nil, commonRawStorageHostError(err)
		}
		state, stored, verifyErr := owner.LookupCommonRawStorageHostOperationReceipt(ownerCtx, identity, payload.RequestID, configuredUser, configuredUser, input)
		if verifyErr != nil || state != l1sqlite.CommonRawStorageHostReceiptCommitted || !sameCommonRawIntakeReceipt(receipt, stored) || !validCommonRawReceipt(stored, payload.RequestID) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "common raw owner returned an invalid intake receipt")
		}
		return commonRawIntakeResult{Receipt: stored}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeCommonRawIntakePayload(mutation.Payload)
		if err != nil || !validCommonRawIntakePayload(payload) {
			return UnknownOutcome(), nil
		}
		ownerCtx, err := commonRawOwnerContext(ctx, configuredUser, payload.RequestID)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := commonRawStorageHostIdentity(mutation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		state, receipt, err := owner.LookupCommonRawStorageHostOperationReceipt(ownerCtx, identity, payload.RequestID, configuredUser, configuredUser, payload.domainInput())
		if err != nil {
			return UnknownOutcome(), nil
		}
		switch state {
		case l1sqlite.CommonRawStorageHostReceiptAbsent:
			return ConfirmedNotCommitted(), nil
		case l1sqlite.CommonRawStorageHostReceiptCommitted:
			if !validCommonRawReceipt(receipt, payload.RequestID) {
				return UnknownOutcome(), nil
			}
			return Committed(commonRawIntakeResult{Receipt: receipt}), nil
		default:
			return UnknownOutcome(), nil
		}
	}); err != nil {
		return err
	}
	return registerCommonRawChatGPTOperations(handler, owner, configuredUser)
}

func commonRawStorageHostIdentity(mutation MutationMetadata) (l1sqlite.CommonRawStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.RequestGeneration <= 0 || mutation.JournalGeneration <= 0 {
		return l1sqlite.CommonRawStorageHostOperationIdentity{}, errors.New("incomplete common raw storage-host operation identity")
	}
	return l1sqlite.NewCommonRawStorageHostOperationIdentity(mutation.OpID, payloadHash(GroupCommonRaw, commonRawIntakeOp, mutation.Payload), mutation.JournalGeneration)
}

// CommonRawIntakeStoreClient preserves the application method's private scope
// checks while sending only requestID and typed raw input to the host. The
// authenticated owner/actor and raw object root never come from the payload.
type CommonRawIntakeStoreClient struct{ client *Client }

func NewCommonRawIntakeStoreClient(client *Client) *CommonRawIntakeStoreClient {
	return &CommonRawIntakeStoreClient{client: client}
}

func (s *CommonRawIntakeStoreClient) IntakeCommonRaw(ctx context.Context, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error) {
	if s == nil || s.client == nil {
		return domainmemory.CommonRawIntakeReceipt{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if !commonRawClientScopeMatches(ctx, requestID, ownerID, actorID) {
		return domainmemory.CommonRawIntakeReceipt{}, NewError(ErrorCodeUnauthorized, "common raw intake requires matching authenticated user scope")
	}
	payload := commonRawIntakePayloadFromDomain(requestID, input)
	var result commonRawIntakeResult
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawIntakeOp, payload, &result); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, commonRawClientError(err)
	}
	if !validCommonRawReceipt(result.Receipt, requestID) {
		return domainmemory.CommonRawIntakeReceipt{}, NewError(ErrorCodeStoreUnavailable, "common raw intake result is invalid")
	}
	return result.Receipt, nil
}

func (s *CommonRawIntakeStoreClient) ImportChatGPTRawBatch(ctx context.Context, requestID, ownerID, actorID string, batch domainmemory.ChatGPTRawImportBatch, apply bool) (domainmemory.ChatGPTRawImportResult, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTRawImportResult{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if !validCommonRawChatGPTID(requestID) {
		return domainmemory.ChatGPTRawImportResult{}, NewError(ErrorCodeSchemaRejected, "ChatGPT Raw import request is invalid")
	}
	if !commonRawClientScopeMatches(ctx, requestID, ownerID, actorID) {
		return domainmemory.ChatGPTRawImportResult{}, NewError(ErrorCodeUnauthorized, "ChatGPT Raw import requires matching authenticated user scope")
	}
	payload := commonRawChatGPTBatchPayload{RequestID: requestID, Batch: batch, Apply: apply}
	var result domainmemory.ChatGPTRawImportResult
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTImportOp, payload, &result); err != nil {
		return domainmemory.ChatGPTRawImportResult{}, commonRawChatGPTClientError(err)
	}
	if !validCommonRawChatGPTBatchResult(result) || result.BatchIndex != batch.BatchIndex || result.BatchCount != batch.BatchCount || result.ExternalManifestSHA256 != batch.ManifestSHA256 || result.ArtifactSHA256 != batch.ArtifactSHA256 {
		return domainmemory.ChatGPTRawImportResult{}, NewError(ErrorCodeOutcomeUnknown, "ChatGPT Raw import result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) AppendChatGPTImportEvent(ctx context.Context, input domainmemory.ChatGPTImportEventInput) (domainmemory.ChatGPTImportEvent, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTImportEvent{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if input.Validate() != nil {
		return domainmemory.ChatGPTImportEvent{}, NewError(ErrorCodeSchemaRejected, "ChatGPT import event is invalid")
	}
	if !commonRawClientScopeMatches(ctx, input.RequestID, input.OwnerID, input.ActorID) {
		return domainmemory.ChatGPTImportEvent{}, NewError(ErrorCodeUnauthorized, "ChatGPT import event requires matching authenticated user scope")
	}
	payload := commonRawChatGPTEventPayload{RequestID: input.RequestID, Binding: input.Binding, Apply: input.Apply, State: input.State, Counts: input.Counts, Warnings: append([]string(nil), input.Warnings...), ErrorCode: input.ErrorCode, FailureReason: input.FailureReason, AuditReference: input.AuditReference}
	var result domainmemory.ChatGPTImportEvent
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTEventOp, payload, &result); err != nil {
		return domainmemory.ChatGPTImportEvent{}, commonRawChatGPTClientError(err)
	}
	if result.EventID == "" || result.ImportID == "" || result.RequestID != input.RequestID || result.OwnerID != input.OwnerID || result.ActorID != input.ActorID || result.State != input.State || result.Apply != input.Apply || result.CreatedAt.IsZero() || !boundedCommonRawChatGPTValue(result) {
		return domainmemory.ChatGPTImportEvent{}, NewError(ErrorCodeOutcomeUnknown, "ChatGPT import event result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) GetChatGPTImportStatus(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportView, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTImportView{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if !validCommonRawChatGPTID(requestID) || !validCommonRawChatGPTID(exportID) {
		return domainmemory.ChatGPTImportView{}, NewError(ErrorCodeSchemaRejected, "ChatGPT import status request is invalid")
	}
	if !commonRawClientScopeMatches(ctx, requestID, ownerID, actorID) {
		return domainmemory.ChatGPTImportView{}, NewError(ErrorCodeUnauthorized, "ChatGPT import status requires matching authenticated user scope")
	}
	var result domainmemory.ChatGPTImportView
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTStatusOp, commonRawChatGPTExportPayload{RequestID: requestID, ExportID: exportID}, &result); err != nil {
		return domainmemory.ChatGPTImportView{}, commonRawChatGPTClientError(err)
	}
	if !validCommonRawChatGPTView(result, ownerID, exportID) {
		return domainmemory.ChatGPTImportView{}, NewError(ErrorCodeStoreUnavailable, "ChatGPT import status result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) GetChatGPTImportProgress(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportProgress, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTImportProgress{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if !validCommonRawChatGPTID(requestID) || !validCommonRawChatGPTID(exportID) {
		return domainmemory.ChatGPTImportProgress{}, NewError(ErrorCodeSchemaRejected, "ChatGPT import progress request is invalid")
	}
	if !commonRawClientScopeMatches(ctx, requestID, ownerID, actorID) {
		return domainmemory.ChatGPTImportProgress{}, NewError(ErrorCodeUnauthorized, "ChatGPT import progress requires matching authenticated user scope")
	}
	var result domainmemory.ChatGPTImportProgress
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTProgressOp, commonRawChatGPTExportPayload{RequestID: requestID, ExportID: exportID}, &result); err != nil {
		return domainmemory.ChatGPTImportProgress{}, commonRawChatGPTClientError(err)
	}
	if !validCommonRawChatGPTProgress(result, requestID, exportID) {
		return domainmemory.ChatGPTImportProgress{}, NewError(ErrorCodeStoreUnavailable, "ChatGPT import progress result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) RetryFailedChatGPTImportJobsForExport(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportRetryResult, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTImportRetryResult{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	input := domainmemory.ChatGPTImportRetryInput{RequestID: requestID, OwnerID: ownerID, ActorID: actorID, ExportID: exportID}
	if input.Validate() != nil {
		return domainmemory.ChatGPTImportRetryResult{}, NewError(ErrorCodeSchemaRejected, "ChatGPT import retry request is invalid")
	}
	if !commonRawClientScopeMatches(ctx, requestID, ownerID, actorID) {
		return domainmemory.ChatGPTImportRetryResult{}, NewError(ErrorCodeUnauthorized, "ChatGPT import retry requires matching authenticated user scope")
	}
	var result domainmemory.ChatGPTImportRetryResult
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTRetryOp, commonRawChatGPTExportPayload{RequestID: requestID, ExportID: exportID}, &result); err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, commonRawChatGPTClientError(err)
	}
	if result.RequestID != requestID || result.ExportID != exportID || result.RequeuedCount < 0 || result.MissingEvidenceCount < 0 || !boundedCommonRawChatGPTValue(result) {
		return domainmemory.ChatGPTImportRetryResult{}, NewError(ErrorCodeOutcomeUnknown, "ChatGPT import retry result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) FinalizeChatGPTImport(ctx context.Context, input domainmemory.ChatGPTImportFinalizeInput) (domainmemory.ChatGPTImportFinalizeResult, error) {
	if s == nil || s.client == nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if input.Validate() != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, NewError(ErrorCodeSchemaRejected, "ChatGPT import finalization request is invalid")
	}
	if !commonRawClientScopeMatches(ctx, input.RequestID, input.OwnerID, input.ActorID) {
		return domainmemory.ChatGPTImportFinalizeResult{}, NewError(ErrorCodeUnauthorized, "ChatGPT import finalization requires matching authenticated user scope")
	}
	payload := commonRawChatGPTFinalizePayload{RequestID: input.RequestID, ExportID: input.ExportID, Apply: input.Apply}
	var result domainmemory.ChatGPTImportFinalizeResult
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTFinalizeOp, payload, &result); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, commonRawChatGPTClientError(err)
	}
	if result.RequestID != input.RequestID || result.ExportID != input.ExportID || result.Apply != input.Apply || result.Status != domainmemory.ChatGPTImportFinalizeStatusCompleted || !boundedCommonRawChatGPTValue(result) {
		return domainmemory.ChatGPTImportFinalizeResult{}, NewError(ErrorCodeOutcomeUnknown, "ChatGPT import finalization result is invalid")
	}
	return result, nil
}

func (s *CommonRawIntakeStoreClient) ReconcileActiveChatGPTImports(ctx context.Context) (int, error) {
	if s == nil || s.client == nil {
		return 0, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	var result int
	if err := s.client.Call(ctx, GroupCommonRaw, commonRawChatGPTReconcileOp, commonRawChatGPTReconcilePayload{}, &result); err != nil {
		return 0, commonRawChatGPTClientError(err)
	}
	if result < 0 {
		return 0, NewError(ErrorCodeOutcomeUnknown, "ChatGPT import reconciliation result is invalid")
	}
	return result, nil
}

func commonRawClientError(err error) error {
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr == nil || !strings.HasPrefix(hostErr.Message, "common_raw_code=") {
		return err
	}
	code := domainmemory.CommonRawErrorCode(strings.TrimPrefix(hostErr.Message, "common_raw_code="))
	switch code {
	case domainmemory.CommonRawErrorInvalid, domainmemory.CommonRawErrorForbidden, domainmemory.CommonRawErrorConflict,
		domainmemory.CommonRawErrorSourceChanged, domainmemory.CommonRawErrorSchema, domainmemory.CommonRawErrorRoot,
		domainmemory.CommonRawErrorObject, domainmemory.CommonRawErrorUnavailable:
		return domainmemory.NewCommonRawError(code, "storage host rejected common raw intake")
	default:
		return err
	}
}

func commonRawClientScopeMatches(ctx context.Context, requestID, ownerID, actorID string) bool {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	return ok && scope.Validate() == nil && scope.RequestID == strings.TrimSpace(requestID) && scope.ActorKind == domaintool.ActorKindUser && scope.ActorID == strings.TrimSpace(actorID) && scope.AuthenticatedUserID == strings.TrimSpace(ownerID) && scope.Allows(domaintool.DataScopeUser)
}

func commonRawOwnerContext(ctx context.Context, configuredUser, requestID string) (context.Context, error) {
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, configuredUser, configuredUser, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return domaintool.WithToolExecutionScope(ctx, scope), nil
}

func isNilCommonRawOwner(owner CommonRawGroupOwner) bool {
	v := reflect.ValueOf(owner)
	return (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface || v.Kind() == reflect.Map || v.Kind() == reflect.Func || v.Kind() == reflect.Slice) && v.IsNil()
}

func decodeCommonRawIntakePayload(raw json.RawMessage) (commonRawIntakePayload, error) {
	if !utf8.Valid(raw) {
		return commonRawIntakePayload{}, errors.New("common raw payload must be valid UTF-8")
	}
	var payload commonRawIntakePayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return commonRawIntakePayload{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return commonRawIntakePayload{}, errors.New("common raw payload has trailing data")
	}
	return payload, nil
}

func validCommonRawIntakePayload(payload commonRawIntakePayload) bool {
	return strings.TrimSpace(payload.RequestID) != "" && len(payload.RequestID) <= commonRawMaxIDLength
}

func commonRawIntakePayloadFromDomain(requestID string, input domainmemory.CommonRawIntakeRequest) commonRawIntakePayload {
	m := input.Manifest
	payload := commonRawIntakePayload{RequestID: strings.TrimSpace(requestID), Manifest: commonRawManifestPayload{
		ContractVersion: m.ContractVersion, SourceType: m.SourceType, SourceIdentity: m.SourceIdentity, ManifestSHA256: m.ManifestSHA256,
		SourceCount: m.SourceCount, AssetCount: m.AssetCount, SchemaVersion: m.SchemaVersion, ConverterVersion: m.ConverterVersion,
		Sensitivity: m.Sensitivity, Rights: m.Rights, License: m.License, Provenance: m.Provenance, AllowEmpty: m.AllowEmpty,
	}, Records: make([]commonRawRecordPayload, 0, len(input.Records)), Assets: make([]commonRawAssetPayload, 0, len(input.Assets))}
	for _, record := range input.Records {
		payload.Records = append(payload.Records, commonRawRecordPayload{SourceRecordID: record.SourceRecordID, ParentID: record.ParentID, ThreadID: record.ThreadID,
			Sensitivity: record.Sensitivity, Role: record.Role, ContentType: record.ContentType, OccurredAt: record.OccurredAt, Content: append([]byte(nil), record.Content...),
			ContentSHA256: record.ContentSHA256, Provenance: record.Provenance, Rights: record.Rights, License: record.License, AssetRefs: append([]string(nil), record.AssetRefs...)})
	}
	for _, asset := range input.Assets {
		payload.Assets = append(payload.Assets, commonRawAssetPayload{SourceAssetID: asset.SourceAssetID, MediaType: asset.MediaType, Content: append([]byte(nil), asset.Content...),
			ContentSHA256: asset.ContentSHA256, Provenance: asset.Provenance, Rights: asset.Rights, License: asset.License})
	}
	return payload
}

func (p commonRawIntakePayload) domainInput() domainmemory.CommonRawIntakeRequest {
	manifest := domainmemory.CommonRawManifest{ContractVersion: p.Manifest.ContractVersion, SourceType: p.Manifest.SourceType, SourceIdentity: p.Manifest.SourceIdentity,
		ManifestSHA256: p.Manifest.ManifestSHA256, SourceCount: p.Manifest.SourceCount, AssetCount: p.Manifest.AssetCount, SchemaVersion: p.Manifest.SchemaVersion,
		ConverterVersion: p.Manifest.ConverterVersion, Sensitivity: p.Manifest.Sensitivity, Rights: p.Manifest.Rights, License: p.Manifest.License,
		Provenance: p.Manifest.Provenance, AllowEmpty: p.Manifest.AllowEmpty}
	input := domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: make([]domainmemory.CommonRawRecord, 0, len(p.Records)), Assets: make([]domainmemory.CommonRawAsset, 0, len(p.Assets))}
	for _, record := range p.Records {
		input.Records = append(input.Records, domainmemory.CommonRawRecord{SourceRecordID: record.SourceRecordID, ParentID: record.ParentID, ThreadID: record.ThreadID,
			Sensitivity: record.Sensitivity, Role: record.Role, ContentType: record.ContentType, OccurredAt: record.OccurredAt, Content: append([]byte(nil), record.Content...),
			ContentSHA256: record.ContentSHA256, Provenance: record.Provenance, Rights: record.Rights, License: record.License, AssetRefs: append([]string(nil), record.AssetRefs...)})
	}
	for _, asset := range p.Assets {
		input.Assets = append(input.Assets, domainmemory.CommonRawAsset{SourceAssetID: asset.SourceAssetID, MediaType: asset.MediaType, Content: append([]byte(nil), asset.Content...),
			ContentSHA256: asset.ContentSHA256, Provenance: asset.Provenance, Rights: asset.Rights, License: asset.License})
	}
	return input
}

func validCommonRawReceipt(receipt domainmemory.CommonRawIntakeReceipt, requestID string) bool {
	if receipt.RequestID != strings.TrimSpace(requestID) || receipt.ManifestID == "" || receipt.Status != domainmemory.CommonRawStateCompleted || receipt.Checkpoint != "completed" || receipt.SourceCount != len(receipt.Records) || receipt.SourceCount < 0 || receipt.AssetCount < 0 || len(receipt.Records) > domainmemory.CommonRawMaxRecords || receipt.AssetCount > domainmemory.CommonRawMaxAssets || receipt.CreatedAt.IsZero() || len(receipt.ManifestSHA256) != 64 {
		return false
	}
	if _, err := hex.DecodeString(receipt.ManifestSHA256); err != nil || receipt.ManifestSHA256 != strings.ToLower(receipt.ManifestSHA256) {
		return false
	}
	for _, record := range receipt.Records {
		if record.RawRecordID == "" || record.SourceRecordID == "" || len(record.ContentSHA256) != 64 || record.ContentSize < 0 || (record.StorageKind != domainmemory.CommonRawStorageInline && record.StorageKind != domainmemory.CommonRawStorageObject) {
			return false
		}
		if _, err := hex.DecodeString(record.ContentSHA256); err != nil || record.ContentSHA256 != strings.ToLower(record.ContentSHA256) {
			return false
		}
		for _, ref := range record.AssetRefs {
			if ref.SourceAssetID == "" || len(ref.SHA256) != 64 || ref.Size < 0 || !safeCommonRawObjectRef(ref.ObjectRef) {
				return false
			}
			if _, err := hex.DecodeString(ref.SHA256); err != nil || ref.SHA256 != strings.ToLower(ref.SHA256) {
				return false
			}
		}
		if record.StorageKind == domainmemory.CommonRawStorageObject && !safeCommonRawObjectRef(record.ObjectRef) {
			return false
		}
	}
	return true
}

func sameCommonRawIntakeReceipt(left, right domainmemory.CommonRawIntakeReceipt) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func safeCommonRawObjectRef(ref string) bool {
	if strings.TrimSpace(ref) != ref || strings.HasPrefix(ref, "/") || strings.Contains(ref, `\`) || strings.Contains(ref, "..") {
		return false
	}
	parts := strings.Split(ref, "/")
	return len(parts) == 4 && parts[0] == "objects" && parts[1] == "sha256" && len(parts[2]) == 2 && len(parts[3]) == 64 && parts[2] == parts[3][:2]
}

func commonRawStorageHostError(err error) error {
	var rawErr *domainmemory.CommonRawError
	if errors.As(err, &rawErr) && rawErr != nil {
		code := ErrorCodeStoreUnavailable
		switch rawErr.Code {
		case domainmemory.CommonRawErrorForbidden:
			code = ErrorCodeUnauthorized
		case domainmemory.CommonRawErrorInvalid, domainmemory.CommonRawErrorSchema:
			code = ErrorCodeSchemaRejected
		case domainmemory.CommonRawErrorConflict, domainmemory.CommonRawErrorSourceChanged:
			code = ErrorCodeDuplicateConflict
		}
		return NewError(code, "common_raw_code="+string(rawErr.Code))
	}
	return NewError(ErrorCodeStoreUnavailable, "common raw owner is unavailable")
}
