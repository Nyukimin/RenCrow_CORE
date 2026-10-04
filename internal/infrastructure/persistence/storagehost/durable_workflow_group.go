package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	appdurable "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domaindurable "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	persistdurable "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupDurableStoreWorkflow = "durable_store_workflow"

	durableWorkflowOpFindByDedupeKey     = "find_by_dedupe_key"
	durableWorkflowOpFindByActionID      = "find_by_action_id"
	durableWorkflowOpFindByRequirementID = "find_by_requirement_id"
	durableWorkflowOpSaveWithReceipt     = "save_with_receipt"

	durableWorkflowMaxPayload = 2 << 20
	durableWorkflowMaxText    = 64 << 10
	durableWorkflowMaxList    = 256
)

var durableWorkflowHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DurableStoreWorkflowGroupOwner is exactly the existing application Store
// contract. Storage-host recovery is a required owner capability kept outside
// that consumer-facing four-method surface.
type DurableStoreWorkflowGroupOwner interface {
	appdurable.Store
}

type durableStoreWorkflowRecoveryOwner interface {
	SaveWithReceiptForStorageHostOperation(context.Context, persistdurable.DurableWorkflowStorageHostOperationIdentity, *domaindurable.WorkflowResult, domaindurable.RequestReceipt) error
	LookupStorageHostOperationReceipt(context.Context, persistdurable.DurableWorkflowStorageHostOperationIdentity) (json.RawMessage, bool, error)
}

type durableWorkflowDedupePayload struct {
	DedupeKey string `json:"dedupe_key"`
}

type durableWorkflowActionPayload struct {
	ActionID modulecore.ActionID `json:"action_id"`
}

type durableWorkflowRequirementPayload struct {
	RequirementID string `json:"requirement_id"`
}

type durableWorkflowSavePayload struct {
	Result  *domaindurable.WorkflowResult `json:"result"`
	Receipt domaindurable.RequestReceipt  `json:"receipt"`
}

type durableWorkflowResultResponse struct {
	Found  bool                          `json:"found"`
	Result *domaindurable.WorkflowResult `json:"result,omitempty"`
}

type durableWorkflowReceiptResponse struct {
	Found   bool                          `json:"found"`
	Receipt *domaindurable.RequestReceipt `json:"receipt,omitempty"`
}

// RegisterDurableStoreWorkflowGroup installs only the four methods of the
// canonical Store contract. The write requires an owner receipt in the same
// SQLite transaction; owners without that hook fail registration closed.
func RegisterDurableStoreWorkflowGroup(handler *Handler, owner DurableStoreWorkflowGroupOwner) error {
	if handler == nil || isNilDurableWorkflowValue(owner) {
		return errors.New("storagehost: durable store workflow group needs a handler and an owner store")
	}
	recoveryOwner, ok := owner.(durableStoreWorkflowRecoveryOwner)
	if !ok || isNilDurableWorkflowValue(recoveryOwner) {
		return errors.New("storagehost: durable store workflow owner lacks atomic receipt reconciliation")
	}
	if err := handler.Register(GroupDurableStoreWorkflow, durableWorkflowOpFindByDedupeKey, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload durableWorkflowDedupePayload
		if decodeDurableWorkflowPayload(raw, &payload) != nil || !validDurableWorkflowKey(payload.DedupeKey) {
			return nil, durableWorkflowSchemaError("dedupe lookup")
		}
		result, err := owner.FindByDedupeKey(ctx, payload.DedupeKey)
		if err != nil {
			return nil, durableWorkflowReadError("dedupe lookup")
		}
		return durableWorkflowResultForKey(result, payload.DedupeKey, false)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupDurableStoreWorkflow, durableWorkflowOpFindByActionID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload durableWorkflowActionPayload
		if decodeDurableWorkflowPayload(raw, &payload) != nil || !validDurableWorkflowActionID(payload.ActionID) {
			return nil, durableWorkflowSchemaError("action receipt lookup")
		}
		receipt, err := owner.FindByActionID(ctx, payload.ActionID)
		if err != nil {
			return nil, durableWorkflowReadError("action receipt lookup")
		}
		if receipt == nil {
			return durableWorkflowReceiptResponse{Found: false}, nil
		}
		if !validDurableWorkflowReceipt(*receipt) || receipt.ActionID != payload.ActionID {
			return nil, durableWorkflowReadError("action receipt lookup")
		}
		response := durableWorkflowReceiptResponse{Found: true, Receipt: receipt}
		if !boundedDurableWorkflowValue(response) {
			return nil, durableWorkflowReadError("action receipt lookup")
		}
		return response, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupDurableStoreWorkflow, durableWorkflowOpFindByRequirementID, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload durableWorkflowRequirementPayload
		if decodeDurableWorkflowPayload(raw, &payload) != nil || !validDurableWorkflowKey(payload.RequirementID) {
			return nil, durableWorkflowSchemaError("requirement lookup")
		}
		result, err := owner.FindByRequirementID(ctx, payload.RequirementID)
		if err != nil {
			return nil, durableWorkflowReadError("requirement lookup")
		}
		return durableWorkflowResultForKey(result, payload.RequirementID, true)
	}); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupDurableStoreWorkflow, durableWorkflowOpSaveWithReceipt, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeDurableWorkflowSave(mutation.Payload)
		if err != nil {
			return nil, ownerRolledBack(durableWorkflowSchemaError("save"))
		}
		identity := durableWorkflowOperationIdentity(mutation, payload)
		if err := recoveryOwner.SaveWithReceiptForStorageHostOperation(ctx, identity, payload.Result, payload.Receipt); err != nil {
			if errors.Is(err, appdurable.ErrRequestConflict) || errors.Is(err, persistdurable.ErrDurableWorkflowStorageHostOperationConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "durable workflow request conflicts with owner state"))
			}
			if errors.Is(err, persistdurable.ErrDurableWorkflowStorageHostRequestConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "durable workflow request conflicts with owner state"))
			}
			return nil, NewError(ErrorCodeStoreUnavailable, "durable workflow owner write failed")
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeDurableWorkflowSave(mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		raw, found, err := recoveryOwner.LookupStorageHostOperationReceipt(ctx, durableWorkflowOperationIdentity(mutation, payload))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return UnknownOutcome(), nil
		}
		return Committed(nil), nil
	})
}

func durableWorkflowResultForKey(result *domaindurable.WorkflowResult, key string, requirement bool) (any, error) {
	if result == nil {
		return durableWorkflowResultResponse{Found: false}, nil
	}
	if !validDurableWorkflowResult(*result) {
		return nil, durableWorkflowReadError("workflow lookup")
	}
	if requirement {
		if result.Requirement.RequirementID != key {
			return nil, durableWorkflowReadError("workflow lookup")
		}
	} else if result.Requirement.DedupeKey != key {
		return nil, durableWorkflowReadError("workflow lookup")
	}
	response := durableWorkflowResultResponse{Found: true, Result: result}
	if !boundedDurableWorkflowValue(response) {
		return nil, durableWorkflowReadError("workflow lookup")
	}
	return response, nil
}

func decodeDurableWorkflowSave(raw json.RawMessage) (durableWorkflowSavePayload, error) {
	var payload durableWorkflowSavePayload
	if err := decodeDurableWorkflowPayload(raw, &payload); err != nil {
		return payload, err
	}
	if !validDurableWorkflowReceipt(payload.Receipt) {
		return payload, errors.New("durable workflow receipt rejected")
	}
	if payload.Result == nil {
		return payload, nil
	}
	if !validDurableWorkflowResult(*payload.Result) || payload.Result.Requirement.RequirementID != payload.Receipt.RequirementID ||
		payload.Result.Requirement.ActionID != payload.Receipt.ActionID || payload.Result.Requirement.UserScope != payload.Receipt.UserScope ||
		domaindurable.HashStorageRequirement(payload.Result.Requirement) != payload.Receipt.PayloadHash {
		return payload, errors.New("durable workflow result and receipt binding rejected")
	}
	return payload, nil
}

func decodeDurableWorkflowPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > durableWorkflowMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("durable workflow payload is not a bounded object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("durable workflow payload schema rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("durable workflow payload has trailing data")
	}
	return nil
}

func validDurableWorkflowResult(result domaindurable.WorkflowResult) bool {
	requirement := result.Requirement
	if !validDurableWorkflowKey(requirement.RequirementID) || !validDurableWorkflowKey(requirement.DedupeKey) ||
		!validDurableWorkflowText(requirement.TraceID, 512, false) || !validDurableWorkflowText(requirement.RequestedBy, 512, false) ||
		!validDurableWorkflowText(requirement.UserScope, 1<<10, false) || !validDurableWorkflowText(requirement.OwnerHint, 512, false) ||
		!validDurableWorkflowText(requirement.OwnerModule, 512, false) || !validDurableWorkflowText(result.Reason, durableWorkflowMaxText, false) ||
		!validDurableWorkflowText(result.ReasonCode, 512, false) || !validDurableWorkflowStrings(requirement.FactsToStore) ||
		!validDurableWorkflowStrings(requirement.SourceSystems) || !validDurableWorkflowStrings(requirement.ReadPatterns) ||
		!validDurableWorkflowStrings(requirement.WritePatterns) || !validDurableWorkflowStrings(requirement.Acceptance) ||
		!validDurableWorkflowStrings(result.EvidenceRefs) {
		return false
	}
	if requirement.ActionID != "" && !validDurableWorkflowActionID(requirement.ActionID) {
		return false
	}
	switch result.Status {
	case "", domaindurable.StatusCompleted, domaindurable.StatusRejected, domaindurable.StatusBlocked:
	default:
		return false
	}
	switch result.Lifecycle {
	case "", domaindurable.LifecycleProposed, domaindurable.LifecycleValidated, domaindurable.LifecycleImplemented, domaindurable.LifecycleProvisioned, domaindurable.LifecycleActive:
	default:
		return false
	}
	switch requirement.RequestedOutcome {
	case "", domaindurable.OutcomeAssess, domaindurable.OutcomeImplement:
	default:
		return false
	}
	return boundedDurableWorkflowValue(result)
}

func validDurableWorkflowReceipt(receipt domaindurable.RequestReceipt) bool {
	if domaindurable.ValidateRequestReceipt(receipt) != nil || !validDurableWorkflowActionID(receipt.ActionID) ||
		!validDurableWorkflowText(receipt.UserScope, 1<<10, false) || !durableWorkflowHashPattern.MatchString(receipt.PayloadHash) ||
		!validDurableWorkflowKey(receipt.RequirementID) {
		return false
	}
	return strings.TrimSpace(string(receipt.ActionID)) == string(receipt.ActionID) && strings.TrimSpace(receipt.UserScope) == receipt.UserScope &&
		strings.TrimSpace(receipt.PayloadHash) == receipt.PayloadHash && strings.TrimSpace(receipt.RequirementID) == receipt.RequirementID
}

func validDurableWorkflowActionID(actionID modulecore.ActionID) bool {
	raw := string(actionID)
	if strings.HasPrefix(raw, "legacy/") {
		return durableWorkflowHashPattern.MatchString(strings.TrimPrefix(raw, "legacy/"))
	}
	return actionID.Validate() == nil
}

func validDurableWorkflowKey(value string) bool {
	return validDurableWorkflowText(value, 512, true) && strings.TrimSpace(value) == value
}

func validDurableWorkflowText(value string, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func validDurableWorkflowStrings(values []string) bool {
	if len(values) > durableWorkflowMaxList {
		return false
	}
	for _, value := range values {
		if !validDurableWorkflowText(value, durableWorkflowMaxText, false) {
			return false
		}
	}
	return true
}

func boundedDurableWorkflowValue(value any) bool {
	encoded, err := json.Marshal(value)
	return err == nil && len(encoded) <= durableWorkflowMaxPayload
}

func durableWorkflowOperationIdentity(mutation MutationMetadata, payload durableWorkflowSavePayload) persistdurable.DurableWorkflowStorageHostOperationIdentity {
	return persistdurable.DurableWorkflowStorageHostOperationIdentity{
		OpID:             mutation.OpID,
		PayloadSHA256:    payloadHash(GroupDurableStoreWorkflow, durableWorkflowOpSaveWithReceipt, mutation.Payload),
		ActionID:         payload.Receipt.ActionID,
		RequirementID:    payload.Receipt.RequirementID,
		WriterGeneration: mutation.JournalGeneration,
	}
}

func durableWorkflowSchemaError(operation string) *Error {
	return NewError(ErrorCodeSchemaRejected, "durable workflow "+operation+" rejected")
}

func durableWorkflowReadError(operation string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "durable workflow "+operation+" failed")
}

func isNilDurableWorkflowValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// DurableStoreWorkflowClient implements the existing four-method Store over
// the storage-host protocol without exposing SQL or filesystem operations.
type DurableStoreWorkflowClient struct{ client *Client }

func NewDurableStoreWorkflowClient(client *Client) *DurableStoreWorkflowClient {
	return &DurableStoreWorkflowClient{client: client}
}

func (store *DurableStoreWorkflowClient) FindByDedupeKey(ctx context.Context, key string) (*domaindurable.WorkflowResult, error) {
	if !validDurableWorkflowKey(key) {
		return nil, durableWorkflowSchemaError("dedupe lookup")
	}
	var response durableWorkflowResultResponse
	if err := store.client.Call(ctx, GroupDurableStoreWorkflow, durableWorkflowOpFindByDedupeKey, durableWorkflowDedupePayload{DedupeKey: key}, &response); err != nil {
		return nil, err
	}
	return validateDurableWorkflowResultResponse(response, key, false)
}

func (store *DurableStoreWorkflowClient) FindByActionID(ctx context.Context, actionID modulecore.ActionID) (*domaindurable.RequestReceipt, error) {
	if !validDurableWorkflowActionID(actionID) {
		return nil, durableWorkflowSchemaError("action receipt lookup")
	}
	var response durableWorkflowReceiptResponse
	if err := store.client.Call(ctx, GroupDurableStoreWorkflow, durableWorkflowOpFindByActionID, durableWorkflowActionPayload{ActionID: actionID}, &response); err != nil {
		return nil, err
	}
	if !response.Found {
		if response.Receipt != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow action receipt response is malformed")
		}
		return nil, nil
	}
	if response.Receipt == nil || !validDurableWorkflowReceipt(*response.Receipt) || response.Receipt.ActionID != actionID {
		return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow action receipt response is malformed")
	}
	return response.Receipt, nil
}

func (store *DurableStoreWorkflowClient) FindByRequirementID(ctx context.Context, requirementID string) (*domaindurable.WorkflowResult, error) {
	if !validDurableWorkflowKey(requirementID) {
		return nil, durableWorkflowSchemaError("requirement lookup")
	}
	var response durableWorkflowResultResponse
	if err := store.client.Call(ctx, GroupDurableStoreWorkflow, durableWorkflowOpFindByRequirementID, durableWorkflowRequirementPayload{RequirementID: requirementID}, &response); err != nil {
		return nil, err
	}
	return validateDurableWorkflowResultResponse(response, requirementID, true)
}

func validateDurableWorkflowResultResponse(response durableWorkflowResultResponse, key string, requirement bool) (*domaindurable.WorkflowResult, error) {
	if !response.Found {
		if response.Result != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow result response is malformed")
		}
		return nil, nil
	}
	if response.Result == nil || !validDurableWorkflowResult(*response.Result) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow result response is malformed")
	}
	if requirement {
		if response.Result.Requirement.RequirementID != key {
			return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow requirement response is mismatched")
		}
	} else if response.Result.Requirement.DedupeKey != key {
		return nil, NewError(ErrorCodeOutcomeUnknown, "durable workflow dedupe response is mismatched")
	}
	return response.Result, nil
}

func (store *DurableStoreWorkflowClient) SaveWithReceipt(ctx context.Context, result *domaindurable.WorkflowResult, receipt domaindurable.RequestReceipt) error {
	payload := durableWorkflowSavePayload{Result: result, Receipt: receipt}
	if _, err := decodeDurableWorkflowSave(mustJSON(payload)); err != nil {
		return durableWorkflowSchemaError("save")
	}
	if err := store.client.Call(ctx, GroupDurableStoreWorkflow, durableWorkflowOpSaveWithReceipt, payload, nil); err != nil {
		var wire *Error
		if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
			return fmt.Errorf("%w: %v", appdurable.ErrRequestConflict, err)
		}
		return err
	}
	return nil
}

var _ appdurable.Store = (*DurableStoreWorkflowClient)(nil)
