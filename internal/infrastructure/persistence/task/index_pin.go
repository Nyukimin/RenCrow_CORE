package task

import (
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// taskPin is what the fold of the Task log compares between two versions of the
// same Task: the criteria revision fixed by its first save and the append-only
// native OPS Resume claims (see foldTaskRecords and transactionStore.appendTask).
type taskPin struct {
	revision string
	claims   []domaintask.NativeOPSResumeClaim
}

func pinOfTask(value domaintask.Task) taskPin {
	return taskPin{revision: value.ExpectedCriteriaRevision, claims: value.NativeResumeClaims}
}

// checkTaskPinTransition refuses a version of a Task that changes its criteria
// revision (including setting or clearing it) or rewrites an earlier Resume
// claim. It is the one rule shared by the fold of the log and the index.
func checkTaskPinTransition(previous taskPin, next domaintask.Task) error {
	if previous.revision != next.ExpectedCriteriaRevision {
		return ErrExpectedCriteriaRevisionImmutable
	}
	if !domaintask.NativeOPSResumeClaimsExtend(previous.claims, next.NativeResumeClaims) {
		return ErrNativeOPSResumeClaimImmutable
	}
	return nil
}

// committedPinLocator reports whether the Task is in the index and, when its
// latest version carries a criteria revision or Resume claims, where that line
// is. A Task with neither needs no read: its pin state is the empty one. The
// caller holds mu for reading or applyMu.
func (ix *taskIndex) committedPinLocator(key key16) (flags uint8, pos linePos, found bool) {
	slot, ok := ix.taskMap[key]
	if !ok {
		return 0, linePos{}, false
	}
	rec := &ix.tasks[slot]
	flags = rec.flags & (flagPinned | flagClaims)
	if flags != 0 {
		pos = ix.lt.pos(rec.stateTail)
	}
	return flags, pos, true
}

// readTaskPin returns the pin state located by committedPinLocator. It needs no
// lock: committed lines never change.
func (ix *taskIndex) readTaskPin(flags uint8, pos linePos, key key16) (taskPin, error) {
	if flags == 0 {
		return taskPin{}, nil
	}
	value, err := ix.readTaskAt(pos, key)
	if err != nil {
		return taskPin{}, err
	}
	return pinOfTask(value), nil
}
