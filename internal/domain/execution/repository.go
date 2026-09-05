package execution

import (
	"context"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Repository は実行監査レコードの永続化I/F
type Repository interface {
	Create(ctx context.Context, record Record) error
	UpdateStatus(ctx context.Context, taskID modulecore.TaskID, actionID string, status Status, errMsg string) (Record, error)
	Get(ctx context.Context, taskID modulecore.TaskID, actionID string) (Record, error)
	CountByStatus(ctx context.Context) (map[Status]int, error)
}
