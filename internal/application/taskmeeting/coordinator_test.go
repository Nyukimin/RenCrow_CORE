package taskmeeting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type coordinatorTestConfig domainmeeting.ConfigSnapshot

func (p coordinatorTestConfig) MeetingConfig(_ context.Context, _ modulecore.ScheduleID, _ time.Time) (domainmeeting.ConfigSnapshot, error) {
	return domainmeeting.ConfigSnapshot(p), nil
}

type coordinatorTestAuthority struct{ access domainmeeting.StoreAccess }

func (p coordinatorTestAuthority) AuthorizeOccurrence(_ context.Context, _ domainmeeting.OccurrenceKey, _ domainmeeting.ConfigSnapshot) (domainmeeting.StoreAccess, error) {
	return p.access, nil
}
func (p coordinatorTestAuthority) IssueResumeEvidence(_ context.Context, meeting domainmeeting.Meeting) (domainmeeting.ResumeEvidence, error) {
	return domainmeeting.ResumeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, ObservedAt: meeting.ScheduledAt.Add(time.Minute)}, nil
}

type coordinatorTestStore struct {
	mu     sync.Mutex
	record domainmeeting.OccurrenceRecord
	loads  int
	saves  int
}

func (p *coordinatorTestStore) LoadOrCreateMeetingOccurrence(_ context.Context, _ domainmeeting.StoreAccess, _ domainmeeting.OccurrenceKey, initial domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loads++
	if p.record.PayloadHash == "" {
		p.record = domainmeeting.OccurrenceRecord{Meeting: initial, PayloadHash: "stable-payload-token", Created: true}
	}
	return p.record, nil
}
func (p *coordinatorTestStore) SaveMeetingOccurrence(_ context.Context, _ domainmeeting.StoreAccess, _ domainmeeting.OccurrenceKey, expected domainmeeting.Guard, hash string, next domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record.Meeting.Guard() != expected || p.record.PayloadHash != hash {
		return domainmeeting.OccurrenceRecord{}, fmt.Errorf("%w: stored guard=%+v expected=%+v stored hash=%q expected hash=%q", domainmeeting.ErrStale, p.record.Meeting.Guard(), expected, p.record.PayloadHash, hash)
	}
	if next.ProgressionRevision != expected.ProgressionRevision+1 {
		return domainmeeting.OccurrenceRecord{}, domainmeeting.ErrConflict
	}
	p.saves++
	p.record.Meeting = next
	p.record.PayloadHash = fmt.Sprintf("payload-%d", next.ProgressionRevision)
	return p.record, nil
}

type coordinatorTestParticipants struct{}

func (coordinatorTestParticipants) EnsureReport(context.Context, domainmeeting.Meeting, string, string, []byte, string) (domainmeeting.Report, domainmeeting.SpeakerEvidence, error) {
	return domainmeeting.Report{}, domainmeeting.SpeakerEvidence{}, errors.New("unexpected participant generation")
}
func (coordinatorTestParticipants) EnsureAnswer(context.Context, domainmeeting.Meeting, string, string, []byte, string, domainmeeting.ConcernID) (domainmeeting.Answer, domainmeeting.SpeakerEvidence, error) {
	return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, errors.New("unexpected participant generation")
}

type coordinatorTestWorld struct{}

func (coordinatorTestWorld) GatherAndRead(context.Context, domainmeeting.Meeting, string, modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	return domainmeeting.WorldScopeEvidence{}, nil, errors.New("unexpected World gather")
}

type countedWorldFailure struct{ calls int }

func (p *countedWorldFailure) GatherAndRead(context.Context, domainmeeting.Meeting, string, modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	p.calls++
	return domainmeeting.WorldScopeEvidence{}, nil, errors.New("World unavailable")
}

type expiringGeneration struct {
	clock      *time.Time
	calls      int
	generated  int
	requestIDs []string
	report     domainmeeting.Report
	evidence   domainmeeting.SpeakerEvidence
}

func (p *expiringGeneration) EnsureReport(_ context.Context, meeting domainmeeting.Meeting, requestID, digest string, _ []byte, actorID string) (domainmeeting.Report, domainmeeting.SpeakerEvidence, error) {
	p.calls++
	p.requestIDs = append(p.requestIDs, requestID)
	if p.generated == 0 {
		p.generated++
		p.report = domainmeeting.Report{Utterance: domainmeeting.Utterance{MessageID: modulecore.NewMessageID(), ActorID: actorID, Body: "保存済みの定時報告です。"},
			Current: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone}, Next: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone},
			ConcernDeclaration: domainmeeting.ConcernsNone, Concerns: []domainmeeting.Concern{}}
		p.evidence = domainmeeting.SpeakerEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key,
			Generation: meeting.Generation, Epoch: meeting.Epoch, ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID,
			ThreadID: meeting.SharedThreadID, MessageID: p.report.Utterance.MessageID, ActorID: actorID,
			Body: p.report.Utterance.Body, ObservedAt: meeting.ScopeEvidence.ObservedAt.Add(time.Minute),
			GenerationRequestID: requestID, GenerationInputSHA256: digest, TaskRef: modulecore.NewTaskID(), RunRef: modulecore.NewRunID(),
			TaskAssignee: actorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
		*p.clock = meeting.ScopeEvidence.ValidUntil.Add(time.Second)
		return domainmeeting.Report{}, domainmeeting.SpeakerEvidence{}, errors.New("generation result persisted; response lost")
	}
	return p.report, p.evidence, nil
}

func (p *expiringGeneration) EnsureAnswer(context.Context, domainmeeting.Meeting, string, string, []byte, string, domainmeeting.ConcernID) (domainmeeting.Answer, domainmeeting.SpeakerEvidence, error) {
	return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, errors.New("unexpected answer generation")
}

type refreshingWorld struct {
	clock *time.Time
	calls int
}

type stableReplayWorld struct {
	requestIDs []string
	scope      domainmeeting.WorldScopeEvidence
	presence   []domainmeeting.PresenceEvidence
}

func (p *stableReplayWorld) GatherAndRead(_ context.Context, _ domainmeeting.Meeting, requestID string, threadID modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	p.requestIDs = append(p.requestIDs, requestID)
	if threadID != p.scope.ThreadID {
		return domainmeeting.WorldScopeEvidence{}, nil, domainmeeting.ErrConflict
	}
	return p.scope, append([]domainmeeting.PresenceEvidence(nil), p.presence...), nil
}

func (p *refreshingWorld) GatherAndRead(_ context.Context, meeting domainmeeting.Meeting, _ string, threadID modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	p.calls++
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1",
		StateSeq: uint64(100 + p.calls), StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: modulecore.NewEvidenceID()}
	scope := domainmeeting.WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation,
		Epoch: meeting.Epoch, Snapshot: snapshot, ThreadID: threadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: *p.clock, ValidUntil: meeting.DeadlineAt.Add(-time.Second)}
	presence := make([]domainmeeting.PresenceEvidence, 0, len(meeting.Participants))
	for _, actor := range meeting.Participants {
		presence = append(presence, domainmeeting.PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key,
			Generation: meeting.Generation, Epoch: meeting.Epoch, ScopeEvidenceID: scope.EvidenceID, Snapshot: snapshot,
			ActorID: actor, WorldActorID: "world-" + actor, Present: true, RoomRef: meeting.Rooms.Living,
			ObservedAt: scope.ObservedAt, ValidUntil: scope.ValidUntil})
	}
	return scope, presence, nil
}

type coordinatorTestConversation struct {
	mu                     sync.Mutex
	threadID               modulecore.ThreadID
	dispatches             int
	readback               *domainmeeting.DeliveryEvidence
	dispatchErr            error
	transcript             *domainmeeting.MinutesArtifactReference
	persistTranscript      func(domainmeeting.Meeting, string) (domainmeeting.MinutesArtifactReference, error)
	findTranscriptCalls    int
	persistTranscriptCalls int
}

func (p *coordinatorTestConversation) EnsureSharedThread(_ context.Context, meeting domainmeeting.Meeting, _ string) (modulecore.ThreadID, error) {
	if p.threadID != "" {
		return p.threadID, nil
	}
	return meeting.SharedThreadID, nil
}
func (p *coordinatorTestConversation) Dispatch(_ context.Context, meeting domainmeeting.Meeting, utterance domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dispatches++
	if p.dispatchErr != nil {
		return domainmeeting.DeliveryEvidence{}, p.dispatchErr
	}
	return coordinatorDeliveryEvidence(meeting, utterance), nil
}
func (p *coordinatorTestConversation) ReadDelivery(_ context.Context, _ domainmeeting.Meeting, _ domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readback == nil {
		return domainmeeting.DeliveryEvidence{}, false, nil
	}
	return *p.readback, true, nil
}
func (p *coordinatorTestConversation) FindTranscript(_ context.Context, meeting domainmeeting.Meeting, requestID string) (domainmeeting.MinutesArtifactReference, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.findTranscriptCalls++
	if p.transcript == nil || p.transcript.RequestID != requestID || p.transcript.Key != meeting.Key || p.transcript.Guard != meeting.Guard() {
		return domainmeeting.MinutesArtifactReference{}, false, nil
	}
	return cloneCoordinatorMinutesReference(*p.transcript), true, nil
}

func (p *coordinatorTestConversation) PersistTranscript(_ context.Context, meeting domainmeeting.Meeting, requestID string) (domainmeeting.MinutesArtifactReference, error) {
	p.mu.Lock()
	p.persistTranscriptCalls++
	factory := p.persistTranscript
	p.mu.Unlock()
	if factory == nil {
		return domainmeeting.MinutesArtifactReference{}, errors.New("unexpected transcript persistence")
	}
	ref, err := factory(meeting, requestID)
	if err != nil {
		return domainmeeting.MinutesArtifactReference{}, err
	}
	p.mu.Lock()
	p.transcript = &ref
	p.mu.Unlock()
	return cloneCoordinatorMinutesReference(ref), nil
}

func cloneCoordinatorMinutesReference(ref domainmeeting.MinutesArtifactReference) domainmeeting.MinutesArtifactReference {
	ref.Audience = append([]string(nil), ref.Audience...)
	return ref
}

func coordinatorFinishingMeeting(t *testing.T, config domainmeeting.ConfigSnapshot, schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) domainmeeting.Meeting {
	t.Helper()
	key, err := OccurrenceKeyFromScheduled(schedule, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	threadID := modulecore.NewThreadID()
	meeting, err = meeting.PrepareSharedThread(meeting.Guard(), key.String()+"|thread")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1", StateSeq: 1,
		StateHash: strings.Repeat("a", 64), OwnerProofID: modulecore.NewEvidenceID()}
	base := meeting.ScheduledAt.Add(2 * time.Minute)
	scope := domainmeeting.WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Snapshot: snapshot, ThreadID: threadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: meeting.DeadlineAt.Add(-time.Second)}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), domainmeeting.PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key,
			Generation: meeting.Generation, Epoch: meeting.Epoch, ScopeEvidenceID: scope.EvidenceID, Snapshot: snapshot,
			ActorID: actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Living,
			ObservedAt: base.Add(time.Second), ValidUntil: scope.ValidUntil})
		if err != nil {
			t.Fatal(err)
		}
	}
	meeting, err = meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for meeting.Status == domainmeeting.StatusReporting {
		turn := meeting.CurrentTurn
		if turn == nil || turn.Kind != domainmeeting.TurnReport {
			t.Fatalf("expected report turn, got %#v", turn)
		}
		report := domainmeeting.Report{Utterance: domainmeeting.Utterance{MessageID: modulecore.NewMessageID(), ActorID: turn.ActorID, Body: "定時報告です。"},
			Current: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone}, Next: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone},
			ConcernDeclaration: domainmeeting.ConcernsNone, Concerns: []domainmeeting.Concern{}}
		speaker := domainmeeting.SpeakerEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch,
			ScopeEvidenceID: scope.EvidenceID, ThreadID: threadID, MessageID: report.Utterance.MessageID, ActorID: turn.ActorID,
			Body: report.Utterance.Body, ObservedAt: base.Add(3 * time.Minute)}
		meeting, err = meeting.RecordReport(meeting.Guard(), report, speaker)
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, base.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		meeting, err = meeting.ConfirmDelivery(meeting.Guard(), coordinatorDeliveryEvidence(meeting, meeting.Reports[len(meeting.Reports)-1].Utterance))
		if err != nil {
			t.Fatal(err)
		}
	}
	if meeting.Status != domainmeeting.StatusFinishing || len(meeting.Reports) != 8 {
		t.Fatalf("meeting fixture did not reach finishing: status=%s reports=%d", meeting.Status, len(meeting.Reports))
	}
	return meeting
}

func coordinatorMinutesReference(t *testing.T, meeting domainmeeting.Meeting, requestID string, createdAt time.Time) domainmeeting.MinutesArtifactReference {
	t.Helper()
	audienceSHA, err := domaintask.TaskMeetingAudienceSHA256(meeting.Participants)
	if err != nil {
		t.Fatal(err)
	}
	return domainmeeting.MinutesArtifactReference{
		RequestID: requestID, ArtifactID: modulecore.NewArtifactID(), WorkstreamID: modulecore.NewWorkstreamID(),
		OwnerUserID: "owner-1", CoordinatorActorID: meeting.Participants[0], Key: meeting.Key,
		RegistrationEvidenceID: meeting.RegistrationEvidenceID, ConfigRevision: meeting.ScheduleRevision,
		ConfigSHA256: strings.Repeat("b", 64), Audience: append([]string(nil), meeting.Participants...), AudienceSHA256: audienceSHA,
		Guard: meeting.Guard(), TranscriptSHA256: strings.Repeat("c", 64), CreatedAt: createdAt,
	}
}

func TestAdvanceRecoversPreDeadlineCompletionBeforeDeadlinePartial(t *testing.T) {
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	input := ScheduledInput{Schedule: schedule, Occurrence: occurrence}
	meeting := coordinatorFinishingMeeting(t, config, schedule, occurrence)
	requestID := meeting.Key.String() + "|transcript"
	meeting, err := meeting.PrepareTranscript(meeting.Guard(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	ref := coordinatorMinutesReference(t, meeting, requestID, meeting.DeadlineAt.Add(-time.Second))
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "finishing"}}
	conversation := &coordinatorTestConversation{transcript: &ref}
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, coordinatorTestParticipants{}, coordinatorTestWorld{}, conversation,
		func() time.Time { return meeting.DeadlineAt.Add(time.Second) })
	result, err := coordinator.Advance(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Meeting.Status != domainmeeting.StatusCompleted || result.Meeting.PartialEvidence != nil || result.Meeting.FinishEvidence == nil ||
		result.Meeting.FinishEvidence.TranscriptArtifactID != ref.ArtifactID || conversation.persistTranscriptCalls != 0 {
		t.Fatalf("deadline recovery must finish from the exact pre-deadline artifact, not downgrade or rewrite: status=%s finish=%#v partial=%#v persists=%d", result.Meeting.Status, result.Meeting.FinishEvidence, result.Meeting.PartialEvidence, conversation.persistTranscriptCalls)
	}
}

func TestDeadlinePartialPersistsAndPinsTerminalMinutesAfterUnknownWrite(t *testing.T) {
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	input := ScheduledInput{Schedule: schedule, Occurrence: occurrence}
	key, err := OccurrenceKeyFromScheduled(schedule, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "running"}}
	conversation := &coordinatorTestConversation{}
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, coordinatorTestParticipants{}, coordinatorTestWorld{}, conversation,
		func() time.Time { return meeting.DeadlineAt.Add(time.Second) })
	conversation.persistTranscript = func(snapshot domainmeeting.Meeting, requestID string) (domainmeeting.MinutesArtifactReference, error) {
		if (snapshot.Status != domainmeeting.StatusPartial && snapshot.Status != domainmeeting.StatusMissed) || snapshot.TranscriptRequestID != requestID || requestID != snapshot.Key.String()+"|partial-minutes|"+string(snapshot.PartialEvidence.EvidenceID) {
			return domainmeeting.MinutesArtifactReference{}, errors.New("partial minutes were not written from the exact persisted terminal snapshot")
		}
		return coordinatorMinutesReference(t, snapshot, requestID, snapshot.DeadlineAt.Add(time.Second)), nil
	}
	result, err := coordinator.Advance(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Meeting.Status != domainmeeting.StatusMissed || result.Meeting.PartialEvidence == nil || result.Meeting.PartialMinutes == nil ||
		conversation.findTranscriptCalls != 1 || conversation.persistTranscriptCalls != 1 {
		t.Fatalf("deadline partial must retain its terminal outcome and exact minutes receipt: status=%s partial=%#v minutes=%#v reads/writes=%d/%d", result.Meeting.Status, result.Meeting.PartialEvidence, result.Meeting.PartialMinutes, conversation.findTranscriptCalls, conversation.persistTranscriptCalls)
	}
}

func (p *coordinatorTestConversation) dispatchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dispatches
}

func TestAdvanceDoesNotRedispatchPreexistingSendingWithoutReceipt(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliverySending)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{}
	coordinator := deliveryCoordinator(config, access, store, conversation)
	result, err := coordinator.Advance(ctx, input)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Advance error = %v, want unavailable", err)
	}
	if conversation.dispatchCount() != 0 {
		t.Fatalf("dispatches = %d, want 0", conversation.dispatchCount())
	}
	if result.Meeting.Reports[0].Utterance.DeliveryState != domainmeeting.DeliverySending {
		t.Fatalf("delivery state changed without receipt: %q", result.Meeting.Reports[0].Utterance.DeliveryState)
	}
}

func TestAdvanceOnlyFreshCASWinnerDispatches(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliveryUnsent)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{}
	first := deliveryCoordinator(config, access, store, conversation)
	second := deliveryCoordinator(config, access, store, conversation)
	var wg sync.WaitGroup
	for _, coordinator := range []*Coordinator{first, second} {
		wg.Add(1)
		go func(coordinator *Coordinator) { defer wg.Done(); _, _ = coordinator.Advance(ctx, input) }(coordinator)
	}
	wg.Wait()
	if got := conversation.dispatchCount(); got != 1 {
		t.Fatalf("dispatches = %d, want exactly one fresh CAS winner", got)
	}
}

func TestAdvanceConfirmsKnownReceiptWithoutRedispatch(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliverySending)
	utterance := meeting.Reports[0].Utterance
	receipt := coordinatorDeliveryEvidence(meeting, utterance)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{readback: &receipt}
	result, err := deliveryCoordinator(config, access, store, conversation).Advance(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.dispatchCount() != 0 {
		t.Fatalf("dispatches = %d, want 0 for already delivered receipt", conversation.dispatchCount())
	}
	if result.Meeting.Reports[0].Utterance.DeliveryState != domainmeeting.DeliveryDelivered {
		t.Fatalf("delivery state = %q, want delivered", result.Meeting.Reports[0].Utterance.DeliveryState)
	}
}

func TestAdvancePersistsBoundedUnknownReason(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliveryUnsent)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{dispatchErr: errors.New("token=super-secret /private/payload.json")}
	_, err := deliveryCoordinator(config, access, store, conversation).Advance(ctx, input)
	if err == nil {
		t.Fatal("dispatch error was not returned")
	}
	store.mu.Lock()
	saved := store.record.Meeting.Reports[0].Utterance
	store.mu.Unlock()
	if saved.DeliveryState != domainmeeting.DeliveryUnknown || saved.UnknownReason != "dispatch_outcome_unknown" {
		t.Fatalf("persisted uncertainty = (%q, %q)", saved.DeliveryState, saved.UnknownReason)
	}
	if strings.Contains(saved.UnknownReason, "super-secret") || strings.Contains(saved.UnknownReason, "payload.json") {
		t.Fatalf("raw dispatch error leaked into durable aggregate: %q", saved.UnknownReason)
	}
}

func TestOpenDueReplayDoesNotResumeAndUnavailablePortsFailClosed(t *testing.T) {
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	loc, _ := time.LoadLocation(domainmeeting.TimezoneJST)
	scheduledAt := occurrence.ScheduledAt.In(loc)
	key := domainmeeting.OccurrenceKey{ScheduleID: schedule.ScheduleID, LocalDate: scheduledAt.Format("2006-01-02"), Slot: scheduledAt.Format("15:04"), Timezone: domainmeeting.TimezoneJST}
	initial, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: initial, PayloadHash: "stored-hash"}}
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, nil, nil, nil, func() time.Time { return scheduledAt.Add(time.Minute) })
	first, err := coordinator.OpenDue(ctx, ScheduledInput{Schedule: schedule, Occurrence: occurrence})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.OpenDue(ctx, ScheduledInput{Schedule: schedule, Occurrence: occurrence})
	if err != nil {
		t.Fatal(err)
	}
	if first.Meeting.Guard() != second.Meeting.Guard() || first.Meeting.Generation != 1 || second.Meeting.Generation != 1 {
		t.Fatal("ordinary due replay changed the meeting generation or guard")
	}
	if first.Ready || !first.Unbound || len(first.Unavailable) != 4 {
		t.Fatalf("missing runtime ports were not exposed as unavailable: %+v", first)
	}
}

func TestOpenDueRejectsCoordinatorOutsideConfiguredRoster(t *testing.T) {
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	access.CoordinatorActorID = "not-registered"
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, &coordinatorTestStore{}, nil, nil, nil, time.Now)
	if _, err := coordinator.OpenDue(ctx, ScheduledInput{Schedule: schedule, Occurrence: occurrence}); err == nil {
		t.Fatal("unregistered current coordinator was accepted")
	}
}

func TestAdvanceGatheringReplaysStableWorldReceiptAcrossCheckpoints(t *testing.T) {
	for _, checkpoint := range []string{"scope", "partial_presence", "all_presence"} {
		t.Run(checkpoint, func(t *testing.T) {
			ctx, schedule, occurrence, config, access := coordinatorFixture(t)
			key := domainmeeting.OccurrenceKey{ScheduleID: schedule.ScheduleID, LocalDate: occurrence.ScheduledAt.Format("2006-01-02"), Slot: occurrence.ScheduledAt.Format("15:04"), Timezone: domainmeeting.TimezoneJST}
			meeting, err := domainmeeting.NewOccurrence(config, key)
			if err != nil {
				t.Fatal(err)
			}
			proof := NativeExecutionProof{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Assignee: access.CoordinatorActorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
			meeting, err = meeting.BindNativeExecution(meeting.Guard(), proof.TaskID, proof.RunID, proof.AdmissionEvidenceID)
			if err != nil {
				t.Fatal(err)
			}
			meeting, err = meeting.BeginGathering(meeting.Guard(), meeting.ScheduledAt.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			threadID := modulecore.NewThreadID()
			meeting, err = meeting.PrepareSharedThread(meeting.Guard(), key.String()+"|thread")
			if err != nil {
				t.Fatal(err)
			}
			gatherRequestID := key.String() + "|gather"
			meeting, err = meeting.PrepareGather(meeting.Guard(), gatherRequestID)
			if err != nil {
				t.Fatal(err)
			}
			base := meeting.ScheduledAt.Add(time.Minute)
			snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1", StateSeq: 24,
				StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: modulecore.NewEvidenceID()}
			scope := domainmeeting.WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch,
				Snapshot: snapshot, ThreadID: threadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
				Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: meeting.DeadlineAt.Add(-time.Second)}
			presence := make([]domainmeeting.PresenceEvidence, 0, len(meeting.Participants))
			for _, actor := range meeting.Participants {
				presence = append(presence, domainmeeting.PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation,
					Epoch: meeting.Epoch, ScopeEvidenceID: scope.EvidenceID, Snapshot: snapshot, ActorID: actor, WorldActorID: "world-" + actor,
					Present: true, RoomRef: meeting.Rooms.Living, ObservedAt: base, ValidUntil: scope.ValidUntil})
			}
			switch checkpoint {
			case "partial_presence":
				meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), scope)
				if err == nil {
					for _, item := range presence[:3] {
						meeting, err = meeting.RecordPresence(meeting.Guard(), item)
						if err != nil {
							break
						}
					}
				}
			case "all_presence":
				meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), scope)
				if err == nil {
					for _, item := range presence {
						meeting, err = meeting.RecordPresence(meeting.Guard(), item)
						if err != nil {
							break
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := meeting
			store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "stable-replay"}}
			world := &stableReplayWorld{scope: scope, presence: presence}
			conversation := &coordinatorTestConversation{threadID: threadID}
			coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, coordinatorTestParticipants{}, world, conversation,
				func() time.Time { return base.Add(time.Second) })
			input := ScheduledInput{Schedule: schedule, Occurrence: occurrence, RootExecution: &proof}
			result, err := coordinator.Advance(ctx, input)
			if err != nil {
				t.Fatalf("Advance from %s checkpoint: %v", checkpoint, err)
			}
			if result.Meeting.Status != domainmeeting.StatusReporting {
				t.Fatalf("status = %q, want reporting", result.Meeting.Status)
			}
			if len(world.requestIDs) != 1 || world.requestIDs[0] != gatherRequestID {
				t.Fatalf("World request IDs = %v, want one replay of %q", world.requestIDs, gatherRequestID)
			}
			if store.saves == 0 || result.Meeting.ProgressionRevision <= before.ProgressionRevision {
				t.Fatal("valid checkpoint resume did not commit actual remaining progression")
			}
		})
	}
}

func coordinatorFixture(t *testing.T) (context.Context, domainscheduler.Schedule, domainscheduler.ScheduledOccurrence, domainmeeting.ConfigSnapshot, domainmeeting.StoreAccess) {
	t.Helper()
	loc, err := time.LoadLocation(domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	actors := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	scheduleID := modulecore.NewScheduleID()
	scheduledAt := time.Date(2026, 11, 18, 8, 0, 0, 0, loc)
	timing := modulecore.TaskTiming{Timezone: domainmeeting.TimezoneJST, Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}}}
	schedule := domainscheduler.Schedule{ScheduleID: scheduleID, Timing: &timing}
	key, err := domainscheduler.OccurrenceKey(schedule, scheduledAt, domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatal(err)
	}
	occurrence := domainscheduler.ScheduledOccurrence{OccurrenceKey: key, ScheduledAt: scheduledAt, Timing: timing}
	config := domainmeeting.ConfigSnapshot{ScheduleID: scheduleID, Revision: 1, Timing: timing, ConfiguredActors: actors,
		WorldActorBindings: worldActorBindings(actors),
		RegisteredActors:   append([]string(nil), actors...), RegistrationEvidenceID: modulecore.NewEvidenceID(),
		Rooms: domainmeeting.RoomRefs{Living: "canonical:living", Dining: "canonical:dining"}, MeetingDeadlineAfterSecs: 3600}
	access := domainmeeting.StoreAccess{CoordinatorActorID: "mio", OwnerUserID: "owner-1", Audience: actors, AuthorityEvidence: modulecore.NewEvidenceID()}
	scope, err := tool.NewToolExecutionScope("meeting-open-test", tool.ActorKindAgent, access.CoordinatorActorID, access.OwnerUserID, []string{tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope), schedule, occurrence, config, access
}

func TestOccurrenceKeyFromScheduledUsesCanonicalJSTInstantAndChecksSchedulerIdentity(t *testing.T) {
	_, schedule, occurrence, _, _ := coordinatorFixture(t)
	want, err := OccurrenceKeyFromScheduled(schedule, occurrence)
	if err != nil {
		t.Fatal(err)
	}

	utcOccurrence := occurrence
	utcOccurrence.ScheduledAt = occurrence.ScheduledAt.UTC()
	got, err := OccurrenceKeyFromScheduled(schedule, utcOccurrence)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("same instant produced different meeting keys: got %+v, want %+v", got, want)
	}

	utcOccurrence.OccurrenceKey += "-changed"
	if _, err := OccurrenceKeyFromScheduled(schedule, utcOccurrence); err == nil {
		t.Fatal("mismatched scheduler occurrence identity was accepted")
	}
}

func TestPendingGenerationReplaysAfterEvidenceExpiryThenRefreshesWithoutRegeneration(t *testing.T) {
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	key := domainmeeting.OccurrenceKey{ScheduleID: schedule.ScheduleID, LocalDate: occurrence.ScheduledAt.Format("2006-01-02"),
		Slot: occurrence.ScheduledAt.Format("15:04"), Timezone: domainmeeting.TimezoneJST}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	proof := NativeExecutionProof{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Assignee: access.CoordinatorActorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
	meeting, err = meeting.BindNativeExecution(meeting.Guard(), proof.TaskID, proof.RunID, proof.AdmissionEvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	base := meeting.ScheduledAt.Add(time.Minute)
	meeting, err = meeting.BeginGathering(meeting.Guard(), base)
	if err != nil {
		t.Fatal(err)
	}
	threadID := modulecore.NewThreadID()
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1",
		StateSeq: 1, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: modulecore.NewEvidenceID()}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), domainmeeting.WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(),
		Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot, ThreadID: threadID,
		LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining, Audience: append([]string(nil), meeting.Participants...),
		ObservedAt: base, ValidUntil: base.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), domainmeeting.PresenceEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key,
			Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot, ActorID: actor, WorldActorID: "world-" + actor,
			Present: true, RoomRef: meeting.Rooms.Living, ObservedAt: base.Add(time.Second), ValidUntil: base.Add(4 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
	}
	meeting, err = meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "initial"}}
	clock := base.Add(3 * time.Second)
	participants := &expiringGeneration{clock: &clock}
	world := &refreshingWorld{clock: &clock}
	conversation := &coordinatorTestConversation{}
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, participants, world, conversation, func() time.Time { return clock })
	input := ScheduledInput{Schedule: schedule, Occurrence: occurrence, RootExecution: &proof}

	if _, err := coordinator.Advance(ctx, input); err != nil { // freeze the generation intent
		t.Fatal(err)
	}
	store.mu.Lock()
	intentMeeting := store.record.Meeting
	store.mu.Unlock()
	if err := bindSpeakerEvidenceToIntent(&domainmeeting.SpeakerEvidence{ScopeEvidenceID: modulecore.NewEvidenceID()}, intentMeeting); !errors.Is(err, domainmeeting.ErrConflict) {
		t.Fatalf("mismatched owner scope reference was relabeled: %v", err)
	}
	if _, err := coordinator.Advance(ctx, input); err == nil { // owner persisted output, then lost its response
		t.Fatal("lost generation response was not surfaced")
	}
	if world.calls != 0 {
		t.Fatal("World refresh ran before the pending generation receipt was recovered")
	}
	if _, err := coordinator.Advance(ctx, input); err != nil { // receipt-first recovery returns the old proof
		t.Fatal(err)
	}
	store.mu.Lock()
	storedAfterReplay := store.record.Meeting
	store.mu.Unlock()
	if len(storedAfterReplay.Reports) != 1 || storedAfterReplay.Reports[0].SpeakerEvidenceID != participants.evidence.EvidenceID {
		t.Fatal("persisted generation result was not recorded exactly once")
	}
	if participants.generated != 1 || participants.calls != 2 {
		t.Fatalf("generation count/calls = %d/%d, want one generation and two receipt lookups", participants.generated, participants.calls)
	}
	if intentMeeting.CurrentTurn == nil || modulecore.RequestID(intentMeeting.CurrentTurn.GenerationRequestID).Validate() != nil ||
		len(participants.requestIDs) != 2 || participants.requestIDs[0] != participants.requestIDs[1] || participants.requestIDs[0] != intentMeeting.CurrentTurn.GenerationRequestID {
		t.Fatalf("generation request identity was not canonical and stable across replay: turn=%#v calls=%#v", intentMeeting.CurrentTurn, participants.requestIDs)
	}
	if _, err := coordinator.Advance(ctx, input); err != nil { // expired current scope is refreshed for the next action
		t.Fatalf("refresh Advance failed (world calls=%d): %v", world.calls, err)
	}
	store.mu.Lock()
	refreshed := store.record.Meeting
	store.mu.Unlock()
	if world.calls != 1 || refreshed.ScopeEvidence.EvidenceID == storedAfterReplay.ScopeEvidence.EvidenceID {
		t.Fatal("stale World evidence was not replaced by one fresh owner read")
	}
	if len(refreshed.Reports) != 1 || len(refreshed.ScopeEvidenceHistory) != 1 || len(refreshed.AttendanceHistory) != len(refreshed.Participants) {
		t.Fatal("refresh did not preserve the historical generation and its World proof")
	}
}

func TestExpiredScopeReadsKnownDeliveryReceiptBeforeWorldRefresh(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliverySending)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	utterance := meeting.Reports[0].Utterance
	receipt := coordinatorDeliveryEvidence(meeting, utterance)
	conversation := &coordinatorTestConversation{readback: &receipt}
	world := &countedWorldFailure{}
	now := meeting.ScopeEvidence.ValidUntil.Add(time.Second)
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store,
		coordinatorTestParticipants{}, world, conversation, func() time.Time { return now })
	result, err := coordinator.Advance(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if world.calls != 0 || conversation.dispatchCount() != 0 || result.Meeting.Reports[0].Utterance.DeliveryState != domainmeeting.DeliveryDelivered {
		t.Fatalf("known receipt was not reconciled first: world=%d dispatch=%d state=%s", world.calls, conversation.dispatchCount(), result.Meeting.Reports[0].Utterance.DeliveryState)
	}
}

func TestExpiredScopeMissingDeliveryReceiptDoesNotRefreshOrResend(t *testing.T) {
	ctx, input, config, access, meeting := deliveryCoordinatorFixture(t, domainmeeting.DeliverySending)
	utterance := meeting.Reports[0].Utterance
	meeting, err := meeting.MarkCurrentDeliveryUnknown(meeting.Guard(), domainmeeting.DeliveryUnknownEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		MessageID: utterance.MessageID, Reason: "dispatch_outcome_unknown", ObservedAt: meeting.ScopeEvidence.ObservedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{}
	world := &countedWorldFailure{}
	now := meeting.ScopeEvidence.ValidUntil.Add(time.Second)
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store,
		coordinatorTestParticipants{}, world, conversation, func() time.Time { return now })
	_, err = coordinator.Advance(ctx, input)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing delivery receipt error = %v, want unavailable", err)
	}
	if world.calls != 0 || conversation.dispatchCount() != 0 {
		t.Fatalf("uncertain old delivery triggered new work: world=%d dispatch=%d", world.calls, conversation.dispatchCount())
	}
	store.mu.Lock()
	state := store.record.Meeting.Reports[0].Utterance.DeliveryState
	store.mu.Unlock()
	if state != domainmeeting.DeliveryUnknown {
		t.Fatalf("missing receipt changed delivery state to %q", state)
	}
}

type terminalAnswerAttemptParticipants struct{}

func (terminalAnswerAttemptParticipants) EnsureReport(context.Context, domainmeeting.Meeting, string, string, []byte, string) (domainmeeting.Report, domainmeeting.SpeakerEvidence, error) {
	return domainmeeting.Report{}, domainmeeting.SpeakerEvidence{}, errors.New("unexpected report generation")
}

func (terminalAnswerAttemptParticipants) EnsureAnswer(_ context.Context, meeting domainmeeting.Meeting, requestID, _ string, _ []byte, actorID string, concernID domainmeeting.ConcernID) (domainmeeting.Answer, domainmeeting.SpeakerEvidence, error) {
	turn := meeting.CurrentTurn
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	proof := domaintask.TaskMeetingGenerationUnknownReference{
		TaskID: taskID, RunID: runID, ReceiptID: domaintask.TaskMeetingGenerationUnknownReceiptID(taskID, runID),
		GenerationRequestID: modulecore.RequestID(requestID), Purpose: domaintask.TaskMeetingPurposeAnswer, ActorID: actorID,
		OccurrenceKey: meeting.Key.String(), ConfigRevision: meeting.ScheduleRevision,
		ConfigSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64), SourceContextSHA256: strings.Repeat("c", 64),
		GenerationScope: domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: turn.ScopeEvidenceID, Generation: turn.Generation, Epoch: turn.Epoch},
		Reason:          domaintask.TaskMeetingGenerationUnknownReasonEffectReturned, EvidenceID: modulecore.NewEvidenceID(), RecordedAt: meeting.ScheduledAt.Add(5 * time.Minute),
	}
	return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, &TerminalAnswerAttemptError{Attempt: domainmeeting.AnswerAttempt{ConcernID: concernID, ReporterID: meeting.Reports[meeting.CurrentReportIndex].Utterance.ActorID, UnknownReceipt: proof}}
}

func TestCoordinatorRecordsOnlyTerminalUnknownAnswerAttemptThenRequestsAnotherActor(t *testing.T) {
	ctx, input, config, access, meeting := answeringCoordinatorFixture(t)
	store := &coordinatorTestStore{record: domainmeeting.OccurrenceRecord{Meeting: meeting, PayloadHash: "payload-initial"}}
	conversation := &coordinatorTestConversation{}
	clock := meeting.ScheduledAt.Add(5 * time.Minute)
	coordinator := NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store,
		terminalAnswerAttemptParticipants{}, coordinatorTestWorld{}, conversation, func() time.Time { return clock })
	for step := 0; step < 3; step++ {
		if _, err := coordinator.Advance(ctx, input); err != nil {
			t.Fatalf("advance step %d: %v", step+1, err)
		}
	}
	store.mu.Lock()
	stored := store.record.Meeting
	store.mu.Unlock()
	if len(stored.AnswerAttempts) != 1 || stored.CurrentTurn != nil || stored.AllConcernsAnswered() {
		t.Fatalf("terminal unknown receipt should be durable without satisfying concern: attempts=%d turn=%#v", len(stored.AnswerAttempts), stored.CurrentTurn)
	}
	opened, err := coordinator.Advance(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Meeting.CurrentTurn == nil || opened.Meeting.CurrentTurn.ActorID != stored.Participants[2] || opened.Meeting.CurrentTurn.ConcernID == nil || *opened.Meeting.CurrentTurn.ConcernID != stored.AnswerAttempts[0].ConcernID {
		t.Fatalf("coordinator should continue with another actor for the unresolved concern: %#v", opened.Meeting.CurrentTurn)
	}
}

func answeringCoordinatorFixture(t *testing.T) (context.Context, ScheduledInput, domainmeeting.ConfigSnapshot, domainmeeting.StoreAccess, domainmeeting.Meeting) {
	t.Helper()
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	key := domainmeeting.OccurrenceKey{ScheduleID: schedule.ScheduleID, LocalDate: occurrence.ScheduledAt.Format("2006-01-02"), Slot: occurrence.ScheduledAt.Format("15:04"), Timezone: domainmeeting.TimezoneJST}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	rootProof := NativeExecutionProof{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Assignee: access.CoordinatorActorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
	meeting, err = meeting.BindNativeExecution(meeting.Guard(), rootProof.TaskID, rootProof.RunID, rootProof.AdmissionEvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	base := meeting.ScheduledAt.Add(time.Minute)
	meeting, err = meeting.BeginGathering(meeting.Guard(), base)
	if err != nil {
		t.Fatal(err)
	}
	threadID := modulecore.NewThreadID()
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1", StateSeq: 7, StateHash: strings.Repeat("a", 64), OwnerProofID: modulecore.NewEvidenceID()}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), domainmeeting.WorldScopeEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot,
		ThreadID: threadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: base.Add(45 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), domainmeeting.PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot,
			ActorID: actorID, WorldActorID: "world-" + actorID, Present: true, RoomRef: meeting.Rooms.Living,
			ObservedAt: base.Add(time.Second), ValidUntil: base.Add(40 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	meeting, err = meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	actorID := meeting.Participants[0]
	report := domainmeeting.Report{
		Utterance: domainmeeting.Utterance{MessageID: modulecore.NewMessageID(), ActorID: actorID, Body: "定時報告です。"},
		Current:   domainmeeting.ReportField{State: domainmeeting.ReportFieldNone}, Next: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone},
		ConcernDeclaration: domainmeeting.ConcernsListed, Concerns: []domainmeeting.Concern{{Description: "deployment evidence lacks a second reviewer"}},
	}
	speaker := domainmeeting.SpeakerEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID, ThreadID: threadID, MessageID: report.Utterance.MessageID,
		ActorID: actorID, Body: report.Utterance.Body, ObservedAt: base.Add(3 * time.Minute)}
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speaker)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, base.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	meeting, err = meeting.ConfirmDelivery(meeting.Guard(), coordinatorDeliveryEvidence(meeting, meeting.Reports[0].Utterance))
	if err != nil {
		t.Fatal(err)
	}
	input := ScheduledInput{Schedule: schedule, Occurrence: occurrence, RootExecution: &rootProof}
	return ctx, input, config, access, meeting
}

func worldActorBindings(actors []string) []domainmeeting.WorldActorBinding {
	out := make([]domainmeeting.WorldActorBinding, 0, len(actors))
	for _, actor := range actors {
		out = append(out, domainmeeting.WorldActorBinding{CoreActorID: actor, WorldActorID: "world-" + actor})
	}
	return out
}

func deliveryCoordinatorFixture(t *testing.T, deliveryState string) (context.Context, ScheduledInput, domainmeeting.ConfigSnapshot, domainmeeting.StoreAccess, domainmeeting.Meeting) {
	t.Helper()
	ctx, schedule, occurrence, config, access := coordinatorFixture(t)
	key := domainmeeting.OccurrenceKey{ScheduleID: schedule.ScheduleID, LocalDate: occurrence.ScheduledAt.Format("2006-01-02"), Slot: occurrence.ScheduledAt.Format("15:04"), Timezone: domainmeeting.TimezoneJST}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	proof := NativeExecutionProof{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Assignee: access.CoordinatorActorID, AdmissionEvidenceID: modulecore.NewEvidenceID()}
	meeting, err = meeting.BindNativeExecution(meeting.Guard(), proof.TaskID, proof.RunID, proof.AdmissionEvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	base := meeting.ScheduledAt.Add(time.Minute)
	meeting, err = meeting.BeginGathering(meeting.Guard(), base)
	if err != nil {
		t.Fatal(err)
	}
	threadID := modulecore.NewThreadID()
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1", StateSeq: 7, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: modulecore.NewEvidenceID()}
	meeting, err = meeting.ConfirmWorldScope(meeting.Guard(), domainmeeting.WorldScopeEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot,
		ThreadID: threadID, LivingRoomRef: meeting.Rooms.Living, DiningRoomRef: meeting.Rooms.Dining,
		Audience: append([]string(nil), meeting.Participants...), ObservedAt: base, ValidUntil: base.Add(45 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range meeting.Participants {
		meeting, err = meeting.RecordPresence(meeting.Guard(), domainmeeting.PresenceEvidence{
			EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch, Snapshot: snapshot,
			ActorID: actor, WorldActorID: "world-" + actor, Present: true, RoomRef: meeting.Rooms.Living,
			ObservedAt: base.Add(time.Second), ValidUntil: base.Add(40 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	meeting, err = meeting.BeginReporting(meeting.Guard(), base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	actor := meeting.Participants[0]
	report := domainmeeting.Report{Utterance: domainmeeting.Utterance{MessageID: modulecore.NewMessageID(), ActorID: actor, Body: "定時報告です。"},
		Current: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone}, Next: domainmeeting.ReportField{State: domainmeeting.ReportFieldNone},
		ConcernDeclaration: domainmeeting.ConcernsNone, Concerns: []domainmeeting.Concern{}}
	speaker := domainmeeting.SpeakerEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: meeting.Generation, Epoch: meeting.Epoch,
		ScopeEvidenceID: meeting.ScopeEvidence.EvidenceID,
		ThreadID:        threadID, MessageID: report.Utterance.MessageID, ActorID: actor, Body: report.Utterance.Body, ObservedAt: base.Add(3 * time.Second)}
	meeting, err = meeting.RecordReport(meeting.Guard(), report, speaker)
	if err != nil {
		t.Fatal(err)
	}
	if deliveryState == domainmeeting.DeliverySending {
		meeting, err = meeting.BeginCurrentDelivery(meeting.Guard(), report.Utterance.MessageID, base.Add(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	} else if deliveryState != domainmeeting.DeliveryUnsent {
		t.Fatalf("unsupported test delivery state %q", deliveryState)
	}
	input := ScheduledInput{Schedule: schedule, Occurrence: occurrence, RootExecution: &proof}
	return ctx, input, config, access, meeting
}

func deliveryCoordinator(config domainmeeting.ConfigSnapshot, access domainmeeting.StoreAccess, store *coordinatorTestStore, conversation *coordinatorTestConversation) *Coordinator {
	return NewCoordinator(coordinatorTestConfig(config), coordinatorTestAuthority{access}, store, coordinatorTestParticipants{}, coordinatorTestWorld{}, conversation,
		func() time.Time { return time.Date(2026, 11, 18, 8, 5, 0, 0, time.FixedZone("JST", 9*60*60)) })
}

func coordinatorDeliveryEvidence(meeting domainmeeting.Meeting, utterance domainmeeting.Utterance) domainmeeting.DeliveryEvidence {
	return domainmeeting.DeliveryEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: utterance.DeliveryGeneration, Epoch: utterance.DeliveryEpoch,
		ScopeEvidenceID: utterance.DeliveryScopeEvidenceID,
		ThreadID:        meeting.SharedThreadID, MessageID: utterance.MessageID, Delivered: true, Audience: append([]string(nil), meeting.Participants...),
		ObservedAt: meeting.ScheduledAt.Add(5 * time.Minute)}
}
