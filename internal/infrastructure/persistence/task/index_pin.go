package task

import (
	"bytes"
	"encoding/json"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// taskPin is what the fold of the Task log compares between two versions of the
// same Task: first-save criteria revision and publication provenance, plus the
// append-only native OPS Resume claims (see foldTaskRecords and appendTask).
type taskPin struct {
	revision               string
	claims                 []domaintask.NativeOPSResumeClaim
	originPublication      domaintask.OriginPublicationProvenance
	hasOriginPublication   bool
	privateAccess          domaintask.TaskPrivateAccess
	hasPrivateAccess       bool
	workstreamID           modulecore.WorkstreamID
	goalID                 modulecore.GoalID
	taskCenterExecution    domaintask.TaskCenterExecution
	hasTaskCenterExecution bool
	nativePlanning         domaintask.NativePlanningInvocation
	hasNativePlanning      bool
	taskMeeting            *domaintask.TaskMeetingInvocation
	taskMeetingResult      *domaintask.TaskMeetingGenerationResultReference
}

func pinOfTask(value domaintask.Task) taskPin {
	pin := taskPin{revision: value.ExpectedCriteriaRevision, claims: value.NativeResumeClaims}
	if value.TaskCenterExecution != nil {
		pin.taskCenterExecution = *value.TaskCenterExecution
		pin.hasTaskCenterExecution = true
	}
	if value.NativePlanningInvocation != nil {
		pin.nativePlanning = *value.NativePlanningInvocation
		pin.hasNativePlanning = true
	}
	if value.TaskMeetingInvocation != nil {
		meeting := *value.TaskMeetingInvocation
		meeting.FrozenConfig = append([]byte(nil), meeting.FrozenConfig...)
		meeting.FrozenInput = append([]byte(nil), meeting.FrozenInput...)
		meeting.Audience = append([]string(nil), meeting.Audience...)
		pin.taskMeeting = &meeting
	}
	if value.TaskMeetingResult != nil {
		result := *value.TaskMeetingResult
		pin.taskMeetingResult = &result
	}
	if value.PrivateAccess != nil {
		pin.privateAccess = *value.PrivateAccess
		pin.hasPrivateAccess = true
		pin.workstreamID = value.WorkstreamID
		pin.goalID = value.GoalID
	}
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
	nextHasTaskCenterExecution := next.TaskCenterExecution != nil
	if previous.hasTaskCenterExecution != nextHasTaskCenterExecution ||
		(previous.hasTaskCenterExecution && previous.taskCenterExecution != *next.TaskCenterExecution) {
		return domaintask.ErrTaskCenterExecutionImmutable
	}
	nextHasNativePlanning := next.NativePlanningInvocation != nil
	if previous.hasNativePlanning != nextHasNativePlanning ||
		(previous.hasNativePlanning && previous.nativePlanning != *next.NativePlanningInvocation) {
		return domaintask.ErrNativePlanningInvocationImmutable
	}
	if previous.taskMeeting == nil != (next.TaskMeetingInvocation == nil) ||
		previous.taskMeeting != nil && !samePinnedTaskMeetingInvocation(previous.taskMeeting, next.TaskMeetingInvocation) {
		return domaintask.ErrTaskMeetingConflict
	}
	if previous.taskMeetingResult != nil && (next.TaskMeetingResult == nil || *previous.taskMeetingResult != *next.TaskMeetingResult) ||
		previous.taskMeetingResult == nil && next.TaskMeetingResult != nil && (next.Status != domaintask.StatusSucceeded || next.TaskMeetingInvocation == nil || next.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && next.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer) {
		return domaintask.ErrTaskMeetingConflict
	}
	if !domaintask.NativeOPSResumeClaimsExtend(previous.claims, next.NativeResumeClaims) {
		return ErrNativeOPSResumeClaimImmutable
	}
	nextHasOriginPublication := next.OriginPublication != nil
	if previous.hasOriginPublication != nextHasOriginPublication ||
		(previous.hasOriginPublication && previous.originPublication != *next.OriginPublication) {
		return ErrTaskOriginPublicationProvenanceImmutable
	}
	nextHasPrivateAccess := next.PrivateAccess != nil
	if previous.hasPrivateAccess != nextHasPrivateAccess ||
		(previous.hasPrivateAccess && (previous.privateAccess != *next.PrivateAccess || previous.workstreamID != next.WorkstreamID || previous.goalID != next.GoalID)) {
		return domaintask.ErrPrivateTaskAccessImmutable
	}
	return nil
}

func samePinnedTaskMeetingInvocation(left, right *domaintask.TaskMeetingInvocation) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
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
	flags = rec.flags & (flagPinned | flagClaims | flagOriginPublication | flagPrivateAccess)
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
