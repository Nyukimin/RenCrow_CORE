package storagehost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/repository"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
)

const (
	GroupGlossary             = "glossary"
	maxGlossaryPayload        = 96 << 10
	maxGlossaryResult         = 1 << 20
	maxGlossaryListSize       = 100
	maxGlossaryViewerPageSize = 200
	maxGlossaryViewerTotal    = int(glossarypersistence.MaxGlossaryViewerPageTotal)
)

// GlossaryGroupOwner is the closed persistence API required by runtime feeds,
// repository consumers, candidate flows and the indexed Viewer lookup. Writes
// must bind their storage-host identity and receipt in the owner's SQLite
// transaction.
type GlossaryGroupOwner interface {
	repository.GlossaryRepository
	SaveCandidate(context.Context, entity.GlossaryCandidate) error
	FindCandidateByID(context.Context, string) (entity.GlossaryCandidate, bool, error)
	SaveForStorageHostOperation(context.Context, glossarypersistence.GlossaryOperationIdentity, *entity.GlossaryItem) error
	DeleteForStorageHostOperation(context.Context, glossarypersistence.GlossaryOperationIdentity, string) error
	SaveCandidateForStorageHostOperation(context.Context, glossarypersistence.GlossaryOperationIdentity, entity.GlossaryCandidate) error
	LookupStorageHostOperationReceipt(context.Context, glossarypersistence.GlossaryOperationIdentity) (glossarypersistence.GlossaryOperationReceipt, bool, error)
	LookupStorageHost(context.Context, glossaryapp.LookupRequest) (glossaryapp.LookupResult, error)
	FindViewerPage(context.Context, int) (int, []*entity.GlossaryItem, error)
}

type glossaryItemPayload struct {
	Item *entity.GlossaryItem `json:"item"`
}

type glossaryTermPayload struct {
	Term string `json:"term"`
}

type glossaryRecentPayload struct {
	Limit int `json:"limit"`
}

type glossaryCategoryPayload struct {
	Category string `json:"category"`
	Limit    int    `json:"limit"`
}

type glossaryIDPayload struct {
	ID string `json:"id"`
}

type glossaryCandidatePayload struct {
	Candidate entity.GlossaryCandidate `json:"candidate"`
}

type glossaryCandidateResult struct {
	Found     bool                      `json:"found"`
	Candidate *entity.GlossaryCandidate `json:"candidate,omitempty"`
}

type glossaryItemsResult struct {
	Items []*entity.GlossaryItem `json:"items"`
}

type glossaryViewerPagePayload struct {
	Limit int `json:"limit"`
}

type glossaryViewerPageResult struct {
	Total int                    `json:"total"`
	Items []*entity.GlossaryItem `json:"items"`
}

type glossaryItemResult struct {
	Item *entity.GlossaryItem `json:"item,omitempty"`
}

type glossaryLookupPayload struct {
	Operation string `json:"operation"`
	Term      string `json:"term"`
	Category  string `json:"category"`
	Limit     int    `json:"limit"`
}

// RegisterGlossaryGroup exposes only canonical repository, candidate and
// indexed-lookup operations. Every mutation is recoverable from an owner
// receipt committed atomically with its data row.
func RegisterGlossaryGroup(handler *Handler, owner GlossaryGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: glossary group needs a handler and an owner")
	}
	if err := handler.RegisterRecoverable(GroupGlossary, "save", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload glossaryItemPayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || validateGlossaryItem(payload.Item) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "glossary item rejected"))
		}
		identity := glossaryMutationIdentity(mutation, "save")
		if err := owner.SaveForStorageHostOperation(ctx, identity, payload.Item); err != nil {
			return nil, glossaryMutationOwnerError(err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload glossaryItemPayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || validateGlossaryItem(payload.Item) != nil {
			return UnknownOutcome(), nil
		}
		return reconcileGlossaryMutation(ctx, owner, mutation, "save")
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupGlossary, "find_by_term", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryTermPayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryText(payload.Term, 200, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary term query rejected")
		}
		item, err := owner.FindByTerm(ctx, payload.Term)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NewError("glossary_term_not_found", "glossary term does not exist")
		}
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary term query failed")
		}
		if validateGlossaryItem(item) != nil || item.Term != payload.Term {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary owner returned a malformed item")
		}
		return boundedGlossaryResult(glossaryItemResult{Item: item})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupGlossary, "find_recent", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryRecentPayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryLimit(payload.Limit, maxGlossaryListSize, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary recent query rejected")
		}
		items, err := owner.FindRecent(ctx, payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary recent query failed")
		}
		if err := validateGlossaryItems(items, payload.Limit); err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary owner returned a malformed recent result")
		}
		if items == nil {
			items = []*entity.GlossaryItem{}
		}
		return boundedGlossaryResult(glossaryItemsResult{Items: items})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupGlossary, "viewer_page", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryViewerPagePayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryLimit(payload.Limit, maxGlossaryViewerPageSize, false) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary Viewer page query rejected")
		}
		total, items, err := owner.FindViewerPage(ctx, payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary Viewer page query failed")
		}
		if items == nil {
			items = []*entity.GlossaryItem{}
		}
		if !validGlossaryViewerPage(total, items, payload.Limit) {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary owner returned a malformed Viewer page")
		}
		return boundedGlossaryResult(glossaryViewerPageResult{Total: total, Items: items})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupGlossary, "find_by_category", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryCategoryPayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryText(payload.Category, 64, true) || !validGlossaryLimit(payload.Limit, maxGlossaryListSize, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary category query rejected")
		}
		items, err := owner.FindByCategory(ctx, payload.Category, payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary category query failed")
		}
		if err := validateGlossaryItems(items, payload.Limit); err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary owner returned a malformed category result")
		}
		if items == nil {
			items = []*entity.GlossaryItem{}
		}
		return boundedGlossaryResult(glossaryItemsResult{Items: items})
	}); err != nil {
		return err
	}
	if err := handler.RegisterRecoverable(GroupGlossary, "delete", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload glossaryIDPayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || !validGlossaryText(payload.ID, 256, true) {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "glossary delete request rejected"))
		}
		identity := glossaryMutationIdentity(mutation, "delete")
		if err := owner.DeleteForStorageHostOperation(ctx, identity, payload.ID); err != nil {
			return nil, glossaryMutationOwnerError(err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload glossaryIDPayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || !validGlossaryText(payload.ID, 256, true) {
			return UnknownOutcome(), nil
		}
		return reconcileGlossaryMutation(ctx, owner, mutation, "delete")
	}); err != nil {
		return err
	}
	if err := handler.RegisterRecoverable(GroupGlossary, "save_candidate", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload glossaryCandidatePayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || entity.ValidateGlossaryCandidate(payload.Candidate) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "glossary candidate rejected"))
		}
		identity := glossaryMutationIdentity(mutation, "save_candidate")
		if err := owner.SaveCandidateForStorageHostOperation(ctx, identity, payload.Candidate); err != nil {
			return nil, glossaryMutationOwnerError(err)
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload glossaryCandidatePayload
		if decodeGlossaryPayload(mutation.Payload, &payload) != nil || entity.ValidateGlossaryCandidate(payload.Candidate) != nil {
			return UnknownOutcome(), nil
		}
		return reconcileGlossaryMutation(ctx, owner, mutation, "save_candidate")
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupGlossary, "find_candidate_by_id", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryIDPayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryText(payload.ID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary candidate lookup rejected")
		}
		candidate, found, err := owner.FindCandidateByID(ctx, payload.ID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary candidate lookup failed")
		}
		result := glossaryCandidateResult{Found: found}
		if found {
			if candidate.ID != payload.ID || entity.ValidateGlossaryCandidate(candidate) != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "glossary owner returned a malformed candidate")
			}
			result.Candidate = &candidate
		}
		return boundedGlossaryResult(result)
	}); err != nil {
		return err
	}
	return handler.Register(GroupGlossary, "lookup", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload glossaryLookupPayload
		if decodeGlossaryPayload(raw, &payload) != nil || !validGlossaryLookup(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "glossary indexed lookup rejected")
		}
		result, err := owner.LookupStorageHost(ctx, glossaryapp.LookupRequest{Operation: payload.Operation, Term: payload.Term, Category: payload.Category, Limit: payload.Limit})
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary indexed lookup failed")
		}
		if validateGlossaryLookupResult(result, payload.Operation) != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "glossary indexed lookup result is malformed")
		}
		return boundedGlossaryResult(result)
	})
}

func decodeGlossaryPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxGlossaryPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("glossary payload is not a bounded object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("glossary payload has trailing data")
	}
	return nil
}

func validateGlossaryItem(item *entity.GlossaryItem) error {
	if item == nil || !validGlossaryText(item.ID, 256, true) || !validGlossaryText(item.Term, 200, true) || !validGlossaryText(item.Explanation, 4096, true) || !validGlossaryText(item.Source, 2048, true) || !validGlossaryText(item.Category, 64, true) || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return errors.New("glossary item fields are invalid or exceed bounds")
	}
	return nil
}

func validGlossaryText(value string, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func validGlossaryLimit(limit, maximum int, allowZero bool) bool {
	return limit <= maximum && (limit > 0 || (allowZero && limit == 0))
}

func validateGlossaryItems(items []*entity.GlossaryItem, requestedLimit int) error {
	if len(items) > requestedLimit || len(items) > maxGlossaryListSize {
		return errors.New("glossary list exceeds requested limit")
	}
	for _, item := range items {
		if err := validateGlossaryItem(item); err != nil {
			return err
		}
	}
	return nil
}

func validGlossaryViewerPage(total int, items []*entity.GlossaryItem, requestedLimit int) bool {
	if total < 0 || total > maxGlossaryViewerTotal || requestedLimit < 1 || requestedLimit > maxGlossaryViewerPageSize || items == nil || len(items) > requestedLimit || len(items) > maxGlossaryViewerPageSize || total < len(items) {
		return false
	}
	for _, item := range items {
		if validateGlossaryItem(item) != nil {
			return false
		}
	}
	return true
}

func validGlossaryLookup(payload glossaryLookupPayload) bool {
	if payload.Limit < 0 || payload.Limit > 20 || !validGlossaryText(payload.Term, 200, false) || !validGlossaryText(payload.Category, 64, false) {
		return false
	}
	switch payload.Operation {
	case "define_term":
		return validGlossaryText(payload.Term, 200, true) && strings.TrimSpace(payload.Category) == ""
	case "list_category":
		return validGlossaryText(payload.Category, 64, true) && strings.TrimSpace(payload.Term) == ""
	default:
		return false
	}
}

func validateGlossaryLookupResult(result glossaryapp.LookupResult, operation string) error {
	if result.Operation != operation || len(result.Items) > 20 || result.Items == nil {
		return errors.New("glossary lookup result shape is invalid")
	}
	for _, item := range result.Items {
		if !validGlossaryText(item.ID, 256, true) || !validGlossaryText(item.Term, 200, true) || !validGlossaryText(item.Explanation, 4096, true) || !validGlossaryText(item.Source, 2048, true) || !validGlossaryText(item.Category, 64, true) || !validGlossaryText(item.UpdatedAt, 64, true) {
			return errors.New("glossary lookup row exceeds bounds")
		}
	}
	return nil
}

func boundedGlossaryResult(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxGlossaryResult {
		return nil, NewError(ErrorCodeStoreUnavailable, "glossary result exceeds the response bound")
	}
	return value, nil
}

func glossaryMutationIdentity(mutation MutationMetadata, operation string) glossarypersistence.GlossaryOperationIdentity {
	return glossarypersistence.GlossaryOperationIdentity{OpID: mutation.OpID, Operation: operation, PayloadHash: payloadHash(GroupGlossary, operation, mutation.Payload)}
}

func glossaryMutationOwnerError(err error) error {
	if errors.Is(err, glossarypersistence.ErrGlossaryOperationConflict) {
		return ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "glossary op_id is bound to a different owner mutation"))
	}
	return err
}

func reconcileGlossaryMutation(ctx context.Context, owner GlossaryGroupOwner, mutation MutationMetadata, operation string) (ReconcileDecision, error) {
	identity := glossaryMutationIdentity(mutation, operation)
	receipt, found, err := owner.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil {
		return UnknownOutcome(), nil
	}
	if !found {
		return ConfirmedNotCommitted(), nil
	}
	if receipt.Identity != identity || !bytes.Equal(bytes.TrimSpace(receipt.ResultJSON), []byte("null")) {
		return UnknownOutcome(), nil
	}
	return Committed(nil), nil
}

type GlossaryStoreClient struct{ client *Client }

func NewGlossaryStoreClient(client *Client) *GlossaryStoreClient {
	return &GlossaryStoreClient{client: client}
}

// Close does not close the shared RPC client.
func (store *GlossaryStoreClient) Close() error { return nil }

func (store *GlossaryStoreClient) Save(ctx context.Context, item *entity.GlossaryItem) error {
	if validateGlossaryItem(item) != nil {
		return NewError(ErrorCodeSchemaRejected, "glossary item rejected")
	}
	return store.client.Call(ctx, GroupGlossary, "save", glossaryItemPayload{Item: item}, nil)
}

func (store *GlossaryStoreClient) FindByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	var result glossaryItemResult
	err := store.client.Call(ctx, GroupGlossary, "find_by_term", glossaryTermPayload{Term: term}, &result)
	if err != nil {
		var wire *Error
		if errors.As(err, &wire) && wire.Code == "glossary_term_not_found" {
			return nil, fmt.Errorf("%w: %w", sql.ErrNoRows, err)
		}
		return nil, err
	}
	if validateGlossaryItem(result.Item) != nil || result.Item.Term != term {
		return nil, NewError(ErrorCodeOutcomeUnknown, "glossary term result is malformed")
	}
	return result.Item, nil
}

func (store *GlossaryStoreClient) FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	var result glossaryItemsResult
	if err := store.client.Call(ctx, GroupGlossary, "find_recent", glossaryRecentPayload{Limit: limit}, &result); err != nil {
		return nil, err
	}
	if result.Items == nil || validateGlossaryItems(result.Items, limit) != nil {
		return nil, NewError(ErrorCodeOutcomeUnknown, "glossary recent result is malformed")
	}
	return result.Items, nil
}

func (store *GlossaryStoreClient) FindViewerPage(ctx context.Context, limit int) (int, []*entity.GlossaryItem, error) {
	if store == nil || store.client == nil {
		return 0, nil, NewError(ErrorCodeStoreUnavailable, "glossary client is unavailable")
	}
	if !validGlossaryLimit(limit, maxGlossaryViewerPageSize, false) {
		return 0, nil, NewError(ErrorCodeSchemaRejected, "glossary Viewer page query rejected")
	}
	var result glossaryViewerPageResult
	if err := store.client.Call(ctx, GroupGlossary, "viewer_page", glossaryViewerPagePayload{Limit: limit}, &result); err != nil {
		return 0, nil, err
	}
	if !validGlossaryViewerPage(result.Total, result.Items, limit) {
		return 0, nil, NewError(ErrorCodeOutcomeUnknown, "glossary Viewer page result is malformed")
	}
	return result.Total, result.Items, nil
}

func (store *GlossaryStoreClient) FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	var result glossaryItemsResult
	if err := store.client.Call(ctx, GroupGlossary, "find_by_category", glossaryCategoryPayload{Category: category, Limit: limit}, &result); err != nil {
		return nil, err
	}
	if result.Items == nil || validateGlossaryItems(result.Items, limit) != nil {
		return nil, NewError(ErrorCodeOutcomeUnknown, "glossary category result is malformed")
	}
	for _, item := range result.Items {
		if item.Category != category {
			return nil, NewError(ErrorCodeOutcomeUnknown, "glossary category result contains a mismatched item")
		}
	}
	return result.Items, nil
}

func (store *GlossaryStoreClient) Delete(ctx context.Context, id string) error {
	return store.client.Call(ctx, GroupGlossary, "delete", glossaryIDPayload{ID: id}, nil)
}

func (store *GlossaryStoreClient) SaveCandidate(ctx context.Context, candidate entity.GlossaryCandidate) error {
	if err := entity.ValidateGlossaryCandidate(candidate); err != nil {
		return NewError(ErrorCodeSchemaRejected, "glossary candidate rejected")
	}
	return store.client.Call(ctx, GroupGlossary, "save_candidate", glossaryCandidatePayload{Candidate: candidate}, nil)
}

func (store *GlossaryStoreClient) FindCandidateByID(ctx context.Context, id string) (entity.GlossaryCandidate, bool, error) {
	var result glossaryCandidateResult
	if err := store.client.Call(ctx, GroupGlossary, "find_candidate_by_id", glossaryIDPayload{ID: id}, &result); err != nil {
		return entity.GlossaryCandidate{}, false, err
	}
	if !result.Found {
		if result.Candidate != nil {
			return entity.GlossaryCandidate{}, false, NewError(ErrorCodeOutcomeUnknown, "glossary candidate result is malformed")
		}
		return entity.GlossaryCandidate{}, false, nil
	}
	if result.Candidate == nil || result.Candidate.ID != id || entity.ValidateGlossaryCandidate(*result.Candidate) != nil {
		return entity.GlossaryCandidate{}, false, NewError(ErrorCodeOutcomeUnknown, "glossary candidate result is malformed")
	}
	return *result.Candidate, true, nil
}

// Lookup returns the concrete application DTO required by existing runtime
// type assertions; it never degrades to untyped JSON maps or candidate rows.
func (store *GlossaryStoreClient) Lookup(ctx context.Context, operation, term, category string, limit int) (any, error) {
	request := glossaryLookupPayload{Operation: operation, Term: term, Category: category, Limit: limit}
	if !validGlossaryLookup(request) {
		return nil, NewError(ErrorCodeSchemaRejected, "glossary indexed lookup rejected")
	}
	var result glossaryapp.LookupResult
	if err := store.client.Call(ctx, GroupGlossary, "lookup", request, &result); err != nil {
		return nil, err
	}
	if validateGlossaryLookupResult(result, operation) != nil {
		return nil, NewError(ErrorCodeOutcomeUnknown, "glossary indexed lookup result is malformed")
	}
	return result, nil
}

var _ repository.GlossaryRepository = (*GlossaryStoreClient)(nil)
var _ interface {
	SaveCandidate(context.Context, entity.GlossaryCandidate) error
	FindCandidateByID(context.Context, string) (entity.GlossaryCandidate, bool, error)
	Lookup(context.Context, string, string, string, int) (any, error)
} = (*GlossaryStoreClient)(nil)
