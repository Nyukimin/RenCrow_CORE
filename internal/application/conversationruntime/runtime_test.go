package conversationruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeGeneratesOnceFromCanonicalHistoryCommitsBeforePublishing(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("final answer"))
	fixture.publisher.beforePublish = func(batch CommittedBundle) error {
		if !fixture.journal.wasCommitted(batch) {
			return errors.New("publication happened before durable commit")
		}
		return nil
	}

	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDelivered || !result.Committed || result.SafeError {
		t.Fatalf("RunAcceptedTurn() = %+v, want a delivered committed response", result)
	}
	if fixture.provider.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want exactly one", fixture.provider.callCount())
	}
	if fixture.journal.commitCount() != 1 || fixture.publisher.publishCount() != 1 {
		t.Fatalf("commit/publish counts = %d/%d, want one each", fixture.journal.commitCount(), fixture.publisher.publishCount())
	}
	if fixture.journal.loadCount() != 1 {
		t.Fatalf("pre-generation committed outcome lookups = %d, want one", fixture.journal.loadCount())
	}
	for _, event := range result.Batch.Events {
		if event.EventID == fixture.input.ReadViewRequest.Accepted.InputEvent.EventID {
			t.Fatal("output bundle re-committed the separately accepted input event")
		}
	}
	request := fixture.provider.lastRequest()
	if messageCount(request.Messages, "user", "current request") != 1 || messageCount(request.Messages, "user", "earlier request") != 1 || messageCount(request.Messages, "assistant", "earlier answer") != 1 {
		t.Fatalf("prompt messages lost or duplicated canonical history/current input: %#v", request.Messages)
	}
	if messageCount(request.Messages, "system", "Mio authored system context") != 1 {
		t.Fatalf("Mio system context was not composed into the prompt: %#v", request.Messages)
	}
	public := publicEvents(result.Batch)
	if len(public) != 1 {
		t.Fatalf("public orchestrator events = %d, want one", len(public))
	}
	assertPublicEnvelope(t, public[0], fixture.input.ReadViewRequest.Accepted.ClientSubmissionID, "reply", "available", "final answer")
	if len(result.Batch.Events) != 2 || result.Batch.Events[1].EventType != "conversation.mio_emission_receipt" || result.Batch.Events[1].ComponentID == "orchestrator" {
		t.Fatalf("receipt must remain private beside public output: %#v", result.Batch.Events)
	}
}

func TestRuntimeRecordsResponseFirstTimingSpansInTheOptInBatch(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("timed reply"))
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 32})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	accepted := fixture.input.ReadViewRequest.Accepted
	if !turn.SetIdentity(string(accepted.TurnID), string(accepted.InputEvent.TraceID)) {
		t.Fatal("could not associate accepted turn identity with timing slot")
	}
	ctx := turntiming.WithBatchTurnRecorder(context.Background(), turn)
	result := fixture.runtime.RunAcceptedTurn(ctx, fixture.input)
	if result.Status != TurnStatusDelivered || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want successful timed response", result)
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion did not mark the timing turn")
	}
	if err := batch.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	var beginPoints, endPoints []uint16
	for index := uint32(0); index < turnSnapshot.StampCount(); index++ {
		stamp, ok := turnSnapshot.Stamp(index)
		if !ok {
			t.Fatalf("timing stamp %d disappeared from frozen snapshot", index)
		}
		if stamp.Component != turntiming.ComponentV2ConversationRuntime {
			t.Fatalf("runtime timing stamp component=%d, want %d", stamp.Component, turntiming.ComponentV2ConversationRuntime)
		}
		switch stamp.Phase {
		case turntiming.PhaseBegin:
			beginPoints = append(beginPoints, stamp.Point)
		case turntiming.PhaseEnd:
			if stamp.Result != uint8(turntiming.ResultOK) {
				t.Fatalf("successful runtime phase ended with result=%d", stamp.Result)
			}
			endPoints = append(endPoints, stamp.Point)
		}
	}
	want := []uint16{
		turntiming.PointV2ReadView, turntiming.PointV2PromptComposition,
		turntiming.PointV2Generation, turntiming.PointV2Commit, turntiming.PointV2Publish,
	}
	if !reflect.DeepEqual(beginPoints, want) || !reflect.DeepEqual(endPoints, want) {
		t.Fatalf("runtime span points begin=%v end=%v, want each phase once in order %v", beginPoints, endPoints, want)
	}
	gotTurnID, gotTraceID, hasIdentity := turnSnapshot.Identity()
	if !hasIdentity || gotTurnID != string(accepted.TurnID) || gotTraceID != string(accepted.InputEvent.TraceID) {
		t.Fatalf("timing slot identity=(%q,%q,%t), want accepted turn=%s trace=%s", gotTurnID, gotTraceID, hasIdentity, accepted.TurnID, accepted.InputEvent.TraceID)
	}
}

func TestRuntimeTimingMarksProviderFailureWithoutLosingSafeErrorResult(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("unused"))
	fixture.provider.err = errors.New("provider unavailable")
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 32})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	ctx := turntiming.WithBatchTurnRecorder(context.Background(), turn)
	result := fixture.runtime.RunAcceptedTurn(ctx, fixture.input)
	if result.Status != TurnStatusDelivered || !result.SafeError || !result.Committed {
		t.Fatalf("provider failure result=%+v, want committed safe error", result)
	}
	if !turn.FinishForeground() {
		t.Fatal("foreground completion did not mark provider error path")
	}
	if err := batch.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnSnapshot, _ := snapshot.Turn(0)
	var generationErrorEnd, generationErrorPoint bool
	for index := uint32(0); index < turnSnapshot.StampCount(); index++ {
		stamp, _ := turnSnapshot.Stamp(index)
		if stamp.Point == turntiming.PointV2Generation && stamp.Phase == turntiming.PhaseEnd && stamp.Result == uint8(turntiming.ResultError) {
			generationErrorEnd = true
		}
		if stamp.Point == turntiming.PointV2Error && stamp.Phase == turntiming.PhasePoint && stamp.Ref == uint32(turntiming.PointV2Generation) && stamp.Result == uint8(turntiming.ResultError) {
			generationErrorPoint = true
		}
	}
	if !generationErrorEnd || !generationErrorPoint {
		t.Fatalf("provider error timing: generation_end=%t error_point=%t stamps=%d", generationErrorEnd, generationErrorPoint, turnSnapshot.StampCount())
	}
}

func TestRuntimeReportReplyUsesOneDirectMioGenerationAndLinksOriginalRequest(t *testing.T) {
	fixture := newReportRuntimeFixture(t, replyEmission("The report supports this answer."))
	first := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if first.Status != TurnStatusDelivered || !first.Committed || first.SafeError {
		t.Fatalf("report reply result = %+v, want delivered committed reply", first)
	}
	if fixture.provider.callCount() != 1 || fixture.journal.commitCount() != 1 || fixture.publisher.publishCount() != 1 {
		t.Fatalf("report reply LLM/commit/publish counts = %d/%d/%d, want one each", fixture.provider.callCount(), fixture.journal.commitCount(), fixture.publisher.publishCount())
	}
	if scope := fixture.provider.lastScope(); scope.Purpose != conversation.PurposeReportReply || scope.ActorID != fixture.input.ReadViewRequest.Accepted.ActorID {
		t.Fatalf("Mio conversation scope = %+v, want report_reply purpose and trusted assignee actor", scope)
	}
	request := fixture.provider.lastRequest()
	if len(request.Messages) < 2 || request.Messages[len(request.Messages)-1].Role != "user" {
		t.Fatalf("report prompt messages = %#v, want server-framed report as final user input", request.Messages)
	}
	for _, expected := range []string{"report_untrusted_data", "untrusted", "元の依頼", "モデルを変更しない", "SQLite commit後にprojection待ち"} {
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, expected) {
			t.Fatalf("Mio report prompt omitted %q: %s", expected, request.Messages[len(request.Messages)-1].Content)
		}
	}

	accepted := fixture.input.ReadViewRequest.Accepted
	report, err := parseAcceptedReport(accepted)
	if err != nil {
		t.Fatalf("parse accepted report: %v", err)
	}
	public := publicEvents(first.Batch)
	if len(public) != 1 {
		t.Fatalf("public report reply events = %d, want one", len(public))
	}
	event := public[0]
	if event.Payload["kind"] != "report_reply" || event.Payload["text"] != "The report supports this answer." ||
		event.Payload["reply_to_message_id"] != string(report.OriginMessageID) ||
		event.Payload["origin_trace_id"] != string(report.OriginTraceID) ||
		event.Payload["origin_turn_id"] != string(report.OriginTurnID) ||
		event.Payload["origin_message_id"] != string(report.OriginMessageID) ||
		event.Payload["delegation_id"] != string(report.DelegationID) ||
		event.Payload["delegation_revision"] != report.DelegationRevision ||
		event.TraceID != accepted.InputEvent.TraceID || event.CausationEventID != accepted.InputEvent.EventID {
		t.Fatalf("public report reply lost canonical report/origin links: %+v", event)
	}
	index := fixture.journal.lastCommit().MessageIndexes[0]
	if index.Kind != "report_reply" || index.Visibility != "public" || index.ToActor != fixture.input.ReadViewRequest.Session.OwnerID ||
		index.ReplyToMessageID != report.OriginMessageID {
		t.Fatalf("public report reply index = %+v, want owner-visible reply linked to original user message", index)
	}
	publicJSON := string(mustJSON(t, event))
	if strings.Contains(publicJSON, report.Report.Summary) || strings.Contains(publicJSON, report.Report.Result) || strings.Contains(publicJSON, "report_untrusted_data") {
		t.Fatalf("public response event leaked private report data: %s", publicJSON)
	}

	replay := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if replay.Status != TurnStatusDelivered || !reflect.DeepEqual(first.Batch, replay.Batch) ||
		fixture.provider.callCount() != 1 || fixture.journal.commitCount() != 1 || fixture.publisher.publishAttempts() != 2 {
		t.Fatalf("report replay regenerated or changed output: status=%s calls=%d commits=%d publish attempts=%d", replay.Status, fixture.provider.callCount(), fixture.journal.commitCount(), fixture.publisher.publishAttempts())
	}
}

func TestRuntimeReportReplyRejectsDelegationEvenWhenAssigneeIsAllowed(t *testing.T) {
	fixture := newReportRuntimeFixture(t, delegationEmission("private follow-up"))
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	commit := fixture.journal.lastCommit()
	public := publicEvents(result.Batch)
	if result.Status != TurnStatusDelivered || !result.Committed || !result.SafeError || fixture.provider.callCount() != 1 {
		t.Fatalf("report reply delegate result = %+v, calls=%d; want one generation and committed safe error", result, fixture.provider.callCount())
	}
	if commit.Delegation != nil || outboxContains(commit.Outbox, "dispatch_delegation") || len(public) != 1 || public[0].Payload["kind"] != "turn_error" {
		t.Fatalf("report reply delegate created work or exposed rejected text: %+v", commit)
	}
	if scope := fixture.provider.lastScope(); scope.Purpose != conversation.PurposeReportReply {
		t.Fatalf("report reply scope purpose = %q, want %q", scope.Purpose, conversation.PurposeReportReply)
	}
}

func TestRuntimeStaleReportFenceReturnsTypedStatusWithoutPublishOrUncertainRetry(t *testing.T) {
	fixture := newReportRuntimeFixture(t, replyEmission("stale answer"))
	fixture.journal.commitError = ErrStaleReport
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusStaleReport || result.Committed || result.SafeError || !errors.Is(result.Cause, ErrStaleReport) {
		t.Fatalf("stale report result = %+v, want typed stale_report without a commit", result)
	}
	if fixture.provider.callCount() != 1 || fixture.journal.commitCount() != 1 || fixture.journal.loadCount() != 1 || fixture.publisher.publishAttempts() != 0 {
		t.Fatalf("stale report calls = generation:%d commit:%d lookup:%d publish:%d; want one generation/commit, no recovery lookup or publish", fixture.provider.callCount(), fixture.journal.commitCount(), fixture.journal.loadCount(), fixture.publisher.publishAttempts())
	}
}

func TestRuntimeCommitsDelegationNoticeAndPrivateInstructionTogether(t *testing.T) {
	fixture := newRuntimeFixture(t, delegationEmission("Shiroへの非公開指示です"))
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDelivered || result.ResultState != "awaiting_readback" || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want committed delegation awaiting readback", result)
	}
	if fixture.journal.commitCount() != 1 {
		t.Fatalf("delegation commit count = %d, want one atomic bundle", fixture.journal.commitCount())
	}
	if len(result.Batch.Events) != 4 || result.Batch.Events[0].ComponentID != "orchestrator" || result.Batch.Events[2].ComponentID == "orchestrator" {
		t.Fatalf("delegation bundle order/components = %#v", result.Batch.Events)
	}
	if result.Batch.Events[2].EventType != "conversation.delegation_instruction" || result.Batch.Events[2].Payload["text"] != "Shiroへの非公開指示です" {
		t.Fatalf("private instruction event missing: %#v", result.Batch.Events[2])
	}
	if result.Batch.Events[2].ActorKind != "agent" || result.Batch.Events[2].ActorID != "mio" {
		t.Fatalf("private instruction event actor=(%q,%q), want authenticated Mio identity", result.Batch.Events[2].ActorKind, result.Batch.Events[2].ActorID)
	}
	if result.Batch.Events[0].Payload["kind"] != "delegation_notice" || result.Batch.Events[0].Payload["availability"] != "pending" {
		t.Fatalf("public delegation notice payload = %#v", result.Batch.Events[0].Payload)
	}
	if result.Batch.Events[0].ActorKind != "" || result.Batch.Events[0].ActorID != "" {
		t.Fatalf("public event must not claim task-scoped actor identity: %+v", result.Batch.Events[0])
	}
	if result.Batch.Events[0].Payload["text"] != result.Batch.Events[0].Payload["content"] {
		t.Fatalf("public text/content diverged: %#v", result.Batch.Events[0].Payload)
	}
	if fixture.journal.lastCommit().Delegation == nil || fixture.journal.lastCommit().Delegation.State != "dispatch_accepted" {
		t.Fatalf("delegation record was not committed: %+v", fixture.journal.lastCommit().Delegation)
	}
	if !outboxContains(fixture.journal.lastCommit().Outbox, "dispatch_delegation") {
		t.Fatalf("delegation dispatch was not accepted atomically: %#v", fixture.journal.lastCommit().Outbox)
	}
	if result.Batch.Events[0].Payload["delegation_id"] != fixture.journal.lastCommit().Delegation.DelegationID {
		t.Fatalf("public delegation correlation does not match committed handoff: %#v", result.Batch.Events[0].Payload)
	}
	publicJSON := string(mustJSON(t, result.Batch.Events[0]))
	if strings.Contains(publicJSON, "Shiroへの非公開指示です") || strings.Contains(publicJSON, "request_id") || strings.Contains(publicJSON, "response_id") || strings.Contains(publicJSON, "raw_content") {
		t.Fatalf("public envelope leaked private instruction or receipt material: %s", publicJSON)
	}
	for _, event := range result.Batch.Events {
		if event.ComponentID == "orchestrator" && strings.Contains(string(mustJSON(t, event)), "Shiroへの非公開指示です") {
			t.Fatalf("private instruction appeared in public envelope %s", event.EventID)
		}
	}
}

func TestRuntimeInvalidEmissionCommitsSafeErrorWithoutDelegation(t *testing.T) {
	fixture := newRuntimeFixture(t, `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"Shiroに依頼したよ"},"delegation":{"to":"not_allowed","goal":"g","instruction":"private","constraints":[],"expected_output":"report","source_message_ids":[]}}`)
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDelivered || !result.SafeError || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want committed safe error", result)
	}
	commit := fixture.journal.lastCommit()
	if commit.ResultState != "completed" || commit.Delegation != nil || outboxContains(commit.Outbox, "dispatch_delegation") {
		t.Fatalf("invalid output created delegation state/work: %+v", commit)
	}
	if len(publicEvents(result.Batch)) != 1 || result.Batch.Events[0].Payload["kind"] != "turn_error" {
		t.Fatalf("invalid output did not produce one generic error message: %#v", result.Batch.Events)
	}
	publicJSON := string(mustJSON(t, result.Batch.Events[0]))
	if strings.Contains(publicJSON, "Shiroに依頼したよ") || strings.Contains(publicJSON, "private") {
		t.Fatalf("safe error leaked rejected model output: %s", publicJSON)
	}
	for _, event := range result.Batch.Events {
		if event.EventType == "conversation.delegation_instruction" {
			t.Fatal("invalid emission committed a delegation instruction")
		}
	}
}

func TestRuntimeUnknownCommitOutcomeLoadsSavedBatchWithoutRegeneration(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("saved answer"))
	fixture.journal.commitAfterSaveError = errors.New("commit acknowledgement lost")
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDelivered || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want recovered saved batch", result)
	}
	if fixture.provider.callCount() != 1 || fixture.journal.commitCount() != 1 || fixture.journal.loadCount() != 2 || fixture.publisher.publishCount() != 1 {
		t.Fatalf("LLM/commit/load/publish counts = %d/%d/%d/%d", fixture.provider.callCount(), fixture.journal.commitCount(), fixture.journal.loadCount(), fixture.publisher.publishCount())
	}
	if !reflect.DeepEqual(result.Batch, fixture.journal.committed) {
		t.Fatalf("published batch differs from saved canonical batch: got %#v want %#v", result.Batch, fixture.journal.committed)
	}
}

func TestRuntimeCommittedReplayPublishesSavedBatchWithoutRegeneration(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("saved once"))
	first := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	second := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if first.Status != TurnStatusDelivered || second.Status != TurnStatusDelivered {
		t.Fatalf("first/replay status = %s/%s, want delivered", first.Status, second.Status)
	}
	if fixture.provider.callCount() != 1 || fixture.journal.commitCount() != 1 || fixture.publisher.publishAttempts() != 2 {
		t.Fatalf("replay LLM/commit/publish attempts = %d/%d/%d, want 1/1/2", fixture.provider.callCount(), fixture.journal.commitCount(), fixture.publisher.publishAttempts())
	}
	if fixture.publisher.uniqueEventCount() != len(first.Batch.Events) || len(fixture.publisher.batches) != 1 {
		t.Fatalf("idempotent publisher replay exposed duplicate events: unique=%d batches=%d", fixture.publisher.uniqueEventCount(), len(fixture.publisher.batches))
	}
	if !reflect.DeepEqual(first.Batch, second.Batch) {
		t.Fatalf("replay batch changed: first=%#v second=%#v", first.Batch, second.Batch)
	}
}

func TestRuntimeCommitWithoutRecoverableReceiptRemainsUncertain(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("answer"))
	fixture.journal.commitError = errors.New("journal unavailable")
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusCommitUncertain || result.Committed || fixture.provider.callCount() != 1 || fixture.publisher.publishCount() != 0 {
		t.Fatalf("RunAcceptedTurn() = %+v; calls=%d publishes=%d", result, fixture.provider.callCount(), fixture.publisher.publishCount())
	}
}

func TestRuntimePublishFailureIsDeliveryPendingNotGenerationFailure(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("already committed"))
	fixture.publisher.publishErr = errors.New("projector unavailable")
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDeliveryPending || !result.Committed || result.SafeError || fixture.journal.commitCount() != 1 {
		t.Fatalf("RunAcceptedTurn() = %+v, want committed delivery_pending response", result)
	}
	if fixture.provider.callCount() != 1 || !errors.Is(result.Cause, fixture.publisher.publishErr) {
		t.Fatalf("generation/publish failure classification = calls:%d cause:%v", fixture.provider.callCount(), result.Cause)
	}
}

func TestRuntimePublicEventProjectsThroughCanonicalViewerLog(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("viewer sees this"))
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	public := publicEvents(result.Batch)
	if len(public) != 1 {
		t.Fatalf("public events = %d, want 1", len(public))
	}
	store := &runtimeCanonicalEventStore{events: public}
	log, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog(): %v", err)
	}
	projected, err := log.Query(context.Background(), viewer.LogFilter{Limit: 1})
	if err != nil || len(projected) != 1 {
		t.Fatalf("CanonicalEventLog.Query(): events=%d error=%v", len(projected), err)
	}
	accepted := fixture.input.ReadViewRequest.Accepted
	if projected[0].Type != "agent.response" || projected[0].From != "mio" || projected[0].To != "user" ||
		projected[0].Content != "viewer sees this" || projected[0].Kind != "reply" || projected[0].Visibility != "public" ||
		projected[0].ClientSubmissionID != accepted.ClientSubmissionID || projected[0].Availability != "available" ||
		projected[0].ReplyToMessageID != accepted.InputMessageID || projected[0].RawContent != "" || projected[0].ActorKind != "" || projected[0].ActorID != "" {
		t.Fatalf("Viewer projection = %+v", projected[0])
	}
}

func TestRuntimeRejectsOversizedSubmissionCorrelationBeforeLoading(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("should not run"))
	fixture.input.ReadViewRequest.Accepted.ClientSubmissionID = strings.Repeat("x", 129)
	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusFailedBeforeCommit || fixture.journal.loadCount() != 0 || fixture.provider.callCount() != 0 || fixture.journal.commitCount() != 0 {
		t.Fatalf("oversized client submission id was not rejected at ingress: result=%+v loads=%d LLM=%d commits=%d", result, fixture.journal.loadCount(), fixture.provider.callCount(), fixture.journal.commitCount())
	}
}

func TestRuntimeCancellationDiscardsLateGenerationWithoutCommit(t *testing.T) {
	request, tail := runtimeInput(t, modulecore.NewSessionID(), 3, "late answer")
	provider := &fakeConversationProvider{response: replyEmission("late answer"), entered: make(chan struct{}, 1), release: make(chan struct{})}
	journal, publisher := &fakeJournal{}, &fakePublisher{}
	runtime := mustRuntime(t, agent.NewMioEmissionGenerator(provider), journal, publisher, []string{"shiro"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan TurnResult, 1)
	go func() { done <- runtime.RunAcceptedTurn(ctx, AcceptedTurnInput{ReadViewRequest: request}) }()
	<-provider.entered
	cancel()
	close(provider.release)
	result := <-done
	if result.Status != TurnStatusFailedBeforeCommit || result.Committed || journal.commitCount() != 0 || publisher.publishCount() != 0 {
		t.Fatalf("late generation result = %+v; commit/publish=%d/%d", result, journal.commitCount(), publisher.publishCount())
	}
	_ = tail
}

func TestRuntimeSerializesOneSessionButAllowsIndependentSession(t *testing.T) {
	firstRequest, _ := runtimeInput(t, modulecore.NewSessionID(), 3, "first")
	firstSession := firstRequest.Session.SessionID
	secondRequest := requestForSameSession(t, firstRequest, 4, "second")
	secondSessionRequest, _ := runtimeInput(t, modulecore.NewSessionID(), 3, "other session")
	gateA, gateB := make(chan struct{}), make(chan struct{})
	generator := &sessionGateGenerator{
		started: make(chan modulecore.SessionID, 4), gates: map[modulecore.SessionID]<-chan struct{}{
			firstSession: gateA, secondSessionRequest.Session.SessionID: gateB,
		},
	}
	journal, publisher := &fakeJournal{}, &fakePublisher{}
	runtime := mustRuntime(t, generator, journal, publisher, []string{"shiro"})
	firstDone := make(chan TurnResult, 1)
	go func() {
		firstDone <- runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: firstRequest})
	}()
	if got := <-generator.started; got != firstSession {
		t.Fatalf("first generator session = %s, want %s", got, firstSession)
	}
	secondDone := make(chan TurnResult, 1)
	go func() {
		secondDone <- runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: secondRequest})
	}()
	select {
	case got := <-generator.started:
		t.Fatalf("same-session turn started before first finished: %s", got)
	case <-time.After(40 * time.Millisecond):
	}
	close(gateA)
	if got := <-generator.started; got != firstSession {
		t.Fatalf("serialized second generator session = %s, want %s", got, firstSession)
	}
	close(gateB)
	if (<-firstDone).Status != TurnStatusDelivered || (<-secondDone).Status != TurnStatusDelivered {
		t.Fatal("serialized turns did not complete")
	}

	// A blocked turn in another session must not hold the first session's lock.
	gateBlocked := make(chan struct{})
	gateIndependent := make(chan struct{})
	blockedRequest, _ := runtimeInput(t, modulecore.NewSessionID(), 3, "blocked")
	independentRequest, _ := runtimeInput(t, modulecore.NewSessionID(), 3, "independent")
	blockedID := blockedRequest.Session.SessionID
	independentID := independentRequest.Session.SessionID
	generator = &sessionGateGenerator{
		started: make(chan modulecore.SessionID, 4), gates: map[modulecore.SessionID]<-chan struct{}{
			blockedID: gateBlocked, independentID: gateIndependent,
		},
	}
	runtime = mustRuntime(t, generator, &fakeJournal{}, &fakePublisher{}, []string{"shiro"})
	blockedDone, independentDone := make(chan TurnResult, 1), make(chan TurnResult, 1)
	go func() {
		blockedDone <- runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: blockedRequest})
	}()
	if got := <-generator.started; got != blockedID {
		t.Fatalf("blocked generator session = %s", got)
	}
	go func() {
		independentDone <- runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: independentRequest})
	}()
	if got := <-generator.started; got != independentID {
		t.Fatalf("independent session waited for another session: got %s", got)
	}
	close(gateIndependent)
	close(gateBlocked)
	if (<-blockedDone).Status != TurnStatusDelivered || (<-independentDone).Status != TurnStatusDelivered {
		t.Fatal("independent session turns did not complete")
	}
}

type runtimeFixture struct {
	runtime   *Runtime
	input     AcceptedTurnInput
	provider  *fakeConversationProvider
	journal   *fakeJournal
	publisher *fakePublisher
}

func newRuntimeFixture(t *testing.T, emission string) runtimeFixture {
	t.Helper()
	request, tail := runtimeInput(t, modulecore.NewSessionID(), 3, "current request")
	provider := &fakeConversationProvider{response: emission}
	journal, publisher := &fakeJournal{}, &fakePublisher{}
	runtime := mustRuntime(t, agent.NewMioEmissionGenerator(provider), journal, publisher, []string{"shiro"})
	runtime.readViews = NewReadViewService(&fakeTailReader{tail: tail})
	return runtimeFixture{runtime: runtime, input: AcceptedTurnInput{ReadViewRequest: request}, provider: provider, journal: journal, publisher: publisher}
}

func newReportRuntimeFixture(t *testing.T, emission string) runtimeFixture {
	t.Helper()
	request, tail, _ := reportReadViewFixture(t)
	provider := &fakeConversationProvider{response: emission}
	journal, publisher := &fakeJournal{}, &fakePublisher{}
	runtime := mustRuntime(t, agent.NewMioEmissionGenerator(provider), journal, publisher, []string{"shiro"})
	runtime.readViews = NewReadViewService(&fakeTailReader{tail: tail})
	return runtimeFixture{runtime: runtime, input: AcceptedTurnInput{ReadViewRequest: request}, provider: provider, journal: journal, publisher: publisher}
}

func runtimeInput(t *testing.T, sessionID modulecore.SessionID, admissionSeq modulecore.EventSeq, currentText string) (ReadViewRequest, CanonicalTail) {
	t.Helper()
	ownerID := "owner-1"
	turnID := modulecore.NewTurnID()
	inputEvent := userEvent(sessionID, turnID, admissionSeq, ownerID, currentText)
	request := ReadViewRequest{
		Principal: Principal{OwnerID: ownerID, ActorID: ownerID},
		Session: SessionState{
			SessionID: sessionID, OwnerID: ownerID, RuntimeVersion: ConversationRuntimeV2,
			CutoverSeq: 0, AdmissionSeq: admissionSeq, ProjectionWatermark: 0, GenerationEpoch: 1,
		},
		Accepted: AcceptedTurn{
			SessionID: sessionID, TurnID: turnID, ActorID: ownerID, TriggerKind: "user_message", ClientSubmissionID: "submission-" + string(turnID),
			InputMessageID: inputEvent.MessageID, AdmissionSeq: admissionSeq, GenerationEpoch: 1,
			State: "accepted", InputEvent: inputEvent,
		},
		Legacy: LegacyProjection{AppliedThrough: 0, Complete: true},
	}
	priorTurnID := modulecore.NewTurnID()
	priorInput := userEvent(sessionID, priorTurnID, 1, ownerID, "earlier request")
	priorReply := assistantEvent(sessionID, priorTurnID, 2, string(modulecore.NewMessageID()), "earlier answer", "mio")
	tail := CanonicalTail{
		ThroughSeq: admissionSeq, Complete: true,
		Messages: []CanonicalMessage{
			canonicalMessage(priorInput, ownerID, "agent", "mio", "user_message"),
			canonicalMessage(priorReply, "mio", "user", ownerID, "assistant_message"),
		},
	}
	return request, tail
}

func requestForSameSession(t *testing.T, previous ReadViewRequest, admissionSeq modulecore.EventSeq, currentText string) ReadViewRequest {
	t.Helper()
	request := previous
	request.Accepted.TurnID = modulecore.NewTurnID()
	request.Accepted.TriggerKind = "user_message"
	request.Accepted.AdmissionSeq = admissionSeq
	request.Accepted.ClientSubmissionID = "submission-" + string(request.Accepted.TurnID)
	request.Accepted.InputEvent = userEvent(request.Session.SessionID, request.Accepted.TurnID, admissionSeq, request.Accepted.ActorID, currentText)
	request.Accepted.InputMessageID = request.Accepted.InputEvent.MessageID
	request.Session.AdmissionSeq = admissionSeq
	return request
}

func mustRuntime(t *testing.T, generator EmissionGenerator, journal TurnJournal, publisher CommittedEventPublisher, allowed []string) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(RuntimeDependencies{
		ReadViews: NewReadViewService(&fakeTailReader{tail: CanonicalTail{ThroughSeq: 10, Complete: true}}),
		Composer:  identityPromptComposer{},
		Generator: generator,
		Journal:   journal,
		Publisher: publisher,
		Config:    RuntimeConfig{PolicyVersion: "policy-test-v1", AllowedAssignees: allowed},
	})
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}
	return runtime
}

type identityPromptComposer struct{}

func (identityPromptComposer) ComposeConversationPrompt(_ context.Context, _ conversation.ConversationScope, snapshot agent.PromptSnapshot) (agent.PromptSnapshot, error) {
	composed := snapshot.Clone()
	composed.Messages = append([]llm.Message{{Role: "system", Content: "Mio authored system context"}}, composed.Messages...)
	return composed, nil
}

type fakeConversationProvider struct {
	mu       sync.Mutex
	response string
	err      error
	entered  chan struct{}
	release  chan struct{}
	calls    int
	requests []llm.GenerateRequest
	scopes   []conversation.ConversationScope
}

func (provider *fakeConversationProvider) GenerateConversation(ctx context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	provider.mu.Lock()
	provider.calls++
	provider.requests = append(provider.requests, cloneGenerateRequest(request))
	provider.scopes = append(provider.scopes, scope)
	provider.mu.Unlock()
	if provider.entered != nil {
		provider.entered <- struct{}{}
	}
	if provider.release != nil {
		select {
		case <-provider.release:
		case <-ctx.Done():
			return llm.GenerateResponse{}, ctx.Err()
		}
	}
	return llm.GenerateResponse{
		Content: provider.response, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(),
	}, provider.err
}

func (*fakeConversationProvider) Name() string { return "test-provider" }

func (provider *fakeConversationProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func (provider *fakeConversationProvider) lastRequest() llm.GenerateRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return cloneGenerateRequest(provider.requests[len(provider.requests)-1])
}

func (provider *fakeConversationProvider) lastScope() conversation.ConversationScope {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.scopes[len(provider.scopes)-1]
}

func cloneGenerateRequest(request llm.GenerateRequest) llm.GenerateRequest {
	cloned := request
	cloned.Messages = append([]llm.Message(nil), request.Messages...)
	return cloned
}

type fakeJournal struct {
	mu                   sync.Mutex
	commits              []CommitBundle
	committed            CommittedBundle
	found                bool
	commitError          error
	commitAfterSaveError error
	loadError            error
	loadCalls            int
}

func (journal *fakeJournal) CommitTurnBundle(_ context.Context, bundle CommitBundle) (CommittedBundle, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.commits = append(journal.commits, cloneCommitBundle(bundle))
	if journal.commitError != nil {
		return CommittedBundle{}, journal.commitError
	}
	committed := CommittedBundle{OperationID: bundle.OperationID, SessionID: bundle.SessionID, TurnID: bundle.TurnID, ResultState: bundle.ResultState}
	committed.Events = append([]modulecore.EventEnvelope(nil), bundle.Events...)
	for index := range committed.Events {
		committed.Events[index].EventSeq = modulecore.EventSeq(20 + index)
	}
	journal.committed, journal.found = cloneCommittedBundle(committed), true
	if journal.commitAfterSaveError != nil {
		return CommittedBundle{}, journal.commitAfterSaveError
	}
	return cloneCommittedBundle(committed), nil
}

func (journal *fakeJournal) LoadCommittedOutcome(_ context.Context, turnID modulecore.TurnID) (CommittedBundle, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.loadCalls++
	if journal.loadError != nil {
		return CommittedBundle{}, false, journal.loadError
	}
	if !journal.found || journal.committed.TurnID != turnID {
		return CommittedBundle{}, false, nil
	}
	return cloneCommittedBundle(journal.committed), true, nil
}

func (journal *fakeJournal) wasCommitted(batch CommittedBundle) bool {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.found && journal.committed.OperationID == batch.OperationID
}

func (journal *fakeJournal) commitCount() int {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return len(journal.commits)
}

func (journal *fakeJournal) loadCount() int {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.loadCalls
}

func (journal *fakeJournal) lastCommit() CommitBundle {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return cloneCommitBundle(journal.commits[len(journal.commits)-1])
}

func cloneCommitBundle(bundle CommitBundle) CommitBundle {
	cloned := bundle
	cloned.Events = append([]modulecore.EventEnvelope(nil), bundle.Events...)
	cloned.MessageIndexes = append([]MessageIndex(nil), bundle.MessageIndexes...)
	cloned.Outbox = append([]WorkItem(nil), bundle.Outbox...)
	if bundle.Delegation != nil {
		delegation := *bundle.Delegation
		cloned.Delegation = &delegation
	}
	return cloned
}

type fakePublisher struct {
	mu            sync.Mutex
	batches       []CommittedBundle
	publishErr    error
	beforePublish func(CommittedBundle) error
	attempts      int
	seenEvents    map[modulecore.EventID]struct{}
}

func (publisher *fakePublisher) PublishCommitted(_ context.Context, batch CommittedBundle) error {
	if publisher.beforePublish != nil {
		if err := publisher.beforePublish(batch); err != nil {
			return err
		}
	}
	publisher.mu.Lock()
	publisher.attempts++
	err := publisher.publishErr
	if err == nil {
		if publisher.seenEvents == nil {
			publisher.seenEvents = make(map[modulecore.EventID]struct{})
		}
		newEvent := false
		for _, event := range batch.Events {
			if _, exists := publisher.seenEvents[event.EventID]; !exists {
				publisher.seenEvents[event.EventID] = struct{}{}
				newEvent = true
			}
		}
		if newEvent {
			publisher.batches = append(publisher.batches, cloneCommittedBundle(batch))
		}
	}
	publisher.mu.Unlock()
	return err
}

func (publisher *fakePublisher) publishCount() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return len(publisher.batches)
}

func (publisher *fakePublisher) publishAttempts() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return publisher.attempts
}

func (publisher *fakePublisher) uniqueEventCount() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return len(publisher.seenEvents)
}

type runtimeCanonicalEventStore struct {
	events []modulecore.EventEnvelope
}

func (store *runtimeCanonicalEventStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	_, err := store.AppendSequenced(ctx, event)
	return err
}

func (store *runtimeCanonicalEventStore) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if event.EventSeq == 0 {
		event.EventSeq = modulecore.EventSeq(len(store.events) + 1)
	}
	store.events = append(store.events, event)
	return event, nil
}

func (store *runtimeCanonicalEventStore) GetByID(_ context.Context, id modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	for _, event := range store.events {
		if event.EventID == id {
			return event, true, nil
		}
	}
	return modulecore.EventEnvelope{}, false, nil
}

func (store *runtimeCanonicalEventStore) ListByComponent(_ context.Context, component string, _ int) ([]modulecore.EventEnvelope, error) {
	var events []modulecore.EventEnvelope
	for _, event := range store.events {
		if event.ComponentID == component {
			events = append(events, event)
		}
	}
	return events, nil
}

type sessionGateGenerator struct {
	started chan modulecore.SessionID
	gates   map[modulecore.SessionID]<-chan struct{}
}

func (generator *sessionGateGenerator) GenerateEmission(_ context.Context, scope conversation.ConversationScope, _ agent.PromptSnapshot, _ conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	generator.started <- scope.SessionID
	if gate := generator.gates[scope.SessionID]; gate != nil {
		<-gate
	}
	emission, _ := conversation.ParseAgentEmission([]byte(replyEmission("gated answer")), scope, conversation.EmissionAllowlist{})
	return emission, agent.InvocationReceipt{Status: "completed"}, nil
}

func replyEmission(text string) string {
	return fmt.Sprintf(`{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":%q}}`, text)
}

func delegationEmission(instruction string) string {
	return fmt.Sprintf(`{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"user","text":"Shiroにお願いしたよ。"},"delegation":{"to":"shiro","goal":"確認を依頼する","instruction":%q,"constraints":[],"expected_output":"根拠付きの報告","source_message_ids":[]}}`, instruction)
}

func messageCount(messages []llm.Message, role, text string) int {
	count := 0
	for _, message := range messages {
		if message.Role == role && message.Content == text {
			count++
		}
	}
	return count
}

func publicEvents(batch CommittedBundle) []modulecore.EventEnvelope {
	var events []modulecore.EventEnvelope
	for _, event := range batch.Events {
		if event.ComponentID == "orchestrator" {
			events = append(events, event)
		}
	}
	return events
}

func assertPublicEnvelope(t *testing.T, event modulecore.EventEnvelope, submissionID, kind, availability, body string) {
	t.Helper()
	if event.EventType != "agent.response" || event.ComponentID != "orchestrator" || event.ActorKind != "" || event.ActorID != "" {
		t.Fatalf("public event identity = %+v", event)
	}
	if event.Payload["kind"] != kind || event.Payload["availability"] != availability || event.Payload["client_submission_id"] != submissionID {
		t.Fatalf("public event correlation/meaning payload = %#v", event.Payload)
	}
	if event.Payload["text"] != body || event.Payload["content"] != body || event.Payload["from"] != "mio" || event.Payload["source_actor_id"] != "mio" || event.Payload["visibility"] != "public" {
		t.Fatalf("public event body/source = %#v", event.Payload)
	}
	for _, forbidden := range []string{"request_id", "response_id", "receipt_id", "raw_content", "prompt", "provider", "model"} {
		if _, exists := event.Payload[forbidden]; exists {
			t.Fatalf("public event contains forbidden key %q: %#v", forbidden, event.Payload)
		}
	}
}

func outboxContains(items []WorkItem, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return encoded
}
