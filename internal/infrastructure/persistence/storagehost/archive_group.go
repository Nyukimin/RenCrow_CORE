package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// GroupArchive is the closed archive-storage capability. It intentionally has
// no SQL, path, export, cleanup, or arbitrary owner-job operation.
const GroupArchive = "archive"

const (
	archiveOpSaveThreadSummary         = "save_thread_summary_with_receipt"
	archiveOpGetThreadSummary          = "get_thread_summary"
	archiveOpGetSessionHistory         = "get_session_history"
	archiveOpSearchByDomain            = "search_by_domain"
	archiveOpSearchKnowledgeFTS        = "search_knowledge_archive_fts"
	archiveOpArchiveUserMemory         = "archive_user_memory_with_receipt"
	archiveOpFindUserMemory            = "find_user_memory_archive"
	archiveOpFindArchiveRequestReceipt = "find_archive_request_receipt"

	archiveMaxResults = 100
)

// ArchiveGroupOwner is the exact archive-domain surface served over RPC.
// Export, cleanup, and path-bearing methods remain local owner jobs.
type ArchiveGroupOwner interface {
	SaveThreadSummaryWithReceipt(context.Context, *domconv.ThreadSummary, *domconv.ThreadSummaryReceipt) error
	GetThreadSummary(context.Context, modulecore.ThreadID) (*domconv.ThreadSummary, error)
	GetSessionHistory(context.Context, string, int) ([]*domconv.ThreadSummary, error)
	SearchByDomain(context.Context, string, int) ([]*domconv.ThreadSummary, error)
	SearchKnowledgeArchiveFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error)
	ArchiveUserMemoryWithReceipt(context.Context, l1sqlite.L1MemoryEvent, l1sqlite.OwnerArchiveRequest) (bool, error)
	FindUserMemoryArchive(context.Context, string, string) (l1sqlite.L1MemoryEvent, bool, error)
	FindArchiveRequestReceipt(context.Context, string, string) (l1sqlite.OwnerArchiveRequest, bool, error)
}

// ArchiveGroupRecoveryOwner proves SQLite outcomes. User-memory additionally
// preserves its RPC result in an owner-side operation receipt; the product
// RequestID receipt remains the archive domain's idempotency key.
type ArchiveGroupRecoveryOwner interface {
	ArchiveGroupOwner
	ReconcileThreadSummaryWrite(context.Context, *domconv.ThreadSummary, *domconv.ThreadSummaryReceipt) (bool, error)
	ArchiveUserMemoryWithReceiptAndOperation(context.Context, l1sqlite.L1MemoryEvent, l1sqlite.OwnerArchiveRequest, string, string) (bool, error)
	ReconcileUserMemoryArchiveWrite(context.Context, l1sqlite.L1MemoryEvent, l1sqlite.OwnerArchiveRequest, string, string) (committed bool, idempotentReplay bool, err error)
}

type archiveSaveThreadSummaryPayload struct {
	Summary *domconv.ThreadSummary        `json:"summary"`
	Receipt *domconv.ThreadSummaryReceipt `json:"receipt"`
}

type archiveGetThreadSummaryPayload struct {
	ThreadID modulecore.ThreadID `json:"thread_id"`
}

type archiveSessionHistoryPayload struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit"`
}

type archiveDomainSearchPayload struct {
	Domain string `json:"domain"`
	Limit  int    `json:"limit"`
}

type archiveKnowledgeFTSPayload struct {
	Domain string `json:"domain"`
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
}

type archiveOwnerRequestDTO struct {
	RequestID   string    `json:"request_id"`
	UserID      string    `json:"user_id"`
	ActorID     string    `json:"actor_id"`
	PayloadHash string    `json:"payload_hash"`
	MemoryID    string    `json:"memory_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type archiveUserMemoryPayload struct {
	Event   l1MemoryEventDTO       `json:"event"`
	Receipt archiveOwnerRequestDTO `json:"receipt"`
}

type archiveFindUserMemoryPayload struct {
	UserID   string `json:"user_id"`
	MemoryID string `json:"memory_id"`
}

type archiveFindRequestReceiptPayload struct {
	UserID    string `json:"user_id"`
	RequestID string `json:"request_id"`
}

type archiveThreadSummaryResult struct {
	Found   bool                   `json:"found"`
	Summary *domconv.ThreadSummary `json:"summary,omitempty"`
}

type archiveThreadSummariesResult struct {
	Summaries []*domconv.ThreadSummary `json:"summaries"`
}

type archiveKnowledgeItemsResult struct {
	Items []l1KnowledgeItemDTO `json:"items"`
}

type archiveUserMemoryResult struct {
	IdempotentReplay bool `json:"idempotent_replay"`
}

type archiveUserMemoryLookupResult struct {
	Found bool              `json:"found"`
	Event *l1MemoryEventDTO `json:"event,omitempty"`
}

type archiveRequestReceiptLookupResult struct {
	Found   bool                    `json:"found"`
	Receipt *archiveOwnerRequestDTO `json:"receipt,omitempty"`
}

// RegisterArchiveGroup installs only the eight typed operations in this
// contract. Recovery is mandatory so an owner without atomic owner receipts
// cannot silently downgrade a write to unsafe at-least-once behavior.
func RegisterArchiveGroup(handler *Handler, owner ArchiveGroupOwner) error {
	if handler == nil || isNilArchiveOwner(owner) {
		return errors.New("storagehost: archive group needs a handler and an owner store")
	}
	recoveryOwner, ok := owner.(ArchiveGroupRecoveryOwner)
	if !ok || isNilArchiveOwner(recoveryOwner) {
		return errors.New("storagehost: archive group owner lacks atomic receipt reconciliation")
	}

	if err := handler.RegisterRecoverable(GroupArchive, archiveOpSaveThreadSummary, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload archiveSaveThreadSummaryPayload
		if decodeL1Payload(mutation.Payload, &payload) != nil || validArchiveThreadSummaryWrite(payload.Summary, payload.Receipt) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "thread summary archive payload rejected"))
		}
		if err := owner.SaveThreadSummaryWithReceipt(ctx, payload.Summary, payload.Receipt); err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "thread summary archive write failed")
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload archiveSaveThreadSummaryPayload
		if decodeL1Payload(mutation.Payload, &payload) != nil || validArchiveThreadSummaryWrite(payload.Summary, payload.Receipt) != nil {
			return UnknownOutcome(), nil
		}
		committed, err := recoveryOwner.ReconcileThreadSummaryWrite(ctx, payload.Summary, payload.Receipt)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !committed {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(nil), nil
	}); err != nil {
		return err
	}

	if err := handler.RegisterRecoverable(GroupArchive, archiveOpArchiveUserMemory, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		_, event, receipt, err := decodeArchiveUserMemoryMutation(mutation.Payload)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "user memory archive payload rejected"))
		}
		replayed, err := recoveryOwner.ArchiveUserMemoryWithReceiptAndOperation(ctx, event, receipt, mutation.OpID, payloadHash(GroupArchive, archiveOpArchiveUserMemory, mutation.Payload))
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "user memory archive write failed")
		}
		return archiveUserMemoryResult{IdempotentReplay: replayed}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		_, event, receipt, err := decodeArchiveUserMemoryMutation(mutation.Payload)
		if err != nil {
			return UnknownOutcome(), nil
		}
		committed, replayed, err := recoveryOwner.ReconcileUserMemoryArchiveWrite(ctx, event, receipt, mutation.OpID, payloadHash(GroupArchive, archiveOpArchiveUserMemory, mutation.Payload))
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !committed {
			return ConfirmedNotCommitted(), nil
		}
		return Committed(archiveUserMemoryResult{IdempotentReplay: replayed}), nil
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupArchive, archiveOpGetThreadSummary, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveGetThreadSummaryPayload
		if decodeL1Payload(raw, &payload) != nil || payload.ThreadID.Validate() != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "thread summary lookup rejected")
		}
		summary, err := owner.GetThreadSummary(ctx, payload.ThreadID)
		if errors.Is(err, domconv.ErrThreadNotFound) {
			return archiveReadyResult(archiveThreadSummaryResult{Found: false})
		}
		if err != nil || summary == nil || summary.ThreadID != payload.ThreadID || validateArchiveThreadSummaryRead(summary) != nil {
			return nil, archiveOwnerReadError("thread summary lookup")
		}
		return archiveReadyResult(archiveThreadSummaryResult{Found: true, Summary: summary})
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupArchive, archiveOpGetSessionHistory, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveSessionHistoryPayload
		if decodeL1Payload(raw, &payload) != nil || !validArchiveSessionHistoryPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "session history lookup rejected")
		}
		summaries, err := owner.GetSessionHistory(ctx, payload.SessionID, payload.Limit)
		if err != nil || !validArchiveSummaryList(summaries, payload.Limit, func(summary *domconv.ThreadSummary) bool {
			return summary.SessionID == payload.SessionID
		}) {
			return nil, archiveOwnerReadError("session history lookup")
		}
		return archiveReadyResult(archiveThreadSummariesResult{Summaries: summaries})
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupArchive, archiveOpSearchByDomain, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveDomainSearchPayload
		if decodeL1Payload(raw, &payload) != nil || !validArchiveDomainSearchPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "domain archive search rejected")
		}
		summaries, err := owner.SearchByDomain(ctx, payload.Domain, payload.Limit)
		if err != nil || !validArchiveSummaryList(summaries, payload.Limit, func(summary *domconv.ThreadSummary) bool {
			return summary.Domain == payload.Domain
		}) {
			return nil, archiveOwnerReadError("domain archive search")
		}
		return archiveReadyResult(archiveThreadSummariesResult{Summaries: summaries})
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupArchive, archiveOpSearchKnowledgeFTS, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveKnowledgeFTSPayload
		if decodeL1Payload(raw, &payload) != nil || !validArchiveKnowledgeFTSPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "knowledge archive search rejected")
		}
		items, err := owner.SearchKnowledgeArchiveFTS(ctx, payload.Domain, payload.Query, payload.Limit)
		if err != nil || len(items) > payload.Limit {
			return nil, archiveOwnerReadError("knowledge archive search")
		}
		encoded := make([]l1KnowledgeItemDTO, len(items))
		for index, item := range items {
			if item.Domain != payload.Domain {
				return nil, archiveOwnerReadError("knowledge archive search")
			}
			encoded[index], err = l1KnowledgeFromDomain(item)
			if err != nil {
				return nil, archiveOwnerReadError("knowledge archive search")
			}
		}
		return archiveReadyResult(archiveKnowledgeItemsResult{Items: encoded})
	}); err != nil {
		return err
	}

	if err := handler.Register(GroupArchive, archiveOpFindUserMemory, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveFindUserMemoryPayload
		if decodeL1Payload(raw, &payload) != nil || !validArchiveUserMemoryKey(payload.UserID, payload.MemoryID) {
			return nil, NewError(ErrorCodeSchemaRejected, "user memory lookup rejected")
		}
		event, found, err := owner.FindUserMemoryArchive(ctx, payload.UserID, payload.MemoryID)
		if err != nil {
			return nil, archiveOwnerReadError("user memory lookup")
		}
		if !found {
			return archiveReadyResult(archiveUserMemoryLookupResult{Found: false})
		}
		if event.ID != payload.MemoryID || event.Namespace != "user:"+payload.UserID || !validArchiveArchivedUserMemory(event, payload.UserID) {
			return nil, archiveOwnerReadError("user memory lookup")
		}
		encoded, err := l1MemoryEventFromDomain(event)
		if err != nil {
			return nil, archiveOwnerReadError("user memory lookup")
		}
		return archiveReadyResult(archiveUserMemoryLookupResult{Found: true, Event: &encoded})
	}); err != nil {
		return err
	}

	return handler.Register(GroupArchive, archiveOpFindArchiveRequestReceipt, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload archiveFindRequestReceiptPayload
		if decodeL1Payload(raw, &payload) != nil || !validArchiveRequestLookupPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "archive request receipt lookup rejected")
		}
		receipt, found, err := owner.FindArchiveRequestReceipt(ctx, payload.UserID, payload.RequestID)
		if err != nil {
			return nil, archiveOwnerReadError("archive request receipt lookup")
		}
		if !found {
			return archiveReadyResult(archiveRequestReceiptLookupResult{Found: false})
		}
		encoded := archiveRequestReceiptDTO(receipt)
		if !validArchiveOwnerRequestDTO(encoded) || encoded.UserID != payload.UserID || encoded.RequestID != payload.RequestID {
			return nil, archiveOwnerReadError("archive request receipt lookup")
		}
		return archiveReadyResult(archiveRequestReceiptLookupResult{Found: true, Receipt: &encoded})
	})
}

func decodeArchiveUserMemoryMutation(raw json.RawMessage) (archiveUserMemoryPayload, l1sqlite.L1MemoryEvent, l1sqlite.OwnerArchiveRequest, error) {
	var payload archiveUserMemoryPayload
	if err := decodeL1Payload(raw, &payload); err != nil {
		return payload, l1sqlite.L1MemoryEvent{}, l1sqlite.OwnerArchiveRequest{}, err
	}
	event, err := payload.Event.domain()
	if err != nil {
		return payload, l1sqlite.L1MemoryEvent{}, l1sqlite.OwnerArchiveRequest{}, err
	}
	receipt := payload.Receipt.owner()
	if !validArchiveOwnerRequestDTO(payload.Receipt) || !validArchiveArchivedUserMemory(event, receipt.UserID) || event.ID != receipt.MemoryID {
		return payload, l1sqlite.L1MemoryEvent{}, l1sqlite.OwnerArchiveRequest{}, errors.New("user memory archive binding rejected")
	}
	return payload, event, receipt, nil
}

func archiveRequestReceiptDTO(receipt l1sqlite.OwnerArchiveRequest) archiveOwnerRequestDTO {
	return archiveOwnerRequestDTO{
		RequestID: receipt.RequestID, UserID: receipt.UserID, ActorID: receipt.ActorID,
		PayloadHash: receipt.PayloadHash, MemoryID: receipt.MemoryID, CreatedAt: receipt.CreatedAt,
	}
}

func (dto archiveOwnerRequestDTO) owner() l1sqlite.OwnerArchiveRequest {
	return l1sqlite.OwnerArchiveRequest{
		RequestID: dto.RequestID, UserID: dto.UserID, ActorID: dto.ActorID,
		PayloadHash: dto.PayloadHash, MemoryID: dto.MemoryID, CreatedAt: dto.CreatedAt,
	}
}

func validArchiveOwnerRequestDTO(receipt archiveOwnerRequestDTO) bool {
	return validL1Text(receipt.RequestID, 256, true) && strings.TrimSpace(receipt.RequestID) == receipt.RequestID &&
		validArchiveUserID(receipt.UserID) && validArchiveUserID(receipt.ActorID) &&
		validL1Hex(receipt.PayloadHash, 64) && validL1Text(receipt.MemoryID, 2_048, true) &&
		strings.TrimSpace(receipt.MemoryID) == receipt.MemoryID && validL1Time(receipt.CreatedAt, false)
}

func validArchiveArchivedUserMemory(event l1sqlite.L1MemoryEvent, userID string) bool {
	return validArchiveUserID(userID) && event.Namespace == "user:"+userID &&
		(event.MemoryState == l1sqlite.MemoryStateConfirmed || event.MemoryState == l1sqlite.MemoryStatePinned) && validL1MemoryEvent(event)
}

func validArchiveThreadSummaryWrite(summary *domconv.ThreadSummary, receipt *domconv.ThreadSummaryReceipt) error {
	if err := validateArchiveThreadSummaryCore(summary); err != nil {
		return err
	}
	if receipt == nil || receipt.ValidateForWrite() != nil || summary.Score != 0 {
		return errors.New("thread summary receipt or score is invalid")
	}
	if summary.Receipt != nil && !sameArchiveThreadSummaryReceipt(summary.Receipt, receipt) {
		return errors.New("thread summary embedded receipt conflicts with receipt field")
	}
	if err := domconv.ValidateSummaryResidual(domconv.SummaryResidual{Summary: summary.Summary, Keywords: summary.Keywords, Provider: receipt.Provider}); err != nil {
		return err
	}
	if !validL1StringSlice(summary.Roles, 32, 128) || len(summary.Roles) != len(receipt.Roles) {
		return errors.New("thread summary roles are invalid")
	}
	for i := range summary.Roles {
		if summary.Roles[i] != receipt.Roles[i] {
			return errors.New("thread summary roles do not match receipt")
		}
	}
	return nil
}

func validateArchiveThreadSummaryRead(summary *domconv.ThreadSummary) error {
	if err := validateArchiveThreadSummaryCore(summary); err != nil {
		return err
	}
	if summary.Receipt == nil || summary.Receipt.GenerationMode == domconv.ThreadSummaryGenerationLegacyUnverified {
		return nil
	}
	if err := summary.Receipt.ValidateForWrite(); err != nil {
		return err
	}
	if err := domconv.ValidateSummaryResidual(domconv.SummaryResidual{Summary: summary.Summary, Keywords: summary.Keywords, Provider: summary.Receipt.Provider}); err != nil {
		return err
	}
	if len(summary.Roles) != len(summary.Receipt.Roles) {
		return errors.New("thread summary roles do not match receipt")
	}
	for i := range summary.Roles {
		if summary.Roles[i] != summary.Receipt.Roles[i] {
			return errors.New("thread summary roles do not match receipt")
		}
	}
	return nil
}

func validateArchiveThreadSummaryCore(summary *domconv.ThreadSummary) error {
	if summary == nil || summary.ThreadID.Validate() != nil || summary.ThreadSeq.Validate() != nil || summary.ThreadKind.Validate() != nil ||
		modulecore.SessionID(summary.SessionID).Validate() != nil || !validL1Text(summary.Domain, 128, false) ||
		strings.TrimSpace(summary.Domain) != summary.Domain || !validL1Text(summary.Summary, 1<<20, true) ||
		!validL1StringSlice(summary.Keywords, 64, 256) || len(summary.Embedding) > 16_384 ||
		!validL1Time(summary.StartTime, true) || !validL1Time(summary.EndTime, true) ||
		math.IsNaN(float64(summary.Score)) || math.IsInf(float64(summary.Score), 0) {
		return errors.New("thread summary is invalid or outside the bounded contract")
	}
	for _, value := range summary.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("thread summary embedding contains a non-finite value")
		}
	}
	return nil
}

func sameArchiveThreadSummaryReceipt(left, right *domconv.ThreadSummaryReceipt) bool {
	if left == nil || right == nil || left.SchemaVersion != right.SchemaVersion || left.GenerationMode != right.GenerationMode ||
		left.Provider != right.Provider || left.FailureCode != right.FailureCode || left.EvidenceSHA256 != right.EvidenceSHA256 ||
		left.SourceTurnCount != right.SourceTurnCount || !left.CreatedAt.Equal(right.CreatedAt) || len(left.Roles) != len(right.Roles) {
		return false
	}
	for i := range left.Roles {
		if left.Roles[i] != right.Roles[i] {
			return false
		}
	}
	return true
}

func validArchiveSummaryList(summaries []*domconv.ThreadSummary, limit int, match func(*domconv.ThreadSummary) bool) bool {
	if len(summaries) > limit {
		return false
	}
	for _, summary := range summaries {
		if validateArchiveThreadSummaryRead(summary) != nil || !match(summary) {
			return false
		}
	}
	return true
}

func validArchiveSessionHistoryPayload(payload archiveSessionHistoryPayload) bool {
	return modulecore.SessionID(payload.SessionID).Validate() == nil && validArchiveLimit(payload.Limit)
}

func validArchiveDomainSearchPayload(payload archiveDomainSearchPayload) bool {
	return validL1Text(payload.Domain, 128, true) && strings.TrimSpace(payload.Domain) == payload.Domain && validArchiveLimit(payload.Limit)
}

func validArchiveKnowledgeFTSPayload(payload archiveKnowledgeFTSPayload) bool {
	return validL1Label(payload.Domain) && validL1Text(payload.Query, l1MaxJSONTextBytes, true) && validArchiveLimit(payload.Limit)
}

func validArchiveLimit(limit int) bool { return limit >= 1 && limit <= archiveMaxResults }

func validArchiveUserID(userID string) bool {
	return validL1Text(userID, 256, true) && strings.TrimSpace(userID) == userID
}

func validArchiveUserMemoryKey(userID, memoryID string) bool {
	return validArchiveUserID(userID) && validL1Text(memoryID, 2_048, true) && strings.TrimSpace(memoryID) == memoryID
}

func validArchiveRequestLookupPayload(payload archiveFindRequestReceiptPayload) bool {
	return validArchiveUserID(payload.UserID) && validL1Text(payload.RequestID, 256, true) && strings.TrimSpace(payload.RequestID) == payload.RequestID
}

func archiveReadyResult(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil || inspectL1JSON(encoded, l1MaxPayloadBytes, l1MaxTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, true) != nil {
		return nil, archiveOwnerReadError("result encoding")
	}
	return value, nil
}

func archiveOwnerReadError(operation string) *Error {
	return NewError(ErrorCodeStoreUnavailable, "conversation archive "+operation+" failed")
}

func archiveSchemaError(operation string) *Error {
	return NewError(ErrorCodeSchemaRejected, "conversation archive "+operation+" rejected")
}

func isNilArchiveOwner(owner ArchiveGroupOwner) bool {
	if owner == nil {
		return true
	}
	value := reflect.ValueOf(owner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ArchiveStoreClient exposes only the archive shapes consumed by conversation
// recall and the existing user-memory archive adapter.
type ArchiveStoreClient struct{ client *Client }

func NewArchiveStoreClient(client *Client) *ArchiveStoreClient {
	return &ArchiveStoreClient{client: client}
}

// Close is a no-op because the RPC client may be shared with other groups.
func (*ArchiveStoreClient) Close() error { return nil }

func (s *ArchiveStoreClient) SaveThreadSummaryWithReceipt(ctx context.Context, summary *domconv.ThreadSummary, receipt *domconv.ThreadSummaryReceipt) error {
	if validArchiveThreadSummaryWrite(summary, receipt) != nil {
		return archiveSchemaError("thread summary write")
	}
	return s.call(ctx, archiveOpSaveThreadSummary, archiveSaveThreadSummaryPayload{Summary: summary, Receipt: receipt}, nil)
}

func (s *ArchiveStoreClient) GetThreadSummary(ctx context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
	if threadID.Validate() != nil {
		return nil, archiveSchemaError("thread summary lookup")
	}
	var result archiveThreadSummaryResult
	if err := s.call(ctx, archiveOpGetThreadSummary, archiveGetThreadSummaryPayload{ThreadID: threadID}, &result); err != nil {
		return nil, err
	}
	if !result.Found {
		if result.Summary != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "thread summary lookup returned an inconsistent result")
		}
		return nil, domconv.ErrThreadNotFound
	}
	if result.Summary == nil || result.Summary.ThreadID != threadID || validateArchiveThreadSummaryRead(result.Summary) != nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "thread summary lookup returned an invalid result")
	}
	return result.Summary, nil
}

func (s *ArchiveStoreClient) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*domconv.ThreadSummary, error) {
	payload := archiveSessionHistoryPayload{SessionID: sessionID, Limit: limit}
	if !validArchiveSessionHistoryPayload(payload) {
		return nil, archiveSchemaError("session history lookup")
	}
	var result archiveThreadSummariesResult
	if err := s.call(ctx, archiveOpGetSessionHistory, payload, &result); err != nil {
		return nil, err
	}
	if !validArchiveSummaryList(result.Summaries, limit, func(summary *domconv.ThreadSummary) bool { return summary.SessionID == sessionID }) {
		return nil, NewError(ErrorCodeStoreUnavailable, "session history returned an invalid result")
	}
	return result.Summaries, nil
}

func (s *ArchiveStoreClient) SearchByDomain(ctx context.Context, domain string, limit int) ([]*domconv.ThreadSummary, error) {
	payload := archiveDomainSearchPayload{Domain: domain, Limit: limit}
	if !validArchiveDomainSearchPayload(payload) {
		return nil, archiveSchemaError("domain archive search")
	}
	var result archiveThreadSummariesResult
	if err := s.call(ctx, archiveOpSearchByDomain, payload, &result); err != nil {
		return nil, err
	}
	if !validArchiveSummaryList(result.Summaries, limit, func(summary *domconv.ThreadSummary) bool { return summary.Domain == domain }) {
		return nil, NewError(ErrorCodeStoreUnavailable, "domain archive search returned an invalid result")
	}
	return result.Summaries, nil
}

func (s *ArchiveStoreClient) SearchKnowledgeArchiveFTS(ctx context.Context, domain, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	payload := archiveKnowledgeFTSPayload{Domain: domain, Query: query, Limit: limit}
	if !validArchiveKnowledgeFTSPayload(payload) {
		return nil, archiveSchemaError("knowledge archive search")
	}
	var result archiveKnowledgeItemsResult
	if err := s.call(ctx, archiveOpSearchKnowledgeFTS, payload, &result); err != nil {
		return nil, err
	}
	if len(result.Items) > limit {
		return nil, NewError(ErrorCodeStoreUnavailable, "knowledge archive search returned too many results")
	}
	items := make([]l1sqlite.L1KnowledgeItem, len(result.Items))
	for index, encoded := range result.Items {
		item, err := encoded.domain()
		if err != nil || item.Domain != domain {
			return nil, NewError(ErrorCodeStoreUnavailable, "knowledge archive search returned an invalid result")
		}
		items[index] = item
	}
	return items, nil
}

func (s *ArchiveStoreClient) ArchiveUserMemoryWithReceipt(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt l1sqlite.OwnerArchiveRequest) (bool, error) {
	encodedEvent, err := l1MemoryEventFromDomain(event)
	if err != nil || !validArchiveArchivedUserMemory(event, receipt.UserID) || event.ID != receipt.MemoryID {
		return false, archiveSchemaError("user memory archive write")
	}
	encodedReceipt := archiveRequestReceiptDTO(receipt)
	if !validArchiveOwnerRequestDTO(encodedReceipt) {
		return false, archiveSchemaError("user memory archive write")
	}
	var result archiveUserMemoryResult
	err = s.call(ctx, archiveOpArchiveUserMemory, archiveUserMemoryPayload{Event: encodedEvent, Receipt: encodedReceipt}, &result)
	return result.IdempotentReplay, err
}

func (s *ArchiveStoreClient) FindUserMemoryArchive(ctx context.Context, userID, memoryID string) (l1sqlite.L1MemoryEvent, bool, error) {
	if !validArchiveUserMemoryKey(userID, memoryID) {
		return l1sqlite.L1MemoryEvent{}, false, archiveSchemaError("user memory lookup")
	}
	var result archiveUserMemoryLookupResult
	if err := s.call(ctx, archiveOpFindUserMemory, archiveFindUserMemoryPayload{UserID: userID, MemoryID: memoryID}, &result); err != nil {
		return l1sqlite.L1MemoryEvent{}, false, err
	}
	if !result.Found {
		if result.Event != nil {
			return l1sqlite.L1MemoryEvent{}, false, NewError(ErrorCodeStoreUnavailable, "user memory lookup returned an inconsistent result")
		}
		return l1sqlite.L1MemoryEvent{}, false, nil
	}
	if result.Event == nil {
		return l1sqlite.L1MemoryEvent{}, false, NewError(ErrorCodeStoreUnavailable, "user memory lookup returned an invalid result")
	}
	event, err := result.Event.domain()
	if err != nil || event.ID != memoryID || !validArchiveArchivedUserMemory(event, userID) {
		return l1sqlite.L1MemoryEvent{}, false, NewError(ErrorCodeStoreUnavailable, "user memory lookup returned an invalid result")
	}
	return event, true, nil
}

func (s *ArchiveStoreClient) FindArchiveRequestReceipt(ctx context.Context, userID, requestID string) (l1sqlite.OwnerArchiveRequest, bool, error) {
	payload := archiveFindRequestReceiptPayload{UserID: userID, RequestID: requestID}
	if !validArchiveRequestLookupPayload(payload) {
		return l1sqlite.OwnerArchiveRequest{}, false, archiveSchemaError("archive request receipt lookup")
	}
	var result archiveRequestReceiptLookupResult
	if err := s.call(ctx, archiveOpFindArchiveRequestReceipt, payload, &result); err != nil {
		return l1sqlite.OwnerArchiveRequest{}, false, err
	}
	if !result.Found {
		if result.Receipt != nil {
			return l1sqlite.OwnerArchiveRequest{}, false, NewError(ErrorCodeStoreUnavailable, "archive request receipt lookup returned an inconsistent result")
		}
		return l1sqlite.OwnerArchiveRequest{}, false, nil
	}
	if result.Receipt == nil || !validArchiveOwnerRequestDTO(*result.Receipt) || result.Receipt.UserID != userID || result.Receipt.RequestID != requestID {
		return l1sqlite.OwnerArchiveRequest{}, false, NewError(ErrorCodeStoreUnavailable, "archive request receipt lookup returned an invalid result")
	}
	return result.Receipt.owner(), true, nil
}

func (s *ArchiveStoreClient) call(ctx context.Context, operation string, payload, result any) error {
	if s == nil || s.client == nil {
		return NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if ctx == nil {
		return archiveSchemaError(operation)
	}
	return s.client.Call(ctx, GroupArchive, operation, payload, result)
}

var _ ArchiveGroupOwner = (*ArchiveStoreClient)(nil)
