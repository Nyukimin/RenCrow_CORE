package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestFailReadbackAtomicallyFreesTurnAndClosesDispatchWork(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	lease := claimReadbackFailureWork(t, store, fixture, fixture.now)
	input := readbackFailureInput(lease, fixture, ReadbackFailureInvalidOutput, fixture.now.Add(time.Second))

	failure, err := store.FailReadback(ctx, input)
	if err != nil {
		t.Fatalf("FailReadback(): %v", err)
	}
	if failure.OperationID != input.OperationID || failure.DelegationID != fixture.delegationID || failure.Revision != 1 ||
		failure.OriginTurnID != fixture.turnID || failure.SessionID != fixture.sessionID ||
		failure.Reason != ReadbackFailureInvalidOutput || failure.Availability != "free" {
		t.Fatalf("terminal failure = %#v, want matching delegation and Free state", failure)
	}
	if failure.Event.EventSeq <= 0 || failure.Event.SessionID != fixture.sessionID || failure.Event.TurnID != fixture.turnID ||
		failure.Event.RequestID != fixture.delegationID || failure.Event.ComponentID != "orchestrator" ||
		payloadString(failure.Event.Payload, "kind") != "handoff_error" || payloadString(failure.Event.Payload, "status") != "failed" ||
		payloadString(failure.Event.Payload, "reason") != string(ReadbackFailureInvalidOutput) ||
		payloadString(failure.Event.Payload, "availability") != "free" || payloadString(failure.Event.Payload, "visibility") != "public" ||
		payloadString(failure.Event.Payload, "from") != "mio" || payloadString(failure.Event.Payload, "to") != "user" ||
		payloadString(failure.Event.Payload, "content") != readbackFailurePublicText {
		t.Fatalf("public terminal event = %#v, want a safe public handoff_error with availability=free", failure.Event)
	}
	if failure.Message.EventID != failure.Event.EventID || failure.Message.MessageID != failure.Event.MessageID ||
		failure.Message.Kind != "handoff_error" || failure.Message.Visibility != "public" || failure.Message.ToKind != "user" {
		t.Fatalf("terminal message index = %#v, want public handoff_error index", failure.Message)
	}
	if _, leaked := failure.Event.Payload["instruction"]; leaked {
		t.Fatal("terminal public event leaked private handoff instruction")
	}

	stored, found, err := store.GetByID(ctx, failure.Event.EventID)
	if err != nil || !found || stored.EventSeq != failure.Event.EventSeq {
		t.Fatalf("GetByID(terminal event): found=%v err=%v event=%#v", found, err, stored)
	}
	var delegationState, turnState, workState string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_delegation WHERE delegation_id=? AND revision=1`, string(fixture.delegationID)).Scan(&delegationState); err != nil {
		t.Fatalf("read delegation state: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_conversation_turn WHERE turn_id=?`, string(fixture.turnID)).Scan(&turnState); err != nil {
		t.Fatalf("read turn state: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&workState); err != nil {
		t.Fatalf("read dispatch state: %v", err)
	}
	var startCount, failureReceiptCount, eventCount, messageCount int
	for _, check := range []struct {
		query string
		args  []any
		into  *int
	}{
		{`SELECT COUNT(*) FROM live_work_outbox WHERE kind='start_delegated_task'`, nil, &startCount},
		{`SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=? AND operation_kind='fail_readback'`, []any{input.OperationID}, &failureReceiptCount},
		{`SELECT COUNT(*) FROM event_envelope WHERE event_id=?`, []any{string(failure.Event.EventID)}, &eventCount},
		{`SELECT COUNT(*) FROM live_message_index WHERE event_id=?`, []any{string(failure.Event.EventID)}, &messageCount},
	} {
		if err := store.db.QueryRowContext(ctx, check.query, check.args...).Scan(check.into); err != nil {
			t.Fatalf("count terminal rows: %v", err)
		}
	}
	if delegationState != "needs_attention" || turnState != "completed" || workState != "completed" ||
		startCount != 0 || failureReceiptCount != 1 || eventCount != 1 || messageCount != 1 {
		t.Fatalf("terminal state delegation=%q turn=%q work=%q taskStarts=%d receipts=%d events=%d indexes=%d; want needs_attention/completed/completed and one committed result", delegationState, turnState, workState, startCount, failureReceiptCount, eventCount, messageCount)
	}
}

func TestFailReadbackRetryAfterRestartReturnsSameCommittedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	lease := claimReadbackFailureWork(t, store, fixture, fixture.now)
	input := readbackFailureInput(lease, fixture, ReadbackFailureExpired, fixture.now.Add(31*time.Second))

	first, err := store.FailReadback(ctx, input)
	if err != nil {
		t.Fatalf("FailReadback(first): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	retry := input
	retry.Work.Now = fixture.now.Add(2 * time.Minute)
	retry.EventID, retry.MessageID = modulecore.NewEventID(), modulecore.NewMessageID()
	retry.OccurredAt = fixture.now.Add(2 * time.Minute)
	second, err := store.FailReadback(ctx, retry)
	if err != nil {
		t.Fatalf("FailReadback(retry after restart): %v", err)
	}
	if second.Event.EventID != first.Event.EventID || second.Event.EventSeq != first.Event.EventSeq ||
		second.Message.MessageID != first.Message.MessageID {
		t.Fatalf("restart retry = event %s/%d message %s; want original %s/%d message %s", second.Event.EventID, second.Event.EventSeq, second.Message.MessageID, first.Event.EventID, first.Event.EventSeq, first.Message.MessageID)
	}
	var eventCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE event_id=?`, string(first.Event.EventID)).Scan(&eventCount); err != nil {
		t.Fatalf("count terminal event after restart: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=? AND operation_kind='fail_readback'`, input.OperationID).Scan(&receiptCount); err != nil {
		t.Fatalf("count terminal receipt after restart: %v", err)
	}
	if eventCount != 1 || receiptCount != 1 {
		t.Fatalf("restart retries left events=%d receipts=%d, want 1 each", eventCount, receiptCount)
	}
}

func TestFailReadbackRejectsStaleRevisionAndLeaseWithoutWritingIntent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(ReadbackFailureInput) ReadbackFailureInput
		want   error
	}{
		{
			name: "stale revision",
			mutate: func(input ReadbackFailureInput) ReadbackFailureInput {
				input.Revision++
				input.OperationID = readbackFailureOperationID(input.DelegationID, input.Revision)
				return input
			},
			want: ErrDelegationWorkConflict,
		},
		{
			name: "stale lease version",
			mutate: func(input ReadbackFailureInput) ReadbackFailureInput {
				input.Work.Version++
				return input
			},
			want: ErrWorkConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			fixture := newDispatchWorkFixture(t, store, false, false)
			lease := claimReadbackFailureWork(t, store, fixture, fixture.now)
			input := test.mutate(readbackFailureInput(lease, fixture, ReadbackFailureInvalidOutput, fixture.now.Add(time.Second)))
			if _, err := store.FailReadback(ctx, input); !errors.Is(err, test.want) {
				t.Fatalf("FailReadback(stale): error=%v, want %v", err, test.want)
			}
			var intentCount, eventCount int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_kind='readback_failure_intent'`).Scan(&intentCount); err != nil {
				t.Fatalf("count failure intents: %v", err)
			}
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE event_id=?`, string(input.EventID)).Scan(&eventCount); err != nil {
				t.Fatalf("count terminal events: %v", err)
			}
			if intentCount != 0 || eventCount != 0 {
				t.Fatalf("stale failure wrote intent=%d events=%d, want none", intentCount, eventCount)
			}
		})
	}
}

func TestFailReadbackIntentSurvivesFinalTransactionFailureAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	lease := claimReadbackFailureWork(t, store, fixture, fixture.now)
	input := readbackFailureInput(lease, fixture, ReadbackFailureInvalidOutput, fixture.now.Add(time.Second))
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER block_readback_failure_terminal BEFORE UPDATE OF state ON live_delegation WHEN NEW.state='needs_attention' BEGIN SELECT RAISE(ABORT, 'blocked terminal transaction'); END`); err != nil {
		t.Fatalf("create terminal transaction failure trigger: %v", err)
	}
	if _, err := store.FailReadback(ctx, input); err == nil {
		t.Fatal("FailReadback() error=nil, want terminal transaction failure")
	}
	var intentCount, eventCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id=? AND operation_kind='readback_failure_intent'`, readbackFailureIntentOperationID(fixture.delegationID, 1)).Scan(&intentCount); err != nil {
		t.Fatalf("count persisted failure intent: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_envelope WHERE event_id=?`, string(input.EventID)).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back failure event: %v", err)
	}
	if intentCount != 1 || eventCount != 0 {
		t.Fatalf("after final transaction failure intent=%d event=%d; want durable intent only", intentCount, eventCount)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER block_readback_failure_terminal`); err != nil {
		t.Fatalf("remove terminal transaction failure trigger: %v", err)
	}
	now := fixture.now.Add(time.Minute)
	claimed, err := store.ClaimWork(ctx, WorkClaimInput{Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(restart): leases=%#v err=%v", claimed, err)
	}
	loaded, err := store.LoadDelegationDispatch(ctx, DelegationDispatchInput{
		WorkID: claimed[0].WorkID, LeaseToken: claimed[0].LeaseToken, Version: claimed[0].Version,
		Now: now, SessionID: fixture.sessionID, Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadDelegationDispatch(restart): %v", err)
	}
	if loaded.FailureIntent == nil || loaded.FailureIntent.Reason != ReadbackFailureInvalidOutput ||
		loaded.FailureIntent.DelegationID != fixture.delegationID || loaded.AcceptedAt != fixture.now ||
		loaded.ReadbackDeadline != fixture.now.Add(delegationReadbackTimeout) {
		t.Fatalf("recovered dispatch = failure intent %#v accepted=%s deadline=%s", loaded.FailureIntent, loaded.AcceptedAt, loaded.ReadbackDeadline)
	}
	retry := input
	retry.Work = WorkLeaseInput{WorkID: claimed[0].WorkID, LeaseToken: claimed[0].LeaseToken, Version: claimed[0].Version, Now: now}
	retry.EventID, retry.MessageID, retry.OccurredAt = modulecore.NewEventID(), modulecore.NewMessageID(), now
	failure, err := store.FailReadback(ctx, retry)
	if err != nil {
		t.Fatalf("FailReadback(recovered intent): %v", err)
	}
	if payloadString(failure.Event.Payload, "reason") != string(ReadbackFailureInvalidOutput) || failure.Availability != "free" {
		t.Fatalf("resumed terminal failure = %#v, want original permanent failure and Free", failure)
	}
	if strings.Contains(failure.Event.Payload["content"].(string), "blocked terminal") {
		t.Fatal("public handoff failure leaked storage error")
	}
}

func TestDispatchRetryAtAttemptLimitWaitsUntilAcceptedReadbackDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	fixture := newDispatchWorkFixtureWithInstructionOffset(t, store, false, false, 40*time.Second)
	dispatchAt := fixture.now.Add(41 * time.Second)
	lease := claimReadbackFailureWork(t, store, fixture, dispatchAt)
	dispatch, err := store.LoadDelegationDispatch(ctx, DelegationDispatchInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version,
		Now: dispatchAt, SessionID: fixture.sessionID, Assignee: fixture.assignee,
	})
	if err != nil {
		t.Fatalf("LoadDelegationDispatch(at T0+41s): %v", err)
	}
	deadline := fixture.now.Add(70 * time.Second)
	if !dispatch.AcceptedAt.Equal(fixture.now.Add(40*time.Second)) || !dispatch.ReadbackDeadline.Equal(deadline) {
		t.Fatalf("dispatch accepted_at=%s deadline=%s; want instruction time %s and immutable deadline %s",
			dispatch.AcceptedAt, dispatch.ReadbackDeadline, fixture.now.Add(40*time.Second), deadline)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE live_work_outbox SET attempt=? WHERE work_id=?`, maxWorkAttempts, lease.WorkID); err != nil {
		t.Fatalf("set attempt limit: %v", err)
	}
	retryAt := dispatchAt
	if err := store.RetryWork(ctx, WorkRetryInput{WorkLeaseInput: WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: retryAt,
	}}); err != nil {
		t.Fatalf("RetryWork(at attempt limit): %v", err)
	}
	var state, notBefore string
	if err := store.db.QueryRowContext(ctx, `SELECT state,not_before FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state, &notBefore); err != nil {
		t.Fatalf("read attempt-limited dispatch: %v", err)
	}
	parsedNotBefore, err := parseWorkTime(notBefore)
	if err != nil || state != "pending" || !parsedNotBefore.Equal(deadline) {
		t.Fatalf("attempt-limited work state=%q not_before=%s err=%v; want pending at instruction+30s deadline %s", state, parsedNotBefore, err, deadline)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	beforeDeadline, err := store.ClaimWork(ctx, WorkClaimInput{Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: deadline.Add(-time.Nanosecond)})
	if err != nil || len(beforeDeadline) != 0 {
		t.Fatalf("ClaimWork(before deadline): leases=%#v err=%v, want no early generation retry", beforeDeadline, err)
	}
	atDeadline, err := store.ClaimWork(ctx, WorkClaimInput{Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: deadline})
	if err != nil || len(atDeadline) != 1 {
		t.Fatalf("ClaimWork(at deadline): leases=%#v err=%v, want one expiry terminalization lease", atDeadline, err)
	}
	recovered, err := store.LoadDelegationDispatch(ctx, DelegationDispatchInput{
		WorkID: atDeadline[0].WorkID, LeaseToken: atDeadline[0].LeaseToken, Version: atDeadline[0].Version,
		Now: deadline, SessionID: fixture.sessionID, Assignee: fixture.assignee,
	})
	if err != nil || !recovered.AcceptedAt.Equal(fixture.now.Add(40*time.Second)) || !recovered.ReadbackDeadline.Equal(deadline) {
		t.Fatalf("recovered dispatch accepted_at=%s deadline=%s err=%v; want immutable instruction time %s and deadline %s",
			recovered.AcceptedAt, recovered.ReadbackDeadline, err, fixture.now.Add(40*time.Second), deadline)
	}
	lateRetryAt := deadline.Add(20 * time.Second)
	if err := store.RetryWork(ctx, WorkRetryInput{WorkLeaseInput: WorkLeaseInput{
		WorkID: atDeadline[0].WorkID, LeaseToken: atDeadline[0].LeaseToken,
		Version: atDeadline[0].Version, Now: lateRetryAt,
	}}); err != nil {
		t.Fatalf("RetryWork(after immutable deadline): %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT not_before FROM live_work_outbox WHERE work_id=?`, atDeadline[0].WorkID).Scan(&notBefore); err != nil {
		t.Fatalf("read retry after deadline: %v", err)
	}
	parsedNotBefore, err = parseWorkTime(notBefore)
	if err != nil || !parsedNotBefore.Equal(deadline) {
		t.Fatalf("late retry not_before=%s err=%v; want the original deadline %s without retry-time rebasing", parsedNotBefore, err, deadline)
	}
}

func TestDelegationReadbackDeadlineFailsClosedWithoutExactInstruction(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *SQLiteStore, dispatchFixture) error
	}{
		{
			name: "missing instruction index",
			mutate: func(ctx context.Context, store *SQLiteStore, fixture dispatchFixture) error {
				conn, err := store.db.Conn(ctx)
				if err != nil {
					return err
				}
				defer conn.Close()
				if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
					return err
				}
				_, err = conn.ExecContext(ctx, `UPDATE live_delegation SET instruction_message_id='missing-instruction' WHERE delegation_id=? AND revision=1`, string(fixture.delegationID))
				return err
			},
		},
		{
			name: "wrong message index",
			mutate: func(ctx context.Context, store *SQLiteStore, fixture dispatchFixture) error {
				_, err := store.db.ExecContext(ctx, `UPDATE live_delegation SET instruction_message_id=? WHERE delegation_id=? AND revision=1`, string(fixture.inputMessageID), string(fixture.delegationID))
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			fixture := newDispatchWorkFixture(t, store, false, false)
			if err := test.mutate(ctx, store, fixture); err != nil {
				t.Fatalf("corrupt instruction reference: %v", err)
			}
			tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatalf("BeginTx(): %v", err)
			}
			defer tx.Rollback()
			_, err = delegationReadbackDeadlineForWork(ctx, tx, LeasedWorkItem{
				WorkItem: WorkItem{SourceEventID: fixture.instructionEventID},
			})
			if !errors.Is(err, ErrDelegationWorkConflict) {
				t.Fatalf("delegationReadbackDeadlineForWork() error=%v, want ErrDelegationWorkConflict for missing/wrong instruction", err)
			}
		})
	}
}

func claimReadbackFailureWork(t *testing.T, store *SQLiteStore, fixture dispatchFixture, now time.Time) LeasedWorkItem {
	t.Helper()
	claimed, err := store.ClaimWork(context.Background(), WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(readback failure): leases=%#v err=%v", claimed, err)
	}
	return claimed[0]
}

func readbackFailureInput(lease LeasedWorkItem, fixture dispatchFixture, reason ReadbackFailureReason, now time.Time) ReadbackFailureInput {
	return ReadbackFailureInput{
		OperationID:  readbackFailureOperationID(fixture.delegationID, 1),
		Work:         WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now},
		DelegationID: fixture.delegationID, Revision: 1, Assignee: fixture.assignee, Reason: reason,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: now,
	}
}
