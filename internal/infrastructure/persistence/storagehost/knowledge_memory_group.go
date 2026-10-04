package storagehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	persistkm "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
)

const (
	GroupKnowledgeMemory = "knowledge_memory"

	knowledgeMemoryOpSavePersonalArchive   = "save_personal_archive"
	knowledgeMemoryOpListPersonalArchive   = "list_personal_archive"
	knowledgeMemoryOpSaveCreativeKnowledge = "save_creative_knowledge"
	knowledgeMemoryOpListCreativeKnowledge = "list_creative_knowledge"
	knowledgeMemoryOpSaveNewsKnowledge     = "save_news_knowledge"
	knowledgeMemoryOpListNewsKnowledge     = "list_news_knowledge"
	knowledgeMemoryOpSaveDailyRule         = "save_daily_rule"
	knowledgeMemoryOpListDailyRules        = "list_daily_rules"
	knowledgeMemoryOpSaveTemporalMarker    = "save_temporal_marker"
	knowledgeMemoryOpListTemporalMarkers   = "list_temporal_markers"
	knowledgeMemoryOpSaveDreamRun          = "save_dream_run"
	knowledgeMemoryOpListDreamRuns         = "list_dream_runs"
	knowledgeMemoryOpSearchPublic          = "search_public"
	knowledgeMemoryOpSearchUser            = "search_user"
	knowledgeMemoryOpProposeCandidate      = "propose_creative_candidate"

	knowledgeMemoryMaxPayload = 2 << 20
	knowledgeMemoryMaxList    = 100
	knowledgeMemoryMaxText    = 1 << 16
)

// KnowledgeMemoryGroupOwner is exactly the canonical typed store and indexed
// search surface; durable candidate recovery is a separate owner capability.
type KnowledgeMemoryGroupOwner interface {
	persistkm.Store
	appkm.IndexedSearcher
}

type knowledgeMemoryRecoveryOwner interface {
	SaveKnowledgeMemoryForStorageHostOperation(context.Context, persistkm.KnowledgeMemoryStorageHostOperationIdentity, persistkm.KnowledgeMemoryStorageHostSave) error
	LookupKnowledgeMemoryStorageHostOperationReceipt(context.Context, persistkm.KnowledgeMemoryStorageHostOperationIdentity, persistkm.KnowledgeMemoryStorageHostSave) (bool, error)
	SaveCreativeCandidateForStorageHostOperation(context.Context, persistkm.KnowledgeMemoryStorageHostOperationIdentity, domainkm.CreativeKnowledgeItem, persistkm.KnowledgeMemoryRequestReceipt) (persistkm.CreativeCandidateStorageHostResult, error)
	LookupCreativeCandidateStorageHostOperationReceipt(context.Context, persistkm.KnowledgeMemoryStorageHostOperationIdentity, domainkm.CreativeKnowledgeItem, persistkm.KnowledgeMemoryRequestReceipt) (persistkm.CreativeCandidateStorageHostResult, bool, error)
}

// KnowledgeMemoryExecutionScope is the closed wire representation of the
// caller's already-authenticated Tool execution scope.
type KnowledgeMemoryExecutionScope struct {
	RequestID            string                          `json:"request_id"`
	ActorKind            domaintool.ActorKind            `json:"actor_kind"`
	ActorID              string                          `json:"actor_id"`
	AuthenticatedUserID  string                          `json:"authenticated_user_id,omitempty"`
	AllowedDataScopes    []string                        `json:"allowed_data_scopes"`
	AuthenticationSource domaintool.AuthenticationSource `json:"authentication_source"`
	AgentRole            string                          `json:"agent_role,omitempty"`
	Purpose              string                          `json:"purpose,omitempty"`
}

type knowledgeMemoryItemPayload[T any] struct {
	Item T `json:"item"`
}

type knowledgeMemoryListPayload struct {
	Limit int `json:"limit"`
}

type knowledgeMemoryListResult[T any] struct {
	Items []T `json:"items"`
}

type knowledgeMemorySearchPayload struct {
	Scope      KnowledgeMemoryExecutionScope `json:"scope"`
	Query      string                        `json:"query"`
	RecordType string                        `json:"record_type,omitempty"`
	Limit      int                           `json:"limit"`
}

type knowledgeMemorySearchResult struct {
	Items []appkm.SearchResult `json:"items"`
}

type knowledgeMemoryCandidatePayload struct {
	Scope     KnowledgeMemoryExecutionScope           `json:"scope"`
	Candidate domainkm.CreativeKnowledgeItem          `json:"candidate"`
	Receipt   persistkm.KnowledgeMemoryRequestReceipt `json:"receipt"`
}

// RegisterKnowledgeMemoryGroup registers only the canonical typed
// knowledge-memory operations.
func RegisterKnowledgeMemoryGroup(handler *Handler, owner KnowledgeMemoryGroupOwner) error {
	if handler == nil || nilKnowledgeMemoryValue(owner) {
		return errors.New("storagehost: knowledge memory group needs a handler and owner")
	}
	recoveryOwner, ok := owner.(knowledgeMemoryRecoveryOwner)
	if !ok || nilKnowledgeMemoryValue(recoveryOwner) {
		return errors.New("storagehost: knowledge memory owner lacks durable candidate recovery")
	}
	registrations := []func() error{
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSavePersonalArchive, domainkm.ValidatePersonalArchiveEntry, func(item domainkm.PersonalArchiveEntry) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySavePersonalArchive, PersonalArchive: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListPersonalArchive, domainkm.ValidatePersonalArchiveEntry, owner.ListPersonalArchiveEntries)
		},
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSaveCreativeKnowledge, domainkm.ValidateCreativeKnowledgeItem, func(item domainkm.CreativeKnowledgeItem) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySaveCreativeKnowledge, CreativeKnowledge: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListCreativeKnowledge, domainkm.ValidateCreativeKnowledgeItem, owner.ListCreativeKnowledgeItems)
		},
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSaveNewsKnowledge, domainkm.ValidateNewsKnowledgeItem, func(item domainkm.NewsKnowledgeItem) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySaveNewsKnowledge, NewsKnowledge: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListNewsKnowledge, domainkm.ValidateNewsKnowledgeItem, owner.ListNewsKnowledgeItems)
		},
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSaveDailyRule, domainkm.ValidateDailyIntakeRule, func(item domainkm.DailyIntakeRule) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySaveDailyRule, DailyRule: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListDailyRules, domainkm.ValidateDailyIntakeRule, owner.ListDailyIntakeRules)
		},
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSaveTemporalMarker, domainkm.ValidateTemporalMemoryMarker, func(item domainkm.TemporalMemoryMarker) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySaveTemporalMarker, TemporalMarker: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListTemporalMarkers, domainkm.ValidateTemporalMemoryMarker, owner.ListTemporalMemoryMarkers)
		},
		func() error {
			return registerKnowledgeMemorySave(handler, recoveryOwner, knowledgeMemoryOpSaveDreamRun, domainkm.ValidateDreamConsolidationRun, func(item domainkm.DreamConsolidationRun) persistkm.KnowledgeMemoryStorageHostSave {
				return persistkm.KnowledgeMemoryStorageHostSave{Kind: persistkm.KnowledgeMemorySaveDreamRun, DreamRun: &item}
			})
		},
		func() error {
			return registerKnowledgeMemoryList(handler, knowledgeMemoryOpListDreamRuns, domainkm.ValidateDreamConsolidationRun, owner.ListDreamConsolidationRuns)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := registerKnowledgeMemorySearch(handler, owner, knowledgeMemoryOpSearchPublic, appkm.SearchScopePublic); err != nil {
		return err
	}
	if err := registerKnowledgeMemorySearch(handler, owner, knowledgeMemoryOpSearchUser, appkm.SearchScopeUser); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupKnowledgeMemory, knowledgeMemoryOpProposeCandidate, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		payload, err := decodeKnowledgeMemoryCandidate(mutation.Payload)
		if err != nil {
			return nil, ownerRolledBack(knowledgeMemorySchemaError("candidate"))
		}
		identity := knowledgeMemoryOperationIdentity(mutation, knowledgeMemoryOpProposeCandidate)
		result, err := recoveryOwner.SaveCreativeCandidateForStorageHostOperation(ctx, identity, payload.Candidate, payload.Receipt)
		if errors.Is(err, persistkm.ErrKnowledgeMemoryRequestConflict) {
			return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "knowledge memory candidate conflicts with owner state"))
		}
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "knowledge memory candidate owner write failed")
		}
		return result, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		payload, err := decodeKnowledgeMemoryCandidate(mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := recoveryOwner.LookupCreativeCandidateStorageHostOperationReceipt(ctx, knowledgeMemoryOperationIdentity(mutation, knowledgeMemoryOpProposeCandidate), payload.Candidate, payload.Receipt)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		if !boundedKnowledgeMemoryValue(result) {
			return UnknownOutcome(), nil
		}
		return Committed(result), nil
	})
}

func registerKnowledgeMemorySave[T any](handler *Handler, owner knowledgeMemoryRecoveryOwner, operation string, validate func(T) error, wrap func(T) persistkm.KnowledgeMemoryStorageHostSave) error {
	return handler.RegisterRecoverable(GroupKnowledgeMemory, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload knowledgeMemoryItemPayload[T]
		if decodeKnowledgeMemoryPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil || !boundedKnowledgeMemoryValue(payload.Item) {
			return nil, ownerRolledBack(knowledgeMemorySchemaError(operation))
		}
		if err := owner.SaveKnowledgeMemoryForStorageHostOperation(ctx, knowledgeMemoryOperationIdentity(mutation, operation), wrap(payload.Item)); err != nil {
			if errors.Is(err, persistkm.ErrKnowledgeMemoryRequestConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "knowledge memory operation conflicts with owner state"))
			}
			return nil, knowledgeMemoryStoreError(operation)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload knowledgeMemoryItemPayload[T]
		if decodeKnowledgeMemoryPayload(mutation.Payload, &payload) != nil || validate(payload.Item) != nil || !boundedKnowledgeMemoryValue(payload.Item) {
			return UnknownOutcome(), nil
		}
		found, err := owner.LookupKnowledgeMemoryStorageHostOperationReceipt(ctx, knowledgeMemoryOperationIdentity(mutation, operation), wrap(payload.Item))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	})
}

func registerKnowledgeMemoryList[T any](handler *Handler, operation string, validate func(T) error, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupKnowledgeMemory, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload knowledgeMemoryListPayload
		if decodeKnowledgeMemoryPayload(raw, &payload) != nil || payload.Limit < 0 || payload.Limit > knowledgeMemoryMaxList {
			return nil, knowledgeMemorySchemaError(operation)
		}
		limit := payload.Limit
		if limit == 0 {
			limit = 50
		}
		items, err := list(ctx, limit)
		if err != nil || len(items) > limit || !validKnowledgeMemoryItems(items, validate) {
			return nil, knowledgeMemoryStoreError(operation)
		}
		return knowledgeMemoryListResult[T]{Items: items}, nil
	})
}

func registerKnowledgeMemorySearch(handler *Handler, owner KnowledgeMemoryGroupOwner, operation, scopeName string) error {
	return handler.Register(GroupKnowledgeMemory, operation, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload knowledgeMemorySearchPayload
		if decodeKnowledgeMemoryPayload(raw, &payload) != nil {
			return nil, knowledgeMemorySchemaError(operation)
		}
		scope, err := payload.Scope.toDomain()
		if err != nil || !knowledgeMemoryScopeAllowsSearch(scope, scopeName) {
			return nil, NewError(ErrorCodeUnauthorized, "knowledge memory search scope rejected")
		}
		userID := ""
		if scopeName == appkm.SearchScopeUser {
			userID = scope.AuthenticatedUserID
		}
		request := appkm.SearchRequest{Scope: appkm.SearchScope{Scope: scopeName, UserID: userID}, Query: payload.Query, RecordType: payload.RecordType, Limit: payload.Limit}
		if request.Validate() != nil || !boundedKnowledgeMemoryValue(payload) {
			return nil, knowledgeMemorySchemaError(operation)
		}
		items, err := owner.Search(ctx, request)
		if err != nil || !validKnowledgeMemorySearchResults(items, request) {
			return nil, knowledgeMemoryStoreError(operation)
		}
		return knowledgeMemorySearchResult{Items: items}, nil
	})
}

func decodeKnowledgeMemoryCandidate(raw json.RawMessage) (knowledgeMemoryCandidatePayload, error) {
	var payload knowledgeMemoryCandidatePayload
	if decodeKnowledgeMemoryPayload(raw, &payload) != nil || !validKnowledgeMemoryCandidate(payload) {
		return payload, errors.New("knowledge memory candidate payload rejected")
	}
	return payload, nil
}

func validKnowledgeMemoryCandidate(payload knowledgeMemoryCandidatePayload) bool {
	scope, err := payload.Scope.toDomain()
	if err != nil || !knowledgeMemoryAgentUserScope(scope) || domainkm.ValidateCreativeCandidate(payload.Candidate) != nil {
		return false
	}
	r := payload.Receipt
	if r.ActionID.Validate() != nil || !validKnowledgeMemoryText(r.UserID, 512, true) || !validKnowledgeMemoryText(r.ActorID, 512, true) ||
		!validKnowledgeMemoryText(r.PayloadHash, 512, true) || !validKnowledgeMemoryText(r.ItemID, 512, true) || r.CreatedAt.IsZero() {
		return false
	}
	return payload.Candidate.UserID == scope.AuthenticatedUserID && r.UserID == scope.AuthenticatedUserID &&
		r.ActorID == scope.ActorID && r.ItemID == payload.Candidate.ItemID && boundedKnowledgeMemoryValue(payload)
}

func knowledgeMemoryOperationIdentity(mutation MutationMetadata, operation string) persistkm.KnowledgeMemoryStorageHostOperationIdentity {
	return persistkm.KnowledgeMemoryStorageHostOperationIdentity{
		OpID: mutation.OpID, PayloadSHA256: payloadHash(GroupKnowledgeMemory, operation, mutation.Payload),
		WriterGeneration: mutation.JournalGeneration,
	}
}

func (scope KnowledgeMemoryExecutionScope) toDomain() (domaintool.ToolExecutionScope, error) {
	domain := domaintool.ToolExecutionScope{
		RequestID: scope.RequestID, ActorKind: scope.ActorKind, ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, AllowedDataScopes: append([]string(nil), scope.AllowedDataScopes...),
		AuthenticationSource: scope.AuthenticationSource, AgentRole: scope.AgentRole, Purpose: scope.Purpose,
	}
	if domain.Validate() != nil || !validKnowledgeMemoryText(scope.RequestID, 512, true) || !validKnowledgeMemoryText(scope.ActorID, 512, true) ||
		!validKnowledgeMemoryText(scope.AuthenticatedUserID, 512, false) || !validKnowledgeMemoryText(scope.AgentRole, 512, false) ||
		!validKnowledgeMemoryText(scope.Purpose, 2048, false) || len(scope.AllowedDataScopes) > 3 || !boundedKnowledgeMemoryValue(scope) {
		return domaintool.ToolExecutionScope{}, errors.New("knowledge memory execution scope rejected")
	}
	return domain, nil
}

func knowledgeMemoryScopeAllowsSearch(scope domaintool.ToolExecutionScope, name string) bool {
	if name == appkm.SearchScopePublic {
		return scope.Allows(domaintool.DataScopePublic)
	}
	return knowledgeMemoryAgentUserScope(scope)
}

func knowledgeMemoryAgentUserScope(scope domaintool.ToolExecutionScope) bool {
	return scope.ActorKind == domaintool.ActorKindAgent &&
		scope.AuthenticationSource == domaintool.AuthenticationSourceAgentOrchestrator &&
		scope.AuthenticatedUserID != "" && scope.Allows(domaintool.DataScopeUser)
}

func validKnowledgeMemorySearchResults(items []appkm.SearchResult, request appkm.SearchRequest) bool {
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	if len(items) > limit {
		return false
	}
	for _, item := range items {
		if item.Scope != request.Scope.Scope || item.UserID != request.Scope.UserID ||
			(item.RecordType != "creative_knowledge" && item.RecordType != "news_knowledge") ||
			(request.RecordType != "" && item.RecordType != request.RecordType) ||
			!validKnowledgeMemoryText(item.RecordID, 512, true) || !validKnowledgeMemoryText(item.Title, knowledgeMemoryMaxText, true) ||
			!validKnowledgeMemoryText(item.Summary, knowledgeMemoryMaxText, false) ||
			(item.Visibility != "public" && item.Visibility != "private") || !validKnowledgeMemorySHA256(item.ContentSHA256) {
			return false
		}
		if request.Scope.Scope == appkm.SearchScopePublic && item.Visibility != "public" {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, item.SourceUpdatedAt); err != nil {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, item.IndexedAt); err != nil {
			return false
		}
	}
	return boundedKnowledgeMemoryValue(items)
}

func decodeKnowledgeMemoryPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > knowledgeMemoryMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("knowledge memory payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("knowledge memory payload rejected")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("knowledge memory payload rejected")
	}
	return nil
}

func validKnowledgeMemoryItems[T any](items []T, validate func(T) error) bool {
	if len(items) > knowledgeMemoryMaxList {
		return false
	}
	for _, item := range items {
		if validate(item) != nil {
			return false
		}
	}
	return boundedKnowledgeMemoryValue(items)
}

func validKnowledgeMemoryText(value string, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 || strings.TrimSpace(value) != value {
		return false
	}
	return !required || value != ""
}

func validKnowledgeMemorySHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func boundedKnowledgeMemoryValue(value any) bool {
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= knowledgeMemoryMaxPayload
}

func knowledgeMemorySchemaError(operation string) *Error {
	return NewError(ErrorCodeSchemaRejected, "knowledge memory "+operation+" rejected")
}

func knowledgeMemoryStoreError(operation string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "knowledge memory "+operation+" failed")
}

func nilKnowledgeMemoryValue(value any) bool {
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

// KnowledgeMemoryClient implements the canonical store and indexed search
// ports over the closed storage-host operations.
type KnowledgeMemoryClient struct{ client *Client }

func NewKnowledgeMemoryClient(client *Client) *KnowledgeMemoryClient {
	return &KnowledgeMemoryClient{client: client}
}

func (store *KnowledgeMemoryClient) SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSavePersonalArchive, item, domainkm.ValidatePersonalArchiveEntry)
}

func (store *KnowledgeMemoryClient) ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListPersonalArchive, limit, domainkm.ValidatePersonalArchiveEntry)
}

func (store *KnowledgeMemoryClient) SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSaveCreativeKnowledge, item, domainkm.ValidateCreativeKnowledgeItem)
}

func (store *KnowledgeMemoryClient) ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListCreativeKnowledge, limit, domainkm.ValidateCreativeKnowledgeItem)
}

func (store *KnowledgeMemoryClient) SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSaveNewsKnowledge, item, domainkm.ValidateNewsKnowledgeItem)
}

func (store *KnowledgeMemoryClient) ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListNewsKnowledge, limit, domainkm.ValidateNewsKnowledgeItem)
}

func (store *KnowledgeMemoryClient) SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSaveDailyRule, item, domainkm.ValidateDailyIntakeRule)
}

func (store *KnowledgeMemoryClient) ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListDailyRules, limit, domainkm.ValidateDailyIntakeRule)
}

func (store *KnowledgeMemoryClient) SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSaveTemporalMarker, item, domainkm.ValidateTemporalMemoryMarker)
}

func (store *KnowledgeMemoryClient) ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListTemporalMarkers, limit, domainkm.ValidateTemporalMemoryMarker)
}

func (store *KnowledgeMemoryClient) SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error {
	return callKnowledgeMemorySave(store, ctx, knowledgeMemoryOpSaveDreamRun, item, domainkm.ValidateDreamConsolidationRun)
}

func (store *KnowledgeMemoryClient) ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error) {
	return callKnowledgeMemoryList(store, ctx, knowledgeMemoryOpListDreamRuns, limit, domainkm.ValidateDreamConsolidationRun)
}

func (store *KnowledgeMemoryClient) Search(ctx context.Context, request appkm.SearchRequest) ([]appkm.SearchResult, error) {
	if request.Validate() != nil {
		return nil, knowledgeMemorySchemaError("search")
	}
	scope, err := knowledgeMemoryScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	operation := knowledgeMemoryOpSearchPublic
	if request.Scope.Scope == appkm.SearchScopeUser {
		operation = knowledgeMemoryOpSearchUser
		if request.Scope.UserID != scope.AuthenticatedUserID || !knowledgeMemoryAgentUserScope(scope.toUncheckedDomain()) {
			return nil, NewError(ErrorCodeUnauthorized, "knowledge memory search scope rejected")
		}
	} else if !scope.toUncheckedDomain().Allows(domaintool.DataScopePublic) {
		return nil, NewError(ErrorCodeUnauthorized, "knowledge memory search scope rejected")
	}
	payload := knowledgeMemorySearchPayload{Scope: scope, Query: request.Query, RecordType: request.RecordType, Limit: request.Limit}
	var result knowledgeMemorySearchResult
	if err := store.client.Call(ctx, GroupKnowledgeMemory, operation, payload, &result); err != nil {
		return nil, err
	}
	if !validKnowledgeMemorySearchResults(result.Items, request) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "knowledge memory search response malformed")
	}
	return result.Items, nil
}

// SaveCreativeCandidateWithReceipt writes a private candidate using the
// authenticated Agent scope carried by ctx.
func (store *KnowledgeMemoryClient) SaveCreativeCandidateWithReceipt(ctx context.Context, candidate domainkm.CreativeKnowledgeItem, receipt persistkm.KnowledgeMemoryRequestReceipt) (bool, error) {
	scope, err := knowledgeMemoryScopeFromContext(ctx)
	if err != nil {
		return false, err
	}
	payload := knowledgeMemoryCandidatePayload{Scope: scope, Candidate: candidate, Receipt: receipt}
	if !validKnowledgeMemoryCandidate(payload) {
		return false, knowledgeMemorySchemaError("candidate")
	}
	var result persistkm.CreativeCandidateStorageHostResult
	if err := store.client.Call(ctx, GroupKnowledgeMemory, knowledgeMemoryOpProposeCandidate, payload, &result); err != nil {
		var wire *Error
		if errors.As(err, &wire) && wire.Code == ErrorCodeDuplicateConflict {
			return false, persistkm.ErrKnowledgeMemoryRequestConflict
		}
		return false, err
	}
	return result.Replayed, nil
}

func callKnowledgeMemorySave[T any](store *KnowledgeMemoryClient, ctx context.Context, operation string, item T, validate func(T) error) error {
	if validate(item) != nil || !boundedKnowledgeMemoryValue(item) {
		return knowledgeMemorySchemaError(operation)
	}
	return store.client.Call(ctx, GroupKnowledgeMemory, operation, knowledgeMemoryItemPayload[T]{Item: item}, nil)
}

func callKnowledgeMemoryList[T any](store *KnowledgeMemoryClient, ctx context.Context, operation string, limit int, validate func(T) error) ([]T, error) {
	if limit < 0 || limit > knowledgeMemoryMaxList {
		return nil, knowledgeMemorySchemaError(operation)
	}
	var result knowledgeMemoryListResult[T]
	if err := store.client.Call(ctx, GroupKnowledgeMemory, operation, knowledgeMemoryListPayload{Limit: limit}, &result); err != nil {
		return nil, err
	}
	if !validKnowledgeMemoryItems(result.Items, validate) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "knowledge memory list response malformed")
	}
	return result.Items, nil
}

func knowledgeMemoryScopeFromContext(ctx context.Context) (KnowledgeMemoryExecutionScope, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return KnowledgeMemoryExecutionScope{}, NewError(ErrorCodeUnauthorized, "knowledge memory execution scope missing or invalid")
	}
	wire := KnowledgeMemoryExecutionScope{
		RequestID: scope.RequestID, ActorKind: scope.ActorKind, ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, AllowedDataScopes: append([]string(nil), scope.AllowedDataScopes...),
		AuthenticationSource: scope.AuthenticationSource, AgentRole: scope.AgentRole, Purpose: scope.Purpose,
	}
	if _, err := wire.toDomain(); err != nil {
		return KnowledgeMemoryExecutionScope{}, NewError(ErrorCodeUnauthorized, "knowledge memory execution scope missing or invalid")
	}
	return wire, nil
}

func (scope KnowledgeMemoryExecutionScope) toUncheckedDomain() domaintool.ToolExecutionScope {
	return domaintool.ToolExecutionScope{
		RequestID: scope.RequestID, ActorKind: scope.ActorKind, ActorID: scope.ActorID,
		AuthenticatedUserID: scope.AuthenticatedUserID, AllowedDataScopes: append([]string(nil), scope.AllowedDataScopes...),
		AuthenticationSource: scope.AuthenticationSource, AgentRole: scope.AgentRole, Purpose: scope.Purpose,
	}
}

var _ persistkm.Store = (*KnowledgeMemoryClient)(nil)
var _ appkm.IndexedSearcher = (*KnowledgeMemoryClient)(nil)
