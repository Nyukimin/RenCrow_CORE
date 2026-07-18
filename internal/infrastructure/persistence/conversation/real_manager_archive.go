package conversation

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func (r *RealConversationManager) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]*domconv.ThreadSummary, error) {
	if r == nil || r.archiveStore == nil {
		return []*domconv.ThreadSummary{}, nil
	}
	return r.archiveStore.GetSessionHistory(ctx, sessionID, limit)
}

func (r *RealConversationManager) SearchByDomain(ctx context.Context, domain string, limit int) ([]*domconv.ThreadSummary, error) {
	if r == nil || r.archiveStore == nil {
		return []*domconv.ThreadSummary{}, nil
	}
	return r.archiveStore.SearchByDomain(ctx, domain, limit)
}

func (r *RealConversationManager) SearchKnowledgeArchiveFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	if r == nil || r.archiveStore == nil {
		return []l1sqlite.L1KnowledgeItem{}, nil
	}
	return r.archiveStore.SearchKnowledgeArchiveFTS(ctx, domain, query, limit)
}

func (r *RealConversationManager) ExportThreadSummariesParquet(ctx context.Context, outputPath string) error {
	if r == nil || r.archiveStore == nil {
		return nil
	}
	return r.archiveStore.ExportThreadSummariesParquet(ctx, outputPath)
}

func (r *RealConversationManager) ExportL1ArchivesParquet(ctx context.Context, outputDir string) (map[string]string, error) {
	if r == nil || r.archiveStore == nil {
		return map[string]string{}, nil
	}
	return r.archiveStore.ExportL1ArchivesParquet(ctx, outputDir)
}
