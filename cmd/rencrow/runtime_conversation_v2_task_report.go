package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	responseFirstTaskLeaseDuration    = 2 * time.Minute
	responseFirstTaskDrainPeriod      = time.Second
	responseFirstTaskDrainTimeout     = 30 * time.Second
	responseFirstTaskDrainBatch       = 8
	responseFirstRecoveryPageSize     = 64
	responseFirstTaskLeaseReserve     = 15 * time.Second
	responseFirstTaskExecutionTimeout = responseFirstTaskLeaseDuration - responseFirstTaskLeaseReserve
	responseFirstReportDrainTimeout   = responseFirstTaskExecutionTimeout
	responseFirstRecoveryTimeout      = responseFirstTaskExecutionTimeout
)

var responseFirstTaskDrainShutdownWait = 5 * time.Second

var responseFirstTaskAssigneeOrder = []string{"shiro", "midori"}

type responseFirstTaskReportPersistence interface {
	ClaimWork(context.Context, eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error)
	LoadDelegationTask(context.Context, eventstore.DelegationTaskSourceInput) (eventstore.DelegationTaskSource, error)
	BindDelegationTask(context.Context, eventstore.BindDelegationTaskInput) error
	RetryWork(context.Context, eventstore.WorkRetryInput) error
	AcceptReport(context.Context, eventstore.ReportInput) (eventstore.AcceptedTurn, error)
}

type responseFirstPendingRecoveryStore interface {
	ReadPendingAcceptedV2SessionPage(context.Context, string, modulecore.SessionID, int) ([]modulecore.SessionID, bool, error)
}

type responseFirstDelegatedExecutionPersistence interface {
	responseFirstTaskReportPersistence
	LoadDelegatedExecution(context.Context, eventstore.DelegatedExecutionSourceInput) (eventstore.DelegatedExecutionSource, error)
}

// responseFirstDelegationTaskStore adapts only the EventStore calls needed to
// admit a saved readback into TaskManager. Lease identity and assignee remain
// EventStore-owned, and source loading revalidates the live lease.
type responseFirstDelegationTaskStore struct {
	store responseFirstTaskReportPersistence
}

func (s responseFirstDelegationTaskStore) ClaimDelegatedTaskWork(ctx context.Context, request conversationruntime.DelegatedTaskClaimRequest) ([]conversationruntime.DelegatedTaskLease, error) {
	claimed, err := s.store.ClaimWork(ctx, eventstore.WorkClaimInput{
		Kind: request.Kind, Assignee: request.Assignee, Limit: request.Limit,
		LeaseDuration: request.LeaseDuration, Now: request.Now,
	})
	if err != nil {
		return nil, err
	}
	leases := make([]conversationruntime.DelegatedTaskLease, 0, len(claimed))
	for _, item := range claimed {
		leases = append(leases, responseFirstDelegatedTaskLease(item))
	}
	return leases, nil
}

func (s responseFirstDelegationTaskStore) LoadDelegationTask(ctx context.Context, lease conversationruntime.DelegatedTaskLease, actorID string, now time.Time) (conversationruntime.DelegatedTaskSource, error) {
	loaded, err := s.store.LoadDelegationTask(ctx, eventstore.DelegationTaskSourceInput{
		WorkLeaseInput: eventstore.WorkLeaseInput{
			WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
		},
		SessionID: lease.SessionID, Assignee: actorID,
	})
	if err != nil {
		return conversationruntime.DelegatedTaskSource{}, err
	}
	if loaded.Work.WorkID != lease.WorkID || loaded.Work.LeaseToken != lease.LeaseToken || loaded.Work.Version != lease.Version ||
		loaded.Work.Assignee != actorID || loaded.Work.SessionID != lease.SessionID || loaded.Work.SourceEventID != lease.SourceEventID {
		return conversationruntime.DelegatedTaskSource{}, errors.New("loaded delegated Task source does not match its claimed lease")
	}
	contextMessages := make([]conversationruntime.DelegatedTaskContextMessage, 0, len(loaded.Context))
	for _, message := range loaded.Context {
		contextMessages = append(contextMessages, conversationruntime.DelegatedTaskContextMessage{
			MessageID: message.MessageID, Role: message.Role, Text: message.Text,
		})
	}
	return conversationruntime.DelegatedTaskSource{
		OwnerID: loaded.OwnerID, Handoff: loaded.Handoff, Readback: loaded.Readback, Context: contextMessages,
	}, nil
}

func (s responseFirstDelegationTaskStore) BindDelegationTask(ctx context.Context, request conversationruntime.DelegatedTaskBindRequest) error {
	return s.store.BindDelegationTask(ctx, eventstore.BindDelegationTaskInput{
		DelegationID: request.DelegationID, Revision: request.Revision, TaskID: request.TaskID,
		TaskOperationID: request.TaskOperationID, WorkID: request.Lease.WorkID,
		LeaseToken: request.Lease.LeaseToken, Version: request.Lease.Version, Now: request.Now,
	})
}

func (s responseFirstDelegationTaskStore) RetryDelegatedTaskWork(ctx context.Context, lease conversationruntime.DelegatedTaskLease, now time.Time) error {
	return s.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: eventstore.WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
	}})
}

func responseFirstDelegatedTaskLease(item eventstore.LeasedWorkItem) conversationruntime.DelegatedTaskLease {
	return conversationruntime.DelegatedTaskLease{
		WorkID: item.WorkID, DedupeKey: item.DedupeKey, Kind: item.Kind,
		SourceEventID: item.SourceEventID, SessionID: item.SessionID, Priority: item.Priority,
		Attempt: item.Attempt, LeaseToken: item.LeaseToken, LeaseExpires: item.LeaseExpiresAt,
		Version: item.Version, Assignee: item.Assignee,
	}
}

type responseFirstReportDeliveryJournal struct {
	store responseFirstTaskReportPersistence
}

func (j responseFirstReportDeliveryJournal) AcceptReport(ctx context.Context, input conversationruntime.ReportAcceptanceInput) (conversationruntime.AcceptedTurn, error) {
	accepted, err := j.store.AcceptReport(ctx, eventstore.ReportInput{
		Report: input.Report, TrustedSender: input.TrustedSender, EventID: input.EventID,
		MessageID: input.MessageID, TurnID: input.TurnID, TraceID: input.TraceID, OccurredAt: input.OccurredAt,
	})
	if err != nil {
		return conversationruntime.AcceptedTurn{}, err
	}
	return toConversationAcceptedTurn(accepted), nil
}

type responseFirstDelegatedExecutionStore struct {
	store responseFirstDelegatedExecutionPersistence
	mu    sync.Mutex
	next  int
}

func (s *responseFirstDelegatedExecutionStore) ClaimDelegatedExecutionWork(ctx context.Context, request conversationruntime.DelegatedExecutionClaimRequest) ([]conversationruntime.DelegatedExecutionLease, error) {
	assignees := responseFirstTaskAssigneeOrder
	s.mu.Lock()
	start := s.next % len(assignees)
	s.next = (s.next + 1) % len(assignees)
	s.mu.Unlock()
	for offset := 0; offset < len(assignees); offset++ {
		assignee := assignees[(start+offset)%len(assignees)]
		claimed, err := s.store.ClaimWork(ctx, eventstore.WorkClaimInput{
			Kind: request.Kind, Assignee: assignee, Limit: request.Limit,
			LeaseDuration: request.LeaseDuration, Now: request.Now,
		})
		if err != nil {
			return nil, err
		}
		if len(claimed) == 0 {
			continue
		}
		leases := make([]conversationruntime.DelegatedExecutionLease, 0, len(claimed))
		for _, item := range claimed {
			leases = append(leases, responseFirstDelegatedExecutionLease(item))
		}
		return leases, nil
	}
	return nil, nil
}

func (s *responseFirstDelegatedExecutionStore) LoadDelegatedExecution(ctx context.Context, lease conversationruntime.DelegatedExecutionLease, actorID string, now time.Time) (conversationruntime.DelegatedExecutionSource, error) {
	loaded, err := s.store.LoadDelegatedExecution(ctx, eventstore.DelegatedExecutionSourceInput{
		WorkLeaseInput: eventstore.WorkLeaseInput{WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now},
		SessionID:      lease.SessionID, Assignee: actorID,
	})
	if err != nil {
		return conversationruntime.DelegatedExecutionSource{}, err
	}
	if loaded.Work.WorkID != lease.WorkID || loaded.Work.LeaseToken != lease.LeaseToken || loaded.Work.Version != lease.Version ||
		loaded.Work.Assignee != actorID || loaded.Work.SessionID != lease.SessionID || loaded.Work.SourceEventID != lease.SourceEventID {
		return conversationruntime.DelegatedExecutionSource{}, errors.New("loaded delegated execution source does not match its claimed lease")
	}
	contextMessages := make([]conversationruntime.DelegatedTaskContextMessage, 0, len(loaded.Context))
	for _, message := range loaded.Context {
		contextMessages = append(contextMessages, conversationruntime.DelegatedTaskContextMessage{
			MessageID: message.MessageID, Role: message.Role, Text: message.Text,
		})
	}
	return conversationruntime.DelegatedExecutionSource{
		Lease: lease, OwnerID: loaded.OwnerID, TaskID: loaded.TaskID,
		Handoff: loaded.Handoff, Readback: loaded.Readback, Context: contextMessages,
	}, nil
}

func (s *responseFirstDelegatedExecutionStore) RetryDelegatedExecutionWork(ctx context.Context, lease conversationruntime.DelegatedExecutionLease, now time.Time) error {
	return s.store.RetryWork(ctx, eventstore.WorkRetryInput{WorkLeaseInput: eventstore.WorkLeaseInput{
		WorkID: lease.WorkID, LeaseToken: lease.LeaseToken, Version: lease.Version, Now: now,
	}})
}

func responseFirstDelegatedExecutionLease(item eventstore.LeasedWorkItem) conversationruntime.DelegatedExecutionLease {
	return conversationruntime.DelegatedExecutionLease{
		WorkID: item.WorkID, DedupeKey: item.DedupeKey, Kind: item.Kind,
		SourceEventID: item.SourceEventID, SessionID: item.SessionID, Priority: item.Priority,
		Attempt: item.Attempt, LeaseToken: item.LeaseToken, LeaseExpires: item.LeaseExpiresAt,
		Version: item.Version, Assignee: item.Assignee,
	}
}

// configureTaskReport wires the Task-owner admission and report-delivery
// applications only for the two currently supported readback Actors.
func (r *responseFirstConversationRuntime) configureTaskReport(tasks *taskmanager.Manager, configuredAssignees []string, shiro, midori conversationruntime.DelegatedPrivateWorker) error {
	if r == nil || r.store == nil || tasks == nil {
		return errors.New("response-first Task/report wiring requires a runtime, EventStore, and TaskManager")
	}
	persistence, ok := r.store.(responseFirstTaskReportPersistence)
	if !ok {
		return errors.New("response-first EventStore does not implement delegated Task/report persistence")
	}
	allowed := make(map[string]struct{}, len(configuredAssignees))
	for _, assignee := range configuredAssignees {
		allowed[assignee] = struct{}{}
	}
	taskStore := responseFirstDelegationTaskStore{store: persistence}
	admissions := make([]*conversationruntime.DelegationTaskAdmission, 0, len(responseFirstTaskAssigneeOrder))
	for _, actorID := range responseFirstTaskAssigneeOrder {
		if _, configured := allowed[actorID]; !configured {
			continue
		}
		if err := superagent.ValidateActorID(actorID); err != nil {
			return fmt.Errorf("configure delegated Task admission for %q: %w", actorID, err)
		}
		admission, err := conversationruntime.NewDelegationTaskAdmission(conversationruntime.DelegationTaskAdmissionDependencies{
			Store: taskStore, Tasks: tasks,
			Config: conversationruntime.DelegationTaskAdmissionConfig{ActorID: actorID},
		})
		if err != nil {
			return fmt.Errorf("configure delegated Task admission for %q: %w", actorID, err)
		}
		admissions = append(admissions, admission)
	}
	delivery, err := conversationruntime.NewReportDeliveryService(conversationruntime.ReportDeliveryDependencies{
		Tasks: tasks, Journal: responseFirstReportDeliveryJournal{store: persistence},
		BindAcceptedTurnContext: r.bindAcceptedReportTiming,
	})
	if err != nil {
		return fmt.Errorf("configure response-first report delivery: %w", err)
	}
	executionPersistence, ok := r.store.(responseFirstDelegatedExecutionPersistence)
	if !ok {
		return errors.New("response-first EventStore does not implement delegated execution persistence")
	}
	executor, err := conversationruntime.NewDelegatedTaskExecutor(conversationruntime.DelegatedTaskExecutionDependencies{
		Store: &responseFirstDelegatedExecutionStore{store: executionPersistence}, Tasks: tasks,
		Shiro: shiro, Midori: midori,
	})
	if err != nil {
		return fmt.Errorf("configure response-first delegated execution: %w", err)
	}
	r.taskAdmissions = admissions
	r.executor = executor
	r.reportDelivery = delivery
	if r.dispatchWake == nil {
		r.dispatchWake = make(chan struct{}, 1)
	}
	return nil
}

// startResponseFirstDrain owns a fixed set of v2 background lanes under one
// cancelable process context. Readback/Task admission, delegated execution,
// report delivery, canonical projection, and accepted-turn recovery can make
// progress independently; each lane claims durable owner work before acting.
func (r *responseFirstConversationRuntime) startResponseFirstDrain() func() {
	stop := r.startResponseFirstDrainWithStatus()
	if stop == nil {
		return nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), responseFirstTaskDrainShutdownWait)
		defer cancel()
		if err := stop(ctx); err != nil {
			log.Printf("response-first background drain shutdown incomplete error_type=%T", err)
		}
	}
}

// startResponseFirstDrainWithStatus returns a bounded stop handle so the
// timing batch can distinguish a drained background lane from a timeout.
func (r *responseFirstConversationRuntime) startResponseFirstDrainWithStatus() func(context.Context) error {
	if r == nil || (r.dispatcher == nil && len(r.taskAdmissions) == 0 && r.executor == nil && r.reportDelivery == nil && r.projection == nil && r.pendingRecoveryStore() == nil) {
		return nil
	}
	if r.dispatchWake == nil {
		r.dispatchWake = make(chan struct{}, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	executionWake := make(chan struct{}, 1)
	reportWake := make(chan struct{}, 1)
	recoveryWake := make(chan struct{}, 1)
	var projectionWake <-chan struct{}
	var lanes sync.WaitGroup
	startLane := func(name string, wake <-chan struct{}, timeout time.Duration, drain func(context.Context) (bool, error)) {
		lanes.Add(1)
		go func() {
			defer lanes.Done()
			runResponseFirstDrainLane(ctx, wake, timeout, drain, func(err error) {
				log.Printf("response-first %s drain deferred error_type=%T", name, err)
			})
		}()
	}

	// Readback commits its durable acceptance before Task admission can claim
	// the resulting work item. This lane never waits for private worker output.
	if r.dispatcher != nil || len(r.taskAdmissions) != 0 {
		startLane("readback/Task admission", r.dispatchWake, responseFirstTaskDrainTimeout, func(workCtx context.Context) (bool, error) {
			progressed, err := r.drainOneResponseFirstPriorityWork(workCtx)
			if progressed {
				signalResponseFirstDrainLane(executionWake)
			}
			return progressed, err
		})
	}

	if r.executor != nil {
		startLane("delegated Task execution", executionWake, responseFirstTaskExecutionTimeout, func(workCtx context.Context) (bool, error) {
			progressed, reportPending, err := r.drainOneResponseFirstExecutionWork(workCtx)
			if reportPending {
				// The complete Task report is already durable before StartOne
				// returns; only wake the independent report owner here.
				signalResponseFirstDrainLane(reportWake)
			}
			return progressed, err
		})
	}

	if r.reportDelivery != nil {
		startLane("delegated Task report", reportWake, responseFirstReportDrainTimeout, func(workCtx context.Context) (bool, error) {
			progressed, retryRecovery, err := r.drainOneResponseFirstReportWork(workCtx)
			if retryRecovery {
				signalResponseFirstDrainLane(recoveryWake)
			}
			return progressed, err
		})
	}
	if r.pendingRecoveryStore() != nil {
		lanes.Add(1)
		go func() {
			defer lanes.Done()
			r.runResponseFirstRecoveryLane(ctx, recoveryWake)
		}()
	}
	if r.projection != nil {
		projectionWake = r.projection.wake
		startLane("canonical conversation projection", projectionWake, responseFirstTaskDrainTimeout, func(workCtx context.Context) (bool, error) {
			return r.projection.drainOne(workCtx)
		})
	}
	go func() {
		lanes.Wait()
		close(done)
	}()
	r.signalReadbackDrain()
	signalResponseFirstDrainLane(executionWake)
	signalResponseFirstDrainLane(reportWake)
	if r.projection != nil {
		signalResponseFirstDrainLane(r.projection.wake)
	}
	return func(stopCtx context.Context) error {
		if stopCtx == nil {
			stopCtx = context.Background()
		}
		cancel()
		timer := time.NewTimer(responseFirstTaskDrainShutdownWait)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		default:
		}
		select {
		case <-done:
			return nil
		case <-stopCtx.Done():
			return stopCtx.Err()
		case <-timer.C:
			return context.DeadlineExceeded
		}
	}
}

func runResponseFirstDrainLane(
	ctx context.Context,
	wake <-chan struct{},
	timeout time.Duration,
	drain func(context.Context) (bool, error),
	onError func(error),
) {
	if ctx == nil || drain == nil || timeout <= 0 {
		return
	}
	ticker := time.NewTicker(responseFirstTaskDrainPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		for batch := 0; batch < responseFirstTaskDrainBatch; batch++ {
			if err := ctx.Err(); err != nil {
				return
			}
			workCtx, workCancel := context.WithTimeout(ctx, timeout)
			progressed, err := drain(workCtx)
			workCancel()
			if err != nil {
				if onError != nil {
					onError(err)
				}
				break
			}
			if !progressed {
				break
			}
		}
	}
}

func signalResponseFirstDrainLane(wake chan<- struct{}) {
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *responseFirstConversationRuntime) runResponseFirstRecoveryLane(ctx context.Context, wake <-chan struct{}) {
	ticker := time.NewTicker(responseFirstTaskDrainPeriod)
	defer ticker.Stop()
	pending := true
	for {
		if pending {
			workCtx, workCancel := context.WithTimeout(ctx, responseFirstRecoveryTimeout)
			err := r.ResumeAllPendingAcceptedTurns(workCtx)
			workCancel()
			if err != nil {
				log.Printf("response-first accepted-turn recovery deferred error_type=%T", err)
			} else {
				pending = false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
			pending = true
		}
	}
}

func (r *responseFirstConversationRuntime) pendingRecoveryStore() responseFirstPendingRecoveryStore {
	if r == nil || r.store == nil {
		return nil
	}
	store, _ := r.store.(responseFirstPendingRecoveryStore)
	return store
}

func (r *responseFirstConversationRuntime) ResumeAllPendingAcceptedTurns(ctx context.Context) error {
	if r == nil || r.store == nil || ctx == nil {
		return errors.New("response-first recovery runtime is unavailable")
	}
	recovery := r.pendingRecoveryStore()
	if recovery == nil {
		return errors.New("response-first EventStore cannot enumerate pending v2 sessions")
	}
	cursor := modulecore.SessionID("")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sessions, hasMore, err := recovery.ReadPendingAcceptedV2SessionPage(ctx, r.ownerID, cursor, responseFirstRecoveryPageSize)
		if err != nil {
			return fmt.Errorf("read pending accepted v2 session page: %w", err)
		}
		for _, sessionID := range sessions {
			if err := r.ResumePendingAcceptedTurns(ctx, sessionID); err != nil {
				return err
			}
		}
		if !hasMore {
			return nil
		}
		if len(sessions) == 0 {
			return errors.New("pending accepted v2 session page reports more data without a cursor")
		}
		cursor = sessions[len(sessions)-1]
	}
}

func (r *responseFirstConversationRuntime) drainOneResponseFirstPriorityWork(ctx context.Context) (bool, error) {
	progressed := false
	if r.dispatcher != nil {
		result, err := r.dispatcher.DispatchOne(ctx)
		if result.Claimed {
			progressed = true
		}
		if err != nil {
			return progressed, fmt.Errorf("dispatch one readback before Task admission: %w", err)
		}
	}
	for _, admission := range r.taskAdmissions {
		result, err := admission.StartOne(ctx)
		if result.Claimed {
			progressed = true
		}
		if err != nil {
			return progressed, fmt.Errorf("admit one delegated Task: %w", err)
		}
	}
	return progressed, nil
}

func (r *responseFirstConversationRuntime) drainOneResponseFirstExecutionWork(ctx context.Context) (bool, bool, error) {
	if r.executor != nil {
		result, err := r.executor.StartOne(ctx)
		if err != nil {
			return result.Claimed, result.ReportPending, fmt.Errorf("execute one delegated Task: %w", err)
		}
		return result.Claimed, result.ReportPending, nil
	}
	return false, false, nil
}

func (r *responseFirstConversationRuntime) drainOneResponseFirstReportWork(ctx context.Context) (bool, bool, error) {
	if r.reportDelivery == nil {
		return false, false, nil
	}
	delivered, err := r.reportDelivery.DeliverOne(ctx)
	if err != nil {
		return false, true, fmt.Errorf("deliver one delegated Task report: %w", err)
	}
	if !delivered.Delivered {
		return false, false, nil
	}
	if err := r.publisher.PublishAcceptedInput(ctx, delivered.AcceptedTurn.InputEvent); err != nil {
		log.Printf("response-first accepted report publication pending session_id=%s turn_id=%s error_type=%T", delivered.AcceptedTurn.SessionID, delivered.AcceptedTurn.TurnID, err)
	}
	accepted := viewer.V2AcceptedTurn{
		SessionID: delivered.AcceptedTurn.SessionID, TurnID: delivered.AcceptedTurn.TurnID,
		TraceID: delivered.AcceptedTurn.InputEvent.TraceID, InputMessageID: delivered.AcceptedTurn.InputMessageID,
		ClientSubmissionID: delivered.AcceptedTurn.ClientSubmissionID, ActorID: delivered.AcceptedTurn.ActorID,
	}
	processCtx := delivered.ProcessContext
	if processCtx == nil {
		processCtx = ctx
	}
	if err := r.processAcceptedReport(processCtx, accepted); err != nil {
		return true, true, fmt.Errorf("process delivered report through Mio response-first runtime: %w", err)
	}
	return true, false, nil
}

var _ conversationruntime.ReportDeliveryTaskOwner = (*taskmanager.Manager)(nil)
