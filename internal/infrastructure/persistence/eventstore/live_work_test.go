package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLiveWorkOutboxAPIsExist(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{
		"ClaimWork",
		"AckWork",
		"RetryWork",
		"LoadDelegationDispatch",
		"FailReadback",
		"BindDelegationTask",
	} {
		if !reflect.ValueOf(store).MethodByName(name).IsValid() {
			t.Errorf("eventstore API %s is missing", name)
		}
	}
}

func TestClaimWorkPriorityLeaseExpiryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	ctx := context.Background()
	newDispatchWorkFixture(t, store, true, false)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	first, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(first) != 1 {
		t.Fatalf("ClaimWork(first): items=%#v err=%v", first, err)
	}
	if first[0].Kind != "dispatch_delegation" || first[0].Priority != 5 || first[0].Attempt != 1 {
		t.Fatalf("first claim = %#v, want high-priority dispatch", first[0])
	}
	projection, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(projection) != 1 || projection[0].Kind != "project_conversation" || projection[0].Priority != 10 {
		t.Fatalf("ClaimWork(projection): items=%#v err=%v", projection, err)
	}
	duplicate, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 2, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("ClaimWork(while active): items=%#v err=%v, want no duplicate active lease", duplicate, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(restart): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reclaimed, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now.Add(time.Minute)})
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("ClaimWork(expired lease): items=%#v err=%v", reclaimed, err)
	}
	if reclaimed[0].WorkID != first[0].WorkID || reclaimed[0].LeaseToken == first[0].LeaseToken || reclaimed[0].Version != first[0].Version+1 || reclaimed[0].Attempt != 2 {
		t.Fatalf("expired reclaim = %#v, want same work with fresh token/version and attempt 2", reclaimed[0])
	}
	if err := store.AckWork(ctx, WorkLeaseInput{WorkID: reclaimed[0].WorkID, LeaseToken: first[0].LeaseToken, Version: first[0].Version, Now: now.Add(time.Minute)}); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("AckWork(stale lease): %v, want ErrWorkConflict", err)
	}
	if err := store.AckWork(ctx, WorkLeaseInput{WorkID: projection[0].WorkID, LeaseToken: projection[0].LeaseToken, Version: projection[0].Version, Now: now.Add(30 * time.Second)}); err != nil {
		t.Fatalf("AckWork(projection): %v", err)
	}
	if err := store.AckWork(ctx, WorkLeaseInput{WorkID: reclaimed[0].WorkID, LeaseToken: reclaimed[0].LeaseToken, Version: reclaimed[0].Version, Now: now.Add(time.Minute)}); !errors.Is(err, ErrWorkResultNotPersisted) {
		t.Fatalf("AckWork(dispatch before readback): %v, want ErrWorkResultNotPersisted", err)
	}
	if err := store.RetryWork(ctx, WorkRetryInput{WorkLeaseInput: WorkLeaseInput{WorkID: reclaimed[0].WorkID, LeaseToken: reclaimed[0].LeaseToken, Version: reclaimed[0].Version, Now: now.Add(time.Minute)}}); err != nil {
		t.Fatalf("RetryWork(dispatch): %v", err)
	}
	var state, notBefore string
	if err := store.db.QueryRow(`SELECT state,not_before FROM live_work_outbox WHERE work_id=?`, reclaimed[0].WorkID).Scan(&state, &notBefore); err != nil {
		t.Fatalf("read retried dispatch: %v", err)
	}
	if state != "pending" || notBefore == "" {
		t.Fatalf("retried dispatch state=%q not_before=%q, want delayed pending", state, notBefore)
	}
}

func TestClaimWorkKindFilterReturnsTrustedDispatchAssignee(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	startFixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
	if _, err := store.AcceptReadback(ctx, startFixture.input); err != nil {
		t.Fatalf("AcceptReadback(start-work fixture): %v", err)
	}
	fixture := newDispatchWorkFixture(t, store, true, false)

	claimed, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(dispatch only): items=%#v err=%v", claimed, err)
	}
	if claimed[0].Kind != "dispatch_delegation" || claimed[0].Assignee != fixture.assignee {
		t.Fatalf("dispatch lease = %#v, want trusted assignee %q", claimed[0], fixture.assignee)
	}
	var projectionState, taskStartState string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE kind='project_conversation'`).Scan(&projectionState); err != nil {
		t.Fatalf("read projection state: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE kind='start_delegated_task'`).Scan(&taskStartState); err != nil {
		t.Fatalf("read task-start state: %v", err)
	}
	if projectionState != "pending" || taskStartState != "pending" {
		t.Fatalf("non-dispatch states = projection:%q task-start:%q, want both pending", projectionState, taskStartState)
	}
}

func TestClaimWorkAssigneeFilterPreventsDelegatedTaskStealing(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := createAcceptedReadbackWorkFixture(t, store)
	second := createAcceptedReadbackWorkFixture(t, store)
	// The shared fixture defaults to Shiro; give the second stored delegation a
	// distinct trusted assignee to exercise actor-scoped work selection.
	if _, err := store.db.ExecContext(ctx, `UPDATE live_delegation SET assignee='midori' WHERE delegation_id=? AND revision=?`, string(second.delegationID), int64(second.revision)); err != nil {
		t.Fatalf("set second stored delegation assignee: %v", err)
	}
	now := first.input.OccurredAt.Add(time.Second)
	for _, owner := range []struct {
		fixture  readbackFixture
		assignee string
	}{
		{fixture: first, assignee: "shiro"},
		{fixture: second, assignee: "midori"},
	} {
		start, err := store.ClaimWork(ctx, WorkClaimInput{
			Kind: "start_delegated_task", Assignee: owner.assignee, Limit: 1, LeaseDuration: time.Minute, Now: now,
		})
		if err != nil || len(start) != 1 {
			t.Fatalf("ClaimWork(task start for %s): items=%#v err=%v", owner.assignee, start, err)
		}
		if err := store.BindDelegationTask(ctx, BindDelegationTaskInput{
			DelegationID: owner.fixture.delegationID, Revision: owner.fixture.revision, TaskID: modulecore.NewTaskID(),
			TaskOperationID: delegationTaskOperationID(owner.fixture.delegationID, owner.fixture.revision), WorkID: start[0].WorkID,
			LeaseToken: start[0].LeaseToken, Version: start[0].Version, Now: now,
		}); err != nil {
			t.Fatalf("BindDelegationTask(%s): %v", owner.assignee, err)
		}
	}

	shiro, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "execute_delegated_task", Assignee: "shiro", Limit: 4, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(shiro) != 1 {
		t.Fatalf("ClaimWork(Shiro execute): items=%#v err=%v, want only Shiro's work", shiro, err)
	}
	if shiro[0].WorkID != delegationExecuteWorkID(first.delegationID, first.revision) || shiro[0].Assignee != "shiro" {
		t.Fatalf("Shiro lease = %#v, want first delegation with trusted assignee Shiro", shiro[0])
	}

	midori, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "execute_delegated_task", Assignee: "midori", Limit: 4, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(midori) != 1 {
		t.Fatalf("ClaimWork(Midori execute): items=%#v err=%v, want only Midori's work", midori, err)
	}
	if midori[0].WorkID != delegationExecuteWorkID(second.delegationID, second.revision) || midori[0].Assignee != "midori" {
		t.Fatalf("Midori lease = %#v, want second delegation with trusted assignee Midori", midori[0])
	}

	if _, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "execute_delegated_task", Assignee: "kuro", Limit: 4, LeaseDuration: time.Minute, Now: now,
	}); err != nil {
		t.Fatalf("ClaimWork(nonmatching actor): %v", err)
	}
}

func TestClaimWorkAssigneeFilterPopulatesTaskStartLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := createAcceptedReadbackWorkFixture(t, store)
	second := createAcceptedReadbackWorkFixture(t, store)
	// The shared fixture defaults to Shiro; give the second stored delegation a
	// distinct trusted assignee to exercise actor-scoped work selection.
	if _, err := store.db.ExecContext(ctx, `UPDATE live_delegation SET assignee='midori' WHERE delegation_id=? AND revision=?`, string(second.delegationID), int64(second.revision)); err != nil {
		t.Fatalf("set second stored delegation assignee: %v", err)
	}

	shiro, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "start_delegated_task", Assignee: "shiro", Limit: 4, LeaseDuration: time.Minute, Now: first.input.OccurredAt.Add(time.Second),
	})
	if err != nil || len(shiro) != 1 {
		t.Fatalf("ClaimWork(Shiro task start): items=%#v err=%v, want only Shiro's work", shiro, err)
	}
	if shiro[0].WorkID != readbackStartWorkID(first.delegationID, first.revision) || shiro[0].Assignee != "shiro" {
		t.Fatalf("Shiro task-start lease = %#v, want first delegation with trusted assignee Shiro", shiro[0])
	}

	midori, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "start_delegated_task", Assignee: "midori", Limit: 4, LeaseDuration: time.Minute, Now: second.input.OccurredAt.Add(time.Second),
	})
	if err != nil || len(midori) != 1 {
		t.Fatalf("ClaimWork(Midori task start): items=%#v err=%v, want only Midori's work", midori, err)
	}
	if midori[0].WorkID != readbackStartWorkID(second.delegationID, second.revision) || midori[0].Assignee != "midori" {
		t.Fatalf("Midori task-start lease = %#v, want second delegation with trusted assignee Midori", midori[0])
	}
}

func TestClaimWorkCompletesDispatchUsingDispatchDedupeKeyAfterReadback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	claimed, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(): items=%#v err=%v", claimed, err)
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusNeedsClarification,
		UnderstoodGoal: fixture.handoff.Goal, ExpectedOutput: fixture.handoff.ExpectedOutput,
		Text: "I need one detail clarified before starting.",
	}
	if _, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID: "dispatch-readback-" + string(fixture.turnID), DelegationID: fixture.delegationID,
		Revision: 1, Assignee: fixture.assignee, Readback: readback,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.now,
	}); err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}

	claimed, err = store.ClaimWork(ctx, WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second),
	})
	if err != nil || len(claimed) != 0 {
		t.Fatalf("ClaimWork(after readback): items=%#v err=%v, want no terminal dispatch claim", claimed, err)
	}
	var state string
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM live_work_outbox WHERE kind='dispatch_delegation'`).Scan(&state); err != nil {
		t.Fatalf("read dispatch state: %v", err)
	}
	if state != "completed" {
		t.Fatalf("dispatch state = %q, want completed after readback", state)
	}
}

func TestLoadDelegationDispatchEnforcesLeaseRecipientContextAndReadback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	claimed, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: fixture.now})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(): items=%#v err=%v", claimed, err)
	}
	lease := claimed[0]
	if lease.Assignee != fixture.assignee {
		t.Fatalf("claimed assignee = %q, want trusted stored target %q", lease.Assignee, fixture.assignee)
	}
	input := DelegationDispatchInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now, SessionID: fixture.sessionID, Assignee: lease.Assignee}
	loaded, err := store.LoadDelegationDispatch(ctx, input)
	if err != nil {
		t.Fatalf("LoadDelegationDispatch(): %v", err)
	}
	if loaded.Work.Assignee != fixture.assignee || loaded.Handoff.ToActor != fixture.assignee || len(loaded.Context) != 1 || loaded.Context[0].MessageID != fixture.inputMessageID || loaded.Context[0].Role != "user" {
		t.Fatalf("loaded private dispatch = %#v", loaded)
	}
	wrongSession := input
	wrongSession.SessionID = modulecore.NewSessionID()
	if _, err := store.LoadDelegationDispatch(ctx, wrongSession); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadDelegationDispatch(other session): %v, want ErrWorkConflict", err)
	}
	wrongRecipient := input
	wrongRecipient.Assignee = "kuro"
	if _, err := store.LoadDelegationDispatch(ctx, wrongRecipient); !errors.Is(err, ErrDelegationWorkConflict) {
		t.Fatalf("LoadDelegationDispatch(other recipient): %v, want delegation conflict", err)
	}
	expired := input
	expired.Now = lease.LeaseExpiresAt
	if _, err := store.LoadDelegationDispatch(ctx, expired); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadDelegationDispatch(expired): %v, want ErrWorkConflict", err)
	}
	publicEvents, err := store.ListByComponent(ctx, "orchestrator", 20)
	if err != nil {
		t.Fatalf("ListByComponent(orchestrator): %v", err)
	}
	for _, event := range publicEvents {
		if event.EventID == fixture.contextEventID || event.EventID == fixture.instructionEventID {
			t.Fatalf("private handoff event %s reached public orchestrator relay", event.EventID)
		}
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee, SchemaVersion: "agent.readback.v1",
		Status: conversation.ReadbackStatusAccepted, UnderstoodGoal: fixture.handoff.Goal,
		AcknowledgedConstraintKeys: []string{"keep_scope"}, ExpectedOutput: fixture.handoff.ExpectedOutput, Text: "I understand the task.",
	}
	if _, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID: "dispatch-readback-" + string(fixture.turnID), DelegationID: fixture.delegationID, Revision: 1,
		Assignee: fixture.assignee, Readback: readback, EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.now,
	}); err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	if err := store.AckWork(ctx, WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now}); err != nil {
		t.Fatalf("AckWork(after durable readback): %v", err)
	}
	if _, err := store.LoadDelegationDispatch(ctx, input); !errors.Is(err, ErrWorkConflict) {
		t.Fatalf("LoadDelegationDispatch(completed lease): %v, want ErrWorkConflict", err)
	}
}

func TestLoadDelegationDispatchRejectsSupersededRevision(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	claimed, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "dispatch_delegation", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimWork(): items=%#v err=%v", claimed, err)
	}
	lease := claimed[0]
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO live_delegation (delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision)
		SELECT delegation_id,revision+1,origin_turn_id,assignee,instruction_message_id,context_event_id,'dispatch_accepted',NULL,0
		FROM live_delegation WHERE delegation_id=? AND revision=1`, string(fixture.delegationID)); err != nil {
		t.Fatalf("insert superseding revision: %v", err)
	}
	_, err = store.LoadDelegationDispatch(ctx, DelegationDispatchInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version,
		Now: fixture.now, SessionID: lease.SessionID, Assignee: lease.Assignee,
	})
	if !errors.Is(err, ErrDelegationWorkConflict) {
		t.Fatalf("LoadDelegationDispatch(superseded): error = %v, want ErrDelegationWorkConflict", err)
	}
}

func TestLoadDelegationDispatchRejectsContextHashMismatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, true)
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: fixture.now})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(): items=%#v err=%v", claims, err)
	}
	_, err = store.LoadDelegationDispatch(ctx, DelegationDispatchInput{
		WorkID: claims[0].WorkID, LeaseToken: claims[0].LeaseToken, Version: claims[0].Version,
		Now: fixture.now, SessionID: fixture.sessionID, Assignee: fixture.assignee,
	})
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("LoadDelegationDispatch(hash mismatch) error = %v, want context validation failure", err)
	}
}

func TestTaskStartRetryRequiresDurableBindAndBindIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
	if _, err := store.AcceptReadback(ctx, fixture.input); err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	now := fixture.input.OccurredAt.Add(time.Second)
	claims, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 4, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 || claims[0].Kind != "start_delegated_task" {
		t.Fatalf("ClaimWork(task start): items=%#v err=%v", claims, err)
	}
	lease := claims[0]
	leaseInput := WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now}
	if err := store.AckWork(ctx, leaseInput); !errors.Is(err, ErrWorkResultNotPersisted) {
		t.Fatalf("AckWork(before Task bind): %v, want ErrWorkResultNotPersisted", err)
	}
	if err := store.RetryWork(ctx, WorkRetryInput{WorkLeaseInput: leaseInput}); err != nil {
		t.Fatalf("RetryWork(before Task bind): %v", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("task start after RetryWork state=%q err=%v, want pending", state, err)
	}
	claims, err = store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now.Add(2 * time.Second)})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(task retry): items=%#v err=%v", claims, err)
	}
	lease = claims[0]
	bind := BindDelegationTaskInput{
		DelegationID: fixture.delegationID, Revision: fixture.revision, TaskID: modulecore.NewTaskID(),
		TaskOperationID: delegationTaskOperationID(fixture.delegationID, fixture.revision), WorkID: lease.WorkID,
		LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now.Add(2 * time.Second),
	}
	if err := store.BindDelegationTask(ctx, bind); err != nil {
		t.Fatalf("BindDelegationTask(): %v", err)
	}
	if err := store.BindDelegationTask(ctx, bind); err != nil {
		t.Fatalf("BindDelegationTask(same Task ID): %v, want idempotent success", err)
	}
	differentTask := bind
	differentTask.TaskID = modulecore.NewTaskID()
	if err := store.BindDelegationTask(ctx, differentTask); !errors.Is(err, ErrDelegationWorkConflict) {
		t.Fatalf("BindDelegationTask(different Task ID): %v, want conflict", err)
	}
	staleRevision := bind
	staleRevision.Revision++
	if err := store.BindDelegationTask(ctx, staleRevision); !errors.Is(err, ErrDelegationWorkConflict) {
		t.Fatalf("BindDelegationTask(stale revision): %v, want conflict", err)
	}
	if err := store.AckWork(ctx, WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("AckWork(after durable Task bind): %v", err)
	}
	var boundTask, finalState string
	if err := store.db.QueryRow(`SELECT d.work_task_id,w.state FROM live_delegation d JOIN live_work_outbox w ON w.work_id=? WHERE d.delegation_id=? AND d.revision=?`, lease.WorkID, string(fixture.delegationID), fixture.revision).Scan(&boundTask, &finalState); err != nil {
		t.Fatalf("read bound Task result: %v", err)
	}
	if boundTask != string(bind.TaskID) || finalState != "completed" {
		t.Fatalf("durable task result = task:%q work:%q, want %q/completed", boundTask, finalState, bind.TaskID)
	}
}

func TestBindDelegationTaskRejectsStaleRevisionAndCanceledDelegation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, readbackFixture)
	}{
		{name: "stale revision", mutate: func(t *testing.T, store *SQLiteStore, fixture readbackFixture) {
			if _, err := store.db.Exec(`INSERT INTO live_delegation (delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision) SELECT delegation_id,revision+1,origin_turn_id,assignee,instruction_message_id,context_event_id,'dispatch_accepted',NULL,0 FROM live_delegation WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), int64(fixture.revision)); err != nil {
				t.Fatalf("insert newer delegation revision: %v", err)
			}
		}},
		{name: "canceled delegation", mutate: func(t *testing.T, store *SQLiteStore, fixture readbackFixture) {
			if _, err := store.db.Exec(`UPDATE live_delegation SET state='canceled' WHERE delegation_id=? AND revision=?`, string(fixture.delegationID), int64(fixture.revision)); err != nil {
				t.Fatalf("cancel delegation: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
			if _, err := store.AcceptReadback(ctx, fixture.input); err != nil {
				t.Fatalf("AcceptReadback(): %v", err)
			}
			now := fixture.input.OccurredAt.Add(time.Second)
			claims, err := store.ClaimWork(ctx, WorkClaimInput{Limit: 1, LeaseDuration: time.Minute, Now: now})
			if err != nil || len(claims) != 1 {
				t.Fatalf("ClaimWork(task start): items=%#v err=%v", claims, err)
			}
			test.mutate(t, store, fixture)
			lease := claims[0]
			input := BindDelegationTaskInput{
				DelegationID: fixture.delegationID, Revision: fixture.revision, TaskID: modulecore.NewTaskID(),
				TaskOperationID: delegationTaskOperationID(fixture.delegationID, fixture.revision), WorkID: lease.WorkID,
				LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
			}
			if err := store.BindDelegationTask(ctx, input); !errors.Is(err, ErrDelegationWorkConflict) {
				t.Fatalf("BindDelegationTask() error = %v, want ErrDelegationWorkConflict", err)
			}
		})
	}
}

type dispatchFixture struct {
	now                time.Time
	sessionID          modulecore.SessionID
	turnID             modulecore.TurnID
	delegationID       modulecore.RequestID
	assignee           string
	inputMessageID     modulecore.MessageID
	contextEventID     modulecore.EventID
	instructionEventID modulecore.EventID
	handoff            conversation.HandoffEnvelope
}

func newDispatchWorkFixture(t *testing.T, store *SQLiteStore, includeProjection bool, corruptContextHash bool) dispatchFixture {
	return newDispatchWorkFixtureWithInstructionOffset(t, store, includeProjection, corruptContextHash, 0)
}

func newDispatchWorkFixtureWithInstructionOffset(t *testing.T, store *SQLiteStore, includeProjection bool, corruptContextHash bool, instructionOffset time.Duration) dispatchFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "please delegate this task")
	inputEvent.OccurredAt = now
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "accept-work-" + string(turnID), OwnerID: "user-1", RuntimeVersion: "v2", ClientSubmissionID: "submission-" + string(turnID),
		ActorID: "user-1", TriggerKind: "user_message", GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
	})
	if err != nil {
		t.Fatalf("AcceptTurn(): %v", err)
	}
	delegationID, assignee := modulecore.NewRequestID(), "shiro"
	instructionMessageID := modulecore.NewMessageID()
	contextMessages := []DelegationContextMessage{{MessageID: inputEvent.MessageID, Role: "user", Text: "please delegate this task"}}
	contextMaterial := struct {
		SourceRevision  string                     `json:"source_revision"`
		CutoverRevision string                     `json:"cutover_revision"`
		Messages        []DelegationContextMessage `json:"messages"`
	}{SourceRevision: "1", CutoverRevision: "0", Messages: contextMessages}
	encoded, err := json.Marshal(contextMaterial)
	if err != nil {
		t.Fatalf("marshal context hash material: %v", err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	if corruptContextHash {
		hash = strings.Repeat("f", 64)
	}
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: turnID, OriginUserMessageID: inputEvent.MessageID,
		SessionID: sessionID, SourceTraceID: inputEvent.TraceID, FromActor: "mio", ToActor: assignee,
		InstructionMessageID: instructionMessageID, ContextSnapshotHash: hash, AllowedCapabilities: []string{}, ReplyTo: inputEvent.MessageID,
		Goal: "Summarize the task", Instruction: "Inspect and report.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within this task.", SourceMessageIDs: []modulecore.MessageID{inputEvent.MessageID}}},
		ExpectedOutput: "A concise report",
	}
	if err := conversation.ValidateHandoffEnvelope(handoff); err != nil {
		t.Fatalf("ValidateHandoffEnvelope(): %v", err)
	}
	notice := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	notice.SessionID, notice.TurnID, notice.MessageID, notice.CausationEventID = sessionID, turnID, modulecore.NewMessageID(), inputEvent.EventID
	notice.OccurredAt = now
	notice.Payload = map[string]any{"kind": "delegation_notice", "from": "mio", "source_actor_id": "mio", "to": "user", "visibility": "public", "content": "I delegated this task.", "text": "I delegated this task."}
	contextEvent := eventFixture(inputEvent.TraceID, "conversation.delegation_context", "conversation_runtime")
	contextEvent.SessionID, contextEvent.TurnID, contextEvent.CausationEventID = sessionID, turnID, inputEvent.EventID
	contextEvent.OccurredAt = now
	contextEvent.Payload = map[string]any{"source_revision": "1", "cutover_revision": "0", "messages": contextMessages, "context_snapshot_hash": hash, "handoff": handoff}
	instruction := eventFixture(inputEvent.TraceID, "conversation.delegation_instruction", "delegation_mailbox")
	instruction.SessionID, instruction.TurnID, instruction.MessageID = sessionID, turnID, instructionMessageID
	instruction.CausationEventID, instruction.ActorKind, instruction.ActorID = notice.EventID, "agent", "mio"
	instruction.OccurredAt = now.Add(instructionOffset)
	instruction.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": assignee, "text": handoff.Instruction}
	outbox := []WorkItem{{WorkID: "dispatch:" + string(turnID), DedupeKey: "delegation:" + string(delegationID) + ":1:start", Kind: "dispatch_delegation", SourceEventID: instruction.EventID, SessionID: sessionID, Priority: 5}}
	if includeProjection {
		outbox = append(outbox, WorkItem{WorkID: "projection:" + string(turnID), DedupeKey: "projection:" + string(turnID), Kind: "project_conversation", SourceEventID: notice.EventID, SessionID: sessionID, Priority: 10})
	}
	if _, err := store.CommitTurnBundle(ctx, CommitInput{
		OperationID: "commit-work-" + string(turnID), SessionID: sessionID, TurnID: turnID, GenerationEpoch: 1, ResultState: "awaiting_readback",
		Events: []modulecore.EventEnvelope{notice, contextEvent, instruction},
		MessageIndexes: []MessageIndex{
			{MessageID: notice.MessageID, EventID: notice.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "delegation_notice", Visibility: "public"},
			{MessageID: instruction.MessageID, EventID: instruction.EventID, SessionID: sessionID, TurnID: turnID, FromActor: "mio", ToKind: "agent", ToActor: assignee, Kind: "delegation_instruction", Visibility: "private"},
		},
		Delegation: &DelegationRecord{DelegationID: string(delegationID), Revision: 1, OriginTurnID: turnID, Assignee: assignee, InstructionMessageID: instruction.MessageID, ContextEventID: contextEvent.EventID, State: "dispatch_accepted"},
		Outbox:     outbox,
	}); err != nil {
		t.Fatalf("CommitTurnBundle(): %v", err)
	}
	return dispatchFixture{now: now, sessionID: accepted.SessionID, turnID: turnID, delegationID: delegationID, assignee: assignee,
		inputMessageID: inputEvent.MessageID, contextEventID: contextEvent.EventID, instructionEventID: instruction.EventID, handoff: handoff}
}

func TestExecuteDelegatedTaskAckRequiresAcceptedReportAndRetryKeepsWorkPending(t *testing.T) {
	store := newTestStore(t)
	fixture := newReportFixture(t, store)
	now := fixture.input.OccurredAt.Add(time.Second)
	lease := claimExecuteDelegationWork(t, store, now)
	input := WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now}

	if err := store.AckWork(context.Background(), input); !errors.Is(err, ErrWorkResultNotPersisted) {
		t.Fatalf("AckWork(before accepted report): %v, want ErrWorkResultNotPersisted", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("work state after rejected Ack = %q err=%v, want leased", state, err)
	}

	if err := store.RetryWork(context.Background(), WorkRetryInput{WorkLeaseInput: input}); err != nil {
		t.Fatalf("RetryWork(before accepted report): %v", err)
	}
	var notBefore string
	if err := store.db.QueryRow(`SELECT state,not_before FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state, &notBefore); err != nil {
		t.Fatalf("read retried execution work: %v", err)
	}
	if state != "pending" || notBefore != formatWorkTime(now.Add(time.Second)) {
		t.Fatalf("execution work after retry = state:%q not_before:%q, want pending at %s", state, notBefore, formatWorkTime(now.Add(time.Second)))
	}

	reclaimed := claimExecuteDelegationWork(t, store, now.Add(time.Second))
	if reclaimed.WorkID != lease.WorkID || reclaimed.Attempt != 2 || reclaimed.LeaseToken == lease.LeaseToken || reclaimed.Version != lease.Version+2 {
		t.Fatalf("reclaimed execution work = %#v, want same item with fresh lease/version and attempt 2", reclaimed)
	}
}

func TestExecuteDelegatedTaskAckRequiresAcceptedReportForBoundTaskAndDelegation(t *testing.T) {
	t.Run("wrong task report is not completion proof", func(t *testing.T) {
		store := newTestStore(t)
		fixture := newReportFixture(t, store)
		now := fixture.input.OccurredAt.Add(time.Second)
		lease := claimExecuteDelegationWork(t, store, now)

		wrongTask := fixture.input
		wrongTask.Report.WorkTaskID = modulecore.NewTaskID()
		if _, err := store.AcceptReport(context.Background(), wrongTask); !errors.Is(err, ErrReportTaskMismatch) {
			t.Fatalf("AcceptReport(wrong task): %v, want ErrReportTaskMismatch", err)
		}
		if err := store.AckWork(context.Background(), WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now}); !errors.Is(err, ErrWorkResultNotPersisted) {
			t.Fatalf("AckWork(after rejected wrong-task report): %v, want ErrWorkResultNotPersisted", err)
		}
	})

	t.Run("report for another delegation is not completion proof", func(t *testing.T) {
		store := newTestStore(t)
		first := newReportFixture(t, store)
		now := first.input.OccurredAt.Add(time.Second)
		lease := claimExecuteDelegationWork(t, store, now)
		second := newReportFixture(t, store)
		if second.delegationID == first.delegationID {
			t.Fatal("report fixtures unexpectedly share a delegation ID")
		}
		if _, err := store.AcceptReport(context.Background(), second.input); err != nil {
			t.Fatalf("AcceptReport(other delegation): %v", err)
		}
		if err := store.AckWork(context.Background(), WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now}); !errors.Is(err, ErrWorkResultNotPersisted) {
			t.Fatalf("AckWork(after unrelated report): %v, want ErrWorkResultNotPersisted", err)
		}
	})
}

func TestExecuteDelegatedTaskAckReplaysFromAcceptedReportReceiptAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-report.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := newReportFixture(t, store)
	now := fixture.input.OccurredAt.Add(time.Second)
	lease := claimExecuteDelegationWork(t, store, now)
	input := WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now}
	if _, err := store.AcceptReport(context.Background(), fixture.input); err != nil {
		t.Fatalf("AcceptReport(): %v", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("execution work after atomic report acceptance = %q err=%v, want completed", state, err)
	}
	if err := store.AckWork(context.Background(), input); err != nil {
		t.Fatalf("AckWork(after accepted report): %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close(before report replay): %v", err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	if err := store.AckWork(context.Background(), input); err != nil {
		t.Fatalf("AckWork(replay after report state transition and restart): %v", err)
	}
	if err := store.db.QueryRow(`SELECT state FROM live_work_outbox WHERE work_id=?`, lease.WorkID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("execution work after replay = %q err=%v, want completed", state, err)
	}
}

func createAcceptedReadbackWorkFixture(t *testing.T, store *SQLiteStore) readbackFixture {
	t.Helper()
	fixture := newReadbackFixture(t, store, conversation.ReadbackStatusAccepted)
	if _, err := store.AcceptReadback(context.Background(), fixture.input); err != nil {
		t.Fatalf("AcceptReadback(assignee work fixture): %v", err)
	}
	return fixture
}

func claimExecuteDelegationWork(t *testing.T, store *SQLiteStore, now time.Time) LeasedWorkItem {
	t.Helper()
	claims, err := store.ClaimWork(context.Background(), WorkClaimInput{
		Kind: "execute_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimWork(execute delegated task): claims=%#v err=%v", claims, err)
	}
	return claims[0]
}
