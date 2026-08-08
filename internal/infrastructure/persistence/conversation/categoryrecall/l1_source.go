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
	items, err := s.store.SearchKnowledgeItemsFTS(ctx, query.Category, query.Message, boundedLimit(query.Limit))
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	result := domconv.CategoryRecallResult{}
	for _, item := range items {
		category := normalizeCategory(item.Domain)
		if category == "" || category == "general" {
			category = normalizeCategory(query.Category)
		}
		if query.Category != "" && category != normalizeCategory(query.Category) {
			continue
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
		}
		sensitivity := metadataString(item.Meta, "sensitivity")
		if sensitivity == "" {
			sensitivity = "normal"
		}
		state := metadataString(item.Meta, "validation_status")
		if state == "" {
			state = domconv.CategoryRecordStateValidated
		}
		result.Records = append(result.Records, domconv.CategoryRecallRecord{
			Category: category, SourceID: s.ID(), RecordID: item.ID, Title: title, Summary: summary,
			ProvenanceURLs: nonEmptyStrings(item.SourceURL), RetrievedAt: retrievedAt, ValidatedAt: validatedAt,
			FreshUntil: freshUntil, State: state, Sensitivity: sensitivity,
			Scope: scope, Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		})
	}
	return result, nil
}
