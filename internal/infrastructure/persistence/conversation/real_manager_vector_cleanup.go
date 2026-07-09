package conversation

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func (m *RealConversationManager) CleanupMemoryVectors(ctx context.Context, items []l1sqlite.L1VectorCleanupItem) (*l1sqlite.L1VectorCleanupResult, error) {
	if m == nil || m.vectordbStore == nil || len(items) == 0 {
		return &l1sqlite.L1VectorCleanupResult{}, nil
	}
	return m.vectordbStore.CleanupMemoryVectors(ctx, items)
}
