package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	testBuild   = "0123456789abcdef0123456789abcdef01234567"
	secretText  = "SECRET-USER-TEXT-must-not-be-logged"
	secretFinal = "SECRET-FINAL-TEXT-must-not-be-logged"
)

// harnessID makes a well-formed Harness ID of a kind (the client of the real
// protocol checks the schema patterns of every ID it sends or receives).
func harnessID(prefix string) string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return prefix + "_" + id.String()
}

// fakeClient is a scripted Harness client. Like the real client it refuses an
// input that does not satisfy the protocol schema (protocol.Encode) before
// "sending" it, so a payload CORE builds is checked against the real schema.
type fakeClient struct {
	mu   sync.Mutex
	caps protocol.CapabilitiesResult
	done chan struct{}
	note chan client.Notification

	opens   []protocol.SessionOpenInput
	starts  []protocol.StartInput
	resumes []protocol.ResumeInput

	// Each mutation sees a caller-bounded step context.
	openCtxErrs       []error
	openHadDeadline   []bool
	startCtxErrs      []error
	startHadDeadline  []bool
	resumeCtxErrs     []error
	resumeHadDeadline []bool

	awaits     []awaitCall
	interrupts []interruptCall
	shutdown   []protocol.ShutdownInput
	aborts     int

	onOpen                func(n int, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error)
	onStart               func(n int, in protocol.StartInput) (protocol.StartResult, error)
	onResume              func(ctx context.Context, n int, in protocol.ResumeInput) (protocol.ResumeResult, error)
	onRunGet              func(protocol.RunGetInput) (protocol.RunInfo, error)
	onSessionGet          func(protocol.SessionGetInput) (protocol.SessionInfo, error)
	onReceiptGet          func(protocol.ReceiptGetInput) (protocol.ReceiptRecord, error)
	onEventsRead          func(protocol.EventsReadInput) (protocol.EventsReadResult, error)
	onEvidenceRead        func(protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error)
	onRunGetContext       func(context.Context, protocol.RunGetInput) (protocol.RunInfo, error)
	onEventsReadContext   func(context.Context, protocol.EventsReadInput) (protocol.EventsReadResult, error)
	onEvidenceReadContext func(context.Context, protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error)
	onInterrupt           func(ctx context.Context, runID, keyPrefix string) (protocol.InterruptReceipt, error)
	onAwait               func(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error)
	onShutdown            func(in protocol.ShutdownInput) error

	thread, task, run, receipt, trace string
	sessionID                         string
	lastResult                        protocol.RunResult
	lastStart                         protocol.StartResult
	lastResume                        protocol.ResumeResult
}

type awaitCall struct {
	runID string
	opts  client.AwaitOptions
}

type interruptCall struct {
	runID     string
	keyPrefix string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		caps:      protocol.CapabilitiesResult{ProtocolVersion: protocol.ProtocolVersion, BuildRevision: testBuild},
		done:      make(chan struct{}),
		note:      make(chan client.Notification, 64),
		thread:    harnessID("thr"),
		task:      harnessID("tsk"),
		run:       harnessID("run"),
		receipt:   harnessID("rcp"),
		sessionID: harnessID("ses"),
	}
}

func (f *fakeClient) Capabilities() protocol.CapabilitiesResult { return f.caps }
func (f *fakeClient) Notifications() <-chan client.Notification { return f.note }
func (f *fakeClient) Done() <-chan struct{}                     { return f.done }

func (f *fakeClient) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.done:
	default:
		close(f.done)
		close(f.note)
	}
}

func (f *fakeClient) Abort() {
	f.mu.Lock()
	f.aborts++
	f.mu.Unlock()
	f.end()
}

func (f *fakeClient) Shutdown(_ context.Context, in protocol.ShutdownInput) error {
	f.mu.Lock()
	f.shutdown = append(f.shutdown, in)
	hook := f.onShutdown
	f.mu.Unlock()
	if hook != nil {
		return hook(in)
	}
	f.end()
	return nil
}

func (f *fakeClient) session() protocol.SessionInfo {
	return protocol.SessionInfo{
		SessionID: f.sessionID, ThreadID: f.thread, WorkspacePath: "/w", ContextRevision: 3, ControlRevision: 4,
		Binding: protocol.Binding{Kind: "alias", Selector: "s", ProfileRevision: "r"}, PolicyRef: "p", ExecutionMode: protocol.ModeStructuredOnly,
	}
}

func (f *fakeClient) SessionOpen(ctx context.Context, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
	if _, err := protocol.Encode(in); err != nil {
		return protocol.SessionOpenResult{}, err
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.opens = append(f.opens, in)
	f.openCtxErrs = append(f.openCtxErrs, ctx.Err())
	f.openHadDeadline = append(f.openHadDeadline, hasDeadline)
	n, hook := len(f.opens), f.onOpen
	f.mu.Unlock()
	if hook != nil {
		return hook(n, in)
	}
	return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: f.session()}, nil
}

func (f *fakeClient) TurnStart(ctx context.Context, in protocol.StartInput) (protocol.StartResult, error) {
	if _, err := protocol.Encode(in); err != nil {
		return protocol.StartResult{}, err
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.starts = append(f.starts, in)
	f.startCtxErrs = append(f.startCtxErrs, ctx.Err())
	f.startHadDeadline = append(f.startHadDeadline, hasDeadline)
	n, hook := len(f.starts), f.onStart
	f.mu.Unlock()
	if hook != nil {
		result, err := hook(n, in)
		if err == nil {
			f.mu.Lock()
			f.lastStart = result
			f.mu.Unlock()
		}
		return result, err
	}
	result := f.startResult(in)
	f.mu.Lock()
	f.lastStart = result
	f.mu.Unlock()
	return result, nil
}

func (f *fakeClient) RunResume(ctx context.Context, in protocol.ResumeInput) (protocol.ResumeResult, error) {
	if _, err := protocol.Encode(in); err != nil {
		return protocol.ResumeResult{}, err
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.resumes = append(f.resumes, in)
	f.resumeCtxErrs = append(f.resumeCtxErrs, ctx.Err())
	f.resumeHadDeadline = append(f.resumeHadDeadline, hasDeadline)
	n, hook := len(f.resumes), f.onResume
	f.mu.Unlock()
	if hook != nil {
		result, err := hook(ctx, n, in)
		if err == nil {
			f.setResumeResult(result)
		}
		return result, err
	}
	result := protocol.ResumeResult{
		ReceiptID: harnessID("rcp"), TaskID: in.TaskID, ThreadID: f.thread, PreviousRunID: in.ExpectedLastRunID,
		RunID: harnessID("run"), TraceID: harnessID("trc"), CheckpointID: in.CheckpointID, EffectiveLimits: in.Limits,
		DeadlineAt: protocol.FormatTimestamp(time.Now().UTC().Add(time.Hour)), RecoveryPolicyRevision: strings.Repeat("a", 64),
	}
	f.setResumeResult(result)
	return result, nil
}

func (f *fakeClient) setResumeResult(result protocol.ResumeResult) {
	f.mu.Lock()
	f.task, f.thread, f.run, f.receipt, f.trace = result.TaskID, result.ThreadID, result.RunID, result.ReceiptID, result.TraceID
	f.lastResume = result
	f.lastResult = protocol.RunResult{}
	f.mu.Unlock()
}

func (f *fakeClient) startResult(in protocol.StartInput) protocol.StartResult {
	now := time.Now().UTC()
	f.mu.Lock()
	f.thread = in.ThreadID
	f.mu.Unlock()
	traceID := harnessID("trc")
	f.mu.Lock()
	f.trace = traceID
	f.mu.Unlock()
	return protocol.StartResult{
		ReceiptID: f.receipt, Accepted: true, SessionID: harnessID("ses"), ThreadID: in.ThreadID, TurnID: harnessID("turn"),
		TaskID: f.task, RunID: f.run, TraceID: traceID, EffectiveLimits: in.Limits,
		DeadlineAt: protocol.FormatTimestamp(now.Add(time.Hour)), RecoveryPolicyRevision: strings.Repeat("a", 64),
		Intake: protocol.IntakeReceipt{
			ReceiptID: f.receipt, MessageID: harnessID("msg"), ThreadID: in.ThreadID, EvidenceID: harnessID("evd"),
			Principal: "core:local", Entrypoint: protocol.EntrypointStdioCore, CallerProfileDigest: strings.Repeat("a", 64),
			DeclaredOrigin: protocol.OriginAutomation, EffectiveOrigin: protocol.OriginAutomation, ProofBasis: protocol.ProofBasisAutomation,
			AcceptedSequence: 1, AcceptedAt: protocol.FormatTimestamp(now),
		},
	}
}

func (f *fakeClient) InterruptRun(ctx context.Context, runID, keyPrefix string) (protocol.InterruptReceipt, error) {
	f.mu.Lock()
	f.interrupts = append(f.interrupts, interruptCall{runID: runID, keyPrefix: keyPrefix})
	hook := f.onInterrupt
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, runID, keyPrefix)
	}
	return protocol.InterruptReceipt{}, nil
}

func (f *fakeClient) AwaitRun(ctx context.Context, runID string, opts client.AwaitOptions) (protocol.RunResult, error) {
	f.mu.Lock()
	f.awaits = append(f.awaits, awaitCall{runID: runID, opts: opts})
	hook := f.onAwait
	f.mu.Unlock()
	if hook != nil {
		result, err := hook(ctx, runID, opts)
		if err == nil {
			f.mu.Lock()
			f.lastResult = result
			f.mu.Unlock()
		}
		return result, err
	}
	result := f.runResult("completed", "passed")
	f.mu.Lock()
	f.lastResult = result
	f.mu.Unlock()
	return result, nil
}

func (f *fakeClient) RunGet(ctx context.Context, in protocol.RunGetInput) (protocol.RunInfo, error) {
	f.mu.Lock()
	hook, contextHook, result, resumed := f.onRunGet, f.onRunGetContext, f.lastResult, f.lastResume.RunID != ""
	f.mu.Unlock()
	if contextHook != nil {
		return contextHook(ctx, in)
	}
	if hook != nil {
		return hook(in)
	}
	if result.RunID == "" {
		result = f.runResult("completed", "passed")
	}
	lastEventSeq := int64(5)
	if !resumed {
		lastEventSeq = 7
	}
	return protocol.RunInfo{RunID: f.run, TaskID: f.task, ThreadID: f.thread, Terminal: true, Result: &result, LastEventSeq: lastEventSeq}, nil
}

func (f *fakeClient) SessionGet(_ context.Context, in protocol.SessionGetInput) (protocol.SessionInfo, error) {
	f.mu.Lock()
	hook := f.onSessionGet
	f.mu.Unlock()
	if hook != nil {
		return hook(in)
	}
	session := f.session()
	if in.ThreadID != session.ThreadID {
		return protocol.SessionInfo{}, errors.New("unknown thread")
	}
	return session, nil
}

func (f *fakeClient) ReceiptGet(_ context.Context, in protocol.ReceiptGetInput) (protocol.ReceiptRecord, error) {
	f.mu.Lock()
	hook, start, resumed := f.onReceiptGet, f.lastStart, f.lastResume
	f.mu.Unlock()
	if hook != nil {
		return hook(in)
	}
	if in.ReceiptID == resumed.ReceiptID && resumed.ReceiptID != "" {
		payload, err := protocol.NewReceiptPayload(resumed)
		if err != nil {
			return protocol.ReceiptRecord{}, err
		}
		return protocol.ReceiptRecord{ReceiptID: resumed.ReceiptID, Operation: "run/resume", Stage: "accepted", Result: &payload}, nil
	}
	if in.ReceiptID == start.ReceiptID && start.ReceiptID != "" {
		payload, err := protocol.NewReceiptPayload(start)
		if err != nil {
			return protocol.ReceiptRecord{}, err
		}
		return protocol.ReceiptRecord{ReceiptID: start.ReceiptID, Operation: "turn/start", Stage: "accepted", Result: &payload}, nil
	}
	return protocol.ReceiptRecord{}, errors.New("unknown receipt")
}

func (f *fakeClient) EventsRead(ctx context.Context, in protocol.EventsReadInput) (protocol.EventsReadResult, error) {
	f.mu.Lock()
	hook, contextHook := f.onEventsRead, f.onEventsReadContext
	f.mu.Unlock()
	if contextHook != nil {
		return contextHook(ctx, in)
	}
	if hook != nil {
		return hook(in)
	}
	events := f.runEvents()
	result := protocol.EventsReadResult{Events: []protocol.Event{}}
	for _, event := range events {
		if event.EventSeq > in.AfterSeq && int64(len(result.Events)) < in.Limit {
			result.Events = append(result.Events, event)
		}
	}
	if len(result.Events) > 0 {
		result.NextAfterSeq = result.Events[len(result.Events)-1].EventSeq
	} else {
		result.NextAfterSeq = in.AfterSeq
	}
	result.HasMore = result.NextAfterSeq < int64(len(events))
	return result, nil
}

func (f *fakeClient) EvidenceRead(ctx context.Context, in protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error) {
	f.mu.Lock()
	hook, contextHook := f.onEvidenceRead, f.onEvidenceReadContext
	f.mu.Unlock()
	if contextHook != nil {
		return contextHook(ctx, in)
	}
	if hook != nil {
		return hook(in)
	}
	return protocol.EvidenceReadResult{
		EvidenceID: in.EvidenceID, ProjectionVersion: "raw/v1", TotalBytes: 1,
		ReturnedRange: protocol.ByteRange{Start: 0, End: 0}, Partial: true, RawHash: strings.Repeat("b", 64), CaptureComplete: true,
	}, nil
}

func (f *fakeClient) runEvents() []protocol.Event {
	f.mu.Lock()
	result, resumed := f.lastResult, f.lastResume.RunID != ""
	threadID, taskID, runID, traceID := f.thread, f.task, f.run, f.trace
	resume, start := f.lastResume, f.lastStart
	f.mu.Unlock()
	if result.RunID == "" {
		result = f.runResult("completed", "passed")
	}
	actionID, attemptID := harnessID("act"), harnessID("att")
	events := make([]protocol.Event, 0, 7)
	appendEvent := func(sequence int64, payload protocol.EventPayload, task, run, message *string) {
		common := protocol.EventCommon{EventID: harnessID("evt"), EventSeq: sequence, ThreadID: threadID,
			TaskID: task, RunID: run, MessageID: message, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if accepted, ok := payload.(protocol.InputAcceptedPayload); ok {
			common.ReceiptID = protocol.Str(accepted.Intake.ReceiptID)
			common.EvidenceID = protocol.Str(accepted.Intake.EvidenceID)
		}
		if terminal, ok := payload.(protocol.RunTerminalPayload); ok {
			if terminal.ResultEvidenceID != "" {
				common.EvidenceID = protocol.Str(terminal.ResultEvidenceID)
			}
			if terminal.Code != "" {
				common.Code = protocol.Str(terminal.Code)
			}
		}
		event, err := protocol.BuildTypedEvent(common, payload)
		if err != nil {
			panic(err)
		}
		events = append(events, event)
	}
	sequence := int64(1)
	if !resumed {
		intake := start.Intake
		messageID := intake.MessageID
		appendEvent(sequence, protocol.InputAcceptedPayload{Intake: intake, QueueItemID: nil, Disposition: "initial"},
			protocol.Str(taskID), protocol.Str(runID), protocol.Str(messageID))
		sequence++
		appendEvent(sequence, protocol.TaskCreatedPayload{TaskID: taskID, TurnID: protocol.Str(harnessID("turn")), Kind: "work"},
			protocol.Str(taskID), nil, nil)
		sequence++
	}
	var previousRunID *string
	if resumed {
		previousRunID = protocol.Str(resume.PreviousRunID)
	}
	appendEvent(sequence, protocol.RunStartedPayload{RunID: runID, PreviousRunID: previousRunID, TraceID: traceID, WriterEpoch: 1,
		EffectiveLimits: protocol.Limits{MaxModelSteps: 1, MaxToolCallsPerStep: 1, DeadlineSeconds: 1, MaxCaptureBytes: 2048, MaxGenerationAttempts: 1},
		DeadlineAt:      protocol.FormatTimestamp(time.Now().UTC().Add(time.Hour)), RecoveryPolicyRevision: strings.Repeat("a", 64)},
		protocol.Str(taskID), protocol.Str(runID), nil)
	sequence++
	appendEvent(sequence, protocol.ActionPreparedPayload{ActionID: actionID, AttemptID: attemptID, Kind: "verification", Name: "process.exec", ArgsHash: strings.Repeat("c", 64), PolicyRevision: "policy-revision"},
		protocol.Str(taskID), protocol.Str(runID), nil)
	sequence++
	appendEvent(sequence, protocol.ActionDispatchStartedPayload{ActionID: actionID, AttemptID: attemptID, WriterEpoch: 1, ControlRevision: 1},
		protocol.Str(taskID), protocol.Str(runID), nil)
	sequence++
	appendEvent(sequence, protocol.ActionCompletedPayload{ActionID: actionID, AttemptID: attemptID, EffectState: "completed", ExitCode: protocol.Int(0),
		ResultEvidenceIDs: append([]string(nil), result.Verification.EvidenceIDs...), CaptureComplete: true},
		protocol.Str(taskID), protocol.Str(runID), nil)
	sequence++
	appendEvent(sequence, protocol.RunTerminalPayload{Status: result.Status, Code: result.Code, ResultEvidenceID: firstEvidenceID(result.EvidenceIDs), LastCheckpointID: nil},
		protocol.Str(taskID), protocol.Str(runID), nil)
	return events
}

func firstEvidenceID(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (f *fakeClient) runResult(status, verification string) protocol.RunResult {
	evidenceIDs := []string{}
	verificationIDs := []string{}
	var criteriaRevision *string
	if verification == "passed" {
		evidenceID := harnessID("evd")
		evidenceIDs = []string{evidenceID}
		verificationIDs = []string{evidenceID}
		criteria := strings.Repeat("a", 64)
		criteriaRevision = &criteria
	}
	return protocol.RunResult{
		RunID: f.run, TaskID: f.task, Status: status, Code: "FINAL_RESPONSE_ACCEPTED", FinalText: secretFinal,
		Verification: protocol.Verification{Status: verification, EvidenceIDs: verificationIDs, CriteriaRevision: criteriaRevision},
		EvidenceIDs:  evidenceIDs, UnresolvedActionIDs: []string{},
	}
}

func TestFakeStartResultSatisfiesThePinnedProtocol(t *testing.T) {
	f := newFakeClient()
	result := f.startResult(protocol.StartInput{ThreadID: f.thread, Limits: protocol.Limits{
		MaxModelSteps: 1, MaxToolCallsPerStep: 1, DeadlineSeconds: 1, MaxCaptureBytes: 2048, MaxGenerationAttempts: 1,
	}})
	if _, err := protocol.Encode(result); err != nil {
		t.Fatalf("fake StartResult must satisfy the pinned protocol: %v (cause: %v)", err, errors.Unwrap(err))
	}
	for _, status := range []string{"completed", "incomplete", "rejected", "blocked", "cancelled", "failed", "restart_required"} {
		if _, err := protocol.Encode(f.runResult(status, "not_run")); err != nil {
			t.Fatalf("fake RunResult status %q must satisfy the pinned protocol: %v (cause: %v)", status, err, errors.Unwrap(err))
		}
	}
}

// push delivers a confirmed Event notification, as the Harness would.
func (f *fakeClient) push(event protocol.Event) {
	f.note <- client.Notification{Kind: client.NotificationEvent, Event: &event}
}

// recorder is a stateful Action owner fake for source-unit tests. Persistence
// and close/reopen behavior use the real JSONL owner in focused integration
// tests below.
type recorder struct {
	mu             sync.Mutex
	created        []actionmanager.CreateInput
	completed      []completion
	createErr      error
	prepareErr     error
	markErr        error
	outcomeErr     error
	observationErr error
	completeErr    error
	actionID       modulecore.ActionID
	attemptID      modulecore.AttemptID
	action         domainaction.Action
	attempt        domainaction.Attempt
}

type completion struct {
	actionID  modulecore.ActionID
	attemptID modulecore.AttemptID
	attempt   domainaction.AttemptStatus
	action    domainaction.Status
	summary   string
}

func newRecorder() *recorder {
	return &recorder{actionID: modulecore.NewActionID(), attemptID: modulecore.NewAttemptID()}
}

func (r *recorder) EnsureNativeDelegation(ctx context.Context, in actionmanager.EnsureNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.createErr
	}
	if r.action.ActionID != "" {
		if r.action.TaskID != in.TaskID || r.action.RunID != in.RunID || !sameTestReference(r.attempt.NativeDelegation.InputReference, in.InputReference) ||
			r.attempt.NativeDelegation.ExpectedCriteriaRevision != in.ExpectedCriteriaRevision {
			return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
		}
		return r.action, r.attempt.Clone(), nil
	}
	r.created = append(r.created, actionmanager.CreateInput{TaskID: in.TaskID, RunID: in.RunID, Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName})
	now := time.Now().UTC()
	mode := in.Mode
	if mode == "" {
		mode = domainaction.NativeDelegationModeOpenStart
	}
	reason := domainaction.AttemptStartReasonFirst
	if mode == domainaction.NativeDelegationModeResume {
		reason = domainaction.AttemptStartReasonExplicitResume
	}
	r.action = domainaction.Action{ActionID: r.actionID, TaskID: in.TaskID, RunID: in.RunID, Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName,
		Status: domainaction.StatusOpen, CurrentAttemptID: r.attemptID, CreatedAt: now, UpdatedAt: now}
	r.attempt = domainaction.Attempt{AttemptID: r.attemptID, ActionID: r.actionID, StartReason: reason,
		Status: domainaction.AttemptStatusRunning, StartedAt: now, NativeDelegation: &domainaction.NativeDelegation{Mode: mode, InputReference: cloneTestReference(in.InputReference),
			ExpectedCriteriaRevision: in.ExpectedCriteriaRevision, ResumeSource: cloneTestResumeSource(in.ResumeSource)}}
	return r.action, r.attempt.Clone(), nil
}

func cloneTestResumeSource(source *domainaction.NativeDelegationResumeSource) *domainaction.NativeDelegationResumeSource {
	if source == nil {
		return nil
	}
	copy := *source
	if source.CheckpointID != nil {
		checkpoint := *source.CheckpointID
		copy.CheckpointID = &checkpoint
	}
	return &copy
}

func (r *recorder) PrepareNativeMutation(ctx context.Context, in actionmanager.PrepareNativeMutationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prepareErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.prepareErr
	}
	if err := r.matchPair(in.ActionID, in.AttemptID); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	hash := sha256.Sum256(in.Payload)
	prepared := domainaction.NativeMutation{Key: in.Key, Payload: append([]byte(nil), in.Payload...), PayloadSHA256: hex.EncodeToString(hash[:]), Status: domainaction.NativeMutationStatusPrepared}
	slot := testNativeMutationSlot(r.attempt.NativeDelegation, in.Slot)
	if slot == nil {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	if *slot != nil {
		if (*slot).Key == prepared.Key && (*slot).PayloadSHA256 == prepared.PayloadSHA256 && bytes.Equal((*slot).Payload, prepared.Payload) {
			return r.action, r.attempt.Clone(), nil
		}
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	if in.Slot == domainaction.NativeMutationSlotStart && (r.attempt.NativeDelegation.Open == nil || r.attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusAccepted) {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	if in.Slot == domainaction.NativeMutationSlotResume && (r.attempt.NativeDelegation.EffectiveMode() != domainaction.NativeDelegationModeResume || r.attempt.NativeDelegation.ResumeSource == nil) {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	*slot = &prepared
	return r.action, r.attempt.Clone(), nil
}

func (r *recorder) MarkNativeMutationDeliveryUnknown(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, slotName domainaction.NativeMutationSlot) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.markErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.markErr
	}
	if err := r.matchPair(actionID, attemptID); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	slot := testNativeMutationSlot(r.attempt.NativeDelegation, slotName)
	if slot == nil || *slot == nil {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	if (*slot).Status == domainaction.NativeMutationStatusPrepared {
		(*slot).Status = domainaction.NativeMutationStatusDeliveryUnknown
	} else if (*slot).Status != domainaction.NativeMutationStatusDeliveryUnknown {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	return r.action, r.attempt.Clone(), nil
}

func (r *recorder) RecordNativeMutationOutcome(ctx context.Context, in actionmanager.RecordNativeMutationOutcomeInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outcomeErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.outcomeErr
	}
	if err := r.matchPair(in.ActionID, in.AttemptID); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if in.Slot == domainaction.NativeMutationSlotStart &&
		(r.attempt.NativeDelegation.EffectiveMode() != domainaction.NativeDelegationModeOpenStart || r.attempt.NativeDelegation.Open == nil || r.attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusAccepted) ||
		in.Slot == domainaction.NativeMutationSlotResume &&
			(r.attempt.NativeDelegation.EffectiveMode() != domainaction.NativeDelegationModeResume || r.attempt.NativeDelegation.ResumeSource == nil) {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	slot := testNativeMutationSlot(r.attempt.NativeDelegation, in.Slot)
	if slot == nil || *slot == nil || (*slot).Status != domainaction.NativeMutationStatusDeliveryUnknown {
		return domainaction.Action{}, domainaction.Attempt{}, actionmanager.ErrNativeDelegationConflict
	}
	(*slot).Status, (*slot).Result, (*slot).ErrorCode = in.Status, append([]byte(nil), in.Result...), in.ErrorCode
	if in.Status == domainaction.NativeMutationStatusRejected {
		now := time.Now().UTC()
		r.attempt, _ = r.attempt.Close(domainaction.AttemptStatusFailed, now, "native mutation rejected")
		r.action, _ = r.action.Close(domainaction.StatusFailed, now, "native mutation rejected")
	}
	return r.action, r.attempt.Clone(), nil
}

func (r *recorder) RecordNativeObservation(ctx context.Context, in actionmanager.RecordNativeObservationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observationErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.observationErr
	}
	if err := r.matchPair(in.ActionID, in.AttemptID); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	observation := in.Observation
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	r.attempt.NativeDelegation.LastObservation = &observation
	return r.action, r.attempt.Clone(), nil
}

func (r *recorder) CompleteNativeDelegation(ctx context.Context, in actionmanager.CompleteNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completeErr != nil {
		return domainaction.Action{}, domainaction.Attempt{}, r.completeErr
	}
	if err := r.matchPair(in.ActionID, in.AttemptID); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	r.attempt.NativeDelegation.RunResult = append([]byte(nil), in.RunResult...)
	if in.Proof != nil {
		proof := *in.Proof
		proof.Evidence = append([]domainaction.NativeDelegationEvidenceProof(nil), in.Proof.Evidence...)
		r.attempt.NativeDelegation.Proof = &proof
	}
	r.attempt, _ = r.attempt.Close(in.AttemptStatus, time.Now().UTC(), in.Summary)
	r.action, _ = r.action.Close(in.ActionStatus, time.Now().UTC(), in.Summary)
	r.completed = append(r.completed, completion{in.ActionID, in.AttemptID, in.AttemptStatus, in.ActionStatus, in.Summary})
	return r.action, r.attempt.Clone(), nil
}

func (r *recorder) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.action.ActionID == "" || filter.TaskID != "" && r.action.TaskID != filter.TaskID || filter.RunID != "" && r.action.RunID != filter.RunID {
		return nil, nil
	}
	return []domainaction.Action{r.action}, nil
}

func (r *recorder) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempt.AttemptID == "" || filter.ActionID != "" && r.attempt.ActionID != filter.ActionID {
		return nil, nil
	}
	return []domainaction.Attempt{r.attempt.Clone()}, nil
}

func (r *recorder) matchPair(actionID modulecore.ActionID, attemptID modulecore.AttemptID) error {
	if actionID != r.actionID || attemptID != r.attemptID || r.action.ActionID != actionID || r.attempt.AttemptID != attemptID {
		return actionmanager.ErrNativeDelegationConflict
	}
	return nil
}

func sameTestReference(left, right *conversation.AcceptedOPSInputReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneTestReference(reference *conversation.AcceptedOPSInputReference) *conversation.AcceptedOPSInputReference {
	if reference == nil {
		return nil
	}
	copy := *reference
	return &copy
}

func testNativeMutationSlot(d *domainaction.NativeDelegation, slot domainaction.NativeMutationSlot) **domainaction.NativeMutation {
	if d == nil {
		return nil
	}
	switch slot {
	case domainaction.NativeMutationSlotOpen:
		if d.EffectiveMode() != domainaction.NativeDelegationModeOpenStart {
			return nil
		}
		return &d.Open
	case domainaction.NativeMutationSlotStart:
		if d.EffectiveMode() != domainaction.NativeDelegationModeOpenStart {
			return nil
		}
		return &d.Start
	case domainaction.NativeMutationSlotResume:
		if d.EffectiveMode() != domainaction.NativeDelegationModeResume {
			return nil
		}
		return &d.Resume
	default:
		return nil
	}
}

type fakeTaskOwner struct {
	mu               sync.Mutex
	taskError        error
	fenceError       error
	fenceErrorAtCall int
	task             *domaintask.Task
	getCalls         int
	fenceCalls       int
	lastActor        string
	lastTaskID       modulecore.TaskID
	lastRunID        modulecore.RunID
	lastScopeValue   any
	lastToolScope    domaintool.ToolExecutionScope
	lastHadDeadline  bool
	lastContextErr   error
	fenceActive      bool
}

func (f *fakeTaskOwner) Get(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.taskError != nil {
		return domaintask.Task{}, f.taskError
	}
	if f.task != nil {
		copy := *f.task
		return copy, nil
	}
	return domaintask.Task{TaskID: taskID, OwnerID: "shiro", Assignee: "shiro", Route: domaintask.RouteOperations, Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64)}, nil
}

func (f *fakeTaskOwner) ExecuteRunEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, effect func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.fenceCalls++
	f.lastActor = actorID
	f.lastTaskID = taskID
	f.lastRunID = runID
	f.lastScopeValue = ctx.Value(delegationTestScopeKey{})
	f.lastToolScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	f.lastHadDeadline = hasDeadline
	f.lastContextErr = ctx.Err()
	err := f.fenceError
	if f.fenceErrorAtCall != 0 && f.fenceCalls != f.fenceErrorAtCall {
		err = nil
	}
	if err == nil {
		f.fenceActive = true
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		f.mu.Lock()
		f.fenceActive = false
		f.mu.Unlock()
	}()
	return effect(ctx)
}

func (f *fakeTaskOwner) ExecuteNativeOPSResumeActionEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, _ domaintask.NativeOPSResumeClaim, effect func(context.Context) error) error {
	return f.ExecuteRunEffect(ctx, taskID, runID, actorID, effect)
}

func (f *fakeTaskOwner) CompleteNativeOPSResumeRun(ctx context.Context, taskID modulecore.TaskID, _ modulecore.RunID, _ string, _ domaintask.NativeOPSResumeClaim, _ domaintask.Status, _, _ string) (domaintask.Task, error) {
	return f.Get(ctx, taskID)
}

func (f *fakeTaskOwner) VerifyNativeOPSResumeReplay(ctx context.Context, _ modulecore.TaskID, _ modulecore.RunID, _ string, _ domaintask.NativeOPSResumeClaim, _ domaintask.Status) error {
	return ctx.Err()
}

type delegationTestScopeKey struct{}

// logSink collects JSON log lines.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) write(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *logSink) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// events returns the decoded entries whose event is name.
func (l *logSink) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range l.all() {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a log line is not one JSON object: %q: %v", line, err)
		}
		if entry["event"] == name {
			out = append(out, entry)
		}
	}
	return out
}

// testDeployment builds Settings and a Runtime around a scripted client factory.
type testDeployment struct {
	t        *testing.T
	settings Settings
	rec      *recorder
	tasks    *fakeTaskOwner
	log      *logSink
	runtime  *Runtime

	mu      sync.Mutex
	clients []*fakeClient
	configs []client.Config
	startFn func(n int, cfg client.Config) (*fakeClient, error)
	now     time.Time
}

func newDeployment(t *testing.T) *testDeployment {
	t.Helper()
	root := t.TempDir()
	d := &testDeployment{
		t: t,
		settings: Settings{
			HarnessBinary: filepath.Join(root, "bin", "rencrow-harness"), HarnessConfig: filepath.Join(root, "etc", "harness.json"),
			ExpectedBuildRevision: testBuild, ExpectedCriteriaRevision: strings.Repeat("a", 64),
			Workspace:       Workspace{Path: root, PolicyRef: "workspace-write", ExecutionMode: protocol.ModeStructuredOnly},
			Binding:         Binding{Selector: "shiro-worker-exec", ProfileRevision: "rev-1", ExecutionRole: "worker"},
			Limits:          protocol.Limits{MaxModelSteps: 10, MaxToolCallsPerStep: 8, DeadlineSeconds: 1800, MaxCaptureBytes: 67108864, MaxGenerationAttempts: 32},
			RestartCooldown: time.Minute,
		},
		rec:   newRecorder(),
		tasks: &fakeTaskOwner{},
		log:   &logSink{},
		now:   time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
	runtime, err := NewRuntime(d.settings, d.rec, d.tasks, Options{
		Start:   d.start,
		Now:     func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
		Log:     d.log.write,
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() {
		_ = runtime.Close(context.Background())
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.clients {
			c.end()
		}
	})
	return d
}

func (d *testDeployment) start(_ context.Context, cfg client.Config) (Client, error) {
	d.mu.Lock()
	d.configs = append(d.configs, cfg)
	n := len(d.configs) // the 1-based number of this start call
	fn := d.startFn
	d.mu.Unlock()
	var c *fakeClient
	if fn != nil {
		var err error
		if c, err = fn(n, cfg); err != nil {
			return nil, err
		}
	} else {
		c = newFakeClient()
	}
	d.mu.Lock()
	d.clients = append(d.clients, c)
	d.mu.Unlock()
	return c, nil
}

func (d *testDeployment) startCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.configs)
}

func (d *testDeployment) client(i int) *fakeClient {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clients[i]
}

func (d *testDeployment) advance(by time.Duration) {
	d.mu.Lock()
	d.now = d.now.Add(by)
	d.mu.Unlock()
}

// turn builds the input, the execution identity context and the typed messages
// of one delegation.
func newTurn(t *testing.T, text string) (context.Context, conversation.TurnInput, []llm.Message) {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), text, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domaintool.DeriveAgentToolExecutionScope(
		context.Background(), "delegation-test", shiroActorID, "worker", "ops", true,
	)
	if err != nil {
		t.Fatalf("derive trusted Shiro scope: %v", err)
	}
	ctx, err = domainexecution.WithIdentity(ctx, input.RootTaskID(), modulecore.NewRunID(), input.TraceID())
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: "system", Content: "character prompt", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "stable context", Type: llm.PromptContextStable},
		{Role: "system", Content: "recall memory", Type: llm.PromptContextRecall},
		{Role: "system", Content: "variable context", Type: llm.PromptContextVariable},
		{Role: "user", Content: text, Type: llm.PromptContextUser},
	}
	return ctx, input, messages
}

func mustContain(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must contain %q: %s", what, want, text)
		}
	}
}

func mustNotContain(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if bad != "" && strings.Contains(text, bad) {
			t.Fatalf("%s must not contain %q: %s", what, bad, text)
		}
	}
}
