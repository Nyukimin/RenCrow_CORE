package execution

import "context"

// Repository は実行監査レコードの永続化I/F
type Repository interface {
	Create(ctx context.Context, record Record) error
	UpdateStatus(ctx context.Context, jobID, actionID string, status Status, errMsg string) (Record, error)
	Get(ctx context.Context, jobID, actionID string) (Record, error)
	CountByStatus(ctx context.Context) (map[Status]int, error)
}
