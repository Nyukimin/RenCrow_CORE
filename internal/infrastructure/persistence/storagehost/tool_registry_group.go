package storagehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupToolRegistry      = "tool_registry"
	maxToolRegistryPayload = 96 << 10
	maxToolRegistryResult  = 1 << 20
)

var (
	toolRegistryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	toolRegistryHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ToolRegistryGroupOwner is the complete typed owner contract for capability
// registration. Receipt-aware registration remains the existing domain
// extension; no SQL, filesystem path, or backend handle crosses this boundary.
type ToolRegistryGroupOwner interface {
	capability.ToolRegistry
	capability.ToolRegistryReceiptOwner
	toolregistry.ToolRegistryStorageHostReceiptOwner
}

type ToolRegistryClient struct{ client *Client }

func NewToolRegistryClient(client *Client) *ToolRegistryClient {
	return &ToolRegistryClient{client: client}
}

type toolRegistryEntryPayload struct {
	Entry capability.ToolEntry `json:"entry"`
}

type toolRegistryPlatformPayload struct {
	Platform string `json:"platform"`
}

type toolRegistryGetPayload struct {
	Name string `json:"name"`
}

type toolRegistryReceiptPayload struct {
	Entry       capability.ToolEntry `json:"entry"`
	ActionID    string               `json:"action_id"`
	ActorID     string               `json:"actor_id"`
	PayloadHash string               `json:"payload_hash"`
}

type toolRegistryFindReceiptPayload struct {
	ActionID string `json:"action_id"`
}

type toolRegistryEntryResult struct {
	Entry capability.ToolEntry `json:"entry"`
}

type toolRegistryListResult struct {
	Entries []capability.ToolEntry `json:"entries"`
}

type toolRegistryReceiptResult struct {
	Receipt        capability.ToolRegistryRequestReceipt `json:"receipt"`
	RequestReplay  bool                                  `json:"request_replay,omitempty"`
	SemanticDedupe bool                                  `json:"semantic_dedupe,omitempty"`
}

type toolRegistryReceiptFoundResult struct {
	Found   bool                                   `json:"found"`
	Receipt *capability.ToolRegistryRequestReceipt `json:"receipt,omitempty"`
}

// RegisterToolRegistryGroup installs exactly the current capability read and
// write contract. Receipt-aware registration reconciles through the exact
// owner result proof; legacy Register proves commit from a natural-key readback.
func RegisterToolRegistryGroup(handler *Handler, owner ToolRegistryGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: tool registry group needs a handler and an owner")
	}
	if err := handler.RegisterRecoverable(GroupToolRegistry, "register", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload toolRegistryEntryPayload
		if decodeToolRegistryPayload(mutation.Payload, &payload) != nil || validateToolRegistryEntry(payload.Entry) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "tool registry entry rejected"))
		}
		if err := owner.Register(ctx, payload.Entry); err != nil {
			return nil, err
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload toolRegistryEntryPayload
		if decodeToolRegistryPayload(mutation.Payload, &payload) != nil || validateToolRegistryEntry(payload.Entry) != nil {
			return UnknownOutcome(), nil
		}
		stored, err := owner.Get(ctx, payload.Entry.Name)
		if err != nil {
			if errors.Is(err, capability.ErrToolRegistryEntryNotFound) {
				return ConfirmedNotCommitted(), nil
			}
			return UnknownOutcome(), nil
		}
		if validateToolRegistryStoredEntry(stored) != nil || !sameToolRegistryStoredEntry(payload.Entry, stored) {
			return UnknownOutcome(), nil
		}
		return Committed(nil), nil
	}); err != nil {
		return err
	}
	if err := handler.RegisterRecoverable(GroupToolRegistry, "register_with_receipt", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeToolRegistryReceiptMutation(mutation.Payload)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "tool registry receipt request rejected"))
		}
		identity, err := toolRegistryStorageHostOperationIdentity(mutation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "tool registry storage-host identity rejected"))
		}
		result, err := owner.RegisterWithReceiptForStorageHost(ctx, payload.Entry, modulecore.ActionID(payload.ActionID), payload.ActorID, payload.PayloadHash, identity)
		if err != nil {
			if errors.Is(err, toolregistry.ErrToolRegistryRequestConflict) || errors.Is(err, toolregistry.ErrToolRegistryEntryConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "tool registry receipt conflicts with durable owner state"))
			}
			return nil, err
		}
		if !validToolRegistryReceipt(result.Receipt, payload) || (result.RequestReplay && result.SemanticDedupe) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "tool registry owner returned a receipt that does not match the request")
		}
		return toolRegistryReceiptResult{Receipt: result.Receipt, RequestReplay: result.RequestReplay, SemanticDedupe: result.SemanticDedupe}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeToolRegistryReceiptMutation(mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		identity, err := toolRegistryStorageHostOperationIdentity(mutation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.FindStorageHostRegistrationResult(ctx, identity)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if found {
			if !validToolRegistryReceipt(result.Receipt, payload) || (result.RequestReplay && result.SemanticDedupe) {
				return UnknownOutcome(), nil
			}
			return Committed(toolRegistryReceiptResult{Receipt: result.Receipt, RequestReplay: result.RequestReplay, SemanticDedupe: result.SemanticDedupe}), nil
		}
		// A canonical action receipt without the exact operation proof may be a
		// legacy commit. It is insufficient evidence to synthesize a result or
		// authorize the owner mutation to run again.
		_, receiptFound, err := owner.FindActionReceipt(ctx, modulecore.ActionID(payload.ActionID))
		if err != nil || receiptFound {
			return UnknownOutcome(), nil
		}
		return ConfirmedNotCommitted(), nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupToolRegistry, "list_for_platform", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload toolRegistryPlatformPayload
		if decodeToolRegistryPayload(raw, &payload) != nil || !validToolRegistryPlatform(payload.Platform) {
			return nil, NewError(ErrorCodeSchemaRejected, "tool registry platform query rejected")
		}
		entries, err := owner.ListForPlatform(ctx, payload.Platform)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry list failed")
		}
		if len(entries) > 4096 {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry list exceeds the response bound")
		}
		for i := range entries {
			if validateToolRegistryStoredEntry(entries[i]) != nil || !containsToolPlatform(entries[i].Platforms, payload.Platform) {
				return nil, NewError(ErrorCodeStoreUnavailable, "tool registry owner returned a malformed entry")
			}
		}
		if len(entries) == 0 {
			entries = []capability.ToolEntry{}
		}
		result := toolRegistryListResult{Entries: entries}
		if err := boundedToolRegistryResult(result); err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry list exceeds the response bound")
		}
		return result, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupToolRegistry, "get", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload toolRegistryGetPayload
		if decodeToolRegistryPayload(raw, &payload) != nil || !validToolRegistryName(payload.Name) {
			return nil, NewError(ErrorCodeSchemaRejected, "tool registry get request rejected")
		}
		entry, err := owner.Get(ctx, payload.Name)
		if err != nil {
			if errors.Is(err, capability.ErrToolRegistryEntryNotFound) {
				return nil, NewError("tool_registry_entry_not_found", "tool entry does not exist")
			}
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry get failed")
		}
		if validateToolRegistryStoredEntry(entry) != nil || entry.Name != payload.Name {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry owner returned a malformed entry")
		}
		result := toolRegistryEntryResult{Entry: entry}
		if err := boundedToolRegistryResult(result); err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry entry exceeds the response bound")
		}
		return result, nil
	}); err != nil {
		return err
	}
	return handler.Register(GroupToolRegistry, "find_action_receipt", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload toolRegistryFindReceiptPayload
		if decodeToolRegistryPayload(raw, &payload) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "tool registry receipt lookup rejected")
		}
		actionID := modulecore.ActionID(payload.ActionID)
		if actionID.Validate() != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "tool registry action id is not canonical")
		}
		receipt, found, err := owner.FindActionReceipt(ctx, actionID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry receipt lookup failed")
		}
		if found && !validToolRegistryStoredReceipt(receipt, actionID) {
			return nil, NewError(ErrorCodeStoreUnavailable, "tool registry owner returned a malformed receipt")
		}
		result := toolRegistryReceiptFoundResult{Found: found}
		if found {
			result.Receipt = &receipt
		}
		return result, nil
	})
}

func decodeToolRegistryPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxToolRegistryPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("tool registry payload is not a bounded object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("tool registry payload has trailing data")
	}
	return nil
}

func decodeToolRegistryReceiptMutation(raw json.RawMessage) (toolRegistryReceiptPayload, error) {
	var payload toolRegistryReceiptPayload
	if err := decodeToolRegistryPayload(raw, &payload); err != nil {
		return payload, err
	}
	actionID := modulecore.ActionID(payload.ActionID)
	if err := validateToolRegistryEntry(payload.Entry); err != nil || actionID.Validate() != nil || !validToolRegistryActor(payload.ActorID) || !toolRegistryHashPattern.MatchString(payload.PayloadHash) {
		return payload, errors.New("tool registry receipt identity or entry rejected")
	}
	return payload, nil
}

func toolRegistryStorageHostOperationIdentity(mutation MutationMetadata) (toolregistry.ToolRegistryStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return toolregistry.ToolRegistryStorageHostOperationIdentity{}, errors.New("tool registry storage-host mutation identity is incomplete")
	}
	payloadSHA256 := sha256.Sum256(mutation.Payload)
	return toolregistry.ToolRegistryStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: toolregistry.ToolRegistryRegisterWithReceiptOperation,
		PayloadSHA256: hex.EncodeToString(payloadSHA256[:]), WriterGeneration: mutation.JournalGeneration,
	}, nil
}

func validateToolRegistryEntry(entry capability.ToolEntry) error {
	if !validToolRegistryName(entry.Name) || !boundedText(entry.Description, 4096, true) || len(entry.SchemaJSON) == 0 || len(entry.SchemaJSON) > 64<<10 || !utf8.ValidString(entry.SchemaJSON) || !boundedText(string(entry.Source), 1024, true) || !boundedText(entry.CreatedBy, 128, true) {
		return errors.New("tool registry entry fields exceed bounds")
	}
	if len(entry.Platforms) == 0 || len(entry.Platforms) > 3 {
		return errors.New("tool registry platforms are empty or excessive")
	}
	seen := make(map[string]struct{}, len(entry.Platforms))
	for _, platform := range entry.Platforms {
		if !validToolRegistryPlatform(platform) {
			return errors.New("tool registry platform is unsupported")
		}
		if _, duplicate := seen[platform]; duplicate {
			return errors.New("tool registry platform is duplicated")
		}
		seen[platform] = struct{}{}
	}
	if _, err := capability.ParseToolDefinition(entry); err != nil {
		return fmt.Errorf("tool registry schema rejected: %w", err)
	}
	return nil
}

func validateToolRegistryStoredEntry(entry capability.ToolEntry) error {
	if err := validateToolRegistryEntry(entry); err != nil {
		return err
	}
	if entry.CreatedAt.IsZero() {
		return errors.New("stored tool registry timestamp is missing")
	}
	return nil
}

func validToolRegistryName(name string) bool { return toolRegistryNamePattern.MatchString(name) }

func validToolRegistryPlatform(platform string) bool {
	switch platform {
	case "linux", "windows", "darwin":
		return true
	default:
		return false
	}
}

func validToolRegistryActor(actor string) bool {
	return boundedText(actor, 128, true) && !strings.ContainsAny(actor, "\r\n\t")
}

func boundedText(value string, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	if required && strings.TrimSpace(value) == "" {
		return false
	}
	return true
}

func containsToolPlatform(platforms []string, want string) bool {
	for _, platform := range platforms {
		if platform == want {
			return true
		}
	}
	return false
}

func sameToolRegistryStoredEntry(request, stored capability.ToolEntry) bool {
	// Legacy Register intentionally preserves CreatedAt on conflict-upsert; all
	// columns that operation writes must match the exact natural-key row.
	if request.Name != stored.Name || request.Description != stored.Description || request.SchemaJSON != stored.SchemaJSON || request.Source != stored.Source || request.CreatedBy != stored.CreatedBy || len(request.Platforms) != len(stored.Platforms) {
		return false
	}
	for i := range request.Platforms {
		if request.Platforms[i] != stored.Platforms[i] {
			return false
		}
	}
	return true
}

func validToolRegistryReceipt(receipt capability.ToolRegistryRequestReceipt, request toolRegistryReceiptPayload) bool {
	return validToolRegistryStoredReceipt(receipt, modulecore.ActionID(request.ActionID)) && receipt.ActorID == request.ActorID && receipt.PayloadHash == request.PayloadHash && receipt.ToolName == request.Entry.Name
}

func validToolRegistryStoredReceipt(receipt capability.ToolRegistryRequestReceipt, actionID modulecore.ActionID) bool {
	return receipt.ActionID == actionID && validToolRegistryActor(receipt.ActorID) && toolRegistryHashPattern.MatchString(receipt.PayloadHash) && validToolRegistryName(receipt.ToolName) && !receipt.CreatedAt.IsZero()
}

func boundedToolRegistryResult(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxToolRegistryResult {
		return errors.New("tool registry result exceeds bound")
	}
	return nil
}

// Close is intentionally a no-op: the RPC client may be shared by other owner
// groups in this CORE process.
func (registry *ToolRegistryClient) Close() error { return nil }

func (registry *ToolRegistryClient) Register(ctx context.Context, entry capability.ToolEntry) error {
	return registry.client.Call(ctx, GroupToolRegistry, "register", toolRegistryEntryPayload{Entry: entry}, nil)
}

func (registry *ToolRegistryClient) ListForPlatform(ctx context.Context, platform string) ([]capability.ToolEntry, error) {
	var result toolRegistryListResult
	err := registry.client.Call(ctx, GroupToolRegistry, "list_for_platform", toolRegistryPlatformPayload{Platform: platform}, &result)
	if err != nil {
		return nil, err
	}
	if result.Entries == nil {
		return nil, NewError(ErrorCodeOutcomeUnknown, "tool registry list result is malformed")
	}
	for _, entry := range result.Entries {
		if validateToolRegistryStoredEntry(entry) != nil || !containsToolPlatform(entry.Platforms, platform) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "tool registry list result contains a malformed entry")
		}
	}
	return result.Entries, nil
}

func (registry *ToolRegistryClient) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	var result toolRegistryEntryResult
	err := registry.client.Call(ctx, GroupToolRegistry, "get", toolRegistryGetPayload{Name: name}, &result)
	if err != nil {
		var wire *Error
		if errors.As(err, &wire) && wire.Code == "tool_registry_entry_not_found" {
			return capability.ToolEntry{}, fmt.Errorf("%w: %w", capability.ErrToolRegistryEntryNotFound, err)
		}
		return capability.ToolEntry{}, err
	}
	if validateToolRegistryStoredEntry(result.Entry) != nil || result.Entry.Name != name {
		return capability.ToolEntry{}, NewError(ErrorCodeOutcomeUnknown, "tool registry get result is malformed")
	}
	return result.Entry, nil
}

func (registry *ToolRegistryClient) RegisterWithReceipt(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string) (capability.ToolRegistryRegistrationResult, error) {
	var result toolRegistryReceiptResult
	payload := toolRegistryReceiptPayload{Entry: entry, ActionID: string(actionID), ActorID: actorID, PayloadHash: payloadHash}
	if err := registry.client.Call(ctx, GroupToolRegistry, "register_with_receipt", payload, &result); err != nil {
		return capability.ToolRegistryRegistrationResult{}, err
	}
	if !validToolRegistryReceipt(result.Receipt, payload) {
		return capability.ToolRegistryRegistrationResult{}, NewError(ErrorCodeOutcomeUnknown, "tool registry receipt result is malformed")
	}
	return capability.ToolRegistryRegistrationResult{Receipt: result.Receipt, RequestReplay: result.RequestReplay, SemanticDedupe: result.SemanticDedupe}, nil
}

func (registry *ToolRegistryClient) FindActionReceipt(ctx context.Context, actionID modulecore.ActionID) (capability.ToolRegistryRequestReceipt, bool, error) {
	var result toolRegistryReceiptFoundResult
	if err := registry.client.Call(ctx, GroupToolRegistry, "find_action_receipt", toolRegistryFindReceiptPayload{ActionID: string(actionID)}, &result); err != nil {
		return capability.ToolRegistryRequestReceipt{}, false, err
	}
	if !result.Found {
		if result.Receipt != nil {
			return capability.ToolRegistryRequestReceipt{}, false, NewError(ErrorCodeOutcomeUnknown, "tool registry receipt result is malformed")
		}
		return capability.ToolRegistryRequestReceipt{}, false, nil
	}
	if result.Receipt == nil || !validToolRegistryStoredReceipt(*result.Receipt, actionID) {
		return capability.ToolRegistryRequestReceipt{}, false, NewError(ErrorCodeOutcomeUnknown, "tool registry receipt result is malformed")
	}
	return *result.Receipt, true, nil
}

var (
	_ capability.ToolRegistry             = (*ToolRegistryClient)(nil)
	_ capability.ToolRegistryReceiptOwner = (*ToolRegistryClient)(nil)
)
