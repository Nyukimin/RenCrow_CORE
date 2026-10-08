package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// delegationActionName is the name of the one delegation Action of a Run.
	delegationActionName = "native_harness.delegate"
	// maxSends is how many times one unchanged request is sent while its outcome
	// stays unknown: the first send and two resends.
	maxSends = 3
)

// DelegateNativeCoding implements agent.NativeCodingDelegate: it hands one
// selected turn to RenCrow_Harness and returns the Harness's RunResult as the
// typed projection.
//
// One delegation is: a delegation Action and Attempt recorded in the parent
// Task Run (the Harness's own Tool Actions are not re-issued here); a Harness
// Session opened under the key core.<AttemptID>.open; one turn/start under
// core.<AttemptID>.start carrying the typed ContextBlocks (F32, Recall without
// a source for now), the user text, CORE's IDs as the upstream reference, the
// configured complete limits and no OriginProof (Automation: see the package
// documentation); and the wait for the Run's end.
//
// The first start payload is kept and sent again unchanged, with the same key,
// whenever its outcome is unknown. A new key is never made for the same
// delegation, so a lost answer cannot become a second Harness Task. The
// cancellation of ctx becomes a turn/interrupt (a stop signal is a record, not
// proof that anything stopped), after which the Run's end is waited for.
//
// It returns an error only when there is no RunResult: the delegation was
// refused or never started (agent.ErrNativeCodingRejected,
// agent.ErrNativeCodingBlocked) or its end cannot be known
// (agent.ErrNativeCodingOutcomeUnknown). It never runs the work elsewhere.
func (r *Runtime) DelegateNativeCoding(ctx context.Context, request agent.NativeCodingRequest) (agent.NativeCodingResult, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, rejected("execution_identity_required")
	}
	blocks, userText, err := nativeharnessclient.MaterializeContextRevision(request.Messages, nil)
	if err != nil {
		return agent.NativeCodingResult{}, rejected("context_invalid")
	}
	if err := ctx.Err(); err != nil {
		return agent.NativeCodingResult{}, err // nothing was started
	}
	c, err := r.acquire(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	action, attempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: identity.TaskID, RunID: identity.RunID, Kind: domainaction.KindDelegation, Name: delegationActionName,
	})
	if err != nil {
		return agent.NativeCodingResult{}, blocked("action_not_recorded")
	}
	d := &delegation{
		r:         r,
		actionID:  action.ActionID,
		attemptID: attempt.AttemptID,
		parent:    identity,
		input:     request.Input,
		blocks:    blocks,
		userText:  userText,
	}
	d.corr = correlation{
		traceID: string(request.Input.TraceID()), taskID: string(identity.TaskID), turnID: string(request.Input.TurnID()),
		actionID: string(action.ActionID), attemptID: string(attempt.AttemptID),
	}
	return d.run(ctx, c)
}

// delegation is the state of one delegation. It is used by one goroutine.
type delegation struct {
	r         *Runtime
	actionID  modulecore.ActionID
	attemptID modulecore.AttemptID
	parent    domainexecution.Identity
	input     conversation.TurnInput
	blocks    []nativeharnessclient.ContextBlock
	userText  string
	corr      correlation
	closed    bool
}

func (d *delegation) key(suffix string) string { return "core." + string(d.attemptID) + "." + suffix }

func (d *delegation) logFields(extra map[string]any) map[string]any {
	fields := d.corr.fields()
	fields["run_id"] = string(d.parent.RunID)
	for key, value := range extra {
		fields[key] = value
	}
	return fields
}

func (d *delegation) run(ctx context.Context, c Client) (agent.NativeCodingResult, error) {
	r := d.r
	r.log.emit("info", eventDelegateStarted, d.logFields(nil))

	// Opening the session and starting the turn are short and must not be cut
	// short by the turn's own cancellation: a cancellation between the write and
	// the answer would leave a Run whose existence CORE cannot learn. Each send
	// has a context of its own that the cancellation does not reach (and that has
	// its own StepTimeout); the cancellation is honored right after, by the stop
	// signal.
	openInput := protocol.SessionOpenInput{
		WorkspacePath: r.settings.Workspace.Path,
		Binding: protocol.Binding{
			Kind: "alias", Selector: r.settings.Binding.Selector, ProfileRevision: r.settings.Binding.ProfileRevision,
			AgentID: protocol.Str(shiroAgentID), ExecutionRole: protocol.Str(r.settings.Binding.ExecutionRole),
		},
		PolicyRef:      r.settings.Workspace.PolicyRef,
		ExecutionMode:  r.settings.Workspace.ExecutionMode,
		IdempotencyKey: d.key("open"),
	}
	open, c, err := sendUnchanged(ctx, r, c, func(stepCtx context.Context, c Client) (protocol.SessionOpenResult, error) {
		return c.SessionOpen(stepCtx, openInput)
	})
	if err != nil {
		return d.refused(ctx, err)
	}
	threadID := open.Session.ThreadID
	r.threads.put(threadID, d.corr)

	startInput := d.startInput(open.Session)
	start, c, err := sendUnchanged(ctx, r, c, func(stepCtx context.Context, c Client) (protocol.StartResult, error) {
		return c.TurnStart(stepCtx, startInput)
	})
	if err != nil {
		return d.refused(ctx, err)
	}
	r.log.emit("info", eventDelegateAccepted, d.logFields(map[string]any{
		"harness_thread": harnessRef(start.ThreadID), "harness_task": harnessRef(start.TaskID),
		"harness_run": harnessRef(start.RunID), "harness_receipt": harnessRef(start.ReceiptID),
	}))

	runResult, err := c.AwaitRun(ctx, start.RunID, client.AwaitOptions{
		InterruptKeyPrefix: d.key("stop"),
		CancelGrace:        r.settings.CancelGrace,
	})
	if err != nil {
		return d.unknownEnd(ctx, start, err)
	}
	result := d.project(runResult, start)
	d.finish(ctx, result)
	return result, nil
}

// startInput builds the one turn/start payload of the delegation. It is built
// once and sent unchanged for every resend.
func (d *delegation) startInput(session protocol.SessionInfo) protocol.StartInput {
	blocks := make([]protocol.ContextBlock, 0, len(d.blocks))
	for _, block := range d.blocks {
		converted := protocol.ContextBlock{Kind: string(block.Kind), Text: block.Text, Revision: block.Revision}
		if block.Source != nil {
			converted.Source = &protocol.SourceRef{
				Owner: block.Source.Owner, SourceID: block.Source.SourceID, RawHash: block.Source.RawHash,
				ProjectionVersion: block.Source.ProjectionVersion,
				Range:             protocol.ByteRange{Start: block.Source.Range.Start, End: block.Source.Range.End},
				Origin:            block.Source.Origin, Sequence: block.Source.Sequence,
			}
		}
		blocks = append(blocks, converted)
	}
	turnID, actionID, attemptID := string(d.input.TurnID()), string(d.actionID), string(d.attemptID)
	return protocol.StartInput{
		ThreadID: session.ThreadID,
		// No OriginProof: a request CORE generated is Automation. CORE has no
		// ThreadID at reception time and no store of the accepted original input
		// (design inquiries D5 and D6), so it cannot sign a relay of the user's
		// own words. Human authority is therefore not claimed.
		Input:         protocol.InputMessage{Text: d.userText},
		ContextBlocks: blocks,
		Upstream: &protocol.Upstream{
			Owner: upstreamOwner, TaskID: string(d.parent.TaskID), TraceID: string(d.input.TraceID()),
			TurnID: &turnID, ActionID: &actionID, AttemptID: &attemptID,
			// CORE has no canonical Session or Thread ID for this turn (D5): null.
		},
		ExpectedContextRevision: session.ContextRevision,
		ExpectedControlRevision: session.ControlRevision,
		IdempotencyKey:          d.key("start"),
		Limits:                  d.r.settings.Limits,
	}
}

// project keeps the RunResult as the typed value it is. completed is only that a
// valid final answer was settled; the verification is a separate statement and
// is carried as it is.
func (d *delegation) project(runResult protocol.RunResult, start protocol.StartResult) agent.NativeCodingResult {
	return agent.NativeCodingResult{
		Status:         agent.NativeCodingRunStatus(runResult.Status),
		Code:           runResult.Code,
		FinalText:      runResult.FinalText,
		Verification:   agent.NativeCodingVerification(runResult.Verification.Status),
		Resumable:      runResult.Resumable,
		HarnessTask:    harnessRef(runResult.TaskID),
		HarnessRun:     harnessRef(runResult.RunID),
		HarnessReceipt: harnessRef(start.ReceiptID),
		ActionID:       d.actionID,
		AttemptID:      d.attemptID,
	}
}

// finish closes the delegation Action with the end the Harness determined:
// completed is a succeeded delegation (not an accepted Task: the verification is
// in the summary), cancelled a cancelled one, every other status a failed one.
func (d *delegation) finish(ctx context.Context, result agent.NativeCodingResult) {
	attemptStatus, actionStatus := domainaction.AttemptStatusFailed, domainaction.StatusFailed
	switch result.Status {
	case agent.NativeRunCompleted:
		attemptStatus, actionStatus = domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded
	case agent.NativeRunCancelled:
		attemptStatus, actionStatus = domainaction.AttemptStatusCancelled, domainaction.StatusCancelled
	}
	d.complete(ctx, attemptStatus, actionStatus, summarize(result))
	d.r.log.emit("info", eventDelegateFinished, d.logFields(map[string]any{
		"status": string(result.Status), "code": result.Code, "verification": string(result.Verification), "resumable": result.Resumable,
		"accepted": result.Accepted(), "harness_task": result.HarnessTask, "harness_run": result.HarnessRun, "harness_receipt": result.HarnessReceipt,
	}))
}

// complete closes the Action once, with a context that the turn's cancellation
// cannot cut short. A failure to record is logged and does not change the
// delegation's result.
func (d *delegation) complete(ctx context.Context, attemptStatus domainaction.AttemptStatus, actionStatus domainaction.Status, summary string) {
	if d.closed {
		return
	}
	d.closed = true
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, _, err := d.r.actions.CompleteAttempt(recordCtx, d.actionID, d.attemptID, attemptStatus, actionStatus, summary); err != nil {
		d.r.log.emit("error", "native_harness.delegate.record_failed", d.logFields(nil))
	}
}

// refused ends a delegation whose session or start was refused or never made.
// Nothing ran in the Harness (a refusal writes nothing), so the Attempt failed
// and the turn ends with a typed error, never on another route.
func (d *delegation) refused(ctx context.Context, err error) (agent.NativeCodingResult, error) {
	if errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		d.complete(ctx, domainaction.AttemptStatusFailed, domainaction.StatusFailed, "native_harness outcome_unknown before the Run was admitted")
		d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{"phase": "start"}))
		return agent.NativeCodingResult{}, err
	}
	typed := classifyRefusal(err)
	detail := refusalDetail(typed)
	d.complete(ctx, domainaction.AttemptStatusFailed, domainaction.StatusFailed, "native_harness refused: "+detail)
	d.r.log.emit("error", eventDelegateFinished, d.logFields(map[string]any{"status": "refused", "code": detail}))
	return agent.NativeCodingResult{}, typed
}

// unknownEnd ends a delegation that the Harness accepted but whose end CORE
// could not learn: the connection ended, or a stop was recorded and the Run did
// not end in time. The Run keeps its own state in the Harness store; CORE does
// not start the work again.
func (d *delegation) unknownEnd(ctx context.Context, start protocol.StartResult, cause error) (agent.NativeCodingResult, error) {
	summary := fmt.Sprintf("native_harness outcome_unknown harness_task=%s/%s harness_run=%s/%s harness_receipt=%s/%s",
		agent.HarnessOwner, start.TaskID, agent.HarnessOwner, start.RunID, agent.HarnessOwner, start.ReceiptID)
	attemptStatus, actionStatus := domainaction.AttemptStatusFailed, domainaction.StatusFailed
	switch {
	case errors.Is(cause, client.ErrRunStillActive):
		// The stop signal was recorded and the Run did not end within the grace.
		attemptStatus, actionStatus = domainaction.AttemptStatusCancelled, domainaction.StatusCancelled
		summary += " stop_signal_recorded"
	case errors.Is(cause, client.ErrStopping):
		// CORE is shutting down: the orderly stop asked the Harness to cancel its
		// Runs, and the client no longer reads them.
		attemptStatus, actionStatus = domainaction.AttemptStatusCancelled, domainaction.StatusCancelled
		summary += " core_shutdown"
	case ctx.Err() != nil:
		summary += " stop_signal_unconfirmed"
	}
	d.complete(ctx, attemptStatus, actionStatus, summary)
	d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{
		"phase": "await", "harness_task": harnessRef(start.TaskID), "harness_run": harnessRef(start.RunID), "harness_receipt": harnessRef(start.ReceiptID),
	}))
	err := fmt.Errorf("%w: the Run's end could not be read", agent.ErrNativeCodingOutcomeUnknown)
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(err, ctxErr)
	}
	return agent.NativeCodingResult{}, err
}

// sendUnchanged sends one request until its outcome is known, always with the
// same input and the same idempotency key (the Harness answers a repeated key
// from its receipt). A start that may have been delivered is never made again
// under a new key, so a lost answer cannot become a second Harness Task. A
// connection that ended is replaced by a client of the same Runtime, and the
// same input goes to it.
//
// Once any send has had an unknown outcome the request stays unknown until a
// send is answered (accepted or refused): a later send that finds the
// connection already closed is not evidence that nothing was delivered.
//
// Every send has its own context, detached from parent's cancellation and
// bounded by StepTimeout, so a slow replacement of the client cannot use up the
// time of the next send.
func sendUnchanged[T any](parent context.Context, r *Runtime, c Client, call func(context.Context, Client) (T, error)) (T, Client, error) {
	var zero T
	everUnknown := false
	unknown := func() error {
		return fmt.Errorf("%w: the request may have been delivered", agent.ErrNativeCodingOutcomeUnknown)
	}
	for attempt := 1; ; attempt++ {
		stepCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), r.settings.StepTimeout)
		out, err := call(stepCtx, c)
		cancel()
		if err == nil {
			return out, c, nil
		}
		lost := errors.Is(err, client.ErrOutcomeUnknown)
		everUnknown = everUnknown || lost
		// Not answered: the outcome was lost, the connection was already closed, or
		// the send did not get written in time. Anything else is an answer (a
		// refusal) or an input that cannot be sent, and ends the request.
		retry := lost || errors.Is(err, client.ErrClosed) || errors.Is(err, context.DeadlineExceeded)
		if !retry {
			return zero, c, err
		}
		if attempt >= maxSends {
			if everUnknown {
				return zero, c, unknown()
			}
			return zero, c, err
		}
		time.Sleep(r.backoff(attempt))
		select {
		case <-c.Done():
			next, acquireErr := r.acquire(parent)
			if acquireErr != nil {
				if everUnknown {
					return zero, c, errors.Join(unknown(), acquireErr)
				}
				return zero, c, acquireErr
			}
			c = next
		default:
		}
	}
}

// classifyRefusal turns the error of a refused call into the typed refusal of
// the profile. A request that does not satisfy the protocol is a rejection (the
// input cannot be sent); every other refusal or failure is a block.
func classifyRefusal(err error) error {
	if errors.Is(err, agent.ErrNativeCodingBlocked) || errors.Is(err, agent.ErrNativeCodingRejected) {
		return err
	}
	var remote *client.RemoteError
	switch code := protocol.CodeOf(err); {
	case errors.As(err, &remote):
		return blocked("harness_refused_" + code)
	case code == protocol.CodeInvalidParams || code == protocol.CodeInvalidRequest:
		return rejected("request_invalid")
	case errors.Is(err, client.ErrClosed):
		return blocked("harness_connection_closed")
	default:
		return blocked("harness_call_failed")
	}
}

// refusalDetail is the cause code of a typed refusal, for the Action summary and
// the log (the code only: no path, no Harness message).
func refusalDetail(err error) string {
	for _, sentinel := range []error{agent.ErrNativeCodingRejected, agent.ErrNativeCodingBlocked} {
		if errors.Is(err, sentinel) {
			return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
		}
	}
	return "refused"
}

// summarize is the summary kept on the delegation Action and Attempt: the
// status values and the Harness references with their owner, never the final
// text, the prompt or any Tool content.
func summarize(result agent.NativeCodingResult) string {
	return fmt.Sprintf("native_harness status=%s code=%s verification=%s resumable=%t harness_task=%s/%s harness_run=%s/%s harness_receipt=%s/%s",
		result.Status, orDash(result.Code), result.Verification, result.Resumable,
		result.HarnessTask.Owner, result.HarnessTask.ID, result.HarnessRun.Owner, result.HarnessRun.ID,
		result.HarnessReceipt.Owner, result.HarnessReceipt.ID)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
