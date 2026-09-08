package action

import (
	"context"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Store is the persistence boundary owned by the Action owner.
// Transaction callbacks receive a store that is valid only for the callback.
type Store interface {
	Transaction(context.Context, func(Store) error) error
	ReadTransaction(context.Context, func(Store) error) error
	SaveAction(context.Context, Action) error
	GetAction(context.Context, modulecore.ActionID) (Action, error)
	ListActions(context.Context, Filter) ([]Action, error)
	SaveAttempt(context.Context, Attempt) error
	GetAttempt(context.Context, modulecore.AttemptID) (Attempt, error)
	ListAttempts(context.Context, AttemptFilter) ([]Attempt, error)
}
