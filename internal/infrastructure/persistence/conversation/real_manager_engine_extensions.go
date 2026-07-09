package conversation

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func (r *RealConversationManager) GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error) {
	if r == nil || r.l1Store == nil {
		return nil, nil
	}
	return r.l1Store.GetFreshSearchCache(ctx, provider, rawQuery, now)
}

func (r *RealConversationManager) SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	if r == nil || r.l1Store == nil {
		return nil, nil
	}
	return r.l1Store.SearchKnowledgeItemsFTS(ctx, domain, query, limit)
}

func (r *RealConversationManager) SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error) {
	if r == nil || r.l1Store == nil {
		return nil, nil
	}
	return r.l1Store.SearchWikiPageIndex(ctx, query, limit)
}

func (r *RealConversationManager) SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error {
	if r == nil || r.l1Store == nil {
		return nil
	}
	return r.l1Store.SaveRecallTrace(ctx, trace)
}
