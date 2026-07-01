package conversation

import (
	"context"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

func (r *RealConversationManager) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*domconv.ThreadSummary, error) {
	if r == nil || r.duckdbStore == nil {
		return []*domconv.ThreadSummary{}, nil
	}
	return r.duckdbStore.GetSessionHistory(ctx, sessionID, limit)
}

func (r *RealConversationManager) SearchByDomain(ctx context.Context, domain string, limit int) ([]*domconv.ThreadSummary, error) {
	if r == nil || r.duckdbStore == nil {
		return []*domconv.ThreadSummary{}, nil
	}
	return r.duckdbStore.SearchByDomain(ctx, domain, limit)
}

func (r *RealConversationManager) SearchKnowledgeArchiveFTS(ctx context.Context, domain string, query string, limit int) ([]L1KnowledgeItem, error) {
	if r == nil || r.duckdbStore == nil {
		return []L1KnowledgeItem{}, nil
	}
	return r.duckdbStore.SearchKnowledgeArchiveFTS(ctx, domain, query, limit)
}

func (r *RealConversationManager) ExportThreadSummariesParquet(ctx context.Context, outputPath string) error {
	if r == nil || r.duckdbStore == nil {
		return nil
	}
	return r.duckdbStore.ExportThreadSummariesParquet(ctx, outputPath)
}

func (r *RealConversationManager) ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error) {
	if r == nil || r.duckdbStore == nil {
		return map[string]string{}, nil
	}
	return r.duckdbStore.ExportL1ArchivesParquet(ctx, outputDir)
}
