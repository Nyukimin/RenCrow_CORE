package task

import (
	"context"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Store is the canonical durable Task/Run store contract. Transaction
// callbacks receive the transaction view, so all reads and writes in one
// domain decision observe the same pending state and commit together.
type Store interface {
	WriterGeneration() (uint64, error)

	SaveTask(context.Context, Task) error
	GetTask(context.Context, modulecore.TaskID) (Task, error)
	ListTasks(context.Context, Filter) ([]Task, error)

	SaveRun(context.Context, Run) error
	GetRun(context.Context, modulecore.RunID) (Run, error)
	ListRuns(context.Context, RunFilter) ([]Run, error)

	SaveContext(context.Context, SharedRoleContext) error
	GetContext(context.Context, modulecore.TaskID) (SharedRoleContext, error)

	SaveNotification(context.Context, Notification) error
	ListNotifications(context.Context, int, bool) ([]Notification, error)

	Transaction(context.Context, func(Store) error) error
	ReadTransaction(context.Context, func(Store) error) error
}
