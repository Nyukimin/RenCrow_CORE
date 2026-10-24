package task

import (
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// taskPin is what the fold of the Task log compares between two versions of the
// same Task: first-save criteria revision and publication provenance, plus the
// append-only native OPS Resume claims (see foldTaskRecords and appendTask).
type taskPin struct {
	revision             string
	claims               []domaintask.NativeOPSResumeClaim
	originPublication    domaintask.OriginPublicationProvenance
	hasOriginPublication bool
}

func pinOfTask(value domaintask.Task) taskPin {
	pin := taskPin{revision: value.ExpectedCriteriaRevision, claims: value.NativeResumeClaims}
	if value.OriginPublication != nil {
		pin.originPublication = *value.OriginPublication
		pin.hasOriginPublication = true
	}
	return pin
}

// checkTaskPinTransition refuses changes to first-save criteria revision or
// origin publication provenance, and rewrites of earlier Resume claims. It is
// the one rule shared by the fold of the log and the index.
func checkTaskPinTransition(previous taskPin, next domaintask.Task) error {
	if previous.revision != next.ExpectedCriteriaRevision {
		return ErrExpectedCriteriaRevisionImmutable
	}
	if !domaintask.NativeOPSResumeClaimsExtend(previous.claims, next.NativeResumeClaims) {
		return ErrNativeOPSResumeClaimImmutable
	}
	nextHasOriginPublication := next.OriginPublication != nil
	if previous.hasOriginPublication != nextHasOriginPublication ||
		(previous.hasOriginPublication && previous.originPublication != *next.OriginPublication) {
		return ErrTaskOriginPublicationProvenanceImmutable
	}
	return nil
}

// committedPinLocator reports whether the Task is in the index and, when its
// latest version carries immutable first-save data, where that line is. A Task
// with none needs no read: its pin state is the empty one. The caller holds mu
// for reading or applyMu.
func (ix *taskIndex) committedPinLocator(key key16) (flags uint8, pos linePos, found bool) {
	slot, ok := ix.taskMap[key]
	if !ok {
		return 0, linePos{}, false
	}
	rec := &ix.tasks[slot]
	flags = rec.flags & (flagPinned | flagClaims | flagOriginPublication)
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
