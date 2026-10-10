package categoryrecall

import (
	"context"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const DefaultL1CategoryRecallFreshness = 24 * time.Hour

type L1KnowledgeStore interface {
	SearchKnowledgeItemsFTS(context.Context, string, string, int) ([]l1sqlite.L1KnowledgeItem, error)
}

type L1KnowledgeRecallStore interface {
	SearchKnowledgeItemsForCategoryRecall(context.Context, string, string, int, func(l1sqlite.L1KnowledgeItem) bool) ([]l1sqlite.L1KnowledgeItem, error)
}

type L1KnowledgeSource struct {
	store L1KnowledgeStore
}

func NewL1KnowledgeSource(store L1KnowledgeStore) *L1KnowledgeSource {
	return &L1KnowledgeSource{store: store}
}

func (s *L1KnowledgeSource) ID() string { return "knowledge_l1" }

func (s *L1KnowledgeSource) Categories() []string {
	return []string{"movie", "drama", "person", "hobby", "book", "game", "news", "investment", "general"}
}

func (s *L1KnowledgeSource) Search(ctx context.Context, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	if s == nil || s.store == nil {
		return domconv.CategoryRecallResult{}, errUnavailable("L1 Knowledge store is not configured")
	}
	var items []l1sqlite.L1KnowledgeItem
	var err error
	result := domconv.CategoryRecallResult{}
	provenanceIntent := domconv.HasNativeRecallProvenanceIntent(ctx)
	if provenanceIntent {
		if recallStore, ok := s.store.(L1KnowledgeRecallStore); ok {
			items, err = recallStore.SearchKnowledgeItemsForCategoryRecall(ctx, query.Category, query.Message, boundedLimit(query.Limit), func(item l1sqlite.L1KnowledgeItem) bool {
				record, matchesCategory := knowledgeCategoryRecallRecord(item, query, s.ID(), provenanceIntent)
				if !matchesCategory {
					return false
				}
				if failure, rejected := domconv.CheckCategoryRecallRecordForRole(record, query, "worker"); rejected {
					result.Failures = append(result.Failures, failure)
					return false
				}
				return true
			})
		} else {
			items, err = s.store.SearchKnowledgeItemsFTS(ctx, query.Category, query.Message, boundedLimit(query.Limit))
		}
	} else {
		items, err = s.store.SearchKnowledgeItemsFTS(ctx, query.Category, query.Message, boundedLimit(query.Limit))
	}
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	for _, item := range items {
		if failureCode := knowledgeSourceFailureCode(item.SourceFailure); failureCode != "" {
			result.Failures = append(result.Failures, domconv.CategoryRecallFailure{
				Category: query.Category, SourceID: s.ID(), RecordID: item.ID,
				Code: failureCode, State: failureCode, Reason: failureCode,
				Retryable:  failureCode == domconv.CategoryRecallFailureSourceUnavailable,
				ObservedAt: query.Time,
			})
			continue
		}
		record, matchesCategory := knowledgeCategoryRecallRecord(item, query, s.ID(), provenanceIntent)
		if !matchesCategory {
			continue
		}
		quote := strings.TrimSpace(item.SummaryDraft)
		quoteCandidate := quote != "" && strings.Contains(item.RawText, quote)
		if provenanceIntent && quoteCandidate && item.PromptSource == nil {
			result.Failures = append(result.Failures, domconv.CategoryRecallFailure{
				Category: query.Category, SourceID: s.ID(), RecordID: item.ID,
				Code: domconv.CategoryRecallFailureInvalid, State: "invalid",
				Reason: domconv.CategoryRecallFailureInvalid, ObservedAt: query.Time,
			})
			continue
		}
		promptSource := record.PromptSource
		if !provenanceIntent {
			promptSource = nil
		}
		record.PromptSource = promptSource
		result.Records = append(result.Records, record)
	}
	return result, nil
}

func knowledgeCategoryRecallRecord(item l1sqlite.L1KnowledgeItem, query domconv.CategoryRecallQuery, sourceID string, provenanceIntent bool) (domconv.CategoryRecallRecord, bool) {
	category := normalizeCategory(item.Domain)
	if category == "" || category == "general" {
		category = normalizeCategory(query.Category)
	}
	if query.Category != "" && category != normalizeCategory(query.Category) {
		return domconv.CategoryRecallRecord{}, false
	}
	summary := strings.TrimSpace(item.SummaryDraft)
	if summary == "" {
		summary = strings.TrimSpace(item.RawText)
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = summary
	}
	retrievedAt := metadataTime(item.Meta, "retrieved_at")
	if retrievedAt.IsZero() {
		retrievedAt = item.UpdatedAt
	}
	validatedAt := metadataTime(item.Meta, "validated_at")
	if validatedAt.IsZero() {
		validatedAt = item.UpdatedAt
	}
	freshUntil := metadataTime(item.Meta, "fresh_until")
	if freshUntil.IsZero() && (category == "news" || category == "investment") && !retrievedAt.IsZero() {
		freshUntil = retrievedAt.Add(DefaultL1CategoryRecallFreshness)
	}
	scope := metadataString(item.Meta, "scope")
	if scope == "" {
		scope = "public"
	} else if provenanceIntent && strings.HasPrefix(query.UserScope, "user:") && scope == strings.TrimPrefix(query.UserScope, "user:") {
		// Older Knowledge rows store the authenticated owner ID directly.
		// Native provenance intent normalizes only after storage has applied
		// typed-scope filtering. Legacy recall keeps its original selection.
		scope = query.UserScope
	}
	sensitivity := metadataString(item.Meta, "sensitivity")
	if sensitivity == "" {
		sensitivity = "normal"
	}
	state := metadataString(item.Meta, "validation_status")
	if state == "" {
		state = domconv.CategoryRecordStateValidated
	}
	return domconv.CategoryRecallRecord{
		Category: category, SourceID: sourceID, RecordID: item.ID, Title: title, Summary: summary,
		ProvenanceURLs: nonEmptyStrings(item.SourceURL), RetrievedAt: retrievedAt, ValidatedAt: validatedAt,
		FreshUntil: freshUntil, State: state, Sensitivity: sensitivity,
		Scope: scope, Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		PromptSource: item.PromptSource,
	}, true
}

func knowledgeSourceFailureCode(failure l1sqlite.L1KnowledgeSourceFailure) string {
	switch failure {
	case l1sqlite.L1KnowledgeSourceFailureScopeDenied:
		return domconv.CategoryRecallFailureScopeDenied
	case l1sqlite.L1KnowledgeSourceFailureInvalid:
		return domconv.CategoryRecallFailureInvalid
	case l1sqlite.L1KnowledgeSourceFailureSourceUnavailable:
		return domconv.CategoryRecallFailureSourceUnavailable
	default:
		return ""
	}
}
