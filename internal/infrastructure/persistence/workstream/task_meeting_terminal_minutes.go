package workstream

import (
	"context"
	"strings"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

// FindTerminalTaskMeetingMinutes resolves the immutable minutes pinned by the
// terminal occurrence's finish or partial evidence. Unlike the pre-finalization
// lookup, its caller guard names the terminal checkpoint, not the transcript's
// earlier checkpoint. Both authorization calls run outside a SQL transaction.
func (s *SQLiteStore) FindTerminalTaskMeetingMinutes(ctx context.Context, ownerUserID string, key domainmeeting.OccurrenceKey, requestID string, guard domainmeeting.Guard, authorize TaskMeetingMinutesFindAuthorizationCheck) (TaskMeetingMinutesReference, bool, error) {
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || ownerUserID != strings.TrimSpace(ownerUserID) || key.Validate() != nil ||
		requestID == "" || requestID != strings.TrimSpace(requestID) || guard.Generation == 0 || guard.Epoch == 0 || guard.ProgressionRevision == 0 || authorize == nil {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanForbidden
	}
	if err := authorize(ctx, ownerUserID, key, requestID, guard, nil); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	workstreamID, err := findTaskMeetingOccurrenceWorkstream(ctx, s.db, ownerUserID, key)
	if err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	stored, _, found, err := findMeetingOccurrence(ctx, s.db, key, string(workstreamID))
	if err != nil || !found {
		return TaskMeetingMinutesReference{}, false, err
	}
	meeting := stored.Meeting
	if stored.Access.OwnerUserID != ownerUserID || meeting.Guard() != guard || meeting.TranscriptRequestID != requestID ||
		(meeting.Status != domainmeeting.StatusCompleted && meeting.Status != domainmeeting.StatusPartial && meeting.Status != domainmeeting.StatusMissed) {
		return TaskMeetingMinutesReference{}, false, domainworkstream.ErrTaskPlanNotFound
	}
	summary := taskMeetingOccurrenceSummary(stored, workstreamID, meeting.ScheduledAt)
	ref, found, err := findTaskMeetingMinutesReference(ctx, s.db, workstreamID, summary, meeting)
	if err != nil || !found {
		return TaskMeetingMinutesReference{}, false, err
	}
	if err := authorize(ctx, ownerUserID, key, requestID, guard, &ref); err != nil {
		return TaskMeetingMinutesReference{}, false, err
	}
	return ref, true, nil
}
