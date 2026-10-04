package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// GroupUserMemory is the closed typed UserMemory surface used by Shiro and
// runtime data operations. It exposes no SQL or filesystem capability.
const GroupUserMemory = "user_memory"

const (
	userMemoryOpCreate    = "create_user_memory"
	userMemoryOpList      = "list_user_memories"
	userMemoryOpFind      = "find_user_memory_by_id"
	userMemoryOpCandidate = "create_user_memory_candidate_with_request"
	userMemoryOpState     = "update_user_memory_state"
	userMemoryOpForget    = "forget_user_memory"
	userMemoryOpSupersede = "supersede_user_memory"

	userMemoryMaxListLimit = 100
	userMemoryMaxIDBytes   = 2_048
	userMemoryMaxUserBytes = 256
	userMemoryMaxReason    = 4_096
)

// UserMemoryGroupOwner is the local behavior surface proxied by this group.
type UserMemoryGroupOwner interface {
	CreateUserMemory(context.Context, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error)
	ListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error)
	FindUserMemoryByID(context.Context, string) (domainmemory.UserMemory, bool, error)
	CreateUserMemoryCandidateWithRequest(context.Context, string, string, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error)
	UpdateUserMemoryState(context.Context, string, string, string) (*domainmemory.UserMemory, error)
	ForgetUserMemory(context.Context, string, string) (*domainmemory.UserMemory, error)
	SupersedeUserMemory(context.Context, string, string, string) (*domainmemory.UserMemory, error)
}

// UserMemoryGroupRecoveryOwner adds the owner transaction and exact-result
// receipt methods required to safely recover an interrupted mutation.
type UserMemoryGroupRecoveryOwner interface {
	UserMemoryGroupOwner
	CreateUserMemoryForStorageHostOperation(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error)
	CreateUserMemoryCandidateWithRequestForStorageHostOperation(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, string, string, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error)
	UpdateUserMemoryStateForStorageHostOperation(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, string, string, string) (*domainmemory.UserMemory, error)
	ForgetUserMemoryForStorageHostOperation(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, string, string) (*domainmemory.UserMemory, error)
	SupersedeUserMemoryForStorageHostOperation(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, string, string, string) (*domainmemory.UserMemory, error)
	LookupUserMemoryStorageHostOperationReceipt(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity) (json.RawMessage, bool, error)
}

type userMemoryInputDTO struct {
	UserID           string   `json:"user_id"`
	Type             string   `json:"type"`
	Statement        string   `json:"statement"`
	State            string   `json:"state"`
	EvidenceEventIDs []string `json:"evidence_event_ids"`
	Confidence       float64  `json:"confidence"`
	Sensitivity      string   `json:"sensitivity"`
	Scope            string   `json:"scope"`
	Source           string   `json:"source"`
}

func userMemoryInputFromDomain(input domainmemory.CreateUserMemoryInput) userMemoryInputDTO {
	return userMemoryInputDTO{UserID: input.UserID, Type: input.Type, Statement: input.Statement, State: input.State,
		EvidenceEventIDs: append([]string(nil), input.EvidenceEventIDs...), Confidence: input.Confidence,
		Sensitivity: input.Sensitivity, Scope: input.Scope, Source: input.Source}
}

func (dto userMemoryInputDTO) domain() domainmemory.CreateUserMemoryInput {
	return domainmemory.CreateUserMemoryInput{UserID: dto.UserID, Type: dto.Type, Statement: dto.Statement, State: dto.State,
		EvidenceEventIDs: append([]string(nil), dto.EvidenceEventIDs...), Confidence: dto.Confidence,
		Sensitivity: dto.Sensitivity, Scope: dto.Scope, Source: dto.Source}
}

type userMemoryCreatePayload struct {
	Input userMemoryInputDTO `json:"input"`
}
type userMemoryListPayload struct {
	UserID          string `json:"user_id"`
	State           string `json:"state"`
	IncludeInactive bool   `json:"include_inactive"`
	Limit           int    `json:"limit"`
}
type userMemoryFindPayload struct {
	ID string `json:"id"`
}
type userMemoryCandidatePayload struct {
	RequestID string             `json:"request_id"`
	ActorID   string             `json:"actor_id"`
	Input     userMemoryInputDTO `json:"input"`
}
type userMemoryStatePayload struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}
type userMemoryForgetPayload struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}
type userMemorySupersedePayload struct {
	ID     string `json:"id"`
	NewID  string `json:"new_id"`
	Reason string `json:"reason"`
}
type userMemoryItemResult struct {
	Memory *domainmemory.UserMemory `json:"memory"`
}
type userMemoryCandidateResult struct {
	Memory           *domainmemory.UserMemory `json:"memory"`
	IdempotentReplay bool                     `json:"idempotent_replay"`
}
type userMemoryListResult struct {
	Items []domainmemory.UserMemory `json:"items"`
}
type userMemoryLookupResult struct {
	Found  bool                     `json:"found"`
	Memory *domainmemory.UserMemory `json:"memory,omitempty"`
}

// RegisterUserMemoryGroup serves only the seven typed UserMemory methods.
// Mutations require durable owner receipts; the storage-host journal alone is
// not sufficient to recover a commit whose response was lost.
func RegisterUserMemoryGroup(handler *Handler, owner UserMemoryGroupOwner) error {
	if handler == nil || isNilUserMemoryOwner(owner) {
		return errors.New("storagehost: user memory group needs a handler and an owner store")
	}
	recovery, ok := owner.(UserMemoryGroupRecoveryOwner)
	if !ok || isNilUserMemoryOwner(recovery) {
		return errors.New("storagehost: user memory owner lacks atomic operation receipts")
	}

	if err := handler.RegisterRecoverable(GroupUserMemory, userMemoryOpCreate, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var p userMemoryCreatePayload
		if decodeL1Payload(mutation.Payload, &p) != nil || !validUserMemoryCreateInput(p.Input.domain()) {
			return nil, rejectUserMemoryPayload(userMemoryOpCreate)
		}
		identity, err := userMemoryOperationIdentity(mutation, userMemoryOpCreate)
		if err != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "user memory operation identity is incomplete")
		}
		item, err := recovery.CreateUserMemoryForStorageHostOperation(ctx, identity, p.Input.domain())
		if err != nil {
			return nil, userMemoryMutationOwnerFailed(userMemoryOpCreate, err)
		}
		if !validUserMemoryResult(item) || !userMemoryCreateResultMatches(item, p.Input.domain()) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "user memory create result is invalid")
		}
		return userMemoryReadyResult(userMemoryItemResult{Memory: item})
	}, userMemoryReconcile(recovery, userMemoryOpCreate, func(raw json.RawMessage, p json.RawMessage) (any, error) {
		var payload userMemoryCreatePayload
		if decodeL1Payload(p, &payload) != nil {
			return nil, errors.New("invalid user memory create request")
		}
		var result userMemoryItemResult
		if decodeUserMemoryReceiptResult(raw, &result) != nil || !validUserMemoryResult(result.Memory) || !userMemoryCreateResultMatches(result.Memory, payload.Input.domain()) {
			return nil, errors.New("invalid user memory create receipt result")
		}
		return result, nil
	})); err != nil {
		return err
	}

	if err := handler.Register(GroupUserMemory, userMemoryOpList, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p userMemoryListPayload
		if decodeL1Payload(raw, &p) != nil || !validUserMemoryListPayload(p) {
			return nil, NewError(ErrorCodeSchemaRejected, "user memory list request rejected")
		}
		items, err := owner.ListUserMemories(ctx, p.UserID, p.State, p.IncludeInactive, p.Limit)
		if err != nil {
			return nil, userMemoryReadOwnerFailed(userMemoryOpList)
		}
		userID := strings.TrimSpace(p.UserID)
		if userID == "" {
			userID = "ren"
		}
		if len(items) > userMemoryEffectiveLimit(p.Limit) {
			return nil, userMemoryReadOwnerFailed(userMemoryOpList)
		}
		for i := range items {
			if !validUserMemoryResult(&items[i]) || items[i].UserID != userID || (p.State != "" && items[i].State != p.State) || (!p.IncludeInactive && !items[i].Active) {
				return nil, userMemoryReadOwnerFailed(userMemoryOpList)
			}
		}
		if items == nil {
			items = []domainmemory.UserMemory{}
		}
		return userMemoryReadyResult(userMemoryListResult{Items: items})
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupUserMemory, userMemoryOpFind, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p userMemoryFindPayload
		if decodeL1Payload(raw, &p) != nil || !validUserMemoryID(p.ID) {
			return nil, NewError(ErrorCodeSchemaRejected, "user memory lookup request rejected")
		}
		item, found, err := owner.FindUserMemoryByID(ctx, p.ID)
		if err != nil {
			return nil, userMemoryReadOwnerFailed(userMemoryOpFind)
		}
		if !found {
			return userMemoryReadyResult(userMemoryLookupResult{Found: false})
		}
		if !validUserMemoryResult(&item) || item.ID != p.ID {
			return nil, userMemoryReadOwnerFailed(userMemoryOpFind)
		}
		return userMemoryReadyResult(userMemoryLookupResult{Found: true, Memory: &item})
	}); err != nil {
		return err
	}

	if err := handler.RegisterRecoverable(GroupUserMemory, userMemoryOpCandidate, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var p userMemoryCandidatePayload
		if decodeL1Payload(mutation.Payload, &p) != nil || !validUserMemoryCandidatePayload(p) {
			return nil, rejectUserMemoryPayload(userMemoryOpCandidate)
		}
		identity, err := userMemoryOperationIdentity(mutation, userMemoryOpCandidate)
		if err != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "user memory operation identity is incomplete")
		}
		item, replay, err := recovery.CreateUserMemoryCandidateWithRequestForStorageHostOperation(ctx, identity, p.RequestID, p.ActorID, p.Input.domain())
		if err != nil {
			return nil, userMemoryMutationOwnerFailed(userMemoryOpCandidate, err)
		}
		if !validUserMemoryResult(item) || !userMemoryCandidateResultMatches(item, p) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "user memory candidate result is invalid")
		}
		return userMemoryReadyResult(userMemoryCandidateResult{Memory: item, IdempotentReplay: replay})
	}, userMemoryReconcile(recovery, userMemoryOpCandidate, func(raw json.RawMessage, p json.RawMessage) (any, error) {
		var payload userMemoryCandidatePayload
		if decodeL1Payload(p, &payload) != nil || !validUserMemoryCandidatePayload(payload) {
			return nil, errors.New("invalid user memory candidate request")
		}
		var result userMemoryCandidateResult
		if decodeUserMemoryReceiptResult(raw, &result) != nil || !validUserMemoryResult(result.Memory) || !userMemoryCandidateResultMatches(result.Memory, payload) {
			return nil, errors.New("invalid user memory candidate receipt result")
		}
		return result, nil
	})); err != nil {
		return err
	}

	if err := registerUserMemoryOperation(handler, recovery, userMemoryOpState,
		func(ctx context.Context, identity l1sqlite.UserMemoryStorageHostOperationIdentity, raw json.RawMessage) (any, error) {
			var p userMemoryStatePayload
			if decodeL1Payload(raw, &p) != nil || !validUserMemoryStatePayload(p) {
				return nil, rejectUserMemoryPayload(userMemoryOpState)
			}
			item, err := recovery.UpdateUserMemoryStateForStorageHostOperation(ctx, identity, p.ID, p.State, p.Reason)
			if err != nil {
				return nil, userMemoryMutationOwnerFailed(userMemoryOpState, err)
			}
			if !validUserMemoryResult(item) || item.ID != p.ID || item.State != strings.TrimSpace(p.State) {
				return nil, NewError(ErrorCodeOutcomeUnknown, "user memory state result is invalid")
			}
			return userMemoryReadyResult(userMemoryItemResult{Memory: item})
		}, func(raw, request json.RawMessage) (any, error) {
			var p userMemoryStatePayload
			var result userMemoryItemResult
			if decodeL1Payload(request, &p) != nil || !validUserMemoryStatePayload(p) || decodeUserMemoryReceiptResult(raw, &result) != nil || !validUserMemoryResult(result.Memory) || result.Memory.ID != p.ID || result.Memory.State != strings.TrimSpace(p.State) {
				return nil, errors.New("invalid user memory state receipt")
			}
			return result, nil
		}); err != nil {
		return err
	}

	if err := registerUserMemoryOperation(handler, recovery, userMemoryOpForget,
		func(ctx context.Context, identity l1sqlite.UserMemoryStorageHostOperationIdentity, raw json.RawMessage) (any, error) {
			var p userMemoryForgetPayload
			if decodeL1Payload(raw, &p) != nil || !validUserMemoryForgetPayload(p) {
				return nil, rejectUserMemoryPayload(userMemoryOpForget)
			}
			item, err := recovery.ForgetUserMemoryForStorageHostOperation(ctx, identity, p.ID, p.Reason)
			if err != nil {
				return nil, userMemoryMutationOwnerFailed(userMemoryOpForget, err)
			}
			if !validUserMemoryResult(item) || item.ID != p.ID || item.Active {
				return nil, NewError(ErrorCodeOutcomeUnknown, "user memory forget result is invalid")
			}
			return userMemoryReadyResult(userMemoryItemResult{Memory: item})
		}, func(raw, request json.RawMessage) (any, error) {
			var p userMemoryForgetPayload
			var result userMemoryItemResult
			if decodeL1Payload(request, &p) != nil || !validUserMemoryForgetPayload(p) || decodeUserMemoryReceiptResult(raw, &result) != nil || !validUserMemoryResult(result.Memory) || result.Memory.ID != p.ID || result.Memory.Active {
				return nil, errors.New("invalid user memory forget receipt")
			}
			return result, nil
		}); err != nil {
		return err
	}

	return registerUserMemoryOperation(handler, recovery, userMemoryOpSupersede,
		func(ctx context.Context, identity l1sqlite.UserMemoryStorageHostOperationIdentity, raw json.RawMessage) (any, error) {
			var p userMemorySupersedePayload
			if decodeL1Payload(raw, &p) != nil || !validUserMemorySupersedePayload(p) {
				return nil, rejectUserMemoryPayload(userMemoryOpSupersede)
			}
			item, err := recovery.SupersedeUserMemoryForStorageHostOperation(ctx, identity, p.ID, p.NewID, p.Reason)
			if err != nil {
				return nil, userMemoryMutationOwnerFailed(userMemoryOpSupersede, err)
			}
			if !validUserMemoryResult(item) || item.ID != p.ID || item.Active || item.SupersededBy != strings.TrimSpace(p.NewID) {
				return nil, NewError(ErrorCodeOutcomeUnknown, "user memory supersede result is invalid")
			}
			return userMemoryReadyResult(userMemoryItemResult{Memory: item})
		}, func(raw, request json.RawMessage) (any, error) {
			var p userMemorySupersedePayload
			var result userMemoryItemResult
			if decodeL1Payload(request, &p) != nil || !validUserMemorySupersedePayload(p) || decodeUserMemoryReceiptResult(raw, &result) != nil || !validUserMemoryResult(result.Memory) || result.Memory.ID != p.ID || result.Memory.Active || result.Memory.SupersededBy != strings.TrimSpace(p.NewID) {
				return nil, errors.New("invalid user memory supersede receipt")
			}
			return result, nil
		})
}

type userMemoryMutationExecute func(context.Context, l1sqlite.UserMemoryStorageHostOperationIdentity, json.RawMessage) (any, error)
type userMemoryMutationDecode func(json.RawMessage, json.RawMessage) (any, error)

func registerUserMemoryMutation(owner UserMemoryGroupRecoveryOwner, operation string, execute userMemoryMutationExecute, decode userMemoryMutationDecode) (OwnerMutationFunc, OwnerReconcileFunc) {
	return func(ctx context.Context, mutation MutationMetadata) (any, error) {
		identity, err := userMemoryOperationIdentity(mutation, operation)
		if err != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "user memory operation identity is incomplete")
		}
		return execute(ctx, identity, mutation.Payload)
	}, userMemoryReconcile(owner, operation, decode)
}

func registerUserMemoryOperation(handler *Handler, owner UserMemoryGroupRecoveryOwner, operation string, execute userMemoryMutationExecute, decode userMemoryMutationDecode) error {
	mutation, reconcile := registerUserMemoryMutation(owner, operation, execute, decode)
	return handler.RegisterRecoverable(GroupUserMemory, operation, mutation, reconcile)
}

func userMemoryReconcile(owner UserMemoryGroupRecoveryOwner, operation string, decode userMemoryMutationDecode) OwnerReconcileFunc {
	return func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		identity, err := userMemoryOperationIdentity(mutation, operation)
		if err != nil {
			return UnknownOutcome(), err
		}
		raw, found, err := owner.LookupUserMemoryStorageHostOperationReceipt(ctx, identity)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			// Once a mutating RPC is journaled as begun, an absent owner
			// receipt cannot distinguish a true pre-commit failure from a
			// committed effect whose receipt was lost or deleted. Do not rerun.
			return UnknownOutcome(), nil
		}
		result, err := decode(raw, mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		return Committed(result), nil
	}
}

func userMemoryOperationIdentity(mutation MutationMetadata, operation string) (l1sqlite.UserMemoryStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return l1sqlite.UserMemoryStorageHostOperationIdentity{}, errors.New("incomplete user memory operation identity")
	}
	return l1sqlite.NewUserMemoryStorageHostOperationIdentity(mutation.OpID, operation, payloadHash(GroupUserMemory, operation, mutation.Payload), mutation.JournalGeneration)
}

func rejectUserMemoryPayload(operation string) error {
	return ownerRolledBack(NewError(ErrorCodeSchemaRejected, "user memory "+operation+" payload rejected"))
}
func userMemoryMutationOwnerFailed(operation string, err error) error {
	if errors.Is(err, l1sqlite.ErrUserMemoryStorageHostOperationConflict) {
		return NewError(ErrorCodeDuplicateConflict, "user memory op_id conflicts with an existing owner receipt")
	}
	return NewError(ErrorCodeStoreUnavailable, "user memory "+operation+" owner failed")
}
func userMemoryReadOwnerFailed(operation string) error {
	return NewError(ErrorCodeStoreUnavailable, "user memory "+operation+" owner read failed")
}

func userMemoryReadyResult(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil || inspectL1JSON(encoded, 8<<20, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true) != nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "user memory owner returned an invalid or oversized result")
	}
	return value, nil
}

func decodeUserMemoryReceiptResult(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || len(raw) > 8<<20 || inspectL1JSON(raw, 8<<20, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true) != nil {
		return errors.New("user memory owner receipt result rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("user memory owner receipt has trailing data")
	}
	return nil
}

func validUserMemoryCreateInput(input domainmemory.CreateUserMemoryInput) bool {
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		userID = "ren"
	}
	typeName := strings.TrimSpace(input.Type)
	statement := strings.TrimSpace(input.Statement)
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = domainmemory.MemoryStateCandidate
	}
	sensitivity := strings.TrimSpace(input.Sensitivity)
	if sensitivity == "" {
		sensitivity = "normal"
	}
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "all_personas"
	}
	confidence := input.Confidence
	if confidence <= 0 {
		confidence = 0.5
	}
	return validL1Text(userID, userMemoryMaxUserBytes, true) && strings.TrimSpace(input.UserID) == input.UserID &&
		domainmemory.ValidateUserMemoryType(typeName) == nil && strings.TrimSpace(input.Type) == input.Type && validL1Text(statement, 2<<20, true) && strings.TrimSpace(input.Statement) == input.Statement &&
		domainmemory.CanPromoteUserMemory(state, input.EvidenceEventIDs, sensitivity, "") == nil && validL1StringSlice(input.EvidenceEventIDs, l1MaxCollectionEntries, userMemoryMaxIDBytes) &&
		validL1Text(sensitivity, 128, true) && validL1Text(scope, 256, true) && validL1Text(input.Source, 256, false) &&
		!math.IsNaN(confidence) && !math.IsInf(confidence, 0)
}

func validUserMemoryListPayload(p userMemoryListPayload) bool {
	if p.UserID != "" && (!validL1Text(p.UserID, userMemoryMaxUserBytes, true) || strings.TrimSpace(p.UserID) != p.UserID) {
		return false
	}
	if p.State != "" && (strings.TrimSpace(p.State) != p.State || domainmemory.ValidateMemoryState(p.State) != nil) {
		return false
	}
	return p.Limit <= userMemoryMaxListLimit
}
func validUserMemoryCandidatePayload(p userMemoryCandidatePayload) bool {
	return validL1Text(p.RequestID, userMemoryMaxIDBytes, true) && strings.TrimSpace(p.RequestID) == p.RequestID &&
		validL1Text(p.ActorID, userMemoryMaxUserBytes, true) && strings.TrimSpace(p.ActorID) == p.ActorID &&
		validUserMemoryCandidateInput(p.Input.domain())
}
func validUserMemoryCandidateInput(input domainmemory.CreateUserMemoryInput) bool {
	if !validL1Text(input.UserID, userMemoryMaxUserBytes, true) || strings.TrimSpace(input.UserID) != input.UserID ||
		domainmemory.ValidateUserMemoryType(input.Type) != nil || strings.TrimSpace(input.Type) != input.Type ||
		!validL1Text(input.Statement, 2<<20, true) || strings.TrimSpace(input.Statement) != input.Statement ||
		!validL1StringSlice(input.EvidenceEventIDs, l1MaxCollectionEntries, userMemoryMaxIDBytes) {
		return false
	}
	sensitivity := strings.TrimSpace(input.Sensitivity)
	if sensitivity == "" {
		sensitivity = "normal"
	}
	confidence := input.Confidence
	if confidence <= 0 {
		confidence = .5
	}
	return domainmemory.CanPromoteUserMemory(domainmemory.MemoryStateCandidate, input.EvidenceEventIDs, sensitivity, "") == nil &&
		validL1Text(sensitivity, 128, true) && validL1Text(input.Scope, 256, false) && !math.IsNaN(confidence) && !math.IsInf(confidence, 0)
}
func validUserMemoryStatePayload(p userMemoryStatePayload) bool {
	return validUserMemoryID(p.ID) && domainmemory.ValidateMemoryState(strings.TrimSpace(p.State)) == nil && strings.TrimSpace(p.State) == p.State && validL1Text(p.Reason, userMemoryMaxReason, false)
}
func validUserMemoryForgetPayload(p userMemoryForgetPayload) bool {
	return validUserMemoryID(p.ID) && validL1Text(p.Reason, userMemoryMaxReason, false)
}
func validUserMemorySupersedePayload(p userMemorySupersedePayload) bool {
	return validUserMemoryID(p.ID) && validL1Text(p.NewID, userMemoryMaxIDBytes, false) && strings.TrimSpace(p.NewID) == p.NewID && validL1Text(p.Reason, userMemoryMaxReason, false)
}
func validUserMemoryID(value string) bool {
	return validL1Text(value, userMemoryMaxIDBytes, true) && strings.TrimSpace(value) == value
}
func userMemoryEffectiveLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	return limit
}

func validUserMemoryResult(item *domainmemory.UserMemory) bool {
	if item == nil || !validUserMemoryID(item.ID) || !validL1Text(item.UserID, userMemoryMaxUserBytes, true) || strings.TrimSpace(item.UserID) != item.UserID ||
		item.Namespace != "user:"+item.UserID || domainmemory.ValidateUserMemoryType(item.Type) != nil || !validL1Text(item.Statement, 2<<20, true) ||
		strings.TrimSpace(item.Statement) != item.Statement || domainmemory.ValidateMemoryState(item.State) != nil ||
		!validL1StringSlice(item.EvidenceEventIDs, l1MaxCollectionEntries, userMemoryMaxIDBytes) ||
		(item.CreatedByEventID != "" && !validL1Text(string(item.CreatedByEventID), userMemoryMaxIDBytes, true)) ||
		(item.UpdatedByEventID != "" && !validL1Text(string(item.UpdatedByEventID), userMemoryMaxIDBytes, true)) ||
		!validL1Text(item.Sensitivity, 128, true) || !validL1Text(item.Scope, 256, true) ||
		math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || math.IsNaN(item.DecayScore) || math.IsInf(item.DecayScore, 0) ||
		!validL1Text(item.LifecycleStatus, 128, false) || !validL1Text(item.SupersededBy, userMemoryMaxIDBytes, false) ||
		!validL1Time(item.CreatedAt, false) || !validL1Time(item.UpdatedAt, false) {
		return false
	}
	return true
}

func userMemoryCreateResultMatches(item *domainmemory.UserMemory, input domainmemory.CreateUserMemoryInput) bool {
	if item == nil {
		return false
	}
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		userID = "ren"
	}
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = domainmemory.MemoryStateCandidate
	}
	sensitivity := strings.TrimSpace(input.Sensitivity)
	if sensitivity == "" {
		sensitivity = "normal"
	}
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "all_personas"
	}
	confidence := input.Confidence
	if confidence <= 0 {
		confidence = .5
	}
	return item.UserID == userID && item.Type == strings.TrimSpace(input.Type) && item.Statement == strings.TrimSpace(input.Statement) && item.State == state &&
		item.Sensitivity == sensitivity && item.Scope == scope && item.Confidence == confidence && item.Active && len(item.EvidenceEventIDs) == len(input.EvidenceEventIDs)
}
func userMemoryCandidateResultMatches(item *domainmemory.UserMemory, p userMemoryCandidatePayload) bool {
	if item == nil {
		return false
	}
	input := p.Input.domain()
	sensitivity := strings.TrimSpace(input.Sensitivity)
	if sensitivity == "" {
		sensitivity = "normal"
	}
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "all_personas"
	}
	confidence := input.Confidence
	if confidence <= 0 {
		confidence = .5
	}
	return item.UserID == input.UserID && item.Type == input.Type && item.Statement == input.Statement && item.State == domainmemory.MemoryStateCandidate &&
		item.Sensitivity == sensitivity && item.Scope == scope && item.Confidence == confidence && item.Active && len(item.EvidenceEventIDs) == len(input.EvidenceEventIDs)
}

func isNilUserMemoryOwner(owner UserMemoryGroupOwner) bool {
	if owner == nil {
		return true
	}
	value := reflect.ValueOf(owner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// UserMemoryStoreClient implements the existing local UserMemory method set.
type UserMemoryStoreClient struct{ client *Client }

func NewUserMemoryStoreClient(client *Client) *UserMemoryStoreClient {
	return &UserMemoryStoreClient{client: client}
}

var _ UserMemoryGroupOwner = (*UserMemoryStoreClient)(nil)

func (*UserMemoryStoreClient) Close() error { return nil }

func (s *UserMemoryStoreClient) call(ctx context.Context, op string, payload any, mutating bool, out any, validate func() error) error {
	if s == nil || s.client == nil {
		return NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if err := s.client.Call(ctx, GroupUserMemory, op, payload, out); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(); err != nil {
			if mutating {
				return NewError(ErrorCodeOutcomeUnknown, "user memory mutation result could not be validated")
			}
			return NewError(ErrorCodeStoreUnavailable, "user memory read result is invalid")
		}
	}
	return nil
}
func (s *UserMemoryStoreClient) CreateUserMemory(ctx context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	if !validUserMemoryCreateInput(input) {
		return nil, NewError(ErrorCodeSchemaRejected, "user memory create request rejected")
	}
	var result userMemoryItemResult
	err := s.call(ctx, userMemoryOpCreate, userMemoryCreatePayload{Input: userMemoryInputFromDomain(input)}, true, &result, func() error {
		if !validUserMemoryResult(result.Memory) || !userMemoryCreateResultMatches(result.Memory, input) {
			return errors.New("invalid user memory result")
		}
		return nil
	})
	return result.Memory, err
}
func (s *UserMemoryStoreClient) ListUserMemories(ctx context.Context, userID, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	p := userMemoryListPayload{UserID: userID, State: state, IncludeInactive: includeInactive, Limit: limit}
	if !validUserMemoryListPayload(p) {
		return nil, NewError(ErrorCodeSchemaRejected, "user memory list request rejected")
	}
	var result userMemoryListResult
	err := s.call(ctx, userMemoryOpList, p, false, &result, func() error {
		effective := userMemoryEffectiveLimit(limit)
		resolved := strings.TrimSpace(userID)
		if resolved == "" {
			resolved = "ren"
		}
		if len(result.Items) > effective {
			return errors.New("too many user memories")
		}
		for i := range result.Items {
			item := &result.Items[i]
			if !validUserMemoryResult(item) || item.UserID != resolved || (state != "" && item.State != state) || (!includeInactive && !item.Active) {
				return errors.New("invalid user memory result")
			}
		}
		return nil
	})
	return result.Items, err
}
func (s *UserMemoryStoreClient) FindUserMemoryByID(ctx context.Context, id string) (domainmemory.UserMemory, bool, error) {
	if !validUserMemoryID(id) {
		return domainmemory.UserMemory{}, false, NewError(ErrorCodeSchemaRejected, "user memory lookup request rejected")
	}
	var result userMemoryLookupResult
	err := s.call(ctx, userMemoryOpFind, userMemoryFindPayload{ID: id}, false, &result, func() error {
		if result.Found {
			if !validUserMemoryResult(result.Memory) || result.Memory.ID != id {
				return errors.New("invalid user memory result")
			}
		} else if result.Memory != nil {
			return errors.New("inconsistent user memory result")
		}
		return nil
	})
	if err != nil {
		return domainmemory.UserMemory{}, false, err
	}
	if !result.Found {
		return domainmemory.UserMemory{}, false, nil
	}
	return *result.Memory, true, nil
}
func (s *UserMemoryStoreClient) CreateUserMemoryCandidateWithRequest(ctx context.Context, requestID, actorID string, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error) {
	p := userMemoryCandidatePayload{RequestID: requestID, ActorID: actorID, Input: userMemoryInputFromDomain(input)}
	if !validUserMemoryCandidatePayload(p) {
		return nil, false, NewError(ErrorCodeSchemaRejected, "user memory candidate request rejected")
	}
	var result userMemoryCandidateResult
	err := s.call(ctx, userMemoryOpCandidate, p, true, &result, func() error {
		if !validUserMemoryResult(result.Memory) || !userMemoryCandidateResultMatches(result.Memory, p) {
			return errors.New("invalid user memory candidate result")
		}
		return nil
	})
	return result.Memory, result.IdempotentReplay, err
}
func (s *UserMemoryStoreClient) UpdateUserMemoryState(ctx context.Context, id, state, reason string) (*domainmemory.UserMemory, error) {
	p := userMemoryStatePayload{ID: id, State: state, Reason: reason}
	if !validUserMemoryStatePayload(p) {
		return nil, NewError(ErrorCodeSchemaRejected, "user memory state request rejected")
	}
	var result userMemoryItemResult
	err := s.call(ctx, userMemoryOpState, p, true, &result, func() error {
		if !validUserMemoryResult(result.Memory) || result.Memory.ID != id || result.Memory.State != state {
			return errors.New("invalid user memory state result")
		}
		return nil
	})
	return result.Memory, err
}
func (s *UserMemoryStoreClient) ForgetUserMemory(ctx context.Context, id, reason string) (*domainmemory.UserMemory, error) {
	p := userMemoryForgetPayload{ID: id, Reason: reason}
	if !validUserMemoryForgetPayload(p) {
		return nil, NewError(ErrorCodeSchemaRejected, "user memory forget request rejected")
	}
	var result userMemoryItemResult
	err := s.call(ctx, userMemoryOpForget, p, true, &result, func() error {
		if !validUserMemoryResult(result.Memory) || result.Memory.ID != id || result.Memory.Active {
			return errors.New("invalid user memory forget result")
		}
		return nil
	})
	return result.Memory, err
}
func (s *UserMemoryStoreClient) SupersedeUserMemory(ctx context.Context, id, newID, reason string) (*domainmemory.UserMemory, error) {
	p := userMemorySupersedePayload{ID: id, NewID: newID, Reason: reason}
	if !validUserMemorySupersedePayload(p) {
		return nil, NewError(ErrorCodeSchemaRejected, "user memory supersede request rejected")
	}
	var result userMemoryItemResult
	err := s.call(ctx, userMemoryOpSupersede, p, true, &result, func() error {
		if !validUserMemoryResult(result.Memory) || result.Memory.ID != id || result.Memory.Active || result.Memory.SupersededBy != newID {
			return errors.New("invalid user memory supersede result")
		}
		return nil
	})
	return result.Memory, err
}
