package eventstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLoadDelegatedExecutionReturnsCurrentAcceptedSourceAndBoundTask(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	fixture, accepted, lease, taskID := newExecuteWorkFixture(t, store)
	input := DelegatedExecutionSourceInput{
		WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(2 * time.Second)},
		SessionID:      fixture.sessionID, Assignee: fixture.assignee,
	}
	source, err := store.LoadDelegatedExecution(ctx, input)
	if err != nil {
		t.Fatalf("LoadDelegatedExecution(): %v", err)
	}
	if source.Work.WorkID != lease.WorkID || source.Work.SourceEventID != accepted.Event.EventID || source.Work.Assignee != fixture.assignee ||
		source.OwnerID != "user-1" || source.TaskID != taskID || !reflect.DeepEqual(source.Handoff, fixture.handoff) ||
		source.Readback.Status != conversation.ReadbackStatusAccepted || source.Readback.Sender != fixture.assignee ||
		len(source.Context) != 1 || source.Context[0].MessageID != fixture.inputMessageID || source.Context[0].Text != "please delegate this task" {
		t.Fatalf("loaded execution source=%+v, want current accepted source and bound Task %s", source, taskID)
	}
}

func TestLoadDelegatedExecutionRejectsWrongActorExpiredLeaseAndStaleRevision(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, dispatchFixture, *DelegatedExecutionSourceInput)
		want   error
	}{
		{name: "wrong actor", mutate: func(_ *testing.T, _ *SQLiteStore, _ dispatchFixture, input *DelegatedExecutionSourceInput) {
			input.Assignee = "kuro"
		}, want: ErrDelegationWorkConflict},
		{name: "expired lease", mutate: func(_ *testing.T, _ *SQLiteStore, fixture dispatchFixture, input *DelegatedExecutionSourceInput) {
			input.WorkLeaseInput.Now = fixture.now.Add(2 * time.Minute)
		}, want: ErrWorkConflict},
		{name: "stale revision", mutate: func(t *testing.T, store *SQLiteStore, fixture dispatchFixture, _ *DelegatedExecutionSourceInput) {
			_, err := store.db.Exec(`INSERT INTO live_delegation (delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision) SELECT delegation_id,revision+1,origin_turn_id,assignee,instruction_message_id,context_event_id,'dispatch_accepted',NULL,0 FROM live_delegation WHERE delegation_id=? AND revision=1`, string(fixture.delegationID))
			if err != nil {
				t.Fatalf("insert newer revision: %v", err)
			}
		}, want: ErrDelegationWorkConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			fixture, _, lease, _ := newExecuteWorkFixture(t, store)
			input := DelegatedExecutionSourceInput{
				WorkLeaseInput: WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: fixture.now.Add(2 * time.Second)},
				SessionID:      fixture.sessionID, Assignee: fixture.assignee,
			}
			test.mutate(t, store, fixture, &input)
			if _, err := store.LoadDelegatedExecution(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("LoadDelegatedExecution(): %v, want %v", err, test.want)
			}
		})
	}
}

func newExecuteWorkFixture(t *testing.T, store *SQLiteStore) (dispatchFixture, ReadbackAcceptance, LeasedWorkItem, modulecore.TaskID) {
	t.Helper()
	ctx := context.Background()
	fixture := newDispatchWorkFixture(t, store, false, false)
	readback := conversation.ReadbackEnvelope{
		DelegationID: fixture.delegationID, Revision: 1, Sender: fixture.assignee,
		SchemaVersion: "agent.readback.v1", Status: conversation.ReadbackStatusAccepted,
		UnderstoodGoal: fixture.handoff.Goal, AcknowledgedConstraintKeys: []string{"keep_scope"},
		ExpectedOutput: fixture.handoff.ExpectedOutput, Text: "I understand and accept the assignment.",
	}
	accepted, err := store.AcceptReadback(ctx, ReadbackInput{
		OperationID: "execution-source-readback-" + string(fixture.turnID), DelegationID: fixture.delegationID,
		Revision: 1, Assignee: fixture.assignee, Readback: readback,
		EventID: modulecore.NewEventID(), MessageID: modulecore.NewMessageID(), OccurredAt: fixture.now,
	})
	if err != nil {
		t.Fatalf("AcceptReadback(): %v", err)
	}
	starts, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "start_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(time.Second),
	})
	if err != nil || len(starts) != 1 {
		t.Fatalf("ClaimWork(start): claims=%+v err=%v", starts, err)
	}
	taskID := modulecore.NewTaskID()
	start := starts[0]
	if err := store.BindDelegationTask(ctx, BindDelegationTaskInput{
		DelegationID: fixture.delegationID, Revision: 1, TaskID: taskID,
		TaskOperationID: delegationTaskOperationID(fixture.delegationID, 1), WorkID: start.WorkID,
		LeaseToken: start.LeaseToken, Version: start.Version, Now: fixture.now.Add(time.Second),
	}); err != nil {
		t.Fatalf("BindDelegationTask(): %v", err)
	}
	executions, err := store.ClaimWork(ctx, WorkClaimInput{
		Kind: "execute_delegated_task", Limit: 1, LeaseDuration: time.Minute, Now: fixture.now.Add(2 * time.Second),
	})
	if err != nil || len(executions) != 1 {
		t.Fatalf("ClaimWork(execute): claims=%+v err=%v", executions, err)
	}
	return fixture, accepted, executions[0], taskID
}
