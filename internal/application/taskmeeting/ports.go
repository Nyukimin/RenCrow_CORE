package taskmeeting

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ConfigurationPort reads the current owner configuration and registration
// snapshot. It must never infer actors or room IDs from World entities.
type ConfigurationPort interface {
	MeetingConfig(context.Context, modulecore.ScheduleID, time.Time) (domainmeeting.ConfigSnapshot, error)
}

// AuthorityPort verifies the current coordinator, owner, and exact audience
// from trusted runtime registration. The returned access value is never
// populated from a scheduler payload or model-visible argument.
type AuthorityPort interface {
	AuthorizeOccurrence(context.Context, domainmeeting.OccurrenceKey, domainmeeting.ConfigSnapshot) (domainmeeting.StoreAccess, error)
	IssueResumeEvidence(context.Context, domainmeeting.Meeting) (domainmeeting.ResumeEvidence, error)
}

type OccurrenceStore interface {
	LoadOrCreateMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
	SaveMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Guard, string, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
}

// ParticipantExecutor must bind each call to the named registered actor and
// normal TaskManager admission; profile lookup alone is not sufficient. The
// frozenSeedJSON bytes are the exact occurrence input first admitted for this
// request. The native TaskManager owner must look up that request before any
// create. If creating a run derives a final LLM input from the actor's remaining
// tasks/persona context, that owner must persist the exact prepared context and
// its source/version references before invoking the model, then reuse those
// bytes on retries instead of rebuilding them from live state. On retries this
// port returns the same native Task/Run/result, never regenerates an unknown run.
type ParticipantExecutor interface {
	// EnsureReport and EnsureAnswer look up the exact persisted request first.
	// They may create only when the native TaskManager owner proves that this
	// request key has never been admitted; an unknown result is never regenerated.
	EnsureReport(context.Context, domainmeeting.Meeting, string, string, []byte, string) (domainmeeting.Report, domainmeeting.SpeakerEvidence, error)
	EnsureAnswer(context.Context, domainmeeting.Meeting, string, string, []byte, string, domainmeeting.ConcernID) (domainmeeting.Answer, domainmeeting.SpeakerEvidence, error)
}

var ErrTerminalAnswerAttempt = errors.New("participant answer generation ended without a known answer")

// TerminalAnswerAttemptError carries only the native owner receipt needed to
// persist one exhausted answer attempt. Runtime adapters must return it only
// after the effect callback ended and the exact TaskManager unknown receipt and
// inactive fence were read back successfully; underlying provider errors are
// intentionally not included in this boundary.
type TerminalAnswerAttemptError struct {
	Attempt domainmeeting.AnswerAttempt
}

func (e *TerminalAnswerAttemptError) Error() string {
	if e == nil {
		return ErrTerminalAnswerAttempt.Error()
	}
	return fmt.Sprintf("%s: actor=%s concern=%s", ErrTerminalAnswerAttempt, e.Attempt.UnknownReceipt.ActorID, e.Attempt.ConcernID.String())
}

func (e *TerminalAnswerAttemptError) Unwrap() error { return ErrTerminalAnswerAttempt }

// WorldPresencePort returns a single canonical snapshot and evidence derived
// from that exact snapshot. It may report unavailable when no owner grant or
// gather action is configured.
type WorldPresencePort interface {
	// GatherAndRead reconciles the exact request key before an ambiguous retry.
	GatherAndRead(context.Context, domainmeeting.Meeting, string, modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error)
}

// GroupConversationPort owns one shared thread and immutable group delivery.
// ReadDelivery must be used to resolve an uncertain dispatch before any retry.
type GroupConversationPort interface {
	// EnsureSharedThread must read back this exact persisted key before creating.
	EnsureSharedThread(context.Context, domainmeeting.Meeting, string) (modulecore.ThreadID, error)
	Dispatch(context.Context, domainmeeting.Meeting, domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, error)
	ReadDelivery(context.Context, domainmeeting.Meeting, domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, bool, error)
	FindTranscript(context.Context, domainmeeting.Meeting, string) (domainmeeting.MinutesArtifactReference, bool, error)
	PersistTranscript(context.Context, domainmeeting.Meeting, string) (domainmeeting.MinutesArtifactReference, error)
}

type ScheduledInput struct {
	Schedule      domainscheduler.Schedule
	Occurrence    domainscheduler.ScheduledOccurrence
	RootExecution *NativeExecutionProof
}

// NativeExecutionProof is passed only by a typed native Goal→Task handoff
// adapter after current TaskManager admission; scheduler log IDs are invalid.
type NativeExecutionProof struct {
	TaskID              modulecore.TaskID
	RunID               modulecore.RunID
	Assignee            string
	AdmissionEvidenceID modulecore.EvidenceID
}
