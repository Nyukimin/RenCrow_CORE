package taskmeeting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrUnavailable = errors.New("scheduled meeting is unavailable")

type Coordinator struct {
	config       ConfigurationPort
	authority    AuthorityPort
	store        OccurrenceStore
	participants ParticipantExecutor
	world        WorldPresencePort
	conversation GroupConversationPort
	now          func() time.Time
}

func NewCoordinator(config ConfigurationPort, authority AuthorityPort, store OccurrenceStore, participants ParticipantExecutor, world WorldPresencePort, conversation GroupConversationPort, clock func() time.Time) *Coordinator {
	if clock == nil {
		clock = time.Now
	}
	return &Coordinator{config: config, authority: authority, store: store, participants: participants, world: world, conversation: conversation, now: clock}
}

type OpenResult struct {
	Meeting     domainmeeting.Meeting
	PayloadHash string
	Created     bool
	Ready       bool
	Unbound     bool
	Unavailable []string
}

// OccurrenceKeyFromScheduled derives the meeting owner's stable schedule/date/
// slot/timezone identity from the scheduler's immutable due occurrence.
func OccurrenceKeyFromScheduled(schedule domainscheduler.Schedule, due domainscheduler.ScheduledOccurrence) (domainmeeting.OccurrenceKey, error) {
	if schedule.ScheduleID == "" || due.ScheduledAt.IsZero() || due.OccurrenceKey == "" || due.Timing.Timezone != domainmeeting.TimezoneJST ||
		due.Timing.Recurrence == nil || due.Timing.Recurrence.Frequency != modulecore.TaskRecurrenceDaily {
		return domainmeeting.OccurrenceKey{}, fmt.Errorf("%w: a typed daily JST scheduler occurrence is required", ErrUnavailable)
	}
	location, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		return domainmeeting.OccurrenceKey{}, fmt.Errorf("load meeting timezone: %w", err)
	}
	local := due.ScheduledAt.In(location)
	key := domainmeeting.OccurrenceKey{
		ScheduleID: schedule.ScheduleID,
		LocalDate:  local.Format("2006-01-02"),
		Slot:       local.Format("15:04"),
		Timezone:   domainmeeting.TimezoneJST,
	}
	if err := key.Validate(); err != nil {
		return domainmeeting.OccurrenceKey{}, err
	}
	identity, err := domainscheduler.OccurrenceKey(schedule, due.ScheduledAt, domainmeeting.TimezoneJST)
	if err != nil || identity != due.OccurrenceKey {
		return domainmeeting.OccurrenceKey{}, fmt.Errorf("%w: scheduler occurrence identity does not match its due instant", ErrUnavailable)
	}
	if !due.Timing.Ready(due.ScheduledAt) {
		return domainmeeting.OccurrenceKey{}, fmt.Errorf("%w: scheduled slot is not ready under its timing policy", ErrUnavailable)
	}
	return key, nil
}

// OpenDue attaches one scheduler due instant to its stable local daily slot and
// loads or creates the durable aggregate. A repeated scheduler tick never
// changes generation; explicit Recover is the only resume path.
func (c *Coordinator) OpenDue(ctx context.Context, input ScheduledInput) (OpenResult, error) {
	if c == nil || c.config == nil || c.authority == nil || c.store == nil {
		return OpenResult{}, ErrUnavailable
	}
	schedule := input.Schedule
	due := input.Occurrence
	key, err := OccurrenceKeyFromScheduled(schedule, due)
	if err != nil {
		return OpenResult{}, err
	}
	config, err := c.config.MeetingConfig(ctx, schedule.ScheduleID, due.ScheduledAt)
	if err != nil {
		return OpenResult{}, fmt.Errorf("load meeting configuration: %w", err)
	}
	if config.ScheduleID != schedule.ScheduleID || config.Timing.Timezone != due.Timing.Timezone || !sameTiming(config.Timing, due.Timing) {
		return OpenResult{}, fmt.Errorf("%w: meeting configuration and scheduled timing disagree", ErrUnavailable)
	}
	access, err := c.authority.AuthorizeOccurrence(ctx, key, config)
	if err != nil {
		return OpenResult{}, fmt.Errorf("authorize meeting occurrence: %w", err)
	}
	if err := validateAuthorizedAccess(access, config); err != nil {
		return OpenResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	initial, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		return OpenResult{}, err
	}
	stored, err := c.store.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil {
		return OpenResult{}, err
	}
	unbound := input.RootExecution == nil
	if proof := input.RootExecution; proof != nil {
		if proof.Assignee != access.CoordinatorActorID || proof.TaskID.Validate() != nil || proof.RunID.Validate() != nil || proof.AdmissionEvidenceID.Validate() != nil {
			return OpenResult{}, fmt.Errorf("%w: root TaskManager proof is invalid or assigned to another actor", ErrUnavailable)
		}
		if stored.Meeting.TaskRef == nil && stored.Meeting.RunRef == nil && stored.Meeting.TaskManagerEvidenceID == "" {
			before := stored.Meeting
			next, bindErr := before.BindNativeExecution(before.Guard(), proof.TaskID, proof.RunID, proof.AdmissionEvidenceID)
			if bindErr != nil {
				return OpenResult{}, bindErr
			}
			stored, err = c.store.SaveMeetingOccurrence(ctx, access, key, before.Guard(), stored.PayloadHash, next)
			if err != nil {
				return OpenResult{}, err
			}
		} else if stored.Meeting.TaskRef == nil || stored.Meeting.RunRef == nil || *stored.Meeting.TaskRef != proof.TaskID || *stored.Meeting.RunRef != proof.RunID || stored.Meeting.TaskManagerEvidenceID != proof.AdmissionEvidenceID {
			return OpenResult{}, domainmeeting.ErrConflict
		}
	}
	unavailable := c.unavailablePorts()
	if unbound {
		unavailable = append(unavailable, "native root TaskManager Task/Run admission proof")
	}
	return OpenResult{Meeting: stored.Meeting, PayloadHash: stored.PayloadHash, Created: stored.Created, Ready: len(unavailable) == 0, Unbound: unbound, Unavailable: unavailable}, nil
}

// Recover is an explicit owner action. It rechecks current authority, reads the
// exact stored aggregate, obtains fresh owner-issued recovery evidence, then
// performs the domain Resume transition under payload and progression CAS.
func (c *Coordinator) Recover(ctx context.Context, input ScheduledInput) (OpenResult, error) {
	opened, err := c.OpenDue(ctx, input)
	if err != nil {
		return OpenResult{}, err
	}
	if opened.Created {
		return OpenResult{}, errors.New("cannot resume a newly created occurrence")
	}
	if c.authority == nil {
		return OpenResult{}, ErrUnavailable
	}
	config, err := c.config.MeetingConfig(ctx, opened.Meeting.Key.ScheduleID, opened.Meeting.ScheduledAt)
	if err != nil {
		return OpenResult{}, err
	}
	access, err := c.authority.AuthorizeOccurrence(ctx, opened.Meeting.Key, config)
	if err != nil {
		return OpenResult{}, err
	}
	evidence, err := c.authority.IssueResumeEvidence(ctx, opened.Meeting)
	if err != nil {
		return OpenResult{}, err
	}
	resumed, err := opened.Meeting.Resume(opened.Meeting.Guard(), evidence)
	if err != nil {
		return OpenResult{}, err
	}
	stored, err := c.store.SaveMeetingOccurrence(ctx, access, resumed.Key, opened.Meeting.Guard(), opened.PayloadHash, resumed)
	if err != nil {
		return OpenResult{}, err
	}
	opened.Meeting, opened.PayloadHash = stored.Meeting, stored.PayloadHash
	return opened, nil
}

// BeginGathering commits the aggregate transition before any World owner call.
func (c *Coordinator) BeginGathering(ctx context.Context, input ScheduledInput) (OpenResult, error) {
	opened, err := c.OpenDue(ctx, input)
	if err != nil {
		return OpenResult{}, err
	}
	if c.world == nil {
		return opened, fmt.Errorf("%w: World presence/gather owner is not configured", ErrUnavailable)
	}
	if opened.Meeting.Status != domainmeeting.StatusScheduled {
		return opened, nil
	}
	config, err := c.config.MeetingConfig(ctx, opened.Meeting.Key.ScheduleID, opened.Meeting.ScheduledAt)
	if err != nil {
		return opened, err
	}
	access, err := c.authority.AuthorizeOccurrence(ctx, opened.Meeting.Key, config)
	if err != nil {
		return opened, err
	}
	before := opened.Meeting
	next, err := before.BeginGathering(before.Guard(), c.now())
	if err != nil {
		return opened, err
	}
	stored, err := c.store.SaveMeetingOccurrence(ctx, access, next.Key, before.Guard(), opened.PayloadHash, next)
	if err != nil {
		return opened, err
	}
	opened.Meeting, opened.PayloadHash = stored.Meeting, stored.PayloadHash
	return opened, nil
}

// Advance persists one domain checkpoint at a time. A caller may safely
// reschedule it after a crash: existing occurrence identity, guards, immutable
// utterances, and canonical delivery readback determine the next action.
func (c *Coordinator) Advance(ctx context.Context, input ScheduledInput) (OpenResult, error) {
	opened, err := c.OpenDue(ctx, input)
	if err != nil {
		return OpenResult{}, err
	}
	if !opened.Ready {
		if c.now().Before(opened.Meeting.DeadlineAt) {
			return opened, fmt.Errorf("%w: %v", ErrUnavailable, opened.Unavailable)
		}
	}
	if opened.Unbound && c.now().Before(opened.Meeting.DeadlineAt) {
		return opened, fmt.Errorf("%w: occurrence is durably unbound from a native TaskManager root execution", ErrUnavailable)
	}
	deadlineElapsed := !c.now().Before(opened.Meeting.DeadlineAt)
	if deadlineElapsed {
		switch opened.Meeting.Status {
		case domainmeeting.StatusCompleted:
			return opened, nil
		case domainmeeting.StatusFinishing:
			// A completed minutes write may have committed before the deadline
			// while its reply or the following aggregate CAS was lost. Resolve
			// that exact receipt before considering partial; the owner performs
			// a same-transaction downgrade guard against the complementary race.
			finished, found, findErr := c.findFinishedTranscript(ctx, input, opened)
			if findErr != nil {
				return opened, findErr
			}
			if found {
				return finished, nil
			}
		case domainmeeting.StatusPartial, domainmeeting.StatusMissed:
			return c.persistPartialTranscript(ctx, input, opened)
		}
		return c.markDeadlinePartial(ctx, input, opened)
	}
	switch opened.Meeting.Status {
	case domainmeeting.StatusScheduled:
		return c.BeginGathering(ctx, input)
	case domainmeeting.StatusGathering:
		return c.advanceGathering(ctx, input, opened)
	case domainmeeting.StatusReporting, domainmeeting.StatusAnswering:
		return c.advanceReporting(ctx, input, opened)
	case domainmeeting.StatusFinishing:
		return c.finish(ctx, input, opened)
	case domainmeeting.StatusCompleted:
		return opened, nil
	case domainmeeting.StatusPartial, domainmeeting.StatusMissed:
		return c.persistPartialTranscript(ctx, input, opened)
	default:
		return opened, fmt.Errorf("%w: unsupported meeting state %q", ErrUnavailable, opened.Meeting.Status)
	}
}

func (c *Coordinator) advanceGathering(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, error) {
	meeting := opened.Meeting
	var err error
	threadRequestID := meeting.ThreadRequestID
	if threadRequestID == "" {
		threadRequestID = meeting.Key.String() + "|thread"
		opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.PrepareSharedThread(current.Guard(), threadRequestID)
		})
		if err != nil {
			return opened, err
		}
		meeting = opened.Meeting
	}
	threadID, err := c.conversation.EnsureSharedThread(ctx, meeting, threadRequestID)
	if err != nil {
		return opened, err
	}
	if err := threadID.Validate(); err != nil {
		return opened, fmt.Errorf("canonical group thread: %w", err)
	}
	gatherRequestID := meeting.GatherRequestID
	if meeting.ScopeEvidence != nil && meeting.NeedsNewWorldObservationAt(c.now()) {
		gatherRequestID = meeting.Key.String() + "|gather|" + string(meeting.ScopeEvidence.EvidenceID)
		opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.PrepareWorldRefresh(current.Guard(), gatherRequestID, c.now())
		})
		if err != nil {
			return opened, err
		}
		meeting = opened.Meeting
	} else if gatherRequestID == "" {
		gatherRequestID = meeting.Key.String() + "|gather"
		opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.PrepareGather(current.Guard(), gatherRequestID)
		})
		if err != nil {
			return opened, err
		}
		meeting = opened.Meeting
	}
	scopeEvidence, presence, err := c.world.GatherAndRead(ctx, meeting, gatherRequestID, threadID)
	if err != nil {
		return opened, err
	}
	if scopeEvidence.ThreadID != threadID {
		return opened, domainmeeting.ErrConflict
	}
	if len(presence) != len(meeting.Participants) {
		return opened, fmt.Errorf("%w: World owner returned an incomplete or expanded presence roster", ErrUnavailable)
	}
	opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
		return current.ConfirmWorldScope(current.Guard(), scopeEvidence)
	})
	if err != nil {
		return opened, err
	}
	meeting = opened.Meeting
	presenceByActor := make(map[string]domainmeeting.PresenceEvidence, len(presence))
	for _, evidence := range presence {
		if _, exists := presenceByActor[evidence.ActorID]; exists {
			return opened, domainmeeting.ErrConflict
		}
		presenceByActor[evidence.ActorID] = evidence
	}
	for _, actorID := range meeting.Participants {
		evidence, found := presenceByActor[actorID]
		if !found {
			return opened, fmt.Errorf("%w: World owner omitted participant %q", ErrUnavailable, actorID)
		}
		opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.RecordPresence(current.Guard(), evidence)
		})
		if err != nil {
			return opened, err
		}
		meeting = opened.Meeting
		if !evidence.Present {
			return opened, fmt.Errorf("%w: participant %q is not proven present in the meeting area", ErrUnavailable, actorID)
		}
	}
	if meeting.Status != domainmeeting.StatusGathering {
		return opened, nil
	}
	return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
		return current.BeginReporting(current.Guard(), c.now())
	})
}

func (c *Coordinator) advanceReporting(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, error) {
	meeting := opened.Meeting
	pendingGeneration := meeting.CurrentTurn != nil && meeting.CurrentTurn.MessageID == nil && meeting.CurrentTurn.GenerationRequestID != ""
	pendingDeliveryReceipt := false
	if meeting.CurrentTurn != nil && meeting.CurrentTurn.MessageID != nil {
		if utterance, found := findUtterance(meeting, *meeting.CurrentTurn.MessageID); found {
			pendingDeliveryReceipt = utterance.DeliveryState == domainmeeting.DeliverySending || utterance.DeliveryState == domainmeeting.DeliveryUnknown
		}
	}
	if meeting.NeedsWorldRefreshAt(c.now()) && !pendingGeneration && !pendingDeliveryReceipt {
		return c.advanceGathering(ctx, input, opened)
	}
	if meeting.Status == domainmeeting.StatusAnswering && meeting.CurrentTurn == nil {
		concern, actor, found := nextAnswerTurn(meeting)
		if !found {
			if !meeting.AllConcernsAnswered() {
				// Every possible responder for at least one concern may have a
				// terminal unknown attempt. Keep the concern unresolved until the
				// bounded deadline; never treat attempts as answers.
				return opened, nil
			}
			return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.AdvanceReporter(current.Guard(), c.now())
			})
		}
		return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.RequestAnswer(current.Guard(), concern, actor, false)
		})
	}
	turn := meeting.CurrentTurn
	if turn == nil {
		return opened, domainmeeting.ErrInvalidState
	}
	if turn.MessageID == nil {
		if turn.GenerationRequestID == "" {
			requestID := generationRequestID()
			inputJSON, err := generationInput(meeting)
			if err != nil {
				return opened, err
			}
			prepared, prepareErr := c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.PrepareCurrentGeneration(current.Guard(), requestID, inputJSON)
			})
			if prepareErr == nil {
				return prepared, nil
			}
			// A concurrent coordinator may have committed the intent while this
			// transition lost its CAS. Reload the durable winner and defer this
			// scheduler checkpoint; the next call uses the exact stored request
			// ID and frozen bytes instead of minting or rebuilding a new request.
			reloaded, reloadErr := c.OpenDue(ctx, input)
			if reloadErr == nil && samePreparedGenerationTurn(meeting, reloaded.Meeting) {
				return reloaded, errors.Join(prepareErr, domainmeeting.ErrStale)
			}
			return opened, errors.Join(prepareErr, reloadErr)
		}
		if modulecore.RequestID(turn.GenerationRequestID).Validate() != nil {
			return opened, fmt.Errorf("%w: stored generation request identity is not a canonical RequestID", ErrUnavailable)
		}
		if turn.Kind == domainmeeting.TurnReport {
			report, evidence, err := c.participants.EnsureReport(ctx, meeting, turn.GenerationRequestID, turn.GenerationInputSHA256, append([]byte(nil), turn.GenerationInputJSON...), turn.ActorID)
			if err != nil {
				return opened, err
			}
			if err := validateGeneratedExecution(meeting, evidence, turn.GenerationRequestID, turn.GenerationInputSHA256); err != nil {
				return opened, err
			}
			if err := bindSpeakerEvidenceToIntent(&evidence, meeting); err != nil {
				return opened, err
			}
			report.Utterance.SourceTaskRef = &evidence.TaskRef
			report.Utterance.SourceRunRef = &evidence.RunRef
			return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.RecordReport(current.Guard(), report, evidence)
			})
		}
		if turn.Kind == domainmeeting.TurnAnswer && turn.ConcernID != nil {
			answer, evidence, err := c.participants.EnsureAnswer(ctx, meeting, turn.GenerationRequestID, turn.GenerationInputSHA256, append([]byte(nil), turn.GenerationInputJSON...), turn.ActorID, *turn.ConcernID)
			if err != nil {
				var terminal *TerminalAnswerAttemptError
				if errors.As(err, &terminal) && terminal != nil {
					return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
						return current.RecordAnswerAttempt(current.Guard(), terminal.Attempt)
					})
				}
				return opened, err
			}
			if err := validateGeneratedExecution(meeting, evidence, turn.GenerationRequestID, turn.GenerationInputSHA256); err != nil {
				return opened, err
			}
			if err := bindSpeakerEvidenceToIntent(&evidence, meeting); err != nil {
				return opened, err
			}
			answer.Utterance.SourceTaskRef = &evidence.TaskRef
			answer.Utterance.SourceRunRef = &evidence.RunRef
			return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.RecordAnswer(current.Guard(), answer, evidence)
			})
		}
		return opened, domainmeeting.ErrInvalidState
	}
	utterance, found := findUtterance(meeting, *turn.MessageID)
	if !found {
		return opened, domainmeeting.ErrInvalidState
	}
	mayDispatch := false
	if utterance.DeliveryState == domainmeeting.DeliveryUnsent {
		var err error
		opened, err = c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.BeginCurrentDelivery(current.Guard(), utterance.MessageID, c.now())
		})
		if err != nil {
			return opened, err
		}
		meeting = opened.Meeting
		utterance, _ = findUtterance(meeting, utterance.MessageID)
		mayDispatch = true
	}
	if utterance.DeliveryState == domainmeeting.DeliverySending || utterance.DeliveryState == domainmeeting.DeliveryUnknown {
		receipt, found, readErr := c.conversation.ReadDelivery(ctx, meeting, utterance)
		if readErr != nil {
			return opened, readErr
		}
		if found {
			if err := bindDeliveryScope(&receipt, utterance); err != nil {
				return opened, err
			}
			return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.ConfirmDelivery(current.Guard(), receipt)
			})
		}
		if utterance.DeliveryState == domainmeeting.DeliveryUnknown {
			return opened, fmt.Errorf("%w: canonical readback found no receipt for an unknown delivery; automatic resend is forbidden", ErrUnavailable)
		}
		if !mayDispatch {
			return opened, fmt.Errorf("%w: canonical readback found no receipt for a previously sending utterance; automatic resend is forbidden", ErrUnavailable)
		}
		receipt, dispatchErr := c.conversation.Dispatch(ctx, meeting, utterance)
		if dispatchErr != nil {
			unknown := domainmeeting.DeliveryUnknownEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch, MessageID: utterance.MessageID, Reason: "dispatch_outcome_unknown", ObservedAt: c.now()}
			updated, saveErr := c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
				return current.MarkCurrentDeliveryUnknown(current.Guard(), unknown)
			})
			return updated, errors.Join(dispatchErr, saveErr)
		}
		if err := bindDeliveryScope(&receipt, utterance); err != nil {
			return opened, err
		}
		return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.ConfirmDelivery(current.Guard(), receipt)
		})
	}
	return opened, domainmeeting.ErrInvalidState
}

func bindDeliveryScope(evidence *domainmeeting.DeliveryEvidence, utterance domainmeeting.Utterance) error {
	if evidence == nil || utterance.DeliveryScopeEvidenceID == "" {
		return fmt.Errorf("%w: delivery attempt has no frozen World scope reference", ErrUnavailable)
	}
	if evidence.ScopeEvidenceID == "" {
		evidence.ScopeEvidenceID = utterance.DeliveryScopeEvidenceID
	} else if evidence.ScopeEvidenceID != utterance.DeliveryScopeEvidenceID {
		return domainmeeting.ErrConflict
	}
	if evidence.Generation == 0 {
		evidence.Generation = utterance.DeliveryGeneration
	} else if evidence.Generation != utterance.DeliveryGeneration {
		return domainmeeting.ErrConflict
	}
	if evidence.Epoch == 0 {
		evidence.Epoch = utterance.DeliveryEpoch
	} else if evidence.Epoch != utterance.DeliveryEpoch {
		return domainmeeting.ErrConflict
	}
	return nil
}

func bindSpeakerEvidenceToIntent(evidence *domainmeeting.SpeakerEvidence, meeting domainmeeting.Meeting) error {
	if evidence == nil || meeting.CurrentTurn == nil || meeting.CurrentTurn.GenerationRequestID == "" {
		return domainmeeting.ErrInvalidState
	}
	expectedScope := meeting.CurrentTurn.ScopeEvidenceID
	if expectedScope == "" {
		var frozen struct {
			ScopeEvidence *domainmeeting.WorldScopeEvidence `json:"scope_evidence"`
		}
		if len(meeting.CurrentTurn.GenerationInputJSON) == 0 || json.Unmarshal(meeting.CurrentTurn.GenerationInputJSON, &frozen) != nil || frozen.ScopeEvidence == nil {
			return fmt.Errorf("%w: frozen generation input has no source World scope", ErrUnavailable)
		}
		expectedScope = frozen.ScopeEvidence.EvidenceID
	}
	if expectedScope == "" || (evidence.ScopeEvidenceID != "" && evidence.ScopeEvidenceID != expectedScope) {
		return domainmeeting.ErrConflict
	}
	if evidence.ScopeEvidenceID == "" {
		evidence.ScopeEvidenceID = expectedScope
	}
	return nil
}

func generationRequestID() string {
	return string(modulecore.NewRequestID())
}

func samePreparedGenerationTurn(expected, stored domainmeeting.Meeting) bool {
	if expected.Key != stored.Key || expected.Status != stored.Status || expected.CurrentTurn == nil || stored.CurrentTurn == nil {
		return false
	}
	want, got := expected.CurrentTurn, stored.CurrentTurn
	if want.Kind != got.Kind || want.ActorID != got.ActorID || want.MessageID != nil || got.MessageID != nil ||
		!sameConcernID(want.ConcernID, got.ConcernID) || got.GenerationRequestID == "" ||
		modulecore.RequestID(got.GenerationRequestID).Validate() != nil || got.Generation != stored.Generation || got.Epoch != stored.Epoch ||
		len(got.GenerationInputJSON) == 0 || !json.Valid(got.GenerationInputJSON) || len(got.GenerationInputSHA256) != 64 ||
		got.GenerationInputSHA256 != digestJSON(got.GenerationInputJSON) {
		return false
	}
	if stored.ScopeEvidence != nil && got.ScopeEvidenceID != stored.ScopeEvidence.EvidenceID {
		return false
	}
	return true
}

func sameConcernID(left, right *domainmeeting.ConcernID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func digestJSON(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func generationInput(meeting domainmeeting.Meeting) ([]byte, error) {
	return json.Marshal(meeting)
}

func validateGeneratedExecution(meeting domainmeeting.Meeting, evidence domainmeeting.SpeakerEvidence, requestID, inputDigest string) error {
	if meeting.CurrentTurn == nil || modulecore.RequestID(requestID).Validate() != nil || evidence.ActorID != meeting.CurrentTurn.ActorID || evidence.TaskAssignee != evidence.ActorID || evidence.AdmissionEvidenceID.Validate() != nil || evidence.GenerationRequestID != requestID || evidence.GenerationInputSHA256 != inputDigest ||
		evidence.TaskRef.Validate() != nil || evidence.RunRef.Validate() != nil || evidence.Key != meeting.Key {
		return fmt.Errorf("%w: participant result is not bound to the persisted request and native TaskManager Task/Run", ErrUnavailable)
	}
	return nil
}

func (c *Coordinator) finish(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, error) {
	requestID := opened.Meeting.TranscriptRequestID
	if requestID == "" {
		requestID = opened.Meeting.Key.String() + "|transcript"
		prepared, err := c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.PrepareTranscript(current.Guard(), requestID)
		})
		if err != nil {
			return prepared, err
		}
		opened = prepared
	}
	if c.conversation == nil {
		return opened, fmt.Errorf("%w: canonical minutes owner is unavailable", ErrUnavailable)
	}
	ref, found, err := c.conversation.FindTranscript(ctx, opened.Meeting, requestID)
	if err != nil {
		return opened, err
	}
	if found {
		return c.finishFromTranscript(ctx, input, opened, ref)
	}
	if !c.now().Before(opened.Meeting.DeadlineAt) {
		return c.markDeadlinePartial(ctx, input, opened)
	}
	ref, err = c.conversation.PersistTranscript(ctx, opened.Meeting, requestID)
	if err != nil {
		return opened, err
	}
	return c.finishFromTranscript(ctx, input, opened, ref)
}

func (c *Coordinator) findFinishedTranscript(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, bool, error) {
	if opened.Meeting.Status != domainmeeting.StatusFinishing || opened.Meeting.TranscriptRequestID == "" || c.conversation == nil {
		return opened, false, nil
	}
	ref, found, err := c.conversation.FindTranscript(ctx, opened.Meeting, opened.Meeting.TranscriptRequestID)
	if err != nil || !found {
		return opened, found, err
	}
	finished, err := c.finishFromTranscript(ctx, input, opened, ref)
	return finished, err == nil, err
}

func (c *Coordinator) finishFromTranscript(ctx context.Context, input ScheduledInput, opened OpenResult, ref domainmeeting.MinutesArtifactReference) (OpenResult, error) {
	if err := validateMinutesReference(opened.Meeting, opened.Meeting.TranscriptRequestID, ref); err != nil || !ref.CreatedAt.Before(opened.Meeting.DeadlineAt) {
		return opened, fmt.Errorf("%w: completed minutes receipt does not match the exact pre-deadline finishing snapshot", domainmeeting.ErrConflict)
	}
	evidence := domainmeeting.FinalizationEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: opened.Meeting.Key, Generation: opened.Meeting.Generation, Epoch: opened.Meeting.Epoch, ProgressionRevision: opened.Meeting.ProgressionRevision, TranscriptArtifactID: ref.ArtifactID, PersistedAt: ref.CreatedAt}
	return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
		return current.Finish(current.Guard(), evidence)
	})
}

func (c *Coordinator) markDeadlinePartial(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, error) {
	if opened.Meeting.Status == domainmeeting.StatusCompleted || opened.Meeting.Status == domainmeeting.StatusPartial || opened.Meeting.Status == domainmeeting.StatusMissed {
		if opened.Meeting.Status == domainmeeting.StatusPartial || opened.Meeting.Status == domainmeeting.StatusMissed {
			return c.persistPartialTranscript(ctx, input, opened)
		}
		return opened, nil
	}
	evidence := domainmeeting.PartialEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: opened.Meeting.Key, Generation: opened.Meeting.Generation, Epoch: opened.Meeting.Epoch, ObservedAt: c.now(), ReasonCode: "deadline_elapsed"}
	if opened.Meeting.Status == domainmeeting.StatusScheduled {
		evidence.ReasonCode = "missed"
	}
	partial, err := c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
		return current.MarkPartial(current.Guard(), evidence)
	})
	if err != nil {
		return partial, err
	}
	return c.persistPartialTranscript(ctx, input, partial)
}

func (c *Coordinator) persistPartialTranscript(ctx context.Context, input ScheduledInput, opened OpenResult) (OpenResult, error) {
	meeting := opened.Meeting
	if meeting.Status != domainmeeting.StatusPartial && meeting.Status != domainmeeting.StatusMissed || meeting.PartialEvidence == nil {
		return opened, domainmeeting.ErrInvalidState
	}
	if meeting.PartialMinutes != nil {
		return opened, nil
	}
	if c.conversation == nil {
		return opened, fmt.Errorf("%w: canonical minutes owner is unavailable for terminal partial", ErrUnavailable)
	}
	requestID := meeting.TranscriptRequestID
	if requestID == "" {
		requestID = meeting.Key.String() + "|partial-minutes|" + string(meeting.PartialEvidence.EvidenceID)
		prepared, err := c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
			return current.PrepareTranscript(current.Guard(), requestID)
		})
		if err != nil {
			return prepared, err
		}
		opened = prepared
		meeting = opened.Meeting
	}
	ref, found, err := c.conversation.FindTranscript(ctx, meeting, requestID)
	if err != nil {
		return opened, err
	}
	if !found {
		ref, err = c.conversation.PersistTranscript(ctx, meeting, requestID)
		if err != nil {
			return opened, err
		}
	}
	if err := validateMinutesReference(meeting, requestID, ref); err != nil {
		return opened, fmt.Errorf("%w: terminal partial minutes receipt does not match its pinned snapshot", domainmeeting.ErrConflict)
	}
	evidence := domainmeeting.PartialMinutesEvidence{EvidenceID: modulecore.NewEvidenceID(), Reference: ref}
	return c.commit(ctx, input, opened, func(current domainmeeting.Meeting) (domainmeeting.Meeting, error) {
		return current.RecordPartialMinutes(current.Guard(), evidence)
	})
}

func validateMinutesReference(meeting domainmeeting.Meeting, requestID string, ref domainmeeting.MinutesArtifactReference) error {
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(meeting.Participants)
	if err != nil || ref.ArtifactID.Validate() != nil || ref.WorkstreamID.Validate() != nil ||
		ref.RequestID != requestID || requestID == "" || ref.Key != meeting.Key || ref.Guard != meeting.Guard() ||
		ref.OwnerUserID == "" || ref.OwnerUserID != strings.TrimSpace(ref.OwnerUserID) || !containsActor(meeting.Participants, ref.CoordinatorActorID) ||
		ref.RegistrationEvidenceID != meeting.RegistrationEvidenceID || ref.ConfigRevision != meeting.ScheduleRevision ||
		!canonicalSHA256(ref.ConfigSHA256) || ref.AudienceSHA256 != audienceSHA || !sameOrderedStrings(ref.Audience, meeting.Participants) ||
		!canonicalSHA256(ref.TranscriptSHA256) || ref.CreatedAt.IsZero() {
		return domainmeeting.ErrConflict
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func sameOrderedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (c *Coordinator) commit(ctx context.Context, input ScheduledInput, opened OpenResult, transition func(domainmeeting.Meeting) (domainmeeting.Meeting, error)) (OpenResult, error) {
	config, err := c.config.MeetingConfig(ctx, opened.Meeting.Key.ScheduleID, opened.Meeting.ScheduledAt)
	if err != nil {
		return opened, err
	}
	access, err := c.authority.AuthorizeOccurrence(ctx, opened.Meeting.Key, config)
	if err != nil {
		return opened, err
	}
	before := opened.Meeting
	next, err := transition(before)
	if err != nil {
		return opened, err
	}
	if next.Guard() == before.Guard() {
		if !domainmeeting.SamePersistedSnapshot(next, before) {
			return opened, fmt.Errorf("%w: meeting transition changed state without advancing its guard", domainmeeting.ErrConflict)
		}
		// Evidence owners replay immutable same-key receipts by returning the
		// existing aggregate unchanged. Treat that as a successful readback; a
		// storage CAS must not persist synthetic progress for a semantic no-op.
		return opened, nil
	}
	stored, err := c.store.SaveMeetingOccurrence(ctx, access, before.Key, before.Guard(), opened.PayloadHash, next)
	if err != nil {
		return opened, err
	}
	opened.Meeting, opened.PayloadHash = stored.Meeting, stored.PayloadHash
	return opened, nil
}

func nextAnswerTurn(meeting domainmeeting.Meeting) (domainmeeting.ConcernID, string, bool) {
	if meeting.CurrentReportIndex < 0 || meeting.CurrentReportIndex >= len(meeting.Reports) {
		return domainmeeting.ConcernID{}, "", false
	}
	report := meeting.Reports[meeting.CurrentReportIndex]
	for index := range report.Concerns {
		id := domainmeeting.ConcernID{ReportMessageID: report.Utterance.MessageID, Ordinal: uint32(index + 1)}
		answered := false
		for _, answer := range meeting.Answers {
			if answer.ConcernID == id && answer.Utterance.DeliveryState == domainmeeting.DeliveryDelivered {
				answered = true
				break
			}
		}
		if answered {
			continue
		}
		for _, actorID := range meeting.Participants {
			if actorID == report.Utterance.ActorID {
				continue
			}
			used := false
			for _, answer := range meeting.Answers {
				if answer.ConcernID == id && answer.Utterance.ActorID == actorID {
					used = true
					break
				}
			}
			if !used {
				for _, attempt := range meeting.AnswerAttempts {
					if attempt.ConcernID == id && attempt.UnknownReceipt.ActorID == actorID {
						used = true
						break
					}
				}
			}
			if !used {
				return id, actorID, true
			}
		}
	}
	return domainmeeting.ConcernID{}, "", false
}

func findUtterance(meeting domainmeeting.Meeting, messageID modulecore.MessageID) (domainmeeting.Utterance, bool) {
	for _, report := range meeting.Reports {
		if report.Utterance.MessageID == messageID {
			return report.Utterance, true
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.MessageID == messageID {
			return answer.Utterance, true
		}
	}
	return domainmeeting.Utterance{}, false
}

func (c *Coordinator) unavailablePorts() []string {
	var missing []string
	if c.participants == nil {
		missing = append(missing, "actor-bound participant executor")
	}
	if c.world == nil {
		missing = append(missing, "World gather/presence owner")
	}
	if c.conversation == nil {
		missing = append(missing, "canonical group conversation owner")
	}
	return missing
}

func validateAuthorizedAccess(access domainmeeting.StoreAccess, config domainmeeting.ConfigSnapshot) error {
	if access.CoordinatorActorID == "" || access.OwnerUserID == "" || access.AuthorityEvidence.Validate() != nil || len(access.Audience) != len(config.ConfiguredActors) {
		return errors.New("authority did not return a complete coordinator, owner, and audience snapshot")
	}
	if !containsActor(config.ConfiguredActors, access.CoordinatorActorID) {
		return errors.New("coordinator is not one of the configured actors")
	}
	left, right := append([]string(nil), access.Audience...), append([]string(nil), config.ConfiguredActors...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return errors.New("authority audience differs from the configured roster")
		}
	}
	return nil
}

func containsActor(actors []string, target string) bool {
	for _, actor := range actors {
		if actor == target {
			return true
		}
	}
	return false
}

func sameTiming(left, right modulecore.TaskTiming) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
