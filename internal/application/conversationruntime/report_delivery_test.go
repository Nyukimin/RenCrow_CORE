package conversationruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReportDeliveryReplaysAcceptedReportAfterTaskMarkFailure(t *testing.T) {
	fixture := newReportDeliveryFixture(t)
	fixture.tasks.markErr = errors.New("Task mark unavailable")

	first, err := fixture.service.DeliverOne(context.Background())
	if err == nil {
		t.Fatal("DeliverOne() error = nil, want Task mark failure")
	}
	if first.Delivered || first.AcceptedTurn.TurnID != "" {
		t.Fatalf("first delivery = %+v, want no successful delivery result", first)
	}
	if fixture.tasks.pendingCount() != 1 {
		t.Fatal("report was dropped after Task mark failure")
	}

	fixture.tasks.markErr = nil
	second, err := fixture.service.DeliverOne(context.Background())
	if err != nil {
		t.Fatalf("DeliverOne() retry: %v", err)
	}
	if !second.Delivered || second.ReportID != fixture.report.ReportID {
		t.Fatalf("retry result = %+v, want delivered report %s", second, fixture.report.ReportID)
	}
	if len(fixture.journal.inputs) != 2 {
		t.Fatalf("AcceptReport calls = %d, want two attempts", len(fixture.journal.inputs))
	}
	firstInput, secondInput := fixture.journal.inputs[0], fixture.journal.inputs[1]
	if firstInput.Report.ReportID != secondInput.Report.ReportID || !reflect.DeepEqual(firstInput.Report, secondInput.Report) || firstInput.TrustedSender != secondInput.TrustedSender {
		t.Fatalf("retry changed saved report identity/body/sender: first=%+v second=%+v", firstInput, secondInput)
	}
	if firstInput.EventID == secondInput.EventID || firstInput.MessageID == secondInput.MessageID || firstInput.TurnID == secondInput.TurnID || firstInput.TraceID == secondInput.TraceID {
		t.Fatal("retry reused runtime envelope IDs instead of asking EventStore's report receipt for the accepted turn")
	}
	for name, validate := range map[string]func() error{
		"event_id":   func() error { return secondInput.EventID.Validate() },
		"message_id": func() error { return secondInput.MessageID.Validate() },
		"turn_id":    func() error { return secondInput.TurnID.Validate() },
		"trace_id":   func() error { return secondInput.TraceID.Validate() },
	} {
		if err := validate(); err != nil {
			t.Errorf("runtime-issued %s is invalid: %v", name, err)
		}
	}
	if fixture.journal.receiptCount() != 1 || !reflect.DeepEqual(second.AcceptedTurn, fixture.journal.firstAccepted) {
		t.Fatalf("accepted report receipt count/turn = %d/%+v, want one durable turn %+v", fixture.journal.receiptCount(), second.AcceptedTurn, fixture.journal.firstAccepted)
	}
	if got, want := fixture.calls, []string{"list", "verify", "accept", "mark", "list", "verify", "accept", "mark"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery call order = %v, want %v", got, want)
	}

	third, err := fixture.service.DeliverOne(context.Background())
	if err != nil || third.Delivered || len(fixture.journal.inputs) != 2 {
		t.Fatalf("DeliverOne() after successful mark = %+v, %v; want no duplicate delivery", third, err)
	}
}

func TestReportDeliveryRetryBindsOneTimingTurnToPersistedReceipt(t *testing.T) {
	fixture := newReportDeliveryFixture(t)
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{
		BatchCapacity: 1, StampsPerTurn: 16, MemoryLimitBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var boundIDs [][2]string
	var created []bool
	var boundTurn turntiming.TurnRecorder
	fixture.service, err = NewReportDeliveryService(ReportDeliveryDependencies{
		Tasks: fixture.tasks, Journal: fixture.journal, Now: func() time.Time { return time.Unix(1, 0) },
		BindAcceptedTurnContext: func(ctx context.Context, accepted AcceptedTurn) context.Context {
			turn, didCreate, bindErr := batch.FindOrBeginTurn(string(accepted.TurnID), string(accepted.InputEvent.TraceID))
			if bindErr != nil {
				t.Errorf("bind accepted report timing: %v", bindErr)
				return ctx
			}
			boundTurn = turn
			created = append(created, didCreate)
			boundIDs = append(boundIDs, [2]string{string(accepted.TurnID), string(accepted.InputEvent.TraceID)})
			if didCreate {
				turn.Mark(turntiming.PointV2Accepted, turntiming.ComponentV2ConversationRuntime, 1, 0)
			}
			return turntiming.WithBatchTurnRecorder(ctx, turn)
		},
	})
	if err != nil {
		t.Fatalf("NewReportDeliveryService(): %v", err)
	}

	fixture.tasks.markErr = errors.New("Task mark unavailable")
	if _, err := fixture.service.DeliverOne(context.Background()); err == nil {
		t.Fatal("first DeliverOne() error = nil, want Task mark failure after canonical acceptance")
	}
	fixture.tasks.markErr = nil
	retried, err := fixture.service.DeliverOne(context.Background())
	if err != nil || !retried.Delivered {
		t.Fatalf("retry DeliverOne() = %+v, %v, want delivered accepted receipt", retried, err)
	}
	if len(created) != 2 || !created[0] || created[1] {
		t.Fatalf("timing slot creation attempts = %v, want [true false] for the same receipt", created)
	}
	if len(boundIDs) != 2 || boundIDs[0] != boundIDs[1] || boundIDs[0][0] != string(retried.AcceptedTurn.TurnID) || boundIDs[0][1] != string(retried.AcceptedTurn.InputEvent.TraceID) {
		t.Fatalf("bound canonical IDs across retry = %v, want the persisted receipt IDs (%s,%s)", boundIDs, retried.AcceptedTurn.TurnID, retried.AcceptedTurn.InputEvent.TraceID)
	}
	contextTurn, ok := turntiming.BatchTurnFromContext(retried.ProcessContext)
	if !ok {
		t.Fatal("successful delivery result did not carry its timing turn into processing context")
	}
	contextIndex, contextValid := contextTurn.Index()
	boundIndex, boundValid := boundTurn.Index()
	if !contextValid || !boundValid || contextIndex != boundIndex {
		t.Fatalf("returned process-context timing slot=(%d,%t), bound slot=(%d,%t)", contextIndex, contextValid, boundIndex, boundValid)
	}
	if !contextTurn.Mark(turntiming.PointV2AsyncStart, turntiming.ComponentV2ConversationRuntime, 1, 0) || !contextTurn.FinishForeground() {
		t.Fatal("retried process context could not record and finish the accepted turn")
	}
	if err := batch.Freeze(); err != nil {
		t.Fatalf("freeze timing batch: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatalf("snapshot timing batch: %v", err)
	}
	if snapshot.TurnCount() != 1 {
		t.Fatalf("timing turn count=%d, want one after duplicate report receipt", snapshot.TurnCount())
	}
	timed, _ := snapshot.Turn(0)
	turnID, traceID, hasIdentity := timed.Identity()
	if !hasIdentity || turnID != boundIDs[0][0] || traceID != boundIDs[0][1] {
		t.Fatalf("timing identity=(%q,%q,%t), want the canonical report receipt IDs", turnID, traceID, hasIdentity)
	}
	acceptedPoints := 0
	for index := uint32(0); index < timed.StampCount(); index++ {
		stamp, _ := timed.Stamp(index)
		if stamp.Point == turntiming.PointV2Accepted {
			acceptedPoints++
		}
	}
	if acceptedPoints != 1 {
		t.Fatalf("report acceptance timing points=%d, want one despite receipt retry", acceptedPoints)
	}
}

func TestReportDeliveryRejectsStoredReportMismatch(t *testing.T) {
	for _, test := range []struct {
		name              string
		mutate            func(*domaintask.DelegationReport)
		includeNonPending bool
	}{
		{name: "trusted sender", mutate: func(report *domaintask.DelegationReport) { report.Envelope.Sender = "kuro" }},
		{name: "Task status", mutate: func(report *domaintask.DelegationReport) { report.TaskStatus = domaintask.StatusFailed }},
		{name: "report identity", mutate: func(report *domaintask.DelegationReport) { report.Envelope.ReportID = modulecore.NewResponseID() }},
		{name: "not pending", mutate: func(report *domaintask.DelegationReport) {
			report.State = domaintask.DelegationReportDelivered
			report.DeliveredAt = 10
		}, includeNonPending: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReportDeliveryFixture(t)
			test.mutate(&fixture.tasks.reports[0])
			fixture.tasks.includeNonPending = test.includeNonPending
			if _, err := fixture.service.DeliverOne(context.Background()); err == nil {
				t.Fatal("DeliverOne() error = nil, want invalid stored report")
			}
			if len(fixture.journal.inputs) != 0 || fixture.tasks.markCalls != 0 {
				t.Fatalf("invalid report crossed delivery boundary: accepts=%d marks=%d", len(fixture.journal.inputs), fixture.tasks.markCalls)
			}
		})
	}
}

func TestReportDeliveryRequiresTaskCompletionBeforeAcceptance(t *testing.T) {
	fixture := newReportDeliveryFixture(t)
	fixture.tasks.verifyErr = errors.New("Task completion changed")
	if _, err := fixture.service.DeliverOne(context.Background()); err == nil || !strings.Contains(err.Error(), "Task completion") {
		t.Fatalf("DeliverOne() error = %v, want Task completion verification failure", err)
	}
	if fixture.tasks.verifyCalls != 1 || len(fixture.journal.inputs) != 0 || fixture.tasks.markCalls != 0 || fixture.tasks.pendingCount() != 1 {
		t.Fatalf("completion failure state: verifies=%d accepts=%d marks=%d pending=%d", fixture.tasks.verifyCalls, len(fixture.journal.inputs), fixture.tasks.markCalls, fixture.tasks.pendingCount())
	}
}

func TestReportDeliveryAcceptanceFailureKeepsTaskReportPending(t *testing.T) {
	fixture := newReportDeliveryFixture(t)
	fixture.journal.acceptErr = errors.New("EventStore unavailable")
	if _, err := fixture.service.DeliverOne(context.Background()); err == nil || !strings.Contains(err.Error(), "EventStore") {
		t.Fatalf("DeliverOne() error = %v, want EventStore acceptance failure", err)
	}
	if fixture.tasks.markCalls != 0 || fixture.tasks.pendingCount() != 1 || fixture.journal.receiptCount() != 0 {
		t.Fatalf("acceptance failure dropped report: marks=%d pending=%d receipts=%d", fixture.tasks.markCalls, fixture.tasks.pendingCount(), fixture.journal.receiptCount())
	}
}

type reportDeliveryFixture struct {
	service *ReportDeliveryService
	tasks   *reportDeliveryTaskOwnerFake
	journal *reportDeliveryJournalFake
	calls   []string
	report  domaintask.DelegationReport
}

func newReportDeliveryFixture(t *testing.T) *reportDeliveryFixture {
	t.Helper()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	report := domaintask.DelegationReport{
		ReportID: modulecore.NewResponseID(), TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(),
		TaskStatus: domaintask.StatusSucceeded, TrustedSender: "shiro",
		Envelope: conversation.ReportEnvelope{
			ReportID: modulecore.NewResponseID(), ReportRevision: 1,
			DelegationID: modulecore.NewRequestID(), DelegationRevision: 3,
			WorkTaskID: modulecore.NewTaskID(), WorkRunID: modulecore.NewRunID(),
			Status: conversation.ReportStatusSucceeded, Summary: "Task complete", Result: "The requested work is complete.",
			EvidenceRefs: []modulecore.EvidenceID{}, ArtifactRefs: []modulecore.ArtifactID{}, Limitations: []string{},
		},
		OperationID: "delegation-report:test", RequestHash: strings.Repeat("a", 64), WriterGeneration: 1,
		State: domaintask.DelegationReportPending, CreatedAt: now.UnixNano(),
	}
	// Keep the Task-owned references and envelope references identical.
	report.Envelope.ReportID, report.Envelope.WorkTaskID, report.Envelope.WorkRunID = report.ReportID, report.TaskID, report.RunID
	if err := report.Validate(); err != nil {
		t.Fatalf("fixture report Validate(): %v", err)
	}
	fixture := &reportDeliveryFixture{report: report}
	fixture.tasks = &reportDeliveryTaskOwnerFake{reports: []domaintask.DelegationReport{report}, calls: &fixture.calls}
	fixture.journal = &reportDeliveryJournalFake{calls: &fixture.calls, receipts: make(map[modulecore.ResponseID]reportDeliveryReceipt)}
	service, err := NewReportDeliveryService(ReportDeliveryDependencies{
		Tasks: fixture.tasks, Journal: fixture.journal, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewReportDeliveryService(): %v", err)
	}
	fixture.service = service
	return fixture
}

type reportDeliveryTaskOwnerFake struct {
	reports           []domaintask.DelegationReport
	calls             *[]string
	includeNonPending bool
	verifyErr         error
	markErr           error
	verifyCalls       int
	markCalls         int
}

func (f *reportDeliveryTaskOwnerFake) ListPendingReports(context.Context) ([]domaintask.DelegationReport, error) {
	*f.calls = append(*f.calls, "list")
	var pending []domaintask.DelegationReport
	for _, report := range f.reports {
		if f.includeNonPending || report.State == domaintask.DelegationReportPending {
			pending = append(pending, report)
		}
	}
	return pending, nil
}

func (f *reportDeliveryTaskOwnerFake) VerifyRunCompletion(_ context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status) error {
	*f.calls = append(*f.calls, "verify")
	f.verifyCalls++
	if f.verifyErr != nil {
		return f.verifyErr
	}
	if len(f.reports) == 0 {
		return errors.New("no stored Task report")
	}
	report := f.reports[0]
	if taskID != report.TaskID || runID != report.RunID || actorID != report.TrustedSender || status != report.TaskStatus {
		return errors.New("Task completion verification received mismatched owner data")
	}
	return nil
}

func (f *reportDeliveryTaskOwnerFake) MarkReportDelivered(_ context.Context, reportID modulecore.ResponseID) error {
	*f.calls = append(*f.calls, "mark")
	f.markCalls++
	if f.markErr != nil {
		return f.markErr
	}
	for index := range f.reports {
		if f.reports[index].ReportID == reportID {
			f.reports[index].State = domaintask.DelegationReportDelivered
			f.reports[index].DeliveredAt = 1
			return nil
		}
	}
	return errors.New("report to mark was not found")
}

func (f *reportDeliveryTaskOwnerFake) pendingCount() int {
	count := 0
	for _, report := range f.reports {
		if report.State == domaintask.DelegationReportPending {
			count++
		}
	}
	return count
}

type reportDeliveryReceipt struct {
	report        conversation.ReportEnvelope
	trustedSender string
	turn          AcceptedTurn
}

type reportDeliveryJournalFake struct {
	calls         *[]string
	inputs        []ReportAcceptanceInput
	receipts      map[modulecore.ResponseID]reportDeliveryReceipt
	acceptErr     error
	firstAccepted AcceptedTurn
}

func (f *reportDeliveryJournalFake) AcceptReport(_ context.Context, input ReportAcceptanceInput) (AcceptedTurn, error) {
	*f.calls = append(*f.calls, "accept")
	f.inputs = append(f.inputs, input)
	if f.acceptErr != nil {
		return AcceptedTurn{}, f.acceptErr
	}
	if existing, ok := f.receipts[input.Report.ReportID]; ok {
		if !reflect.DeepEqual(existing.report, input.Report) || existing.trustedSender != input.TrustedSender {
			return AcceptedTurn{}, errors.New("report receipt content conflict")
		}
		return existing.turn, nil
	}
	sessionID := modulecore.NewSessionID()
	event := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       input.EventID, EventSeq: 5, TraceID: input.TraceID,
		EventType: "conversation.agent_report", ComponentID: "conversation_runtime", OccurredAt: input.OccurredAt,
		SessionID: sessionID, TurnID: input.TurnID, MessageID: input.MessageID,
		RequestID: input.Report.DelegationID, ResponseID: input.Report.ReportID,
		TaskID: input.Report.WorkTaskID, RunID: input.Report.WorkRunID,
		ActorKind: "agent", ActorID: input.TrustedSender,
	}
	accepted := AcceptedTurn{
		ClientSubmissionID: "report:" + string(input.Report.ReportID), SessionID: sessionID,
		TurnID: input.TurnID, ActorID: input.TrustedSender, TriggerKind: "report_reply",
		InputMessageID: input.MessageID, AdmissionSeq: event.EventSeq, GenerationEpoch: 1, State: "accepted", InputEvent: event,
	}
	f.receipts[input.Report.ReportID] = reportDeliveryReceipt{report: input.Report, trustedSender: input.TrustedSender, turn: accepted}
	f.firstAccepted = accepted
	return accepted, nil
}

func (f *reportDeliveryJournalFake) receiptCount() int { return len(f.receipts) }
