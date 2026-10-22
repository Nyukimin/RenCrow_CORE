package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAcceptReportPersistsPrivateReportAndRecoverableMioTurnAtomically(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)

	accepted, err := store.AcceptReport(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReport(): %v", err)
	}
	if accepted.State != "accepted" || accepted.TriggerKind != "report_reply" || accepted.ActorID != fixture.assignee {
		t.Fatalf("accepted report turn = %#v, want accepted report_reply turn for %q", accepted, fixture.assignee)
	}
	if accepted.OperationID != reportAcceptanceOperationID(fixture.report.ReportID) || accepted.ClientSubmissionID != reportSubmissionID(fixture.report.ReportID) {
		t.Fatalf("report acceptance identity = operation:%q submission:%q", accepted.OperationID, accepted.ClientSubmissionID)
	}

	event := accepted.InputEvent
	if event.EventType != "conversation.agent_report" || event.ComponentID != "conversation_runtime" ||
		event.ActorKind != "agent" || event.ActorID != fixture.assignee ||
		event.SessionID != fixture.sessionID || event.TurnID != accepted.TurnID || event.TraceID == fixture.traceID {
		t.Fatalf("accepted report event identity = %#v", event)
	}
	if event.EventSeq <= 0 || event.MessageID != accepted.InputMessageID || event.RequestID != fixture.delegationID ||
		event.ResponseID != fixture.report.ReportID || event.TaskID != fixture.taskID || event.RunID != fixture.report.WorkRunID {
		t.Fatalf("accepted report event references = %#v", event)
	}
	if event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 {
		t.Fatalf("report event has cross-event causal references: %#v", event)
	}
	for key, want := range map[string]string{
		"kind": "agent_report", "visibility": "private", "from": fixture.assignee,
		"source_actor_id": fixture.assignee, "trusted_sender": fixture.assignee, "to": "mio",
		"origin_trace_id": string(fixture.traceID), "origin_turn_id": string(fixture.turnID),
		"origin_message_id": string(fixture.originMessageID), "delegation_id": string(fixture.delegationID),
		"report_id": string(fixture.report.ReportID), "work_task_id": string(fixture.taskID),
	} {
		if got := payloadString(event.Payload, key); got != want {
			t.Errorf("report event payload %s = %q, want %q", key, got, want)
		}
	}
	if got := payloadInt64(event.Payload, "report_revision"); got != int64(fixture.report.ReportRevision) {
		t.Errorf("report_revision payload = %d, want %d", got, fixture.report.ReportRevision)
	}
	reportPayload, ok := event.Payload["report"].(map[string]any)
	if !ok {
		t.Fatalf("report payload type = %T, want map[string]any", event.Payload["report"])
	}
	if _, present := reportPayload["sender"]; present {
		t.Fatal("report body contains sender; sender must come from trusted transport metadata")
	}
	if got := reportPayload["summary"]; got != fixture.report.Summary {
		t.Fatalf("stored report summary = %v, want %q", got, fixture.report.Summary)
	}

	var fromActor, toKind, toActor, kind, visibility string
	var replyTo string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT from_actor, to_kind, to_actor, kind, visibility, COALESCE(reply_to_message_id, '')
		FROM live_message_index WHERE message_id = ?`, string(event.MessageID),
	).Scan(&fromActor, &toKind, &toActor, &kind, &visibility, &replyTo); err != nil {
		t.Fatalf("read report message index: %v", err)
	}
	if fromActor != fixture.assignee || toKind != "agent" || toActor != "mio" || kind != "agent_report" || visibility != "private" || replyTo != "" {
		t.Fatalf("report index = from:%q to:%s/%s kind:%q visibility:%q reply:%q", fromActor, toKind, toActor, kind, visibility, replyTo)
	}

	var delegationState string
	var reportRevision int64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT state, report_revision FROM live_delegation WHERE delegation_id = ? AND revision = ?`,
		string(fixture.delegationID), int64(fixture.delegationRevision),
	).Scan(&delegationState, &reportRevision); err != nil {
		t.Fatalf("read accepted delegation report state: %v", err)
	}
	if delegationState != string(fixture.report.Status) || reportRevision != int64(fixture.report.ReportRevision) {
		t.Fatalf("delegation report state/revision = %q/%d, want %q/%d", delegationState, reportRevision, fixture.report.Status, fixture.report.ReportRevision)
	}

	var eventCount, turnCount, receiptCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(event.EventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE turn_id = ? AND state = 'accepted'`, []any{string(accepted.TurnID)}, &turnCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = 'accept_report'`, []any{accepted.OperationID}, &receiptCount},
	} {
		if err := store.db.QueryRowContext(context.Background(), check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count accepted report writes: %v", err)
		}
	}
	if eventCount != 1 || turnCount != 1 || receiptCount != 1 {
		t.Fatalf("report write counts = event:%d turn:%d receipt:%d, want one each", eventCount, turnCount, receiptCount)
	}

	pending, err := store.ReadPendingAcceptedTurns(context.Background(), fixture.sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(report recovery): %v", err)
	}
	if len(pending) != 1 || !reflect.DeepEqual(pending[0], accepted) {
		t.Fatalf("pending accepted turns = %#v, want the accepted report turn %#v", pending, accepted)
	}
}

func TestAcceptReportIsIdempotentByReportIDAndConflictsOnChangedContent(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)

	first, err := store.AcceptReport(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReport(first): %v", err)
	}
	retry := fixture.input
	retry.EventID = modulecore.NewEventID()
	retry.MessageID = modulecore.NewMessageID()
	retry.TurnID = modulecore.NewTurnID()
	retry.TraceID = modulecore.NewTraceID()
	retry.OccurredAt = fixture.input.OccurredAt.Add(time.Minute)
	retry.Report.Sender = "forged-body-sender"
	replayed, err := store.AcceptReport(context.Background(), retry)
	if err != nil {
		t.Fatalf("AcceptReport(retry): %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replayed report turn = %#v, want first durable turn %#v", replayed, first)
	}

	changed := fixture.input
	changed.Report.Result += " with changed content"
	if _, err := store.AcceptReport(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("AcceptReport(same ID, changed content) error = %v, want ErrOperationConflict", err)
	}

	newReportID := fixture.input
	newReportID.Report.ReportID = modulecore.NewResponseID()
	if _, err := store.AcceptReport(context.Background(), newReportID); !errors.Is(err, ErrReportConflict) {
		t.Fatalf("AcceptReport(new report ID for closed delegation) error = %v, want ErrReportConflict", err)
	}
	assertReportAcceptedOnce(t, store, first)
}

func TestRecoverableReportTurnRequiresExactTrustedActor(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)
	accepted, err := store.AcceptReport(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReport(): %v", err)
	}
	message := MessageIndex{
		MessageID: accepted.InputMessageID, EventID: accepted.InputEvent.EventID,
		SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: fixture.assignee, ToKind: "agent", ToActor: "mio",
		Kind: "agent_report", Visibility: "private",
	}
	mismatched := accepted.InputEvent
	mismatched.ActorID = "kuro"
	if err := validateRecoverableTurnActor(accepted, accepted.OperationID, accepted.ClientSubmissionID, mismatched, message); err == nil {
		t.Fatal("recovery accepted a report whose envelope actor differs from the trusted turn actor")
	}
}

func TestAcceptReportRejectsWrongActorTaskAndStaleOrCanceledDelegation(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   error
		mutate func(*reportFixture)
	}{
		{name: "wrong trusted actor", want: ErrReportAssigneeMismatch, mutate: func(f *reportFixture) { f.input.TrustedSender = "kuro" }},
		{name: "wrong task", want: ErrReportTaskMismatch, mutate: func(f *reportFixture) { f.input.Report.WorkTaskID = modulecore.NewTaskID() }},
		{name: "stale delegation revision", want: ErrReportConflict, mutate: func(f *reportFixture) { f.input.Report.DelegationRevision++ }},
		{name: "stale report revision", want: ErrReportConflict, mutate: func(f *reportFixture) { f.input.Report.ReportRevision++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := newReportFixture(t, store)
			test.mutate(&fixture)
			if _, err := store.AcceptReport(context.Background(), fixture.input); !errors.Is(err, test.want) {
				t.Fatalf("AcceptReport() error = %v, want %v", err, test.want)
			}
			assertReportNotAccepted(t, store, fixture)
		})
	}

	t.Run("canceled delegation", func(t *testing.T) {
		store := newTestStore(t)
		fixture := newReportFixture(t, store)
		if _, err := store.db.ExecContext(context.Background(), `UPDATE live_delegation SET state = 'canceled' WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision)); err != nil {
			t.Fatalf("cancel delegation: %v", err)
		}
		if _, err := store.AcceptReport(context.Background(), fixture.input); !errors.Is(err, ErrReportConflict) {
			t.Fatalf("AcceptReport(canceled delegation) error = %v, want ErrReportConflict", err)
		}
		assertReportNotAccepted(t, store, fixture)
	})
}

func TestAcceptReportRollsBackEventTurnReceiptAndRevisionOnIndexFailure(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)
	if err := store.db.QueryRowContext(context.Background(), `SELECT input_message_id FROM live_conversation_turn WHERE turn_id = ?`, string(fixture.turnID)).Scan(&fixture.input.MessageID); err != nil {
		t.Fatalf("read existing origin message id: %v", err)
	}

	if _, err := store.AcceptReport(context.Background(), fixture.input); err == nil {
		t.Fatal("AcceptReport(duplicate message index) error = nil, want uniqueness failure")
	}
	assertReportNotAccepted(t, store, fixture)
}

func TestAcceptReportKeepsReportUnacceptedWhenExecuteWorkCompletionFails(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)
	if _, err := store.db.ExecContext(context.Background(), `
		CREATE TRIGGER reject_execute_work_completion
		BEFORE UPDATE OF state ON live_work_outbox
		WHEN OLD.kind='execute_delegated_task' AND NEW.state='completed'
		BEGIN SELECT RAISE(ABORT, 'injected execute-work completion failure'); END`); err != nil {
		t.Fatalf("install execute-work failure trigger: %v", err)
	}

	if _, err := store.AcceptReport(context.Background(), fixture.input); err == nil {
		t.Fatal("AcceptReport() error = nil, want execute-work completion failure")
	}
	assertReportNotAccepted(t, store, fixture)
	var workState string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT state FROM live_work_outbox WHERE dedupe_key=?`,
		delegationExecuteDedupeKey(fixture.delegationID, fixture.delegationRevision),
	).Scan(&workState); err != nil {
		t.Fatalf("read execute work after failed report acceptance: %v", err)
	}
	if workState != "pending" {
		t.Fatalf("execute work after failed report acceptance = %q, want pending", workState)
	}
}

func TestCommitReportReplyRejectsCanceledOrSupersededDelegation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, reportFixture)
	}{
		{
			name: "canceled",
			mutate: func(t *testing.T, store *SQLiteStore, fixture reportFixture) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `UPDATE live_delegation SET state = 'canceled' WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision)); err != nil {
					t.Fatalf("cancel report delegation: %v", err)
				}
			},
		},
		{
			name: "superseded",
			mutate: func(t *testing.T, store *SQLiteStore, fixture reportFixture) {
				t.Helper()
				_, err := store.db.ExecContext(context.Background(), `
					INSERT INTO live_delegation
						(delegation_id, revision, origin_turn_id, assignee, instruction_message_id, context_event_id, state, work_task_id, report_revision)
					SELECT delegation_id, revision + 1, origin_turn_id, assignee, instruction_message_id, context_event_id, 'dispatch_accepted', NULL, 0
					FROM live_delegation WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision))
				if err != nil {
					t.Fatalf("supersede report delegation: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := newReportFixture(t, store)
			accepted, err := store.AcceptReport(context.Background(), fixture.input)
			if err != nil {
				t.Fatalf("AcceptReport(): %v", err)
			}
			commit := reportReplyCommitInput(accepted)
			test.mutate(t, store, fixture)
			if _, err := store.CommitTurnBundle(context.Background(), commit); !errors.Is(err, ErrReportConflict) {
				t.Fatalf("CommitTurnBundle(stale report reply) error = %v, want ErrReportConflict", err)
			}
			var eventCount, receiptCount int
			if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, string(commit.Events[0].EventID)).Scan(&eventCount); err != nil {
				t.Fatalf("count uncommitted report reply event: %v", err)
			}
			if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, commit.OperationID).Scan(&receiptCount); err != nil {
				t.Fatalf("count uncommitted report reply receipt: %v", err)
			}
			var turnState string
			if err := store.db.QueryRowContext(context.Background(), `SELECT state FROM live_conversation_turn WHERE turn_id = ?`, string(accepted.TurnID)).Scan(&turnState); err != nil {
				t.Fatalf("read report reply state after stale commit: %v", err)
			}
			if eventCount != 0 || receiptCount != 0 || turnState != "accepted" {
				t.Fatalf("stale report reply wrote event:%d receipt:%d turn_state:%q", eventCount, receiptCount, turnState)
			}
		})
	}
}

func TestCommitReportReplyQuarantinesWhenCurrentReportRevisionAdvances(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)
	accepted, err := store.AcceptReport(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("AcceptReport(): %v", err)
	}
	commit := reportReplyCommitInput(accepted)

	// Model the owner reopening report intake, then accepting a newer durable
	// report while Mio is generating the earlier reply.
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE live_delegation SET state = 'accepted'
		WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision)); err != nil {
		t.Fatalf("reopen report intake: %v", err)
	}
	latestInput := fixture.input
	latestInput.Report.ReportID = modulecore.NewResponseID()
	latestInput.Report.ReportRevision = fixture.report.ReportRevision + 1
	latestInput.Report.WorkRunID = modulecore.NewRunID()
	latestInput.Report.Status = conversation.ReportStatusPartial
	latestInput.Report.Summary = "The newer report contains partial results."
	latestInput.Report.Result = "The newer report supersedes the earlier result."
	latestInput.EventID = modulecore.NewEventID()
	latestInput.MessageID = modulecore.NewMessageID()
	latestInput.TurnID = modulecore.NewTurnID()
	latestInput.TraceID = modulecore.NewTraceID()
	latestInput.OccurredAt = fixture.input.OccurredAt.Add(time.Second)
	latestAccepted, err := store.AcceptReport(context.Background(), latestInput)
	if err != nil {
		t.Fatalf("AcceptReport(newer revision): %v", err)
	}

	if _, err := store.CommitTurnBundle(context.Background(), commit); !errors.Is(err, ErrReportConflict) || !errors.Is(err, ErrStaleReportRevision) {
		t.Fatalf("CommitTurnBundle(stale report reply) error = %v, want ErrReportConflict and ErrStaleReportRevision", err)
	}

	var turnState string
	if err := store.db.QueryRowContext(context.Background(), `SELECT state FROM live_conversation_turn WHERE turn_id = ?`, string(accepted.TurnID)).Scan(&turnState); err != nil {
		t.Fatalf("read stale report turn state: %v", err)
	}
	if turnState != "quarantined" {
		t.Fatalf("stale report turn state = %q, want quarantined", turnState)
	}

	var oldReportEventCount, latestReportEventCount, replyEventCount, commitReceiptCount, quarantineReceiptCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(accepted.InputEvent.EventID)}, &oldReportEventCount},
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(latestAccepted.InputEvent.EventID)}, &latestReportEventCount},
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(commit.Events[0].EventID)}, &replyEventCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = 'commit_turn_bundle'`, []any{commit.OperationID}, &commitReceiptCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = 'quarantine_stale_report'`, []any{"quarantine-stale-report:" + string(accepted.TurnID)}, &quarantineReceiptCount},
	} {
		if err := store.db.QueryRowContext(context.Background(), check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count stale report writes: %v", err)
		}
	}
	if oldReportEventCount != 1 || latestReportEventCount != 1 || replyEventCount != 0 || commitReceiptCount != 0 || quarantineReceiptCount != 1 {
		t.Fatalf("stale report writes = old_report:%d latest_report:%d reply:%d commit_receipt:%d quarantine_receipt:%d", oldReportEventCount, latestReportEventCount, replyEventCount, commitReceiptCount, quarantineReceiptCount)
	}

	var latestState string
	var latestRevision int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT state, report_revision FROM live_delegation WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision)).Scan(&latestState, &latestRevision); err != nil {
		t.Fatalf("read latest report state: %v", err)
	}
	if latestState != "partial" || latestRevision != int64(fixture.report.ReportRevision+1) {
		t.Fatalf("latest report state/revision = %q/%d, want partial/%d", latestState, latestRevision, fixture.report.ReportRevision+1)
	}

	var receipt struct {
		TurnID                 modulecore.TurnID `json:"turn_id"`
		State                  string            `json:"state"`
		Reason                 string            `json:"reason"`
		AcceptedReportRevision uint64            `json:"accepted_report_revision"`
		CurrentReportRevision  int64             `json:"current_report_revision"`
	}
	var receiptJSON string
	if err := store.db.QueryRowContext(context.Background(), `SELECT result_json FROM live_operation_receipt WHERE operation_id = ?`, "quarantine-stale-report:"+string(accepted.TurnID)).Scan(&receiptJSON); err != nil {
		t.Fatalf("read quarantine receipt body: %v", err)
	}
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
		t.Fatalf("decode quarantine receipt: %v", err)
	}
	if receipt.TurnID != accepted.TurnID || receipt.State != "quarantined" || receipt.Reason != "report_revision_changed" ||
		receipt.AcceptedReportRevision != fixture.report.ReportRevision || receipt.CurrentReportRevision != int64(latestInput.Report.ReportRevision) {
		t.Fatalf("quarantine receipt = %+v", receipt)
	}

	pending, err := store.ReadPendingAcceptedTurns(context.Background(), fixture.sessionID, "user-1")
	if err != nil {
		t.Fatalf("ReadPendingAcceptedTurns(after stale report quarantine): %v", err)
	}
	if len(pending) != 1 || pending[0].TurnID != latestAccepted.TurnID {
		t.Fatalf("pending turns after stale report quarantine = %#v, want only latest report turn %q", pending, latestAccepted.TurnID)
	}
}

func reportReplyCommitInput(accepted AcceptedTurn) CommitInput {
	event := eventFixture(accepted.InputEvent.TraceID, "agent.response", "orchestrator")
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, modulecore.NewMessageID()
	event.CausationEventID = accepted.InputEvent.EventID
	event.Payload = map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user",
		"content": "The assigned work is complete.", "text": "The assigned work is complete.",
	}
	return CommitInput{
		OperationID: "commit-report-reply:" + accepted.ClientSubmissionID,
		SessionID:   accepted.SessionID, TurnID: accepted.TurnID,
		GenerationEpoch: accepted.GenerationEpoch, ResultState: "completed",
		Events: []modulecore.EventEnvelope{event},
		MessageIndexes: []MessageIndex{{
			MessageID: event.MessageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "agent_response",
			ReplyToMessageID: accepted.InputMessageID, Visibility: "public",
		}},
	}
}

type reportFixture struct {
	readbackFixture
	taskID             modulecore.TaskID
	originMessageID    modulecore.MessageID
	delegationRevision uint64
	report             conversation.ReportEnvelope
	input              ReportInput
}

func newReportFixture(t *testing.T, store *SQLiteStore) reportFixture {
	t.Helper()
	base := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
	ctx := context.Background()
	if _, err := store.AcceptReadback(ctx, base.input); err != nil {
		t.Fatalf("AcceptReadback(report fixture): %v", err)
	}
	now := base.input.OccurredAt.Add(time.Second)
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 || claims[0].Kind != "start_delegated_task" {
		t.Fatalf("ClaimWork(report fixture): claims=%#v err=%v", claims, err)
	}
	lease := claims[0]
	var originMessageID string
	if err := store.db.QueryRowContext(ctx, `SELECT input_message_id FROM live_conversation_turn WHERE turn_id = ?`, string(base.turnID)).Scan(&originMessageID); err != nil {
		t.Fatalf("read origin message id: %v", err)
	}
	taskID, runID, reportID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewResponseID()
	if err := store.BindDelegationTask(ctx, BindDelegationTaskInput{
		DelegationID: base.delegationID, Revision: base.revision, TaskID: taskID,
		TaskOperationID: delegationTaskOperationID(base.delegationID, base.revision),
		WorkID:          lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
	}); err != nil {
		t.Fatalf("BindDelegationTask(report fixture): %v", err)
	}
	report := conversation.ReportEnvelope{
		ReportID: reportID, ReportRevision: 1, DelegationID: base.delegationID,
		DelegationRevision: base.revision, WorkTaskID: taskID, WorkRunID: runID,
		Status: conversation.ReportStatusSucceeded, Summary: "The assigned work completed.",
		Result:       "The behavior was confirmed from the canonical source and test.",
		EvidenceRefs: []modulecore.EvidenceID{}, ArtifactRefs: []modulecore.ArtifactID{},
		Limitations: []string{"The production runtime was not restarted."}, Sender: "forged-body-sender",
	}
	return reportFixture{
		readbackFixture: base,
		taskID:          taskID, originMessageID: modulecore.MessageID(originMessageID),
		delegationRevision: base.revision, report: report,
		input: ReportInput{
			Report: report, TrustedSender: base.assignee,
			EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), TurnID: modulecore.NewTurnID(),
			TraceID: modulecore.NewTraceID(), OccurredAt: time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC),
		},
	}
}

func assertReportAcceptedOnce(t *testing.T, store *SQLiteStore, accepted AcceptedTurn) {
	t.Helper()
	var eventCount, turnCount, receiptCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(accepted.InputEvent.EventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE turn_id = ?`, []any{string(accepted.TurnID)}, &turnCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, []any{accepted.OperationID}, &receiptCount},
	} {
		if err := store.db.QueryRowContext(context.Background(), check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count accepted report writes: %v", err)
		}
	}
	if eventCount != 1 || turnCount != 1 || receiptCount != 1 {
		t.Fatalf("report writes after retry/conflict = event:%d turn:%d receipt:%d, want one each", eventCount, turnCount, receiptCount)
	}
}

func assertReportNotAccepted(t *testing.T, store *SQLiteStore, fixture reportFixture) {
	t.Helper()
	var eventCount, messageCount, turnCount, receiptCount int
	for _, check := range []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id = ?`, []any{string(fixture.input.EventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_message_index WHERE event_id = ?`, []any{string(fixture.input.EventID)}, &messageCount},
		{`SELECT COUNT(*) FROM live_conversation_turn WHERE owner_id = 'user-1' AND session_id = ? AND client_submission_id = ?`, []any{string(fixture.sessionID), reportSubmissionID(fixture.report.ReportID)}, &turnCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ?`, []any{reportAcceptanceOperationID(fixture.report.ReportID)}, &receiptCount},
	} {
		if err := store.db.QueryRowContext(context.Background(), check.query, check.args...).Scan(check.out); err != nil {
			t.Fatalf("count rejected report writes: %v", err)
		}
	}
	var state string
	var reportRevision int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT state, report_revision FROM live_delegation WHERE delegation_id = ? AND revision = ?`, string(fixture.delegationID), int64(fixture.delegationRevision)).Scan(&state, &reportRevision); err != nil {
		t.Fatalf("read delegation after rejected report: %v", err)
	}
	if eventCount != 0 || messageCount != 0 || turnCount != 0 || receiptCount != 0 || reportRevision != 0 {
		t.Fatalf("rejected report left event:%d message:%d turn:%d receipt:%d revision:%d state:%q", eventCount, messageCount, turnCount, receiptCount, reportRevision, state)
	}
}

func payloadInt64(payload map[string]any, key string) int64 {
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}
