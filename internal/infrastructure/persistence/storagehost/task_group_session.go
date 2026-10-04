package storagehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	defaultTaskGroupIdleTimeout = 30 * time.Second
	maxTaskGroupSessionLifetime = 10 * time.Minute
	maxTaskGroupSessions        = 32
	maxTaskGroupCommandBytes    = 1 << 20
	maxTaskGroupTranscriptBytes = 8 << 20
	maxTaskGroupSessionCommands = 10000
)

type taskSessionMode string

const (
	taskSessionModeGlobal taskSessionMode = "global"
	taskSessionModeTask   taskSessionMode = "task"
	taskSessionModeRead   taskSessionMode = "read"
)

type taskSessionBeginRequest struct {
	SessionID string            `json:"session_id"`
	Mode      taskSessionMode   `json:"mode"`
	TaskID    modulecore.TaskID `json:"task_id,omitempty"`
}

type taskSessionBeginResult struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type taskSessionIDRequest struct {
	SessionID string `json:"session_id"`
}

type taskSessionCommandRequest struct {
	SessionID string          `json:"session_id"`
	Sequence  uint64          `json:"sequence"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

type taskSessionCommandResult struct {
	Sequence     uint64          `json:"sequence"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

type taskSessionPreparedResult struct {
	TranscriptHash string `json:"transcript_hash"`
}

type taskCommitRequest struct {
	SessionID      string            `json:"session_id"`
	Mode           taskSessionMode   `json:"mode"`
	TaskID         modulecore.TaskID `json:"task_id,omitempty"`
	TranscriptHash string            `json:"transcript_hash"`
}

type taskCommitResult struct {
	SessionID      string `json:"session_id"`
	TranscriptHash string `json:"transcript_hash"`
}

type taskFenceReservationRequest struct {
	ReservationID string            `json:"reservation_id,omitempty"`
	TaskID        modulecore.TaskID `json:"task_id"`
	FenceID       string            `json:"fence_id,omitempty"`
	Action        string            `json:"action"`
}

type taskFenceReservationResult struct {
	Active bool `json:"active"`
}

type taskFenceStatusResult struct {
	Found  bool                                     `json:"found"`
	Status taskpersistence.TaskExecutionFenceStatus `json:"status,omitempty"`
}

type taskFenceMutationRequest struct {
	ReservationID string            `json:"reservation_id"`
	TaskID        modulecore.TaskID `json:"task_id"`
	FenceID       string            `json:"fence_id"`
}

type taskIDPayload struct {
	TaskID modulecore.TaskID `json:"task_id"`
}

type taskRunIDPayload struct {
	RunID modulecore.RunID `json:"run_id"`
}

type taskNotificationListPayload struct {
	Limit         int  `json:"limit"`
	InterruptOnly bool `json:"interrupt_only"`
}

type taskGroupState struct {
	owner       TaskGroupOwner
	idleTimeout time.Duration

	gate           chan struct{}
	mu             sync.Mutex
	sessions       map[string]*taskGroupSession
	reservations   map[string]*taskFenceReservation
	fenceCallbacks map[modulecore.TaskID]string
}

type taskFenceReservation struct {
	taskID modulecore.TaskID
	active bool
}

type taskGroupSession struct {
	state       *taskGroupState
	begin       taskSessionBeginRequest
	fingerprint string
	ctx         context.Context
	cancel      context.CancelFunc
	createdAt   time.Time
	closedAt    time.Time

	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}
	finished  chan taskSessionFinish
	activity  chan struct{}

	commandMu     sync.Mutex
	readPublishMu sync.Mutex
	readExpired   bool
	store         domaintask.Store
	storeErr      error
	prepared      bool
	preparedHash  string
	sequence      uint64
	commandBytes  int
	transcript    [sha256.Size]byte
	commands      map[uint64]taskSessionCommandCache

	result taskCommitResult
	err    error
}

type taskSessionFinish struct {
	commit     bool
	readEnd    bool
	transcript string
}

type taskSessionCommandCache struct {
	fingerprint string
	result      taskSessionCommandResult
}

func newTaskGroupState(owner TaskGroupOwner, idleTimeout time.Duration) *taskGroupState {
	return &taskGroupState{
		owner:          owner,
		idleTimeout:    idleTimeout,
		gate:           make(chan struct{}, 1),
		sessions:       make(map[string]*taskGroupSession),
		reservations:   make(map[string]*taskFenceReservation),
		fenceCallbacks: make(map[modulecore.TaskID]string),
	}
}

func (s *taskGroupState) acquireGate(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *taskGroupState) releaseGate() {
	<-s.gate
}

func (s *taskGroupState) begin(requestContext context.Context, request taskSessionBeginRequest) error {
	if requestContext == nil {
		return NewError(ErrorCodeSchemaRejected, "Task session request context is missing")
	}
	if !opIDPattern.MatchString(request.SessionID) {
		return NewError(ErrorCodeSchemaRejected, "Task session id rejected")
	}
	switch request.Mode {
	case taskSessionModeTask:
		if err := request.TaskID.Validate(); err != nil {
			return NewError(ErrorCodeSchemaRejected, "Task session TaskID rejected")
		}
	case taskSessionModeGlobal, taskSessionModeRead:
		if request.TaskID != "" {
			return NewError(ErrorCodeSchemaRejected, "global Task session must not have a TaskID")
		}
	default:
		return NewError(ErrorCodeSchemaRejected, "Task session mode rejected")
	}
	fingerprint := taskSessionFingerprint(request)
	s.mu.Lock()
	if existing := s.sessions[request.SessionID]; existing != nil {
		same := existing.fingerprint == fingerprint
		active := !isSessionDone(existing)
		s.mu.Unlock()
		if !same {
			return NewError(ErrorCodeDuplicateConflict, "Task session id was reused with a different scope or mode")
		}
		if !active {
			return NewError(ErrorCodeOutcomeUnknown, "Task session has already ended")
		}
		return nil
	}
	if activeSessionCount(s.sessions) >= maxTaskGroupSessions {
		s.mu.Unlock()
		return NewError(ErrorCodeStoreUnavailable, "Task session capacity is full")
	}
	s.mu.Unlock()

	if err := s.acquireGate(requestContext); err != nil {
		return err
	}
	if request.Mode == taskSessionModeTask {
		status, err := s.owner.GetTaskExecutionFence(requestContext, request.TaskID)
		if errors.Is(err, taskpersistence.ErrTaskExecutionFenceNotFound) {
			// No durable fence prevents beginning a task-scoped owner transaction.
		} else if err != nil {
			s.releaseGate()
			return NewError(ErrorCodeStoreUnavailable, "Task execution fence status unavailable")
		} else if status.State == taskpersistence.TaskExecutionFenceStateActive {
			s.mu.Lock()
			localCallback := s.fenceCallbacks[request.TaskID] == status.FenceID
			s.mu.Unlock()
			s.releaseGate()
			if localCallback {
				return NewError(taskFenceWaitCode, "Task transaction waits for the active local execution fence")
			}
			return NewError(ErrorCodeOutcomeUnknown, "Task execution fence is active without a local callback; Task mutation remains blocked")
		} else if status.State != taskpersistence.TaskExecutionFenceStateReleased {
			s.releaseGate()
			return NewError(ErrorCodeOutcomeUnknown, "Task execution fence state is uncertain")
		}
	}
	s.mu.Lock()
	if existing := s.sessions[request.SessionID]; existing != nil {
		same := existing.fingerprint == fingerprint
		active := !isSessionDone(existing)
		s.mu.Unlock()
		s.releaseGate()
		if !same {
			return NewError(ErrorCodeDuplicateConflict, "Task session id was reused with a different scope or mode")
		}
		if !active {
			return NewError(ErrorCodeOutcomeUnknown, "Task session has already ended")
		}
		return nil
	}
	if activeSessionCount(s.sessions) >= maxTaskGroupSessions {
		s.mu.Unlock()
		s.releaseGate()
		return NewError(ErrorCodeStoreUnavailable, "Task session capacity is full")
	}
	s.pruneSessionsLocked(time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), maxTaskGroupSessionLifetime)
	session := &taskGroupSession{
		state: s, begin: request, fingerprint: fingerprint,
		ctx: ctx, cancel: cancel, createdAt: time.Now(),
		ready: make(chan struct{}), done: make(chan struct{}),
		finished: make(chan taskSessionFinish, 1), activity: make(chan struct{}, 1),
		commands: make(map[uint64]taskSessionCommandCache),
	}
	s.sessions[request.SessionID] = session
	s.mu.Unlock()
	go session.run()
	return nil
}

func (s *taskGroupState) pruneSessionsLocked(now time.Time) {
	for id, session := range s.sessions {
		if !session.closedAt.IsZero() && now.Sub(session.closedAt) > maxTaskGroupSessionLifetime {
			delete(s.sessions, id)
		}
	}
}

func activeSessionCount(sessions map[string]*taskGroupSession) int {
	count := 0
	for _, session := range sessions {
		if !isSessionDone(session) {
			count++
		}
	}
	return count
}

func isSessionDone(session *taskGroupSession) bool {
	select {
	case <-session.done:
		return true
	default:
		return false
	}
}

func taskSessionFingerprint(request taskSessionBeginRequest) string {
	encoded := mustJSON(request)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (session *taskGroupSession) run() {
	defer func() {
		session.cancel()
		session.state.releaseGate()
		session.state.mu.Lock()
		session.closedAt = time.Now()
		session.state.mu.Unlock()
		close(session.done)
	}()
	if session.begin.Mode == taskSessionModeRead {
		err := session.state.owner.ReadTransaction(session.ctx, func(store domaintask.Store) error {
			session.setStore(store)
			finish, err := session.waitFinish()
			if err != nil {
				return err
			}
			session.expireReadPublication()
			if !finish.readEnd {
				return ownerRolledBack(errTaskGroupSessionExpired)
			}
			return nil
		})
		session.expireReadPublication()
		session.setDoneError(err)
		return
	}

	var result json.RawMessage
	var err error
	callback := func(store domaintask.Store) (json.RawMessage, error) {
		session.setStore(store)
		finish, waitErr := session.waitFinish()
		if waitErr != nil {
			return nil, ownerRolledBack(waitErr)
		}
		if !finish.commit {
			return nil, ownerRolledBack(errTaskGroupSessionExpired)
		}
		expected, transcriptErr := session.transcriptHash()
		if transcriptErr != nil || expected != finish.transcript {
			return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "Task command transcript changed before commit"))
		}
		committed := taskCommitResult{SessionID: session.begin.SessionID, TranscriptHash: expected}
		return json.Marshal(committed)
	}
	if session.begin.Mode == taskSessionModeTask {
		result, err = session.state.owner.ExecuteIdempotentTaskOperation(session.ctx, session.begin.TaskID, session.begin.SessionID, session.beginRequestHash(), callback)
	} else {
		result, err = session.state.owner.ExecuteIdempotentGlobalTaskOperation(session.ctx, session.begin.SessionID, session.beginRequestHash(), callback)
	}
	if err != nil {
		session.setDoneError(err)
		return
	}
	var committed taskCommitResult
	if decodeErr := json.Unmarshal(result, &committed); decodeErr != nil || committed.SessionID != session.begin.SessionID {
		session.setDoneError(NewError(ErrorCodeOutcomeUnknown, "Task owner receipt result is invalid"))
		return
	}
	session.commandMu.Lock()
	currentTranscript := hex.EncodeToString(session.transcript[:])
	if committed.TranscriptHash != currentTranscript {
		session.err = NewError(ErrorCodeDuplicateConflict, "Task owner receipt transcript mismatch")
		session.commandMu.Unlock()
		return
	}
	session.result = committed
	session.commandMu.Unlock()
}

func (session *taskGroupSession) setStore(store domaintask.Store) {
	session.commandMu.Lock()
	session.store = store
	session.commandMu.Unlock()
	session.readyOnce.Do(func() { close(session.ready) })
	session.signalActivity()
}

func (session *taskGroupSession) setDoneError(err error) {
	session.commandMu.Lock()
	session.err = err
	session.commandMu.Unlock()
	session.readyOnce.Do(func() { close(session.ready) })
}

func (session *taskGroupSession) beginRequestHash() string {
	encoded := mustJSON(struct {
		SessionID string            `json:"session_id"`
		Mode      taskSessionMode   `json:"mode"`
		TaskID    modulecore.TaskID `json:"task_id,omitempty"`
	}{session.begin.SessionID, session.begin.Mode, session.begin.TaskID})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (session *taskGroupSession) waitFinish() (taskSessionFinish, error) {
	timer := time.NewTimer(session.state.idleTimeout)
	defer timer.Stop()
	for {
		select {
		case finish := <-session.finished:
			return finish, nil
		case <-session.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(session.state.idleTimeout)
		case <-timer.C:
			session.expireReadPublication()
			return taskSessionFinish{}, errTaskGroupSessionExpired
		case <-session.ctx.Done():
			session.expireReadPublication()
			return taskSessionFinish{}, session.ctx.Err()
		}
	}
}

func (session *taskGroupSession) signalActivity() {
	select {
	case session.activity <- struct{}{}:
	default:
	}
}

func (session *taskGroupSession) expireReadPublication() {
	if session.begin.Mode != taskSessionModeRead {
		return
	}
	session.readPublishMu.Lock()
	session.readExpired = true
	session.readPublishMu.Unlock()
}

func (session *taskGroupSession) publishReadResult(publish func()) bool {
	if session.begin.Mode != taskSessionModeRead {
		publish()
		return true
	}
	session.readPublishMu.Lock()
	defer session.readPublishMu.Unlock()
	if session.readExpired {
		return false
	}
	publish()
	return true
}

func (session *taskGroupSession) readPublicationExpired() bool {
	if session.begin.Mode != taskSessionModeRead {
		return false
	}
	session.readPublishMu.Lock()
	defer session.readPublishMu.Unlock()
	return session.readExpired
}

func (session *taskGroupSession) transcriptHash() (string, error) {
	session.commandMu.Lock()
	defer session.commandMu.Unlock()
	return hex.EncodeToString(session.transcript[:]), nil
}

func (state *taskGroupState) session(id string) (*taskGroupSession, error) {
	if !opIDPattern.MatchString(id) {
		return nil, NewError(ErrorCodeSchemaRejected, "Task session id rejected")
	}
	state.mu.Lock()
	session := state.sessions[id]
	state.mu.Unlock()
	if session == nil {
		return nil, NewError(ErrorCodeOutcomeUnknown, "Task session is not present on this host")
	}
	return session, nil
}

func (state *taskGroupState) command(ctx context.Context, request taskSessionCommandRequest, readCommand bool) (taskSessionCommandResult, error) {
	session, err := state.session(request.SessionID)
	if err != nil {
		return taskSessionCommandResult{}, err
	}
	if readCommand != (session.begin.Mode == taskSessionModeRead) {
		return taskSessionCommandResult{}, NewError(ErrorCodeDuplicateConflict, "Task command session mode mismatch")
	}
	if request.Sequence == 0 || request.Sequence > maxTaskGroupSessionCommands || len(request.Payload) == 0 || len(request.Payload) > maxTaskGroupCommandBytes || !json.Valid(request.Payload) {
		return taskSessionCommandResult{}, NewError(ErrorCodeSchemaRejected, "Task command sequence or payload rejected")
	}
	if !validTaskCommandKind(request.Kind) {
		return taskSessionCommandResult{}, NewError(ErrorCodeSchemaRejected, "Task command kind is not served")
	}
	if err := session.waitReady(ctx); err != nil {
		return taskSessionCommandResult{}, err
	}
	requestFingerprint := taskCommandFingerprint(request.Kind, request.Payload)
	session.commandMu.Lock()
	defer session.commandMu.Unlock()
	if session.readPublicationExpired() {
		return taskSessionCommandResult{}, NewError(ErrorCodeOutcomeUnknown, "Task read session expired before command publication")
	}
	if cached, exists := session.commands[request.Sequence]; exists {
		if cached.fingerprint != requestFingerprint {
			return taskSessionCommandResult{}, NewError(ErrorCodeDuplicateConflict, "Task command sequence was replayed with different content")
		}
		var replay taskSessionCommandResult
		if !session.publishReadResult(func() { replay = cloneTaskCommandResult(cached.result) }) {
			return taskSessionCommandResult{}, NewError(ErrorCodeOutcomeUnknown, "Task read session expired before cached result publication")
		}
		return replay, nil
	}
	if isSessionDone(session) || session.store == nil {
		return taskSessionCommandResult{}, NewError(ErrorCodeOutcomeUnknown, "Task session ended before command execution")
	}
	if session.prepared {
		return taskSessionCommandResult{}, NewError(ErrorCodeDuplicateConflict, "Task session is already prepared for commit")
	}
	if request.Sequence != session.sequence+1 {
		return taskSessionCommandResult{}, NewError(ErrorCodeDuplicateConflict, "Task command sequence is not the next sequence")
	}
	if session.commandBytes+len(request.Payload) > maxTaskGroupTranscriptBytes {
		return taskSessionCommandResult{}, NewError(ErrorCodeSchemaRejected, "Task session transcript exceeds the payload limit")
	}
	result, commandErr := executeTaskCommand(ctx, session.store, request.Kind, request.Payload)
	response := taskSessionCommandResult{Sequence: request.Sequence}
	if commandErr != nil {
		response.ErrorCode, response.ErrorMessage = taskErrorWire(commandErr)
	} else {
		response.Result = mustJSON(result)
	}
	if !session.publishReadResult(func() {
		encodedTranscript := mustJSON(struct {
			Sequence uint64          `json:"sequence"`
			Kind     string          `json:"kind"`
			Payload  json.RawMessage `json:"payload"`
		}{request.Sequence, request.Kind, append(json.RawMessage(nil), request.Payload...)})
		hasher := sha256.New()
		_, _ = hasher.Write(session.transcript[:])
		_, _ = hasher.Write(encodedTranscript)
		copy(session.transcript[:], hasher.Sum(nil))
		session.sequence = request.Sequence
		session.commandBytes += len(request.Payload)
		session.commands[request.Sequence] = taskSessionCommandCache{fingerprint: requestFingerprint, result: cloneTaskCommandResult(response)}
		session.signalActivity()
	}) {
		return taskSessionCommandResult{}, NewError(ErrorCodeOutcomeUnknown, "Task read session expired before command result publication")
	}
	return response, nil
}

func (session *taskGroupSession) waitReady(ctx context.Context) error {
	select {
	case <-session.ready:
		if err := session.ctx.Err(); err != nil {
			return NewError(ErrorCodeOutcomeUnknown, "Task session ended before becoming ready")
		}
		return nil
	case <-session.done:
		return NewError(ErrorCodeOutcomeUnknown, "Task session ended before becoming ready")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func taskCommandFingerprint(kind string, payload json.RawMessage) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(kind))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(payload)
	return hex.EncodeToString(hasher.Sum(nil))
}

func cloneTaskCommandResult(result taskSessionCommandResult) taskSessionCommandResult {
	result.Result = append(json.RawMessage(nil), result.Result...)
	return result
}

func validTaskCommandKind(kind string) bool {
	switch kind {
	case "writer_generation", "save_task", "get_task", "list_tasks", "save_run", "get_run", "list_runs", "save_context", "get_context", "save_notification", "list_notifications":
		return true
	default:
		return false
	}
}

func executeTaskCommand(ctx context.Context, store domaintask.Store, kind string, payload json.RawMessage) (any, error) {
	switch kind {
	case "writer_generation":
		var request emptyTaskPayload
		if err := decodeTaskGroupPayload(payload, &request); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task writer-generation command rejected")
		}
		return store.WriterGeneration()
	case "save_task":
		var value domaintask.Task
		if err := decodeTaskGroupPayload(payload, &value); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task save command rejected")
		}
		return nil, store.SaveTask(ctx, value)
	case "get_task":
		var request taskIDPayload
		if err := decodeTaskGroupPayload(payload, &request); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task get command rejected")
		}
		return store.GetTask(ctx, request.TaskID)
	case "list_tasks":
		var filter domaintask.Filter
		if err := decodeTaskGroupPayload(payload, &filter); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task list command rejected")
		}
		return store.ListTasks(ctx, filter)
	case "save_run":
		var value domaintask.Run
		if err := decodeTaskGroupPayload(payload, &value); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Run save command rejected")
		}
		return nil, store.SaveRun(ctx, value)
	case "get_run":
		var request taskRunIDPayload
		if err := decodeTaskGroupPayload(payload, &request); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Run get command rejected")
		}
		return store.GetRun(ctx, request.RunID)
	case "list_runs":
		var filter domaintask.RunFilter
		if err := decodeTaskGroupPayload(payload, &filter); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Run list command rejected")
		}
		return store.ListRuns(ctx, filter)
	case "save_context":
		var value domaintask.SharedRoleContext
		if err := decodeTaskGroupPayload(payload, &value); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task context save command rejected")
		}
		return nil, store.SaveContext(ctx, value)
	case "get_context":
		var request taskIDPayload
		if err := decodeTaskGroupPayload(payload, &request); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task context get command rejected")
		}
		return store.GetContext(ctx, request.TaskID)
	case "save_notification":
		var value domaintask.Notification
		if err := decodeTaskGroupPayload(payload, &value); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task notification save command rejected")
		}
		return nil, store.SaveNotification(ctx, value)
	case "list_notifications":
		var request taskNotificationListPayload
		if err := decodeTaskGroupPayload(payload, &request); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "Task notification list command rejected")
		}
		return store.ListNotifications(ctx, request.Limit, request.InterruptOnly)
	default:
		return nil, NewError(ErrorCodeSchemaRejected, "Task command kind is not served")
	}
}

func (state *taskGroupState) prepare(sessionID string, read bool) (taskSessionPreparedResult, error) {
	session, err := state.session(sessionID)
	if err != nil {
		return taskSessionPreparedResult{}, err
	}
	if read || session.begin.Mode == taskSessionModeRead {
		return taskSessionPreparedResult{}, NewError(ErrorCodeSchemaRejected, "read session cannot prepare a commit")
	}
	session.commandMu.Lock()
	defer session.commandMu.Unlock()
	if isSessionDone(session) || session.store == nil {
		return taskSessionPreparedResult{}, NewError(ErrorCodeOutcomeUnknown, "Task session is not active")
	}
	if session.prepared {
		return taskSessionPreparedResult{TranscriptHash: session.preparedHash}, nil
	}
	session.prepared = true
	session.preparedHash = hex.EncodeToString(session.transcript[:])
	session.signalActivity()
	return taskSessionPreparedResult{TranscriptHash: session.preparedHash}, nil
}

func (state *taskGroupState) finish(ctx context.Context, sessionID string, read bool, readEnd bool, transcript string) error {
	session, err := state.session(sessionID)
	if err != nil {
		if isOutcomeUnknown(err) && readEnd {
			return nil
		}
		return err
	}
	if read != (session.begin.Mode == taskSessionModeRead) {
		return NewError(ErrorCodeDuplicateConflict, "Task session finish mode mismatch")
	}
	if isSessionDone(session) {
		return nil
	}
	if read {
		session.expireReadPublication()
	}
	finish := taskSessionFinish{readEnd: readEnd, commit: readEnd, transcript: transcript}
	if !readEnd {
		finish.commit = false
	}
	select {
	case session.finished <- finish:
	case <-session.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-session.done:
		session.commandMu.Lock()
		defer session.commandMu.Unlock()
		if session.err != nil && readEnd {
			return session.err
		}
		return nil
	case <-ctx.Done():
		return NewError(ErrorCodeOutcomeUnknown, "Task session cleanup is still in progress")
	}
}

func (state *taskGroupState) commit(ctx context.Context, owner TaskGroupOwner, operationID string, request taskCommitRequest) (any, error) {
	if request.SessionID != operationID || request.Mode == taskSessionModeRead || !validTranscriptHash(request.TranscriptHash) {
		return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "Task commit identity rejected"))
	}
	session, err := state.session(request.SessionID)
	if err != nil {
		return nil, ownerRolledBack(err)
	}
	if taskSessionFingerprint(session.begin) != taskSessionFingerprint(taskSessionBeginRequest{SessionID: request.SessionID, Mode: request.Mode, TaskID: request.TaskID}) {
		return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "Task commit scope does not match begin"))
	}
	session.commandMu.Lock()
	if isSessionDone(session) || !session.prepared || hex.EncodeToString(session.transcript[:]) != request.TranscriptHash {
		session.commandMu.Unlock()
		return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "Task commit transcript does not match prepared session"))
	}
	session.signalActivity()
	session.commandMu.Unlock()
	select {
	case session.finished <- taskSessionFinish{commit: true, transcript: request.TranscriptHash}:
	case <-session.done:
		return nil, ownerRolledBack(errTaskGroupSessionExpired)
	case <-ctx.Done():
		return nil, NewError(ErrorCodeOutcomeUnknown, "Task commit is still resolving")
	}
	select {
	case <-session.done:
		session.commandMu.Lock()
		defer session.commandMu.Unlock()
		if session.err != nil {
			return nil, session.err
		}
		if session.result.SessionID != request.SessionID || session.result.TranscriptHash != request.TranscriptHash {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task commit owner receipt did not match the request")
		}
		return session.result, nil
	case <-ctx.Done():
		return nil, NewError(ErrorCodeOutcomeUnknown, "Task commit is still resolving")
	}
}

func validTranscriptHash(hash string) bool {
	if len(hash) != sha256.Size*2 || hash != strings.ToLower(hash) {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func reconcileTaskCommit(ctx context.Context, owner TaskGroupOwner, mutation MutationMetadata) (ReconcileDecision, error) {
	var request taskCommitRequest
	if decodeTaskGroupPayload(mutation.Payload, &request) != nil || request.SessionID != mutation.OpID || !validTranscriptHash(request.TranscriptHash) {
		return UnknownOutcome(), nil
	}
	receipt, err := owner.LookupTaskOperation(ctx, mutation.OpID)
	if errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		return ConfirmedNotCommitted(), nil
	}
	if err != nil {
		return UnknownOutcome(), nil
	}
	if receipt.RequestHash != taskBeginReceiptHash(request) || taskGroupReceiptScope(receipt.Scope) != taskReceiptScope(request.Mode) || receipt.TaskID != request.TaskID {
		return UnknownOutcome(), nil
	}
	var result taskCommitResult
	if json.Unmarshal(receipt.Result, &result) != nil || result.SessionID != request.SessionID || result.TranscriptHash != request.TranscriptHash {
		return UnknownOutcome(), nil
	}
	return Committed(result), nil
}

func taskBeginReceiptHash(request taskCommitRequest) string {
	begin := taskSessionBeginRequest{SessionID: request.SessionID, Mode: request.Mode, TaskID: request.TaskID}
	encoded := mustJSON(begin)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func taskReceiptScope(mode taskSessionMode) taskpersistence.TaskOperationScope {
	if mode == taskSessionModeTask {
		return taskpersistence.TaskOperationScopeTask
	}
	return taskpersistence.TaskOperationScopeGlobal
}

func taskGroupReceiptScope(scope taskpersistence.TaskOperationScope) taskpersistence.TaskOperationScope {
	if scope == "" {
		return taskpersistence.TaskOperationScopeTask
	}
	return scope
}

func (state *taskGroupState) reserveFence(ctx context.Context, request taskFenceReservationRequest) (any, error) {
	if err := request.TaskID.Validate(); err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "Task fence task_id rejected")
	}
	if request.Action == "status" {
		status, err := state.owner.GetTaskExecutionFence(ctx, request.TaskID)
		if errors.Is(err, taskpersistence.ErrTaskExecutionFenceNotFound) {
			return taskFenceStatusResult{Found: false}, nil
		}
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "Task execution fence status unavailable")
		}
		return taskFenceStatusResult{Found: true, Status: status}, nil
	}
	if !opIDPattern.MatchString(request.ReservationID) {
		return nil, NewError(ErrorCodeSchemaRejected, "Task fence reservation id rejected")
	}
	switch request.Action {
	case "acquire":
		state.mu.Lock()
		if existing := state.reservations[request.ReservationID]; existing != nil {
			sameTask, active := existing.taskID == request.TaskID, existing.active
			state.mu.Unlock()
			if !sameTask {
				return nil, NewError(ErrorCodeDuplicateConflict, "Task fence reservation id was reused for another Task")
			}
			return taskFenceReservationResult{Active: active}, nil
		}
		state.mu.Unlock()
		if err := state.acquireGate(ctx); err != nil {
			return nil, err
		}
		state.mu.Lock()
		if existing := state.reservations[request.ReservationID]; existing != nil {
			sameTask, active := existing.taskID == request.TaskID, existing.active
			state.mu.Unlock()
			state.releaseGate()
			if !sameTask {
				return nil, NewError(ErrorCodeDuplicateConflict, "Task fence reservation id was reused for another Task")
			}
			return taskFenceReservationResult{Active: active}, nil
		}
		if len(state.reservations) >= 1024 {
			for id, item := range state.reservations {
				if !item.active {
					delete(state.reservations, id)
				}
			}
		}
		if len(state.reservations) >= 1024 {
			state.mu.Unlock()
			state.releaseGate()
			return nil, NewError(ErrorCodeStoreUnavailable, "Task fence reservation capacity is full")
		}
		state.reservations[request.ReservationID] = &taskFenceReservation{taskID: request.TaskID, active: true}
		state.mu.Unlock()
		return taskFenceReservationResult{Active: true}, nil
	case "release":
		state.mu.Lock()
		reservation := state.reservations[request.ReservationID]
		if reservation == nil {
			state.mu.Unlock()
			return taskFenceReservationResult{Active: false}, nil
		}
		if reservation.taskID != request.TaskID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "Task fence reservation id was reused for another Task")
		}
		if !reservation.active {
			state.mu.Unlock()
			return taskFenceReservationResult{Active: false}, nil
		}
		reservation.active = false
		state.mu.Unlock()
		state.releaseGate()
		return taskFenceReservationResult{Active: false}, nil
	case "callback_begin":
		if request.FenceID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "Task fence callback capability rejected")
		}
		state.mu.Lock()
		reservation := state.reservations[request.ReservationID]
		if reservation == nil || !reservation.active || reservation.taskID != request.TaskID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "Task fence callback has no matching active reservation")
		}
		if current := state.fenceCallbacks[request.TaskID]; current != "" && current != request.FenceID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "another Task execution fence callback is active")
		}
		state.mu.Unlock()
		status, err := state.owner.GetTaskExecutionFence(ctx, request.TaskID)
		if err != nil || status.FenceID != request.FenceID || status.State != taskpersistence.TaskExecutionFenceStateActive {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task fence callback capability is not active")
		}
		state.mu.Lock()
		reservation = state.reservations[request.ReservationID]
		if reservation == nil || !reservation.active || reservation.taskID != request.TaskID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "Task fence callback reservation ended")
		}
		if current := state.fenceCallbacks[request.TaskID]; current != "" && current != request.FenceID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "another Task execution fence callback is active")
		}
		state.fenceCallbacks[request.TaskID] = request.FenceID
		state.mu.Unlock()
		return taskFenceReservationResult{Active: true}, nil
	case "callback_end":
		if request.FenceID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "Task fence callback capability rejected")
		}
		state.mu.Lock()
		reservation := state.reservations[request.ReservationID]
		if reservation == nil || !reservation.active || reservation.taskID != request.TaskID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "Task fence callback end has no matching active reservation")
		}
		current := state.fenceCallbacks[request.TaskID]
		if current == "" {
			state.mu.Unlock()
			return taskFenceReservationResult{Active: false}, nil
		}
		if current != request.FenceID {
			state.mu.Unlock()
			return nil, NewError(ErrorCodeDuplicateConflict, "Task fence callback identity changed")
		}
		state.mu.Unlock()
		status, err := state.owner.GetTaskExecutionFence(ctx, request.TaskID)
		if err != nil && !errors.Is(err, taskpersistence.ErrTaskExecutionFenceNotFound) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "Task execution fence status unavailable")
		}
		if err == nil && (status.FenceID != request.FenceID || status.State != taskpersistence.TaskExecutionFenceStateReleased) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "active Task execution fence callback remains blocked")
		}
		state.mu.Lock()
		if state.fenceCallbacks[request.TaskID] == request.FenceID {
			delete(state.fenceCallbacks, request.TaskID)
		}
		state.mu.Unlock()
		return taskFenceReservationResult{Active: false}, nil
	default:
		return nil, NewError(ErrorCodeSchemaRejected, "Task fence reservation action rejected")
	}
}

func (state *taskGroupState) requireReservation(id string, taskID modulecore.TaskID) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	reservation := state.reservations[id]
	if reservation == nil || !reservation.active || reservation.taskID != taskID {
		return NewError(ErrorCodeDuplicateConflict, "Task fence operation has no matching active reservation")
	}
	return nil
}

func reconcileTaskFence(ctx context.Context, owner TaskGroupOwner, mutation MutationMetadata, acquiring bool) (ReconcileDecision, error) {
	var request taskFenceMutationRequest
	if decodeTaskGroupPayload(mutation.Payload, &request) != nil || request.TaskID.Validate() != nil || request.FenceID == "" {
		return UnknownOutcome(), nil
	}
	status, err := owner.GetTaskExecutionFence(ctx, request.TaskID)
	if errors.Is(err, taskpersistence.ErrTaskExecutionFenceNotFound) {
		if acquiring {
			return ConfirmedNotCommitted(), nil
		}
		return UnknownOutcome(), nil
	}
	if err != nil {
		return UnknownOutcome(), nil
	}
	if status.FenceID != request.FenceID {
		return UnknownOutcome(), nil
	}
	if acquiring || status.State == taskpersistence.TaskExecutionFenceStateReleased {
		return Committed(status), nil
	}
	return ConfirmedNotCommitted(), nil
}

func (state *taskGroupState) commandTranscriptForTest(sessionID string) (uint64, string, error) {
	session, err := state.session(sessionID)
	if err != nil {
		return 0, "", err
	}
	session.commandMu.Lock()
	defer session.commandMu.Unlock()
	return session.sequence, hex.EncodeToString(session.transcript[:]), nil
}
