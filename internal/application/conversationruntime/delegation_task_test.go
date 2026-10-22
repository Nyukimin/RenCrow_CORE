package conversationruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDelegationTaskAdmissionCreatesAndBindsReadOnlyTask(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store := newDelegatedTaskAdmissionStore(now)
	manager, closeStore := openDelegatedTaskManager(t, t.TempDir())
	defer closeStore()
	admission, err := NewDelegationTaskAdmission(DelegationTaskAdmissionDependencies{
		Store: store, Tasks: manager,
		Config: DelegationTaskAdmissionConfig{ActorID: "shiro", Now: func() time.Time { return now }},
	})
	if err != nil {
		t.Fatalf("NewDelegationTaskAdmission(): %v", err)
	}
	result, err := admission.StartOne(context.Background())
	if err != nil {
		t.Fatalf("StartOne(): %v", err)
	}
	if !result.Claimed || !result.TaskEnsured || !result.Bound || result.WorkID != store.lease.WorkID || result.TaskID == "" || result.RunID == "" {
		t.Fatalf("StartOne result=%+v, want one ensured and bound Task/Run", result)
	}
	if store.loadCount != 3 {
		t.Fatalf("source loads=%d, want initial, TaskManager preflight, and TaskManager pre-commit checks", store.loadCount)
	}
	if len(store.binds) != 1 || store.binds[0].TaskID != result.TaskID || store.binds[0].TaskOperationID != taskmanager.DelegationTaskOperationID(store.source.Handoff.DelegationID, 1) {
		t.Fatalf("bind requests=%+v, want Task owner ID and canonical operation identity", store.binds)
	}
	task, err := manager.Get(context.Background(), result.TaskID)
	if err != nil {
		t.Fatalf("read created Task: %v", err)
	}
	if task.Title != store.source.Handoff.Goal || task.OwnerID != store.source.OwnerID || task.Assignee != "shiro" || !task.ReadOnly ||
		task.OriginSessionID != store.source.Handoff.SessionID || task.OriginTurnID != store.source.Handoff.OriginTurnID || task.OriginMessageID != store.source.Handoff.OriginUserMessageID ||
		task.Status != domaintask.StatusRunning {
		t.Fatalf("Task=%+v, want running, read-only, source-bound Task for Shiro", task)
	}
	shared, err := manager.Context(context.Background(), result.TaskID)
	if err != nil || shared.TaskID != result.TaskID || shared.UserIntent != store.source.Handoff.Goal || shared.CurrentPlan != store.source.Handoff.Instruction ||
		len(shared.Constraints) != 1 || !strings.Contains(shared.HandoffNotes, "Accepted readback:") || !strings.Contains(shared.HandoffNotes, "User: Please inspect this behavior") {
		t.Fatalf("Task context=%+v err=%v, want stored handoff and public source context", shared, err)
	}
	run, err := manager.GetRun(context.Background(), result.RunID)
	if err != nil || run.TaskID != result.TaskID || run.StartReason != domaintask.RunStartReasonFirst || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("first Run=%+v err=%v, want Task's first running Run", run, err)
	}
}

func TestDelegationTaskAdmissionReplaysSameTaskAndRunAfterBindFailureAndRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)
	store := newDelegatedTaskAdmissionStore(now)
	store.bindFailures = 1
	root := t.TempDir()
	manager, closeManager := openDelegatedTaskManager(t, root)
	defer func() {
		if err := closeManager(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	}()
	firstAdmission := newDelegatedTaskAdmission(t, store, manager, now, "shiro")
	first, err := firstAdmission.StartOne(ctx)
	if err == nil || !first.TaskEnsured || first.Bound || first.TaskID == "" || first.RunID == "" {
		t.Fatalf("first StartOne()=(%+v,%v), want Task committed but bind failure", first, err)
	}
	if store.retryCount != 1 {
		t.Fatalf("retry count=%d, want one lease retry after bind failure", store.retryCount)
	}
	if err := closeManager(); err != nil {
		t.Fatalf("close Task owner before restart: %v", err)
	}
	manager, closeManager = openDelegatedTaskManager(t, root)
	secondAdmission := newDelegatedTaskAdmission(t, store, manager, now, "shiro")
	second, err := secondAdmission.StartOne(ctx)
	if err != nil {
		t.Fatalf("replayed StartOne(): %v", err)
	}
	if !second.Bound || second.TaskID != first.TaskID || second.RunID != first.RunID {
		t.Fatalf("replayed Task/Run=(%s,%s), want original=(%s,%s); result=%+v", second.TaskID, second.RunID, first.TaskID, first.RunID, second)
	}
	if len(store.binds) != 1 || store.binds[0].TaskID != first.TaskID || store.loadCount != 5 {
		t.Fatalf("binds=%+v loads=%d, want one successful bind after two admission attempts", store.binds, store.loadCount)
	}
	allTasks, err := manager.List(ctx, domaintask.Filter{Limit: 10})
	if err != nil || len(allTasks) != 1 || allTasks[0].TaskID != first.TaskID {
		t.Fatalf("stored Tasks=%+v err=%v, want exactly the original Task", allTasks, err)
	}
}

func TestDelegationTaskAdmissionRechecksSourceImmediatelyBeforeTaskCommit(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	store := newDelegatedTaskAdmissionStore(now)
	store.mutateAtLoad = 3
	store.mutate = func(source *DelegatedTaskSource) { source.Readback.Text = "changed before Task commit" }
	manager, closeStore := openDelegatedTaskManager(t, t.TempDir())
	defer closeStore()
	admission := newDelegatedTaskAdmission(t, store, manager, now, "shiro")
	result, err := admission.StartOne(context.Background())
	if !errors.Is(err, ErrDelegationTaskAdmissionInvalid) || !result.Claimed || result.TaskEnsured {
		t.Fatalf("StartOne()=(%+v,%v), want pre-commit current-source rejection", result, err)
	}
	if store.loadCount != 3 {
		t.Fatalf("source loads=%d, want initial and both TaskManager checks", store.loadCount)
	}
	tasks, err := manager.List(context.Background(), domaintask.Filter{Limit: 10})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("Tasks after stale source rejection=%+v err=%v, want no Task commit", tasks, err)
	}
}

func TestDelegationTaskAdmissionRejectsWrongActorAndUnsupportedCapabilities(t *testing.T) {
	for _, test := range []struct {
		name    string
		actorID string
		mutate  func(*DelegatedTaskSource)
	}{
		{name: "wrong actor", actorID: "kuro"},
		{name: "remote owner", actorID: "shiro", mutate: func(source *DelegatedTaskSource) { source.Handoff.ToActor = "remote-agent" }},
		{name: "external capability", actorID: "shiro", mutate: func(source *DelegatedTaskSource) { source.Handoff.AllowedCapabilities = []string{"file_write"} }},
		{name: "unaccepted readback", actorID: "shiro", mutate: func(source *DelegatedTaskSource) {
			source.Readback.Status = conversation.ReadbackStatusNeedsClarification
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
			store := newDelegatedTaskAdmissionStore(now)
			if test.mutate != nil {
				test.mutate(&store.source)
			}
			manager, closeStore := openDelegatedTaskManager(t, t.TempDir())
			defer closeStore()
			admission := newDelegatedTaskAdmission(t, store, manager, now, test.actorID)
			result, err := admission.StartOne(context.Background())
			if err == nil || !result.Claimed || result.TaskEnsured {
				t.Fatalf("StartOne()=(%+v,%v), want fail-closed rejection", result, err)
			}
			tasks, listErr := manager.List(context.Background(), domaintask.Filter{Limit: 10})
			if listErr != nil || len(tasks) != 0 {
				t.Fatalf("Tasks after rejection=%+v err=%v, want no Task", tasks, listErr)
			}
		})
	}
}

type delegatedTaskAdmissionStoreFake struct {
	now          time.Time
	lease        DelegatedTaskLease
	source       DelegatedTaskSource
	loadCount    int
	mutateAtLoad int
	mutate       func(*DelegatedTaskSource)
	bindFailures int
	binds        []DelegatedTaskBindRequest
	retryCount   int
	claimCount   int
}

func newDelegatedTaskAdmissionStore(now time.Time) *delegatedTaskAdmissionStoreFake {
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	delegationID, messageID := modulecore.NewRequestID(), modulecore.NewMessageID()
	handoff := conversation.HandoffEnvelope{
		DelegationID: delegationID, Revision: 1, OriginTurnID: turnID, OriginUserMessageID: messageID,
		SessionID: sessionID, SourceTraceID: modulecore.NewTraceID(), FromActor: "mio", ToActor: "shiro",
		InstructionMessageID: modulecore.NewMessageID(), ContextSnapshotHash: strings.Repeat("a", 64),
		AllowedCapabilities: []string{}, ReplyTo: messageID, Goal: "Inspect the requested behavior",
		Instruction:    "Review the implementation and return evidence.",
		Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay in the assigned scope.", SourceMessageIDs: []modulecore.MessageID{messageID}}},
		ExpectedOutput: "Evidence and limitations",
	}
	readback := conversation.ReadbackEnvelope{
		DelegationID: delegationID, Revision: 1, Sender: "shiro", SchemaVersion: "agent.readback.v1",
		Status: conversation.ReadbackStatusAccepted, UnderstoodGoal: handoff.Goal,
		AcknowledgedConstraintKeys: []string{"keep_scope"}, ExpectedOutput: handoff.ExpectedOutput,
		Text: "I understand and accept the assignment.",
	}
	return &delegatedTaskAdmissionStoreFake{
		now: now,
		lease: DelegatedTaskLease{
			WorkID: fmt.Sprintf("start:%s:1", delegationID), DedupeKey: fmt.Sprintf("delegation:%s:1:start_task", delegationID),
			Kind: StartDelegatedTaskWorkKind, SourceEventID: modulecore.NewEventID(), SessionID: sessionID,
			Priority: 5, Attempt: 1, LeaseToken: "lease-1", LeaseExpires: now.Add(time.Minute), Version: 1, Assignee: "shiro",
		},
		source: DelegatedTaskSource{
			OwnerID: "user-1", Handoff: handoff, Readback: readback,
			Context: []DelegatedTaskContextMessage{{MessageID: messageID, Role: "user", Text: "Please inspect this behavior"}},
		},
	}
}

func (s *delegatedTaskAdmissionStoreFake) ClaimDelegatedTaskWork(_ context.Context, request DelegatedTaskClaimRequest) ([]DelegatedTaskLease, error) {
	s.claimCount++
	if request.Kind != StartDelegatedTaskWorkKind || request.Limit != 1 || request.LeaseDuration <= 0 {
		return nil, fmt.Errorf("unexpected task-start claim: %+v", request)
	}
	lease := s.lease
	lease.Attempt = s.claimCount
	lease.LeaseToken = fmt.Sprintf("lease-%d", s.claimCount)
	lease.Version = int64(s.claimCount)
	lease.LeaseExpires = request.Now.Add(time.Minute)
	s.lease = lease
	return []DelegatedTaskLease{lease}, nil
}

func (s *delegatedTaskAdmissionStoreFake) LoadDelegationTask(_ context.Context, lease DelegatedTaskLease, actorID string, now time.Time) (DelegatedTaskSource, error) {
	s.loadCount++
	if lease.LeaseToken != s.lease.LeaseToken || lease.Version != s.lease.Version || !s.lease.LeaseExpires.After(now) || actorID != s.lease.Assignee {
		return DelegatedTaskSource{}, errors.New("stale lease or wrong Actor")
	}
	source := s.source
	source.Context = append([]DelegatedTaskContextMessage(nil), source.Context...)
	source.Handoff.AllowedCapabilities = append([]string(nil), source.Handoff.AllowedCapabilities...)
	source.Handoff.Constraints = append([]conversation.AgentConstraint(nil), source.Handoff.Constraints...)
	if s.loadCount == s.mutateAtLoad && s.mutate != nil {
		s.mutate(&source)
	}
	return source, nil
}

func (s *delegatedTaskAdmissionStoreFake) BindDelegationTask(_ context.Context, request DelegatedTaskBindRequest) error {
	if request.Lease.LeaseToken != s.lease.LeaseToken || request.Lease.Version != s.lease.Version {
		return errors.New("stale bind lease")
	}
	if s.bindFailures > 0 {
		s.bindFailures--
		return errors.New("injected bind failure")
	}
	s.binds = append(s.binds, request)
	return nil
}

func (s *delegatedTaskAdmissionStoreFake) RetryDelegatedTaskWork(_ context.Context, lease DelegatedTaskLease, _ time.Time) error {
	if lease.LeaseToken != s.lease.LeaseToken || lease.Version != s.lease.Version {
		return errors.New("stale retry lease")
	}
	s.retryCount++
	return nil
}

func newDelegatedTaskAdmission(t *testing.T, store *delegatedTaskAdmissionStoreFake, manager *taskmanager.Manager, now time.Time, actorID string) *DelegationTaskAdmission {
	t.Helper()
	admission, err := NewDelegationTaskAdmission(DelegationTaskAdmissionDependencies{
		Store: store, Tasks: manager,
		Config: DelegationTaskAdmissionConfig{ActorID: actorID, Now: func() time.Time { return now }},
	})
	if err != nil {
		t.Fatalf("NewDelegationTaskAdmission(): %v", err)
	}
	return admission
}

func openDelegatedTaskManager(t *testing.T, root string) (*taskmanager.Manager, func() error) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(filepath.Clean(root))
	if err != nil {
		t.Fatalf("NewJSONLStore(): %v", err)
	}
	return taskmanager.New(store, taskmanager.DefaultParallelLimits()), store.Close
}
