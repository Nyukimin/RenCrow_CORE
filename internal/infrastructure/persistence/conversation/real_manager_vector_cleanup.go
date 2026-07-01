package conversation

import "context"

func (m *RealConversationManager) CleanupMemoryVectors(ctx context.Context, items []L1VectorCleanupItem) (*L1VectorCleanupResult, error) {
	if m == nil || m.vectordbStore == nil || len(items) == 0 {
		return &L1VectorCleanupResult{}, nil
	}
	return m.vectordbStore.CleanupMemoryVectors(ctx, items)
}
