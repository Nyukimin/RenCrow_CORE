package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GroupTask         = "task"
	taskFenceWaitCode = "task_execution_fence_wait"

	taskOpWriterGeneration = "writer_generation"
	taskOpTxBegin          = "tx_begin"
	taskOpTxCommand        = "tx_command"
	taskOpTxPrepareCommit  = "tx_prepare_commit"
	taskOpTxAbort          = "tx_abort"
	taskOpReadBegin        = "read_begin"
	taskOpReadCommand      = "read_command"
	taskOpReadEnd          = "read_end"
	taskOpCommit           = "commit"
	taskOpFenceReservation = "fence_reservation"
	taskOpFenceAcquire     = "fence_acquire"
	taskOpFenceRelease     = "fence_release"
)

var (
	errTaskGroupNotImplemented = errors.New("storagehost: task group is not implemented")
	errTaskGroupSessionExpired = errors.New("storagehost: task session expired or is no longer active")
)

// TaskGroupOwner is the closed owner contract for the CORE Task/Run store.
// The receipt methods commit the exact owner transaction and its protocol
// receipt in one batch; fence methods operate on exact durable capabilities.
type TaskGroupOwner interface {
	domaintask.Store
	ExecuteIdempotentTaskOperation(context.Context, modulecore.TaskID, string, string, func(domaintask.Store) (json.RawMessage, error)) (json.RawMessage, error)
	ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(domaintask.Store) (json.RawMessage, error)) (json.RawMessage, error)
	LookupTaskOperation(context.Context, string) (taskpersistence.TaskOperationReceipt, error)
	AcquireTaskExecutionFence(context.Context, modulecore.TaskID, string) error
	ReleaseTaskExecutionFence(context.Context, modulecore.TaskID, string) error
	GetTaskExecutionFence(context.Context, modulecore.TaskID) (taskpersistence.TaskExecutionFenceStatus, error)
}

// TaskStoreClient implements the domain Store contract over the storage host.
type TaskStoreClient struct{ client *Client }

var _ domaintask.Store = (*TaskStoreClient)(nil)

func NewTaskStoreClient(client *Client) (*TaskStoreClient, error) {
	if client == nil {
		return nil, errors.New("storagehost: task client is nil")
	}
	return &TaskStoreClient{client: client}, nil
}

// RegisterTaskGroup installs only fixed Task/Run transaction, read, and
// execution-fence operations. There is no arbitrary callback, SQL, or path API.
func RegisterTaskGroup(handler *Handler, owner TaskGroupOwner) error {
	return registerTaskGroupWithTimeout(handler, owner, defaultTaskGroupIdleTimeout)
}

func registerTaskGroupWithTimeout(handler *Handler, owner TaskGroupOwner, idleTimeout time.Duration) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: task group needs a handler and an owner store")
	}
	if idleTimeout <= 0 {
		return errors.New("storagehost: task session idle timeout must be positive")
	}
	state := newTaskGroupState(owner, idleTimeout)

	registrations := []struct {
		op string
		fn OperationFunc
	}{
		{taskOpWriterGeneration, func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := decodeTaskGroupPayload(raw, &emptyTaskPayload{}); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task writer-generation request rejected")
			}
			generation, err := owner.WriterGeneration()
			if err != nil || generation == 0 {
				return nil, NewError(ErrorCodeStoreUnavailable, "Task writer generation unavailable")
			}
			return generation, nil
		}},
		{taskOpTxBegin, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionBeginRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task transaction begin request rejected")
			}
			if request.Mode != taskSessionModeGlobal && request.Mode != taskSessionModeTask {
				return nil, NewError(ErrorCodeSchemaRejected, "Task transaction scope rejected")
			}
			if err := state.begin(ctx, request); err != nil {
				return nil, err
			}
			return taskSessionBeginResult{SessionID: request.SessionID, Status: "active"}, nil
		}},
		{taskOpTxCommand, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionCommandRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task transaction command rejected")
			}
			return state.command(ctx, request, false)
		}},
		{taskOpTxPrepareCommit, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionIDRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task transaction prepare request rejected")
			}
			return state.prepare(request.SessionID, false)
		}},
		{taskOpTxAbort, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionIDRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task transaction abort request rejected")
			}
			return nil, state.finish(ctx, request.SessionID, false, false, "")
		}},
		{taskOpReadBegin, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionBeginRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task read begin request rejected")
			}
			if request.Mode != taskSessionModeRead {
				return nil, NewError(ErrorCodeSchemaRejected, "Task read mode rejected")
			}
			if err := state.begin(ctx, request); err != nil {
				return nil, err
			}
			return taskSessionBeginResult{SessionID: request.SessionID, Status: "active"}, nil
		}},
		{taskOpReadCommand, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionCommandRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task read command rejected")
			}
			return state.command(ctx, request, true)
		}},
		{taskOpReadEnd, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskSessionIDRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task read end request rejected")
			}
			return nil, state.finish(ctx, request.SessionID, true, true, "")
		}},
		{taskOpFenceReservation, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request taskFenceReservationRequest
			if err := decodeTaskGroupPayload(raw, &request); err != nil {
				return nil, NewError(ErrorCodeSchemaRejected, "Task fence reservation request rejected")
			}
			return state.reserveFence(ctx, request)
		}},
	}

	for _, registration := range registrations {
		if err := handler.Register(GroupTask, registration.op, false, registration.fn); err != nil {
			return err
		}
	}
	if err := handler.RegisterRecoverable(GroupTask, taskOpCommit, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var request taskCommitRequest
		if err := decodeTaskGroupPayload(mutation.Payload, &request); err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Task commit request rejected"))
		}
		return state.commit(ctx, owner, mutation.OpID, request)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		return reconcileTaskCommit(ctx, owner, mutation)
	}); err != nil {
		return err
	}
	if err := handler.RegisterRecoverable(GroupTask, taskOpFenceAcquire, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var request taskFenceMutationRequest
		if err := decodeTaskGroupPayload(mutation.Payload, &request); err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Task fence acquire request rejected"))
		}
		if err := state.requireReservation(request.ReservationID, request.TaskID); err != nil {
			return nil, ownerRolledBack(err)
		}
		if err := owner.AcquireTaskExecutionFence(ctx, request.TaskID, request.FenceID); err != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task fence acquire could not be confirmed")
		}
		status, err := owner.GetTaskExecutionFence(ctx, request.TaskID)
		if err != nil || status.FenceID != request.FenceID {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task fence acquire could not be confirmed")
		}
		return status, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		return reconcileTaskFence(ctx, owner, mutation, true)
	}); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupTask, taskOpFenceRelease, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var request taskFenceMutationRequest
		if err := decodeTaskGroupPayload(mutation.Payload, &request); err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Task fence release request rejected"))
		}
		if err := state.requireReservation(request.ReservationID, request.TaskID); err != nil {
			return nil, ownerRolledBack(err)
		}
		if err := owner.ReleaseTaskExecutionFence(ctx, request.TaskID, request.FenceID); err != nil {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task fence release could not be confirmed")
		}
		status, err := owner.GetTaskExecutionFence(ctx, request.TaskID)
		if err != nil || status.FenceID != request.FenceID || status.State != taskpersistence.TaskExecutionFenceStateReleased {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task fence release could not be confirmed")
		}
		return status, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		return reconcileTaskFence(ctx, owner, mutation, false)
	})
}

func decodeTaskGroupPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("Task payload must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Task payload has trailing JSON")
	}
	return nil
}

type emptyTaskPayload struct{}

func taskContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *TaskStoreClient) WriterGeneration() (uint64, error) {
	ctx, cancel := taskContext(context.Background(), s.client.timeout)
	defer cancel()
	var generation uint64
	err := s.client.Call(ctx, GroupTask, taskOpWriterGeneration, emptyTaskPayload{}, &generation)
	return generation, err
}

func (s *TaskStoreClient) Transaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	return s.runWriteTransaction(ctx, taskSessionModeGlobal, "", fn)
}

func (s *TaskStoreClient) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	return s.runWriteTransaction(ctx, taskSessionModeTask, taskID, fn)
}

func (s *TaskStoreClient) runWriteTransaction(ctx context.Context, mode taskSessionMode, taskID modulecore.TaskID, fn func(domaintask.Store) error) (resultErr error) {
	if ctx == nil {
		return errors.New("task transaction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operation := &Operation{OpID: newOpID(), Group: GroupTask, Op: taskOpCommit}
	if !opIDPattern.MatchString(operation.OpID) {
		return errors.New("storagehost: generated Task operation id is invalid")
	}
	begin := taskSessionBeginRequest{SessionID: operation.OpID, Mode: mode, TaskID: taskID}
	if err := s.beginWriteSession(ctx, begin); err != nil {
		return err
	}
	view := &taskSessionStore{client: s.client, sessionID: operation.OpID, mode: mode, scopeTaskID: taskID, sequence: &taskCommandSequence{mu: make(chan struct{}, 1)}}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			s.abortSession(operation.OpID)
			panic(panicValue)
		}
	}()
	if err := fn(view); err != nil {
		s.abortSession(operation.OpID)
		return err
	}
	if err := ctx.Err(); err != nil {
		s.abortSession(operation.OpID)
		return err
	}
	var prepared taskSessionPreparedResult
	if err := s.callControlRetry(ctx, taskOpTxPrepareCommit, taskSessionIDRequest{SessionID: operation.OpID}, &prepared); err != nil {
		s.abortSession(operation.OpID)
		return err
	}
	commit := taskCommitRequest{SessionID: operation.OpID, Mode: mode, TaskID: taskID, TranscriptHash: prepared.TranscriptHash}
	operation.payload = mustJSON(commit)
	var committed taskCommitResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.client.Do(ctx, operation, &committed)
		if err == nil {
			if committed.SessionID != operation.OpID || committed.TranscriptHash != prepared.TranscriptHash {
				return NewError(ErrorCodeOutcomeUnknown, "Task commit receipt did not match the prepared transcript")
			}
			return nil
		}
		if !isOutcomeUnknown(err) || ctx.Err() != nil {
			return err
		}
		if handshakeErr := s.client.Handshake(ctx); handshakeErr != nil {
			return NewError(ErrorCodeOutcomeUnknown, "Task commit outcome is unresolved after host reconnect")
		}
		operation.mu.Lock()
		operation.pending = false
		operation.mu.Unlock()
	}
	return err
}

func (s *TaskStoreClient) beginWriteSession(ctx context.Context, begin taskSessionBeginRequest) error {
	for {
		err := s.callControlRetry(ctx, taskOpTxBegin, begin, nil)
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != taskFenceWaitCode {
			return err
		}
		if begin.Mode != taskSessionModeTask {
			return err
		}
		for {
			status, statusErr := s.getTaskExecutionFence(ctx, begin.TaskID)
			if errors.Is(statusErr, taskpersistence.ErrTaskExecutionFenceNotFound) || statusErr == nil && status.State == taskpersistence.TaskExecutionFenceStateReleased {
				break
			}
			if statusErr != nil {
				return NewError(ErrorCodeOutcomeUnknown, "Task execution fence status could not be resolved")
			}
			if status.State != taskpersistence.TaskExecutionFenceStateActive {
				return NewError(ErrorCodeOutcomeUnknown, "Task execution fence is uncertain")
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			}
		}
	}
}

func (s *TaskStoreClient) callControlRetry(ctx context.Context, op string, payload any, out any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = s.client.Call(ctx, GroupTask, op, payload, out)
		if err == nil || !isLostResponse(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (s *TaskStoreClient) abortSession(sessionID string) {
	ctx, cancel := taskContext(context.Background(), s.client.timeout)
	defer cancel()
	_ = s.callControlRetry(ctx, taskOpTxAbort, taskSessionIDRequest{SessionID: sessionID}, nil)
}

func (s *TaskStoreClient) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task read transaction callback is nil")
	}
	if ctx == nil {
		return errors.New("task read transaction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID := newOpID()
	if !opIDPattern.MatchString(sessionID) {
		return errors.New("storagehost: generated Task read session id is invalid")
	}
	begin := taskSessionBeginRequest{SessionID: sessionID, Mode: taskSessionModeRead}
	if err := s.callControlRetry(ctx, taskOpReadBegin, begin, nil); err != nil {
		return err
	}
	view := &taskSessionStore{client: s.client, sessionID: sessionID, mode: taskSessionModeRead, readOnly: true, sequence: &taskCommandSequence{mu: make(chan struct{}, 1)}}
	defer func() {
		ctx, cancel := taskContext(context.Background(), s.client.timeout)
		defer cancel()
		_ = s.callControlRetry(ctx, taskOpReadEnd, taskSessionIDRequest{SessionID: sessionID}, nil)
	}()
	return fn(view)
}

func (s *TaskStoreClient) WithTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fn func() error) error {
	if fn == nil {
		return errors.New("task execution fence callback is nil")
	}
	if ctx == nil {
		return errors.New("task execution fence context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fenceID := newOpID()
	reservationID := newOpID()
	if !opIDPattern.MatchString(fenceID) || !opIDPattern.MatchString(reservationID) {
		return errors.New("storagehost: generated Task fence capability is invalid")
	}
	reservation := taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, Action: "acquire"}
	if err := s.callControlRetry(ctx, taskOpFenceReservation, reservation, nil); err != nil {
		return err
	}
	acquireReq := taskFenceMutationRequest{ReservationID: reservationID, TaskID: taskID, FenceID: fenceID}
	var status taskpersistence.TaskExecutionFenceStatus
	acquireOp := s.client.NewOperation(GroupTask, taskOpFenceAcquire, acquireReq)
	if err := s.doFenceOperation(ctx, acquireOp, taskID, fenceID, true, &status); err != nil {
		s.releaseReservation(reservationID, taskID)
		return err
	}
	if status.FenceID != fenceID || status.State != taskpersistence.TaskExecutionFenceStateActive {
		s.releaseReservation(reservationID, taskID)
		return NewError(ErrorCodeOutcomeUnknown, "Task fence acquisition is not active")
	}
	callbackBegin := taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, FenceID: fenceID, Action: "callback_begin"}
	if err := s.callControlRetry(ctx, taskOpFenceReservation, callbackBegin, nil); err != nil {
		s.releaseReservation(reservationID, taskID)
		releaseErr := s.releaseExactFence(taskID, fenceID, newOpID(), ctx)
		return errors.Join(err, releaseErr)
	}
	s.releaseReservation(reservationID, taskID)

	var callbackErr error
	var callbackPanic any
	func() {
		defer func() { callbackPanic = recover() }()
		callbackErr = fn()
	}()
	reservationID = newOpID()
	reservation = taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, Action: "acquire"}
	reserveCtx := ctx
	if callbackErr != nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		reserveCtx, cancel = taskContext(context.Background(), s.client.timeout)
		defer cancel()
	}
	releaseErr := s.releaseExactFence(taskID, fenceID, reservationID, reserveCtx)
	if callbackPanic != nil {
		panic(callbackPanic)
	}
	if callbackErr != nil {
		return errors.Join(callbackErr, releaseErr)
	}
	return releaseErr
}

func (s *TaskStoreClient) releaseExactFence(taskID modulecore.TaskID, fenceID, reservationID string, ctx context.Context) error {
	if !opIDPattern.MatchString(reservationID) {
		return errors.New("storagehost: generated Task fence reservation id is invalid")
	}
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = taskContext(context.Background(), s.client.timeout)
		defer cancel()
	}
	reservation := taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, Action: "acquire"}
	if err := s.callControlRetry(ctx, taskOpFenceReservation, reservation, nil); err != nil {
		return err
	}
	releaseReq := taskFenceMutationRequest{ReservationID: reservationID, TaskID: taskID, FenceID: fenceID}
	releaseOp := s.client.NewOperation(GroupTask, taskOpFenceRelease, releaseReq)
	var released taskpersistence.TaskExecutionFenceStatus
	releaseErr := s.doFenceOperation(ctx, releaseOp, taskID, fenceID, false, &released)
	if releaseErr == nil {
		callbackEnd := taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, FenceID: fenceID, Action: "callback_end"}
		releaseErr = s.callControlRetry(ctx, taskOpFenceReservation, callbackEnd, nil)
	}
	s.releaseReservation(reservationID, taskID)
	return releaseErr
}

func (s *TaskStoreClient) doFenceOperation(ctx context.Context, operation *Operation, taskID modulecore.TaskID, fenceID string, acquire bool, out *taskpersistence.TaskExecutionFenceStatus) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.client.Do(ctx, operation, out)
		if err == nil {
			return nil
		}
		if !isOutcomeUnknown(err) || ctx.Err() != nil {
			break
		}
		var status taskpersistence.TaskExecutionFenceStatus
		lookupCtx, cancel := taskContext(context.Background(), s.client.timeout)
		status, lookupErr := s.getTaskExecutionFence(lookupCtx, taskID)
		cancel()
		if lookupErr == nil && status.FenceID == fenceID {
			if acquire || status.State == taskpersistence.TaskExecutionFenceStateReleased {
				*out = status
				return nil
			}
			if !acquire && status.State == taskpersistence.TaskExecutionFenceStateActive {
				// The exact capability is still active, so resending this same
				// stable release operation is safe; the external callback is not rerun.
			}
		} else if errors.Is(lookupErr, taskpersistence.ErrTaskExecutionFenceNotFound) && acquire {
			// Durable absence proves that this exact acquire did not land.
		} else {
			return NewError(ErrorCodeOutcomeUnknown, "Task execution fence outcome is uncertain; external work was not retried")
		}
		if handshakeErr := s.client.Handshake(ctx); handshakeErr != nil {
			return NewError(ErrorCodeOutcomeUnknown, "Task execution fence outcome is unresolved after host reconnect")
		}
		operation.mu.Lock()
		operation.pending = false
		operation.mu.Unlock()
	}
	return err
}

func (s *TaskStoreClient) releaseReservation(reservationID string, taskID modulecore.TaskID) {
	ctx, cancel := taskContext(context.Background(), s.client.timeout)
	defer cancel()
	request := taskFenceReservationRequest{ReservationID: reservationID, TaskID: taskID, Action: "release"}
	_ = s.callControlRetry(ctx, taskOpFenceReservation, request, nil)
}

func (s *TaskStoreClient) getTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID) (taskpersistence.TaskExecutionFenceStatus, error) {
	var result taskFenceStatusResult
	err := s.client.Call(ctx, GroupTask, taskOpFenceReservation, taskFenceReservationRequest{TaskID: taskID, Action: "status"}, &result)
	if err != nil {
		return taskpersistence.TaskExecutionFenceStatus{}, err
	}
	if !result.Found {
		return taskpersistence.TaskExecutionFenceStatus{}, taskpersistence.ErrTaskExecutionFenceNotFound
	}
	return result.Status, nil
}

func (s *TaskStoreClient) SaveTask(ctx context.Context, value domaintask.Task) error {
	return s.TaskTransaction(ctx, value.TaskID, func(tx domaintask.Store) error { return tx.SaveTask(ctx, value) })
}

func (s *TaskStoreClient) GetTask(ctx context.Context, id modulecore.TaskID) (value domaintask.Task, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		value, err = tx.GetTask(ctx, id)
		return err
	})
	return value, resultErr
}

func (s *TaskStoreClient) ListTasks(ctx context.Context, filter domaintask.Filter) (values []domaintask.Task, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		values, err = tx.ListTasks(ctx, filter)
		return err
	})
	return values, resultErr
}

func (s *TaskStoreClient) SaveRun(ctx context.Context, value domaintask.Run) error {
	return s.TaskTransaction(ctx, value.TaskID, func(tx domaintask.Store) error { return tx.SaveRun(ctx, value) })
}

func (s *TaskStoreClient) GetRun(ctx context.Context, id modulecore.RunID) (value domaintask.Run, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		value, err = tx.GetRun(ctx, id)
		return err
	})
	return value, resultErr
}

func (s *TaskStoreClient) ListRuns(ctx context.Context, filter domaintask.RunFilter) (values []domaintask.Run, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		values, err = tx.ListRuns(ctx, filter)
		return err
	})
	return values, resultErr
}

func (s *TaskStoreClient) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	return s.TaskTransaction(ctx, value.TaskID, func(tx domaintask.Store) error { return tx.SaveContext(ctx, value) })
}

func (s *TaskStoreClient) GetContext(ctx context.Context, id modulecore.TaskID) (value domaintask.SharedRoleContext, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		value, err = tx.GetContext(ctx, id)
		return err
	})
	return value, resultErr
}

func (s *TaskStoreClient) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	return s.TaskTransaction(ctx, value.TaskID, func(tx domaintask.Store) error { return tx.SaveNotification(ctx, value) })
}

func (s *TaskStoreClient) ListNotifications(ctx context.Context, limit int, interruptOnly bool) (values []domaintask.Notification, resultErr error) {
	resultErr = s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		var err error
		values, err = tx.ListNotifications(ctx, limit, interruptOnly)
		return err
	})
	return values, resultErr
}

func (s *TaskStoreClient) Close() error { return nil }

type taskSessionStore struct {
	client      *Client
	sessionID   string
	mode        taskSessionMode
	scopeTaskID modulecore.TaskID
	readOnly    bool
	sequence    *taskCommandSequence
}

type taskCommandSequence struct {
	mu   chan struct{}
	next uint64
}

func (s *taskSessionStore) command(ctx context.Context, kind string, payload any, out any) error {
	if ctx == nil {
		return errors.New("Task session command context is nil")
	}
	if s.sequence == nil {
		return errors.New("Task session sequence is unavailable")
	}
	select {
	case s.sequence.mu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sequence.mu }()
	encoded := mustJSON(payload)
	request := taskSessionCommandRequest{SessionID: s.sessionID, Sequence: s.sequence.next + 1, Kind: kind, Payload: encoded}
	var response taskSessionCommandResult
	clientOp := taskOpTxCommand
	if s.mode == taskSessionModeRead {
		clientOp = taskOpReadCommand
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = s.client.Call(ctx, GroupTask, clientOp, request, &response)
		if err == nil || !isLostResponse(err) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return err
	}
	if response.Sequence != request.Sequence {
		return NewError(ErrorCodeDuplicateConflict, "Task command response sequence mismatch")
	}
	s.sequence.next = request.Sequence
	if response.ErrorCode != "" {
		return taskCommandWireError{Code: response.ErrorCode, Message: response.ErrorMessage}
	}
	if out != nil && len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, out); err != nil {
			return NewError(ErrorCodeSchemaRejected, "Task command result rejected")
		}
	}
	return nil
}

func (s *taskSessionStore) WriterGeneration() (uint64, error) {
	ctx, cancel := taskContext(context.Background(), s.client.timeout)
	defer cancel()
	var generation uint64
	err := s.command(ctx, "writer_generation", emptyTaskPayload{}, &generation)
	return generation, err
}

func (s *taskSessionStore) Transaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	return fn(s)
}

func (s *taskSessionStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if s.mode != taskSessionModeTask || s.scopeTaskID != taskID {
		return errTaskGroupTaskScope
	}
	return fn(s)
}

func (s *taskSessionStore) WithTaskExecutionFence(context.Context, modulecore.TaskID, func() error) error {
	return errors.New("task execution fence cannot be entered from a transaction")
}

func (s *taskSessionStore) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if fn == nil {
		return errors.New("task read transaction callback is nil")
	}
	view := *s
	view.readOnly = true
	return fn(&view)
}

func (s *taskSessionStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	return s.command(ctx, "save_task", value, nil)
}
func (s *taskSessionStore) GetTask(ctx context.Context, id modulecore.TaskID) (value domaintask.Task, err error) {
	err = s.command(ctx, "get_task", taskIDPayload{TaskID: id}, &value)
	return value, err
}
func (s *taskSessionStore) ListTasks(ctx context.Context, filter domaintask.Filter) (values []domaintask.Task, err error) {
	err = s.command(ctx, "list_tasks", filter, &values)
	return values, err
}
func (s *taskSessionStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	return s.command(ctx, "save_run", value, nil)
}
func (s *taskSessionStore) GetRun(ctx context.Context, id modulecore.RunID) (value domaintask.Run, err error) {
	err = s.command(ctx, "get_run", taskRunIDPayload{RunID: id}, &value)
	return value, err
}
func (s *taskSessionStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) (values []domaintask.Run, err error) {
	err = s.command(ctx, "list_runs", filter, &values)
	return values, err
}
func (s *taskSessionStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	return s.command(ctx, "save_context", value, nil)
}
func (s *taskSessionStore) GetContext(ctx context.Context, id modulecore.TaskID) (value domaintask.SharedRoleContext, err error) {
	err = s.command(ctx, "get_context", taskIDPayload{TaskID: id}, &value)
	return value, err
}
func (s *taskSessionStore) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	if s.readOnly {
		return errTaskGroupReadOnly
	}
	return s.command(ctx, "save_notification", value, nil)
}
func (s *taskSessionStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) (values []domaintask.Notification, err error) {
	err = s.command(ctx, "list_notifications", taskNotificationListPayload{Limit: limit, InterruptOnly: interruptOnly}, &values)
	return values, err
}

var (
	errTaskGroupReadOnly  = errors.New("task transaction is read-only")
	errTaskGroupTaskScope = errors.New("task transaction task_id scope mismatch")
)

type taskCommandWireError struct{ Code, Message string }

func (e taskCommandWireError) Error() string {
	if e.Message != "" {
		return "storagehost: Task command " + e.Code + ": " + e.Message
	}
	return "storagehost: Task command " + e.Code
}
func (e taskCommandWireError) Unwrap() error {
	switch e.Code {
	case "not_found":
		return domaintask.ErrNotFound
	case "context_canceled":
		return context.Canceled
	case "deadline_exceeded":
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func taskErrorWire(err error) (string, string) {
	switch {
	case errors.Is(err, domaintask.ErrNotFound):
		return "not_found", ""
	case errors.Is(err, context.Canceled):
		return "context_canceled", ""
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded", ""
	default:
		return "owner_error", ""
	}
}
