package task

import (
	"errors"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ErrTaskExists reports that a Task with the requested TaskID is already
// stored. A TaskID names one Task for its whole life: creating a Task never
// replaces or re-opens an existing one, so a second creation under the same ID
// is refused instead of appending a new version over it.
//
// It is a distinct error. It is not ErrNotFound, a validation error, or a
// commit-uncertainty error, and callers decide with errors.Is whether a repeat
// is a retransmission of the same request (which may be treated as already
// done when they hold evidence of that) or an internal inconsistency.
var ErrTaskExists = errors.New("task already exists")

// NewTaskExistsError wraps ErrTaskExists with the refused TaskID. It carries
// the ID only: the stored record is never copied into the message.
func NewTaskExistsError(taskID modulecore.TaskID) error {
	return fmt.Errorf("%w: task_id=%s", ErrTaskExists, taskID)
}
