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
	"math"
	"reflect"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const GroupL1 = "l1"

// L1GroupOwner is the closed conversation L1 capability exposed by storage RPC.
type L1GroupOwner interface {
	SaveMessage(context.Context, string, modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, string, domconv.Message, string) error
	SaveSearchCache(context.Context, string, string, string, []string, time.Duration) (*l1sqlite.L1SearchCacheEntry, error)
	GetFreshSearchCache(context.Context, string, string, time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	GetSimilarFreshSearchCache(context.Context, string, string, time.Time, float64) (*l1sqlite.L1SearchCacheEntry, error)
	InvalidateSearchCache(context.Context, string, string) (int64, error)
	SearchKnowledgeItemsFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(context.Context, string, int) ([]l1sqlite.WikiPageIndexItem, error)
	AppendEvent(context.Context, string, string, string, modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, map[string]interface{}, string) (*l1sqlite.L1EventLogEntry, error)
	RecentEvents(context.Context, string, int) ([]l1sqlite.L1EventLogEntry, error)
	UpdateMemoryState(context.Context, string, string) error
	PromoteMemoryToNamespace(context.Context, string, string, string) (*l1sqlite.L1MemoryEvent, error)
	RecentByNamespace(context.Context, string, int) ([]l1sqlite.L1MemoryEvent, error)
	RecentByState(context.Context, string, int) ([]l1sqlite.L1MemoryEvent, error)
	RecentBySession(context.Context, string, int) ([]l1sqlite.L1MemoryEvent, error)
	LatestConversationThreadReference(context.Context, string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool, error)
	SaveRecallTrace(context.Context, domconv.RecallTrace) error
	RecentRecallTraces(context.Context, string, int) ([]domconv.RecallTrace, error)
}

var _ L1GroupOwner = (*l1sqlite.L1SQLiteStore)(nil)

// L1RecoverableGroupOwner contains only the typed SQLite operations whose
// mutation and owner receipt share one L1 transaction.
type L1RecoverableGroupOwner interface {
	L1GroupOwner
	SaveMessageForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, string, domconv.Message, string) error
	SaveSearchCacheForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string, string, []string, time.Duration) (*l1sqlite.L1SearchCacheEntry, error)
	InvalidateSearchCacheForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string) (int64, error)
	AppendEventForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string, string, modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, map[string]interface{}, string) (*l1sqlite.L1EventLogEntry, error)
	UpdateMemoryStateForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string) error
	PromoteMemoryToNamespaceForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string, string) (*l1sqlite.L1MemoryEvent, error)
	SaveRecallTraceForOperation(context.Context, l1sqlite.ConversationL1OperationIdentity, domconv.RecallTrace) error
	LookupConversationL1OperationReceipt(context.Context, l1sqlite.ConversationL1OperationIdentity) (json.RawMessage, bool, error)
	VerifySearchCacheInvalidationResult(context.Context, l1sqlite.ConversationL1OperationIdentity, string, string, string, int64) error
}

var _ L1RecoverableGroupOwner = (*l1sqlite.L1SQLiteStore)(nil)

const (
	l1OpSaveMessage                       = "save_message"
	l1OpSaveSearchCache                   = "save_search_cache"
	l1OpGetFreshSearchCache               = "get_fresh_search_cache"
	l1OpGetSimilarFreshSearchCache        = "get_similar_fresh_search_cache"
	l1OpInvalidateSearchCache             = "invalidate_search_cache"
	l1OpSearchKnowledgeItemsFTS           = "search_knowledge_items_fts"
	l1OpSearchWikiPageIndex               = "search_wiki_page_index"
	l1OpAppendEvent                       = "append_event"
	l1OpRecentEvents                      = "recent_events"
	l1OpUpdateMemoryState                 = "update_memory_state"
	l1OpPromoteMemoryToNamespace          = "promote_memory_to_namespace"
	l1OpRecentByNamespace                 = "recent_by_namespace"
	l1OpRecentByState                     = "recent_by_state"
	l1OpRecentBySession                   = "recent_by_session"
	l1OpLatestConversationThreadReference = "latest_conversation_thread_reference"
	l1OpSaveRecallTrace                   = "save_recall_trace"
	l1OpRecentRecallTraces                = "recent_recall_traces"
)

// RegisterL1Group exposes the closed conversation L1 owner contract. Every
// mutating owner error remains uncertain unless validation rejected it before
// the owner method was entered.
func RegisterL1Group(handler *Handler, store L1GroupOwner) error {
	if handler == nil || isNilL1Owner(store) {
		return errors.New("storagehost: l1 group needs a handler and an owner store")
	}
	recoveryStore, ok := store.(L1RecoverableGroupOwner)
	if !ok || isNilL1Owner(recoveryStore) {
		return errors.New("storagehost: l1 group owner must provide atomic conversation L1 receipts")
	}

	type registration struct {
		name      string
		mutating  bool
		fn        OperationFunc
		execute   OwnerMutationFunc
		reconcile OwnerReconcileFunc
	}
	registrations := []registration{
		{name: l1OpSaveMessage, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpSaveMessage)
				if err != nil {
					return nil, l1MutationFailed(l1OpSaveMessage)
				}
				var payload l1SaveMessagePayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1SaveMessagePayload(payload) {
					return nil, rejectL1Payload(l1OpSaveMessage, true)
				}
				if err := recoveryStore.SaveMessageForOperation(ctx, identity, payload.SessionID, payload.ThreadID, payload.ThreadSeq, payload.ThreadKind, payload.Namespace, payload.Message, payload.MemoryState); err != nil {
					return nil, l1MutationOwnerFailed(l1OpSaveMessage, err)
				}
				return nil, nil
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpSaveMessage, func(raw json.RawMessage) (any, error) {
					if err := validateL1VoidReceipt(raw); err != nil {
						return nil, err
					}
					return nil, nil
				})
			}},
		{name: l1OpSaveSearchCache, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpSaveSearchCache)
				if err != nil {
					return nil, l1MutationFailed(l1OpSaveSearchCache)
				}
				var payload l1SaveSearchCachePayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1SaveSearchCachePayload(payload) {
					return nil, rejectL1Payload(l1OpSaveSearchCache, true)
				}
				entry, err := recoveryStore.SaveSearchCacheForOperation(ctx, identity, payload.Provider, payload.RawQuery, payload.ResultsJSON, payload.SourceURLs, payload.TTL)
				if err != nil {
					return nil, l1MutationOwnerFailed(l1OpSaveSearchCache, err)
				}
				if !entry.MatchesStorageHostSaveRequest(payload.Provider, payload.RawQuery, payload.ResultsJSON, payload.SourceURLs, payload.TTL) {
					return nil, l1MutationFailed(l1OpSaveSearchCache)
				}
				encoded, err := l1SearchCacheFromDomain(entry)
				if err != nil {
					return nil, l1MutationFailed(l1OpSaveSearchCache)
				}
				return l1ReadyResult(l1SearchCacheResult{Entry: encoded})
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpSaveSearchCache, func(raw json.RawMessage) (any, error) {
					var entry *l1sqlite.L1SearchCacheEntry
					var payload l1SaveSearchCachePayload
					if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1SaveSearchCachePayload(payload) ||
						decodeL1ReceiptResult(raw, &entry) != nil || entry == nil ||
						!entry.MatchesStorageHostSaveRequest(payload.Provider, payload.RawQuery, payload.ResultsJSON, payload.SourceURLs, payload.TTL) {
						return nil, errors.New("l1 search cache receipt result is invalid")
					}
					encoded, err := l1SearchCacheFromDomain(entry)
					if err != nil {
						return nil, err
					}
					return l1ReadyResult(l1SearchCacheResult{Entry: encoded})
				})
			}},
		{name: l1OpGetFreshSearchCache, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1SearchCacheQueryPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1SearchCacheQueryPayload(payload) {
				return nil, rejectL1Payload(l1OpGetFreshSearchCache, false)
			}
			entry, err := store.GetFreshSearchCache(ctx, payload.Provider, payload.RawQuery, payload.Now)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpGetFreshSearchCache, err)
			}
			encoded, err := l1SearchCacheFromDomain(entry)
			if err != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid search cache result")
			}
			return l1ReadyResult(l1SearchCacheResult{Entry: encoded})
		}},
		{name: l1OpGetSimilarFreshSearchCache, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1SimilarSearchCachePayload
			if decodeL1Payload(raw, &payload) != nil || !validL1SimilarSearchCachePayload(payload) {
				return nil, rejectL1Payload(l1OpGetSimilarFreshSearchCache, false)
			}
			entry, err := store.GetSimilarFreshSearchCache(ctx, payload.Provider, payload.RawQuery, payload.Now, payload.Threshold)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpGetSimilarFreshSearchCache, err)
			}
			encoded, err := l1SearchCacheFromDomain(entry)
			if err != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid similar search cache result")
			}
			return l1ReadyResult(l1SearchCacheResult{Entry: encoded})
		}},
		{name: l1OpInvalidateSearchCache, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpInvalidateSearchCache)
				if err != nil {
					return nil, l1MutationFailed(l1OpInvalidateSearchCache)
				}
				var payload l1InvalidateSearchCachePayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1InvalidateSearchCachePayload(payload) {
					return nil, rejectL1Payload(l1OpInvalidateSearchCache, true)
				}
				affected, err := recoveryStore.InvalidateSearchCacheForOperation(ctx, identity, payload.Provider, payload.RawQuery)
				if err != nil || affected < 0 {
					return nil, l1MutationOwnerFailed(l1OpInvalidateSearchCache, err)
				}
				return l1ReadyResult(l1AffectedResult{Affected: affected})
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpInvalidateSearchCache, func(raw json.RawMessage) (any, error) {
					var payload l1InvalidateSearchCachePayload
					var receipt l1sqlite.ConversationL1SearchCacheInvalidationResult
					identity, err := l1OperationIdentity(mutation, l1OpInvalidateSearchCache)
					if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1InvalidateSearchCachePayload(payload) ||
						err != nil || decodeL1ReceiptResult(raw, &receipt) != nil || receipt.Affected < 0 {
						return nil, errors.New("l1 search cache invalidation receipt result is invalid")
					}
					if err := recoveryStore.VerifySearchCacheInvalidationResult(ctx, identity, payload.Provider, payload.RawQuery, receipt.EventID, receipt.Affected); err != nil {
						return nil, err
					}
					return l1ReadyResult(l1AffectedResult{Affected: receipt.Affected})
				})
			}},
		{name: l1OpSearchKnowledgeItemsFTS, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1KnowledgeSearchPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1KnowledgeSearchPayload(payload) {
				return nil, rejectL1Payload(l1OpSearchKnowledgeItemsFTS, false)
			}
			items, err := store.SearchKnowledgeItemsFTS(ctx, payload.Domain, payload.Query, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpSearchKnowledgeItemsFTS, err)
			}
			if len(items) > l1EffectiveLimit(payload.Limit, 20) {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned too many knowledge results")
			}
			var encoded []l1KnowledgeItemDTO
			if items != nil {
				encoded = make([]l1KnowledgeItemDTO, len(items))
				for index, item := range items {
					encoded[index], err = l1KnowledgeFromDomain(item)
					if err != nil {
						return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid knowledge result")
					}
				}
			}
			return l1ReadyResult(l1KnowledgeSearchResult{Items: encoded})
		}},
		{name: l1OpSearchWikiPageIndex, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1WikiSearchPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1WikiSearchPayload(payload) {
				return nil, rejectL1Payload(l1OpSearchWikiPageIndex, false)
			}
			items, err := store.SearchWikiPageIndex(ctx, payload.Query, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpSearchWikiPageIndex, err)
			}
			if len(items) > l1EffectiveLimit(payload.Limit, 20) {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned too many wiki results")
			}
			var encoded []l1WikiPageDTO
			if items != nil {
				encoded = make([]l1WikiPageDTO, len(items))
				for index, item := range items {
					encoded[index], err = l1WikiFromDomain(item)
					if err != nil {
						return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid wiki result")
					}
				}
			}
			return l1ReadyResult(l1WikiSearchResult{Items: encoded})
		}},
		{name: l1OpAppendEvent, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpAppendEvent)
				if err != nil {
					return nil, l1MutationFailed(l1OpAppendEvent)
				}
				var payload l1AppendEventPayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || len(payload.Payload) == 0 {
					return nil, rejectL1Payload(l1OpAppendEvent, true)
				}
				eventPayload, err := l1ObjectFromRaw(payload.Payload)
				if err != nil || !validL1AppendEventPayload(payload) {
					return nil, rejectL1Payload(l1OpAppendEvent, true)
				}
				entry, err := recoveryStore.AppendEventForOperation(ctx, identity, payload.EventType, payload.Namespace, payload.SessionID, payload.ThreadID, payload.ThreadSeq, payload.ThreadKind, eventPayload, payload.Source)
				if err != nil || entry == nil || !l1EventMatchesAppendRequest(entry, payload, eventPayload) {
					return nil, l1MutationOwnerFailed(l1OpAppendEvent, err)
				}
				encoded, err := l1EventLogFromDomain(*entry)
				if err != nil {
					return nil, l1MutationFailed(l1OpAppendEvent)
				}
				return l1ReadyResult(l1EventLogResult{Event: encoded})
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpAppendEvent, func(raw json.RawMessage) (any, error) {
					var entry *l1sqlite.L1EventLogEntry
					var payload l1AppendEventPayload
					if decodeL1Payload(mutation.Payload, &payload) != nil || len(payload.Payload) == 0 || !validL1AppendEventPayload(payload) ||
						decodeL1ReceiptResult(raw, &entry) != nil || entry == nil {
						return nil, errors.New("l1 event receipt result is invalid")
					}
					eventPayload, err := l1ObjectFromRaw(payload.Payload)
					if err != nil || !l1EventMatchesAppendRequest(entry, payload, eventPayload) {
						return nil, errors.New("l1 event receipt result does not match request")
					}
					encoded, err := l1EventLogFromDomain(*entry)
					if err != nil {
						return nil, err
					}
					return l1ReadyResult(l1EventLogResult{Event: encoded})
				})
			}},
		{name: l1OpRecentEvents, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1RecentEventsPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1RecentEventsPayload(payload) {
				return nil, rejectL1Payload(l1OpRecentEvents, false)
			}
			events, err := store.RecentEvents(ctx, payload.Namespace, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpRecentEvents, err)
			}
			if len(events) > l1EffectiveLimit(payload.Limit, 20) {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned too many events")
			}
			var encoded []l1EventLogDTO
			if events != nil {
				encoded = make([]l1EventLogDTO, len(events))
				for index, event := range events {
					encoded[index], err = l1EventLogFromDomain(event)
					if err != nil {
						return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid event")
					}
				}
			}
			return l1ReadyResult(l1RecentEventsResult{Events: encoded})
		}},
		{name: l1OpUpdateMemoryState, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpUpdateMemoryState)
				if err != nil {
					return nil, l1MutationFailed(l1OpUpdateMemoryState)
				}
				var payload l1UpdateMemoryStatePayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1UpdateMemoryStatePayload(payload) {
					return nil, rejectL1Payload(l1OpUpdateMemoryState, true)
				}
				if err := recoveryStore.UpdateMemoryStateForOperation(ctx, identity, payload.ID, payload.MemoryState); err != nil {
					return nil, l1MutationOwnerFailed(l1OpUpdateMemoryState, err)
				}
				return nil, nil
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpUpdateMemoryState, func(raw json.RawMessage) (any, error) {
					if err := validateL1VoidReceipt(raw); err != nil {
						return nil, err
					}
					return nil, nil
				})
			}},
		{name: l1OpPromoteMemoryToNamespace, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpPromoteMemoryToNamespace)
				if err != nil {
					return nil, l1MutationFailed(l1OpPromoteMemoryToNamespace)
				}
				var payload l1PromoteMemoryPayload
				if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1PromoteMemoryPayload(payload) {
					return nil, rejectL1Payload(l1OpPromoteMemoryToNamespace, true)
				}
				event, err := recoveryStore.PromoteMemoryToNamespaceForOperation(ctx, identity, payload.ID, payload.TargetNamespace, payload.PromotedBy)
				if err != nil || !l1PromotionResultMatchesRequest(event, payload) {
					return nil, l1MutationOwnerFailed(l1OpPromoteMemoryToNamespace, err)
				}
				encoded, err := l1MemoryEventFromDomain(*event)
				if err != nil {
					return nil, l1MutationFailed(l1OpPromoteMemoryToNamespace)
				}
				return l1ReadyResult(l1MemoryEventResult{Event: &encoded})
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpPromoteMemoryToNamespace, func(raw json.RawMessage) (any, error) {
					var event *l1sqlite.L1MemoryEvent
					if err := decodeL1ReceiptResult(raw, &event); err != nil || event == nil {
						return nil, errors.New("l1 memory promotion receipt result is invalid")
					}
					var payload l1PromoteMemoryPayload
					if decodeL1Payload(mutation.Payload, &payload) != nil || !validL1PromoteMemoryPayload(payload) {
						return nil, errors.New("l1 memory promotion request is invalid")
					}
					if !l1PromotionResultMatchesRequest(event, payload) {
						return nil, errors.New("l1 memory promotion receipt result does not match request")
					}
					encoded, err := l1MemoryEventFromDomain(*event)
					if err != nil {
						return nil, err
					}
					return l1ReadyResult(l1MemoryEventResult{Event: &encoded})
				})
			}},
		{name: l1OpRecentByNamespace, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1RecentByNamespacePayload
			if decodeL1Payload(raw, &payload) != nil || !validL1RecentByNamespacePayload(payload) {
				return nil, rejectL1Payload(l1OpRecentByNamespace, false)
			}
			events, err := store.RecentByNamespace(ctx, payload.Namespace, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpRecentByNamespace, err)
			}
			return l1MemoryEventsResultFromOwner(events, payload.Limit, 20)
		}},
		{name: l1OpRecentByState, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1RecentByStatePayload
			if decodeL1Payload(raw, &payload) != nil || !validL1RecentByStatePayload(payload) {
				return nil, rejectL1Payload(l1OpRecentByState, false)
			}
			events, err := store.RecentByState(ctx, payload.MemoryState, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpRecentByState, err)
			}
			return l1MemoryEventsResultFromOwner(events, payload.Limit, 20)
		}},
		{name: l1OpRecentBySession, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1RecentBySessionPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1RecentBySessionPayload(payload) {
				return nil, rejectL1Payload(l1OpRecentBySession, false)
			}
			events, err := store.RecentBySession(ctx, payload.SessionID, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpRecentBySession, err)
			}
			return l1MemoryEventsResultFromOwner(events, payload.Limit, 20)
		}},
		{name: l1OpLatestConversationThreadReference, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1LatestThreadPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1SessionID(payload.SessionID, false) {
				return nil, rejectL1Payload(l1OpLatestConversationThreadReference, false)
			}
			threadID, threadSeq, threadKind, found, err := store.LatestConversationThreadReference(ctx, payload.SessionID)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpLatestConversationThreadReference, err)
			}
			result := l1LatestThreadResult{ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Found: found}
			if !validL1LatestThreadResult(result) {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid conversation thread reference")
			}
			return l1ReadyResult(result)
		}},
		{name: l1OpSaveRecallTrace, mutating: true,
			execute: func(ctx context.Context, mutation MutationMetadata) (any, error) {
				identity, err := l1OperationIdentity(mutation, l1OpSaveRecallTrace)
				if err != nil {
					return nil, l1MutationFailed(l1OpSaveRecallTrace)
				}
				var payload l1SaveRecallTracePayload
				if decodeL1Payload(mutation.Payload, &payload) != nil {
					return nil, rejectL1Payload(l1OpSaveRecallTrace, true)
				}
				trace, err := payload.Trace.domain()
				if err != nil {
					return nil, rejectL1Payload(l1OpSaveRecallTrace, true)
				}
				if err := recoveryStore.SaveRecallTraceForOperation(ctx, identity, trace); err != nil {
					return nil, l1MutationOwnerFailed(l1OpSaveRecallTrace, err)
				}
				return nil, nil
			},
			reconcile: func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
				return reconcileL1OwnerReceipt(ctx, recoveryStore, mutation, l1OpSaveRecallTrace, func(raw json.RawMessage) (any, error) {
					if err := validateL1VoidReceipt(raw); err != nil {
						return nil, err
					}
					return nil, nil
				})
			}},
		{name: l1OpRecentRecallTraces, fn: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var payload l1RecentRecallTracesPayload
			if decodeL1Payload(raw, &payload) != nil || !validL1SessionID(payload.SessionID, true) || !validL1Limit(payload.Limit) {
				return nil, rejectL1Payload(l1OpRecentRecallTraces, false)
			}
			traces, err := store.RecentRecallTraces(ctx, payload.SessionID, payload.Limit)
			if err != nil {
				return nil, l1OwnerReadFailed(l1OpRecentRecallTraces, err)
			}
			limit := l1EffectiveLimit(payload.Limit, 20)
			if limit > 100 {
				limit = 100
			}
			if len(traces) > limit {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned too many recall traces")
			}
			var encoded []l1RecallTraceDTO
			if traces != nil {
				encoded = make([]l1RecallTraceDTO, len(traces))
				for index, trace := range traces {
					encoded[index], err = l1RecallTraceFromDomain(trace)
					if err != nil {
						return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid recall trace")
					}
				}
			}
			return l1ReadyResult(l1RecentRecallTracesResult{Traces: encoded})
		}},
	}

	for _, item := range registrations {
		var err error
		if item.mutating {
			err = handler.RegisterRecoverable(GroupL1, item.name, item.execute, item.reconcile)
		} else {
			err = handler.Register(GroupL1, item.name, false, item.fn)
		}
		if err != nil {
			return fmt.Errorf("register l1 %s: %w", item.name, err)
		}
	}
	return nil
}

func isNilL1Owner(owner L1GroupOwner) bool {
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

func rejectL1Payload(operation string, mutating bool) error {
	wire := NewError(ErrorCodeSchemaRejected, "l1 "+operation+" payload rejected")
	if mutating {
		return ownerRolledBack(wire)
	}
	return wire
}

func l1OperationIdentity(mutation MutationMetadata, operation string) (l1sqlite.ConversationL1OperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return l1sqlite.ConversationL1OperationIdentity{}, errors.New("l1 mutation identity is incomplete")
	}
	hash := sha256.Sum256(mutation.Payload)
	return l1sqlite.ConversationL1OperationIdentity{
		OpID: mutation.OpID, Operation: operation, PayloadSHA256: hex.EncodeToString(hash[:]),
		WriterGeneration: mutation.JournalGeneration,
	}, nil
}

func reconcileL1OwnerReceipt(ctx context.Context, owner L1RecoverableGroupOwner, mutation MutationMetadata, operation string, decode func(json.RawMessage) (any, error)) (ReconcileDecision, error) {
	identity, err := l1OperationIdentity(mutation, operation)
	if err != nil {
		return UnknownOutcome(), err
	}
	result, found, err := owner.LookupConversationL1OperationReceipt(ctx, identity)
	if err != nil {
		return UnknownOutcome(), err
	}
	if !found {
		return ConfirmedNotCommitted(), nil
	}
	wireResult, err := decode(result)
	if err != nil {
		return UnknownOutcome(), err
	}
	return Committed(wireResult), nil
}

func l1EventMatchesAppendRequest(entry *l1sqlite.L1EventLogEntry, payload l1AppendEventPayload, eventPayload map[string]interface{}) bool {
	return entry != nil && entry.EventType == payload.EventType && entry.Namespace == payload.Namespace &&
		entry.SessionID == payload.SessionID && entry.ThreadID == payload.ThreadID && entry.ThreadSeq == payload.ThreadSeq &&
		entry.ThreadKind == payload.ThreadKind && entry.Source == payload.Source && l1JSONObjectsEqual(entry.Payload, eventPayload)
}

func l1JSONObjectsEqual(left, right map[string]interface{}) bool {
	leftJSON, leftErr := l1ObjectToRaw(left)
	rightJSON, rightErr := l1ObjectToRaw(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func l1PromotionResultMatchesRequest(event *l1sqlite.L1MemoryEvent, payload l1PromoteMemoryPayload) bool {
	if event == nil || event.Namespace != payload.TargetNamespace || event.MemoryState != l1sqlite.MemoryStateConfirmed ||
		event.ID == payload.ID || !strings.HasPrefix(event.ID, payload.TargetNamespace+":"+payload.ID+":") || event.Source != "promoter" {
		return false
	}
	return event.Meta["promoted_from"] == payload.ID && event.Meta["promoted_by"] == payload.PromotedBy
}

func decodeL1ReceiptResult(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || len(raw) > 8<<20 || inspectL1JSON(raw, 8<<20, 8<<20, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, false) != nil {
		return errors.New("l1 operation receipt result is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode l1 operation receipt result: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("l1 operation receipt result contains trailing data")
	}
	return nil
}

func validateL1VoidReceipt(raw json.RawMessage) error {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("l1 void operation receipt result is not null")
	}
	return nil
}

func l1MutationOwnerFailed(operation string, err error) error {
	if errors.Is(err, l1sqlite.ErrConversationL1OperationNeedsSingleSQLiteStore) {
		return ownerRolledBack(NewError(ErrorCodeStoreUnavailable, "conversation memory promotion archive owner lacks atomic receipt reconciliation"))
	}
	return l1MutationFailed(operation)
}

func l1MutationFailed(operation string) error {
	return NewError(ErrorCodeStoreUnavailable, "l1 owner "+operation+" failed")
}

func l1OwnerReadFailed(operation string, err error) error {
	if operation == l1OpLatestConversationThreadReference {
		switch {
		case errors.Is(err, domconv.ErrConversationTurnInvalid):
			return NewError(ErrorCodeSchemaRejected, "conversation thread reference request rejected")
		case errors.Is(err, domconv.ErrConversationTurnUnavailable):
			return NewError(ErrorCodeStoreUnavailable, "conversation thread owner is unavailable")
		case errors.Is(err, domconv.ErrConversationTurnInternal):
			return NewError(turnErrorCodeInternal, "conversation thread owner failed")
		case errors.Is(err, domconv.ErrConversationTurnConflict):
			return NewError(ErrorCodeDuplicateConflict, "conversation thread reference conflicts with stored state")
		case errors.Is(err, domconv.ErrThreadNotFound):
			return NewError(turnErrorCodeThreadAbsent, "conversation thread was not found")
		}
	}
	return NewError(ErrorCodeStoreUnavailable, "l1 owner "+operation+" failed")
}

func validL1SaveMessagePayload(payload l1SaveMessagePayload) bool {
	return validL1SessionID(payload.SessionID, false) && payload.ThreadID.Validate() == nil && payload.ThreadSeq.Validate() == nil &&
		payload.ThreadKind.Validate() == nil && validL1Namespace(payload.Namespace, true) &&
		validL1Text(string(payload.Message.Speaker), 128, true) && validL1Text(payload.Message.Msg, l1MaxTextBytes, true) &&
		validL1Time(payload.Message.Timestamp, true) && validL1Map(payload.Message.Meta) &&
		(payload.MemoryState == "" || validL1MemoryState(payload.MemoryState))
}

func validL1SaveSearchCachePayload(payload l1SaveSearchCachePayload) bool {
	if !validL1Provider(payload.Provider, true) || !validL1Text(payload.RawQuery, l1MaxJSONTextBytes, true) ||
		len(payload.ResultsJSON) > l1MaxJSONValueBytes || (payload.ResultsJSON != "" && inspectL1JSON([]byte(payload.ResultsJSON), l1MaxJSONValueBytes, l1MaxJSONTextBytes, l1MaxJSONDepth, l1MaxJSONNodes, l1MaxCollectionEntries, false) != nil) ||
		!validL1StringSlice(payload.SourceURLs, l1MaxSearchURLs, 8<<10) || payload.TTL < 0 || payload.TTL > 365*24*time.Hour {
		return false
	}
	return true
}

func validL1SearchCacheQueryPayload(payload l1SearchCacheQueryPayload) bool {
	return validL1Provider(payload.Provider, true) && validL1Text(payload.RawQuery, l1MaxJSONTextBytes, true) && validL1Time(payload.Now, true)
}

func validL1SimilarSearchCachePayload(payload l1SimilarSearchCachePayload) bool {
	return validL1Provider(payload.Provider, true) && validL1Text(payload.RawQuery, l1MaxJSONTextBytes, true) && validL1Time(payload.Now, true) &&
		!math.IsNaN(payload.Threshold) && !math.IsInf(payload.Threshold, 0) && payload.Threshold >= 0 && payload.Threshold <= 1
}

func validL1InvalidateSearchCachePayload(payload l1InvalidateSearchCachePayload) bool {
	return validL1Provider(payload.Provider, true) && validL1Text(payload.RawQuery, l1MaxJSONTextBytes, true)
}

func validL1KnowledgeSearchPayload(payload l1KnowledgeSearchPayload) bool {
	return validL1Label(payload.Domain) && validL1Text(payload.Query, l1MaxJSONTextBytes, true) && validL1Limit(payload.Limit)
}

func validL1WikiSearchPayload(payload l1WikiSearchPayload) bool {
	return validL1Text(payload.Query, l1MaxJSONTextBytes, true) && validL1Limit(payload.Limit)
}

func validL1AppendEventPayload(payload l1AppendEventPayload) bool {
	return validL1EventName(payload.EventType) && validL1Namespace(payload.Namespace, false) &&
		validL1SessionThreadTuple(payload.SessionID, payload.ThreadID, payload.ThreadSeq, payload.ThreadKind) &&
		validL1Text(payload.Source, 128, true)
}

func validL1RecentEventsPayload(payload l1RecentEventsPayload) bool {
	return validL1Namespace(payload.Namespace, false) && validL1Limit(payload.Limit)
}

func validL1UpdateMemoryStatePayload(payload l1UpdateMemoryStatePayload) bool {
	return validL1Text(payload.ID, 2_048, true) && strings.TrimSpace(payload.ID) == payload.ID && validL1MemoryState(payload.MemoryState)
}

func validL1PromoteMemoryPayload(payload l1PromoteMemoryPayload) bool {
	return validL1Text(payload.ID, 2_048, true) && strings.TrimSpace(payload.ID) == payload.ID &&
		validL1Namespace(payload.TargetNamespace, false) && validL1Text(payload.PromotedBy, 256, false)
}

func validL1RecentByNamespacePayload(payload l1RecentByNamespacePayload) bool {
	return validL1Namespace(payload.Namespace, false) && validL1Limit(payload.Limit)
}

func validL1RecentByStatePayload(payload l1RecentByStatePayload) bool {
	return validL1MemoryState(payload.MemoryState) && validL1Limit(payload.Limit)
}

func validL1RecentBySessionPayload(payload l1RecentBySessionPayload) bool {
	return validL1SessionID(payload.SessionID, false) && validL1Limit(payload.Limit)
}

func validL1LatestThreadResult(result l1LatestThreadResult) bool {
	if !result.Found {
		return result.ThreadID == "" && result.ThreadSeq == 0 && result.ThreadKind == ""
	}
	return result.ThreadID.Validate() == nil && result.ThreadSeq.Validate() == nil && result.ThreadKind.Validate() == nil
}

func l1MemoryEventsResultFromOwner(events []l1sqlite.L1MemoryEvent, limit, defaultLimit int) (any, error) {
	if len(events) > l1EffectiveLimit(limit, defaultLimit) {
		return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned too many memory events")
	}
	var encoded []l1MemoryEventDTO
	if events != nil {
		encoded = make([]l1MemoryEventDTO, len(events))
		for index, event := range events {
			var err error
			encoded[index], err = l1MemoryEventFromDomain(event)
			if err != nil {
				return nil, NewError(ErrorCodeStoreUnavailable, "l1 owner returned an invalid memory event")
			}
		}
	}
	return l1ReadyResult(l1MemoryEventsResult{Events: encoded})
}

func l1EffectiveLimit(limit, defaultLimit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	return limit
}

func validL1Map(value map[string]interface{}) bool {
	_, err := l1ObjectToRaw(value)
	return err == nil
}

// L1StoreClient implements the conversation L1 owner surface through storage RPC.
type L1StoreClient struct{ client *Client }

func NewL1StoreClient(client *Client) *L1StoreClient { return &L1StoreClient{client: client} }

var _ L1GroupOwner = (*L1StoreClient)(nil)

// Close is a client lifecycle hook; it does not close the shared storage RPC client.
func (*L1StoreClient) Close() error { return nil }

func (s *L1StoreClient) call(ctx context.Context, name string, payload any, mutating bool, destination any, validate func() error) error {
	if s == nil || s.client == nil {
		return NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	encoded, err := marshalL1Payload(payload)
	if err != nil {
		return err
	}
	var output any
	if destination != nil {
		output = &l1CallResult{destination: destination, validate: validate, mutating: mutating}
	}
	result, _ := output.(*l1CallResult)
	if err := s.client.Call(ctx, GroupL1, name, encoded, output); err != nil {
		return err
	}
	if result != nil && result.err != nil {
		return NewError(ErrorCodeStoreUnavailable, "l1 owner result could not be reconstructed")
	}
	return nil
}

func (s *L1StoreClient) SaveMessage(ctx context.Context, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, namespace string, message domconv.Message, memoryState string) error {
	payload := l1SaveMessagePayload{SessionID: sessionID, ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Namespace: namespace, Message: message, MemoryState: memoryState}
	if !validL1SaveMessagePayload(payload) {
		return NewError(ErrorCodeSchemaRejected, "l1 message payload rejected")
	}
	return s.call(ctx, l1OpSaveMessage, payload, true, nil, nil)
}

func (s *L1StoreClient) SaveSearchCache(ctx context.Context, provider, rawQuery, resultsJSON string, sourceURLs []string, ttl time.Duration) (*l1sqlite.L1SearchCacheEntry, error) {
	payload := l1SaveSearchCachePayload{Provider: provider, RawQuery: rawQuery, ResultsJSON: resultsJSON, SourceURLs: sourceURLs, TTL: ttl}
	if !validL1SaveSearchCachePayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 search cache payload rejected")
	}
	var wire l1SearchCacheResult
	var entry *l1sqlite.L1SearchCacheEntry
	err := s.call(ctx, l1OpSaveSearchCache, payload, true, &wire, func() error {
		if wire.Entry == nil {
			return errors.New("saved search cache result is missing")
		}
		var err error
		entry, err = wire.Entry.domain()
		if err != nil {
			return err
		}
		if !entry.MatchesStorageHostSaveRequest(provider, rawQuery, resultsJSON, sourceURLs, ttl) {
			return errors.New("saved search cache result does not match request")
		}
		return nil
	})
	return entry, err
}

func (s *L1StoreClient) GetFreshSearchCache(ctx context.Context, provider, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error) {
	payload := l1SearchCacheQueryPayload{Provider: provider, RawQuery: rawQuery, Now: now}
	if !validL1SearchCacheQueryPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 search cache query rejected")
	}
	var wire l1SearchCacheResult
	var entry *l1sqlite.L1SearchCacheEntry
	err := s.call(ctx, l1OpGetFreshSearchCache, payload, false, &wire, func() error {
		if wire.Entry == nil {
			entry = nil
			return nil
		}
		var err error
		entry, err = wire.Entry.domain()
		return err
	})
	return entry, err
}

func (s *L1StoreClient) GetSimilarFreshSearchCache(ctx context.Context, provider, rawQuery string, now time.Time, threshold float64) (*l1sqlite.L1SearchCacheEntry, error) {
	payload := l1SimilarSearchCachePayload{Provider: provider, RawQuery: rawQuery, Now: now, Threshold: threshold}
	if !validL1SimilarSearchCachePayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 similar search cache query rejected")
	}
	var wire l1SearchCacheResult
	var entry *l1sqlite.L1SearchCacheEntry
	err := s.call(ctx, l1OpGetSimilarFreshSearchCache, payload, false, &wire, func() error {
		if wire.Entry == nil {
			entry = nil
			return nil
		}
		var err error
		entry, err = wire.Entry.domain()
		return err
	})
	return entry, err
}

func (s *L1StoreClient) InvalidateSearchCache(ctx context.Context, provider, rawQuery string) (int64, error) {
	payload := l1InvalidateSearchCachePayload{Provider: provider, RawQuery: rawQuery}
	if !validL1InvalidateSearchCachePayload(payload) {
		return 0, NewError(ErrorCodeSchemaRejected, "l1 search cache invalidation rejected")
	}
	var wire l1AffectedResult
	err := s.call(ctx, l1OpInvalidateSearchCache, payload, true, &wire, func() error {
		if wire.Affected < 0 {
			return errors.New("negative invalidation count")
		}
		return nil
	})
	return wire.Affected, err
}

func (s *L1StoreClient) SearchKnowledgeItemsFTS(ctx context.Context, domain, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	payload := l1KnowledgeSearchPayload{Domain: domain, Query: query, Limit: limit}
	if !validL1KnowledgeSearchPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 knowledge search rejected")
	}
	var wire l1KnowledgeSearchResult
	var items []l1sqlite.L1KnowledgeItem
	err := s.call(ctx, l1OpSearchKnowledgeItemsFTS, payload, false, &wire, func() error {
		if len(wire.Items) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many knowledge results")
		}
		if wire.Items == nil {
			items = nil
			return nil
		}
		items = make([]l1sqlite.L1KnowledgeItem, len(wire.Items))
		for index, dto := range wire.Items {
			item, err := dto.domain()
			if err != nil {
				return err
			}
			items[index] = item
		}
		return nil
	})
	return items, err
}

func (s *L1StoreClient) SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error) {
	payload := l1WikiSearchPayload{Query: query, Limit: limit}
	if !validL1WikiSearchPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 wiki search rejected")
	}
	var wire l1WikiSearchResult
	var items []l1sqlite.WikiPageIndexItem
	err := s.call(ctx, l1OpSearchWikiPageIndex, payload, false, &wire, func() error {
		if len(wire.Items) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many wiki results")
		}
		if wire.Items == nil {
			items = nil
			return nil
		}
		items = make([]l1sqlite.WikiPageIndexItem, len(wire.Items))
		for index, dto := range wire.Items {
			item, err := dto.domain()
			if err != nil {
				return err
			}
			items[index] = item
		}
		return nil
	})
	return items, err
}

func (s *L1StoreClient) AppendEvent(ctx context.Context, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payloadMap map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	payloadRaw, err := l1ObjectToRaw(payloadMap)
	if err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 event payload rejected")
	}
	payload := l1AppendEventPayload{EventType: eventType, Namespace: namespace, SessionID: sessionID, ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Payload: payloadRaw, Source: source}
	if !validL1AppendEventPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 event payload rejected")
	}
	var wire l1EventLogResult
	var entry *l1sqlite.L1EventLogEntry
	err = s.call(ctx, l1OpAppendEvent, payload, true, &wire, func() error {
		value, err := wire.Event.domain()
		if err != nil {
			return err
		}
		if !l1EventMatchesAppendRequest(&value, payload, payloadMap) {
			return errors.New("appended event does not match request")
		}
		entry = &value
		return nil
	})
	return entry, err
}

func (s *L1StoreClient) RecentEvents(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1EventLogEntry, error) {
	payload := l1RecentEventsPayload{Namespace: namespace, Limit: limit}
	if !validL1RecentEventsPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 recent events query rejected")
	}
	var wire l1RecentEventsResult
	var events []l1sqlite.L1EventLogEntry
	err := s.call(ctx, l1OpRecentEvents, payload, false, &wire, func() error {
		if len(wire.Events) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many event results")
		}
		if wire.Events == nil {
			events = nil
			return nil
		}
		events = make([]l1sqlite.L1EventLogEntry, len(wire.Events))
		for index, dto := range wire.Events {
			entry, err := dto.domain()
			if err != nil || entry.Namespace != namespace {
				return errors.New("l1 event result does not match query")
			}
			events[index] = entry
		}
		return nil
	})
	return events, err
}

func (s *L1StoreClient) UpdateMemoryState(ctx context.Context, id, memoryState string) error {
	payload := l1UpdateMemoryStatePayload{ID: id, MemoryState: memoryState}
	if !validL1UpdateMemoryStatePayload(payload) {
		return NewError(ErrorCodeSchemaRejected, "l1 memory state update rejected")
	}
	return s.call(ctx, l1OpUpdateMemoryState, payload, true, nil, nil)
}

func (s *L1StoreClient) PromoteMemoryToNamespace(ctx context.Context, id, targetNamespace, promotedBy string) (*l1sqlite.L1MemoryEvent, error) {
	payload := l1PromoteMemoryPayload{ID: id, TargetNamespace: targetNamespace, PromotedBy: promotedBy}
	if !validL1PromoteMemoryPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 memory promotion rejected")
	}
	var wire l1MemoryEventResult
	var event *l1sqlite.L1MemoryEvent
	err := s.call(ctx, l1OpPromoteMemoryToNamespace, payload, true, &wire, func() error {
		if wire.Event == nil {
			return errors.New("promoted memory result is missing")
		}
		value, err := wire.Event.domain()
		if err != nil || !l1PromotionResultMatchesRequest(&value, payload) {
			return errors.New("promoted memory result does not match request")
		}
		event = &value
		return nil
	})
	return event, err
}

func (s *L1StoreClient) RecentByNamespace(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	payload := l1RecentByNamespacePayload{Namespace: namespace, Limit: limit}
	if !validL1RecentByNamespacePayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 namespace memory query rejected")
	}
	var wire l1MemoryEventsResult
	var events []l1sqlite.L1MemoryEvent
	err := s.call(ctx, l1OpRecentByNamespace, payload, false, &wire, func() error {
		if len(wire.Events) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many memory results")
		}
		var err error
		events, err = l1MemoryEventsToDomain(wire.Events)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Namespace != namespace {
				return errors.New("memory result does not match namespace")
			}
		}
		return nil
	})
	return events, err
}

func (s *L1StoreClient) RecentByState(ctx context.Context, memoryState string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	payload := l1RecentByStatePayload{MemoryState: memoryState, Limit: limit}
	if !validL1RecentByStatePayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 memory state query rejected")
	}
	var wire l1MemoryEventsResult
	var events []l1sqlite.L1MemoryEvent
	err := s.call(ctx, l1OpRecentByState, payload, false, &wire, func() error {
		if len(wire.Events) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many memory results")
		}
		var err error
		events, err = l1MemoryEventsToDomain(wire.Events)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.MemoryState != memoryState {
				return errors.New("memory result does not match state")
			}
		}
		return nil
	})
	return events, err
}

func (s *L1StoreClient) RecentBySession(ctx context.Context, sessionID string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	payload := l1RecentBySessionPayload{SessionID: sessionID, Limit: limit}
	if !validL1RecentBySessionPayload(payload) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 session memory query rejected")
	}
	var wire l1MemoryEventsResult
	var events []l1sqlite.L1MemoryEvent
	err := s.call(ctx, l1OpRecentBySession, payload, false, &wire, func() error {
		if len(wire.Events) > l1EffectiveLimit(limit, 20) {
			return errors.New("too many memory results")
		}
		var err error
		events, err = l1MemoryEventsToDomain(wire.Events)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.SessionID != sessionID {
				return errors.New("memory result does not match session")
			}
		}
		return nil
	})
	return events, err
}

func l1MemoryEventsToDomain(wire []l1MemoryEventDTO) ([]l1sqlite.L1MemoryEvent, error) {
	if wire == nil {
		return nil, nil
	}
	events := make([]l1sqlite.L1MemoryEvent, len(wire))
	for index, dto := range wire {
		event, err := dto.domain()
		if err != nil {
			return nil, err
		}
		events[index] = event
	}
	return events, nil
}

func (s *L1StoreClient) LatestConversationThreadReference(ctx context.Context, sessionID string) (modulecore.ThreadID, modulecore.ThreadSeq, modulecore.ThreadKind, bool, error) {
	payload := l1LatestThreadPayload{SessionID: sessionID}
	if !validL1SessionID(sessionID, false) {
		return "", 0, "", false, mapTurnClientError(NewError(ErrorCodeSchemaRejected, "conversation thread reference query rejected"))
	}
	var wire l1LatestThreadResult
	err := s.call(ctx, l1OpLatestConversationThreadReference, payload, false, &wire, func() error {
		if !validL1LatestThreadResult(wire) {
			return errors.New("conversation thread reference result rejected")
		}
		return nil
	})
	if err != nil {
		return "", 0, "", false, mapTurnClientError(err)
	}
	return wire.ThreadID, wire.ThreadSeq, wire.ThreadKind, wire.Found, nil
}

func (s *L1StoreClient) SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error {
	if !validL1RecallTrace(trace) {
		return NewError(ErrorCodeSchemaRejected, "l1 recall trace payload rejected")
	}
	wire, err := l1RecallTraceFromDomain(trace)
	if err != nil {
		return NewError(ErrorCodeSchemaRejected, "l1 recall trace payload rejected")
	}
	return s.call(ctx, l1OpSaveRecallTrace, l1SaveRecallTracePayload{Trace: wire}, true, nil, nil)
}

func (s *L1StoreClient) RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error) {
	payload := l1RecentRecallTracesPayload{SessionID: sessionID, Limit: limit}
	if !validL1SessionID(sessionID, true) || !validL1Limit(limit) {
		return nil, NewError(ErrorCodeSchemaRejected, "l1 recall trace query rejected")
	}
	var wire l1RecentRecallTracesResult
	var traces []domconv.RecallTrace
	err := s.call(ctx, l1OpRecentRecallTraces, payload, false, &wire, func() error {
		maximum := l1EffectiveLimit(limit, 20)
		if maximum > 100 {
			maximum = 100
		}
		if len(wire.Traces) > maximum {
			return errors.New("too many recall traces")
		}
		if wire.Traces == nil {
			traces = nil
			return nil
		}
		traces = make([]domconv.RecallTrace, len(wire.Traces))
		for index, dto := range wire.Traces {
			trace, err := dto.domain()
			if err != nil || (sessionID != "" && trace.SessionID != sessionID) {
				return errors.New("recall trace result does not match query")
			}
			traces[index] = trace
		}
		return nil
	})
	return traces, err
}
