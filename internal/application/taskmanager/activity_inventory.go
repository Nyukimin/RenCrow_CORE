package taskmanager

import (
	"context"
	"errors"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// ListTaskActivityInventory returns bounded owner-authorized summaries from
// the canonical Task index without exposing the Task's private content.
func (m *Manager) ListTaskActivityInventory(ctx context.Context, filter domaintask.TaskActivityInventoryFilter, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.TaskActivityInventoryPage, error) {
	if m == nil || m.store == nil {
		return domaintask.TaskActivityInventoryPage{}, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.TaskActivityInventoryReader)
	if !ok || reader == nil {
		return domaintask.TaskActivityInventoryPage{}, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.ListTaskActivityInventory(ctx, filter, authorize)
}
