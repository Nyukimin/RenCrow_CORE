package delegation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func delegate(t *testing.T, d *testDeployment, text string) (agent.NativeCodingResult, error, domainexecution.Identity) {
	t.Helper()
	ctx, input, messages := newTurn(t, text)
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	return result, err, identity
}

func TestDelegationHappyPathRecordsOneDelegationActionAndCorrelatesBothSides(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, secretText)
	identity, _ := domainexecution.IdentityFromContext(ctx)

	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding: %v", err)
	}
	c := d.client(0)

	// CORE side: one delegation Action in the parent Task Run, closed succeeded.
	if len(d.rec.created) != 1 {
		t.Fatalf("exactly one delegation Action must be recorded, got %d", len(d.rec.created))
	}
	created := d.rec.created[0]
	if created.Kind != domainaction.KindDelegation || created.TaskID != identity.TaskID || created.RunID != identity.RunID {
		t.Fatalf("the delegation Action must belong to the parent Task Run: %+v", created)
	}
	if len(d.rec.completed) != 1 || d.rec.completed[0].attempt != domainaction.AttemptStatusSucceeded || d.rec.completed[0].action != domainaction.StatusSucceeded {
		t.Fatalf("the Action must be closed once, succeeded: %+v", d.rec.completed)
	}
	if result.ActionID != d.rec.actionID || result.AttemptID != d.rec.attemptID {
		t.Fatalf("the result must name CORE's Action and Attempt: %+v", result)
	}

	// Harness side: the keys and the upstream reference carry CORE's IDs.
	attempt := string(d.rec.attemptID)
	if len(c.opens) != 1 || c.opens[0].IdempotencyKey != "core."+attempt+".open" {
		t.Fatalf("session/open key: %+v", c.opens)
	}
	open := c.opens[0]
	if open.WorkspacePath != d.settings.Workspace.Path || open.PolicyRef != "workspace-write" || open.ExecutionMode != protocol.ModeStructuredOnly {
		t.Fatalf("the session must use the configured workspace: %+v", open)
	}
	if open.Binding.Kind != "alias" || open.Binding.Selector != "shiro-worker-exec" || open.Binding.ProfileRevision != "rev-1" ||
		open.Binding.AgentID == nil || *open.Binding.AgentID != "shiro" || open.Binding.ExecutionRole == nil || *open.Binding.ExecutionRole != "worker" {
		t.Fatalf("the binding must be the existing Shiro execution alias with agent_id shiro: %+v", open.Binding)
	}
	if len(c.starts) != 1 {
		t.Fatalf("turn/start must be sent once, got %d", len(c.starts))
	}
	start := c.starts[0]
	if start.IdempotencyKey != "core."+attempt+".start" {
		t.Fatalf("turn/start key = %q", start.IdempotencyKey)
	}
	if start.ThreadID != c.thread || start.ExpectedContextRevision != 3 || start.ExpectedControlRevision != 4 {
		t.Fatalf("the start must target the opened thread with its revisions: %+v", start)
	}
	if start.Input.Text != secretText || start.Input.OriginProof != nil {
		t.Fatalf("the user text is sent as given, without any OriginProof (Automation): %+v", start.Input)
	}
	if start.Limits != d.settings.Limits {
		t.Fatalf("the complete configured limits must be sent: %+v", start.Limits)
	}
	up := start.Upstream
	if up == nil || up.Owner != "RenCrow_CORE" || up.TaskID != string(identity.TaskID) || up.TraceID != string(input.TraceID()) {
		t.Fatalf("upstream must reference CORE's parent Task and Trace: %+v", up)
	}
	if up.TurnID == nil || *up.TurnID != string(input.TurnID()) || up.ActionID == nil || *up.ActionID != string(d.rec.actionID) ||
		up.AttemptID == nil || *up.AttemptID != attempt {
		t.Fatalf("upstream must reference CORE's turn, Action and Attempt: %+v", up)
	}
	if up.SessionID != nil || up.ThreadID != nil {
		t.Fatalf("CORE has no canonical session or thread for the turn yet (null): %+v", up)
	}

	// The ContextBlocks are the typed F32 projection of the messages.
	want, userText, err := nativeharnessclient.MaterializeContextRevision(messages, nil)
	if err != nil || userText != secretText {
		t.Fatalf("Materialize: %v %q", err, userText)
	}
	if len(start.ContextBlocks) != len(want) || len(want) != 4 {
		t.Fatalf("four typed blocks expected, got %d", len(start.ContextBlocks))
	}
	for i, block := range start.ContextBlocks {
		if block.Kind != string(want[i].Kind) || block.Text != want[i].Text || block.Revision != want[i].Revision || block.Source != nil {
			t.Fatalf("block %d differs from the F32 projection (and Recall has no source yet): %+v vs %+v", i, block, want[i])
		}
	}
	if len(c.awaits) != 1 || c.awaits[0].runID != c.run || c.awaits[0].opts.InterruptKeyPrefix != "core."+attempt+".stop" {
		t.Fatalf("the Run must be awaited with a stop key tied to the Attempt: %+v", c.awaits)
	}

	// The projection keeps the Harness IDs with their owner.
	if result.HarnessTask != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.task}) ||
		result.HarnessRun != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.run}) ||
		result.HarnessReceipt != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.receipt}) {
		t.Fatalf("Harness IDs must carry their owner: %+v", result)
	}
	if !result.Accepted() || result.FinalText != secretFinal {
		t.Fatalf("completed + passed: %+v", result)
	}

	// The CORE record names the Harness IDs by owner and holds no content.
	summary := d.rec.completed[0].summary
	mustContain(t, "summary", summary, "status=completed", "verification=passed",
		"harness_task=RenCrow_Harness/"+c.task, "harness_run=RenCrow_Harness/"+c.run, "harness_receipt=RenCrow_Harness/"+c.receipt)
	mustNotContain(t, "summary", summary, secretText, secretFinal)
}

func TestDelegationKeepsTheRunResultStatusesAndMapsTheActionClosure(t *testing.T) {
	cases := []struct {
		status, code, verification string
		resumable                  bool
		wantAttempt                domainaction.AttemptStatus
		wantAction                 domainaction.Status
	}{
		{"completed", "", "passed", false, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded},
		{"completed", "", "not_run", false, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded},
		{"completed", "", "failed", false, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded},
		{"incomplete", "DEADLINE_EXCEEDED", "not_run", true, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"rejected", "POLICY_REJECTED", "not_run", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"blocked", "MODEL_GENERATION_OUTCOME_UNKNOWN", "unknown", true, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"cancelled", "CANCELLED", "not_run", true, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled},
		{"failed", "TOOL_FAILED", "failed", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"restart_required", "PERSISTENCE_UNCERTAIN", "unknown", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.status+"/"+tc.verification, func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
					result := c.runResult(tc.status, tc.verification)
					result.Code, result.Resumable = tc.code, tc.resumable
					return result, nil
				}
				return c, nil
			}
			result, err, _ := delegate(t, d, "x")
			if err != nil {
				t.Fatalf("a Run the Harness determined is a result, not an error: %v", err)
			}
			if string(result.Status) != tc.status || result.Code != tc.code || string(result.Verification) != tc.verification || result.Resumable != tc.resumable {
				t.Fatalf("the RunResult must be kept as it is: %+v", result)
			}
			if got := result.Accepted(); got != (tc.status == "completed" && tc.verification == "passed") {
				t.Fatalf("Accepted() = %v for %s/%s", got, tc.status, tc.verification)
			}
			done := d.rec.completed[0]
			if done.attempt != tc.wantAttempt || done.action != tc.wantAction {
				t.Fatalf("closure = %s/%s, want %s/%s", done.attempt, done.action, tc.wantAttempt, tc.wantAction)
			}
			mustContain(t, "summary", done.summary, "status="+tc.status, "verification="+tc.verification, "resumable=")
		})
	}
}

func TestDelegationResendsTheSameStartPayloadWithTheSameKeyAfterAnUnknownOutcome(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, in protocol.StartInput) (protocol.StartResult, error) {
			if n < 3 {
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
			}
			return c.startResult(in), nil
		}
		return c, nil
	}
	result, err, _ := delegate(t, d, "x")
	if err != nil || !result.Accepted() {
		t.Fatalf("the third send answers: %+v %v", result, err)
	}
	c := d.client(0)
	if len(c.starts) != 3 {
		t.Fatalf("sends = %d, want 3", len(c.starts))
	}
	for i := 1; i < len(c.starts); i++ {
		if !reflect.DeepEqual(c.starts[0], c.starts[i]) {
			t.Fatalf("a resend must repeat the first payload unchanged:\n first %+v\n resend %+v", c.starts[0], c.starts[i])
		}
	}
	if len(c.opens) != 1 {
		t.Fatalf("the session is opened once, got %d", len(c.opens))
	}
	if len(d.rec.created) != 1 {
		t.Fatal("a resend must not create a second delegation Action")
	}
}

func TestDelegationNeverMakesANewKeyWhenTheOutcomeStaysUnknown(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
			return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("an outcome that stays unknown must be reported as such: %v", err)
	}
	c := d.client(0)
	keys := map[string]bool{}
	for _, start := range c.starts {
		keys[start.IdempotencyKey] = true
	}
	if len(c.starts) != maxSends || len(keys) != 1 {
		t.Fatalf("the same request is sent %d times with one key, got %d sends and keys %v", maxSends, len(c.starts), keys)
	}
	if len(c.awaits) != 0 {
		t.Fatal("no Run is awaited when no acceptance is known")
	}
	if len(d.rec.completed) != 1 || d.rec.completed[0].attempt != domainaction.AttemptStatusFailed {
		t.Fatalf("the Attempt must be closed failed once: %+v", d.rec.completed)
	}
	if got := len(d.log.events(t, eventDelegateUnknown)); got != 1 {
		t.Fatalf("the unknown outcome is logged once, got %d", got)
	}
}

func TestDelegationSendsTheSameInputToAReplacementClientWhenTheConnectionEndedMidStart(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(n int, _ client.Config) (*fakeClient, error) {
		c := newFakeClient()
		if n == 1 {
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				c.end() // the child died after the request was written
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed)
			}
		}
		return c, nil
	}
	result, err, _ := delegate(t, d, "x")
	if err != nil || !result.Accepted() {
		t.Fatalf("the replacement answers: %+v %v", result, err)
	}
	if d.startCalls() != 2 {
		t.Fatalf("a replacement Harness must be started, starts = %d", d.startCalls())
	}
	first, second := d.client(0).starts, d.client(1).starts
	if len(first) != 1 || len(second) != 1 || !reflect.DeepEqual(first[0], second[0]) {
		t.Fatalf("the replacement must receive the first payload unchanged: %+v vs %+v", first, second)
	}
	if len(d.client(1).opens) != 0 {
		t.Fatal("the session is not opened again for the same delegation")
	}
}

func TestDelegationRefusalsAreTypedAndNothingIsAwaited(t *testing.T) {
	t.Run("session refused", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onOpen = func(int, protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
				return protocol.SessionOpenResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "no"}}
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) {
			t.Fatalf("a refused session must block: %v", err)
		}
		mustContain(t, "refusal", err.Error(), "harness_refused_FORBIDDEN")
		if len(d.client(0).starts) != 0 || len(d.client(0).awaits) != 0 {
			t.Fatal("nothing may be started after a refused session")
		}
		if got := d.rec.completed; len(got) != 1 || got[0].attempt != domainaction.AttemptStatusFailed {
			t.Fatalf("the Attempt must close failed: %+v", got)
		}
	})
	t.Run("start refused as invalid", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				return protocol.StartResult{}, protocol.NewError(protocol.CodeInvalidParams, "bad")
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingRejected) {
			t.Fatalf("an input the protocol refuses is a rejection: %v", err)
		}
	})
	t.Run("start refused by the Harness", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				return protocol.StartResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeBusy, Message: "busy"}}
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) {
			t.Fatalf("a Harness refusal blocks the turn: %v", err)
		}
		if len(d.client(0).starts) != 1 {
			t.Fatal("a refusal is an answer: the request is not sent again")
		}
	})
}

func TestDelegationPrerequisitesFailBeforeAnythingIsStarted(t *testing.T) {
	t.Run("no execution identity", func(t *testing.T) {
		d := newDeployment(t)
		_, input, messages := newTurn(t, "x")
		_, err := d.runtime.DelegateNativeCoding(context.Background(), agent.NativeCodingRequest{Input: input, Messages: messages})
		if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("unusable messages", func(t *testing.T) {
		d := newDeployment(t)
		ctx, input, _ := newTurn(t, "x")
		_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: []llm.Message{{Role: "system", Content: "flat", Type: llm.PromptContextCharacter}}})
		if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		d := newDeployment(t)
		ctx, input, messages := newTurn(t, "x")
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		if !errors.Is(err, context.Canceled) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("Action cannot be recorded", func(t *testing.T) {
		d := newDeployment(t)
		d.rec.createErr = errors.New("store down")
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) {
			t.Fatalf("a delegation that CORE cannot record must not run: %v", err)
		}
		if len(d.client(0).opens) != 0 {
			t.Fatal("no session may be opened for a delegation that has no Action")
		}
	})
	t.Run("Harness unavailable", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) { return nil, errors.New("down") }
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) || len(d.rec.created) != 0 {
			t.Fatalf("err=%v actions=%d", err, len(d.rec.created))
		}
	})
}

func TestDelegationCancellationBecomesAStopSignalAndTheRunsEndIsKept(t *testing.T) {
	d := newDeployment(t)
	entered := make(chan struct{})
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		// Like the real client: when the context ends, the stop signal is recorded
		// (through the interrupt key) and the Run's end is waited for.
		c.onAwait = func(ctx context.Context, _ string, opts client.AwaitOptions) (protocol.RunResult, error) {
			close(entered)
			<-ctx.Done()
			if opts.InterruptKeyPrefix == "" {
				return protocol.RunResult{}, ctx.Err()
			}
			result := c.runResult("cancelled", "not_run")
			result.Code, result.Resumable = "CANCELLED", true
			return result, nil
		}
		return c, nil
	}
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result agent.NativeCodingResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the delegation never reached the wait")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil || got.result.Status != agent.NativeRunCancelled || !got.result.Resumable {
			t.Fatalf("a cancelled Run is a typed result: %+v %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the delegation did not return after the cancellation")
	}
	// The Action closes with a context the cancellation cannot cut short.
	if len(d.rec.completed) != 1 || d.rec.completed[0].attempt != domainaction.AttemptStatusCancelled || d.rec.completed[0].action != domainaction.StatusCancelled {
		t.Fatalf("a cancelled delegation closes cancelled, even though the turn's context ended: %+v", d.rec.completed)
	}
	c := d.client(0)
	if c.awaits[0].opts.InterruptKeyPrefix == "" {
		t.Fatal("the cancellation must be tied to the Run through the stop key")
	}
	if len(c.starts) != 1 {
		t.Fatal("a cancellation must not start anything again")
	}
}

func TestDelegationAStopThatDoesNotEndTheRunIsAnUnknownOutcome(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onAwait = func(ctx context.Context, _ string, _ client.AwaitOptions) (protocol.RunResult, error) {
			<-ctx.Done()
			return protocol.RunResult{}, errors.Join(client.ErrRunStillActive, ctx.Err())
		}
		return c, nil
	}
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a stop signal is a record, not proof: %v", err)
	}
	if got := d.rec.completed; len(got) != 1 || got[0].attempt != domainaction.AttemptStatusCancelled || !strings.Contains(got[0].summary, "stop_signal_recorded") {
		t.Fatalf("the Attempt must be closed cancelled with the stop recorded: %+v", got)
	}
}

func TestDelegationALostConnectionWhileAwaitingIsAnUnknownOutcomeAndNotReStarted(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
			c.end()
			return protocol.RunResult{}, client.ErrClosed
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("the end of the Run is unknown: %v", err)
	}
	if d.startCalls() != 1 {
		t.Fatalf("an accepted delegation must not be started again, starts = %d", d.startCalls())
	}
	mustContain(t, "summary", d.rec.completed[0].summary, "outcome_unknown", "harness_run=RenCrow_Harness/")
}

func TestDelegationLogsContainNoContentPathOrKeyAndNameHarnessIDsByOwner(t *testing.T) {
	d := newDeployment(t)
	delegate(t, d, secretText)
	var all strings.Builder
	for _, line := range d.log.all() {
		all.WriteString(line)
		all.WriteByte('\n')
	}
	mustNotContain(t, "log", all.String(), secretText, secretFinal, d.settings.HarnessBinary, d.settings.HarnessConfig, "mac", "origin_proof")
	accepted := d.log.events(t, eventDelegateAccepted)
	if len(accepted) != 1 {
		t.Fatalf("one acceptance line expected, got %d", len(accepted))
	}
	task, ok := accepted[0]["harness_task"].(map[string]any)
	if !ok || task["owner"] != agent.HarnessOwner || task["id"] != d.client(0).task {
		t.Fatalf("a Harness ID is logged only with its owner: %+v", accepted[0])
	}
	for _, field := range []string{"trace_id", "task_id", "action_id", "attempt_id", "run_id", "ts", "level", "event", "module", "schema_version"} {
		if _, ok := accepted[0][field]; !ok {
			t.Fatalf("log line misses %q: %+v", field, accepted[0])
		}
	}
	if accepted[0]["module"] != "RenCrow_CORE" {
		t.Fatalf("module = %v", accepted[0]["module"])
	}
	if got := len(d.log.events(t, eventDelegateFinished)); got != 1 {
		t.Fatalf("one finish line expected, got %d", got)
	}
}

func TestDelegationStartInputSatisfiesTheProtocolSchema(t *testing.T) {
	d := newDeployment(t)
	if _, err, _ := delegate(t, d, "x"); err != nil {
		t.Fatal(err)
	}
	// The fake refuses a payload that fails protocol.Encode like the real client,
	// so a delegation that succeeded sent schema-valid, canonical payloads. Check
	// the IDs that CORE issues are also in the Harness's ID grammar.
	start := d.client(0).starts[0]
	for name, id := range map[string]string{"task": start.Upstream.TaskID, "trace": start.Upstream.TraceID, "turn": *start.Upstream.TurnID, "action": *start.Upstream.ActionID, "attempt": *start.Upstream.AttemptID} {
		if id == "" {
			t.Fatalf("%s id is empty", name)
		}
	}
	if _, err := protocol.Encode(start); err != nil {
		t.Fatalf("the start payload must satisfy the schema: %v", err)
	}
}

func TestDelegationAnUnknownOutcomeStaysUnknownWhenLaterSendsFindTheConnectionClosed(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, _ protocol.StartInput) (protocol.StartResult, error) {
			if n == 1 {
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed) // written, then the connection ended
			}
			return protocol.StartResult{}, client.ErrClosed // later sends find it closed: not evidence that nothing was delivered
		}
		return c, nil // Done stays open: the connection is not replaced
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("a delivered request must not be reported as refused: %v", err)
	}
	if errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("an unknown outcome is not a block: %v", err)
	}
	if got := d.rec.completed[0].summary; strings.Contains(got, "refused") {
		t.Fatalf("the Action must not say the Harness refused: %q", got)
	}
}

func TestDelegationAReplacementThatCannotBeStartedLeavesTheOutcomeUnknown(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(n int, _ client.Config) (*fakeClient, error) {
		if n > 1 {
			return nil, errors.New("down")
		}
		c := newFakeClient()
		c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
			c.end()
			return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed)
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("the request may have reached the first Harness: %v", err)
	}
}

func TestDelegationEverySendHasAContextOfItsOwnThatTheTurnsCancellationDoesNotReach(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onOpen = func(_ int, _ protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
			cancel() // the turn is cancelled while the session is being opened
			return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: c.session()}, nil
		}
		c.onAwait = func(ctx context.Context, _ string, opts client.AwaitOptions) (protocol.RunResult, error) {
			result := c.runResult("cancelled", "not_run")
			result.Code = "CANCELLED"
			return result, nil
		}
		return c, nil
	}
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil || result.Status != agent.NativeRunCancelled {
		t.Fatalf("the Run is started and then stopped: %+v %v", result, err)
	}
	c := d.client(0)
	if len(c.startCtxErrs) != 1 || c.startCtxErrs[0] != nil || !c.startHadDeadline[0] {
		t.Fatalf("turn/start must be sent with a live context that has its own deadline even after the turn was cancelled: %v %v", c.startCtxErrs, c.startHadDeadline)
	}
	if len(c.openCtxErrs) != 1 || c.openCtxErrs[0] != nil {
		t.Fatalf("session/open context: %v", c.openCtxErrs)
	}
}

func TestDelegationClassifiesHowTheStopEndedWithoutClaimingARecordedSignal(t *testing.T) {
	cases := []struct {
		name        string
		awaitErr    error
		cancel      bool
		wantAttempt domainaction.AttemptStatus
		wantInSum   string
		notInSum    string
	}{
		{"stop recorded, run still active", errors.Join(client.ErrRunStillActive, context.Canceled), true, domainaction.AttemptStatusCancelled, "stop_signal_recorded", ""},
		{"stop could not be recorded", errors.Join(context.Canceled, client.ErrClosed), true, domainaction.AttemptStatusFailed, "stop_signal_unconfirmed", "stop_signal_recorded"},
		{"CORE is shutting down", client.ErrStopping, false, domainaction.AttemptStatusCancelled, "core_shutdown", "stop_signal_recorded"},
		{"connection ended without a stop", client.ErrClosed, false, domainaction.AttemptStatusFailed, "outcome_unknown", "stop_signal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "x")
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
					if tc.cancel {
						cancel() // the turn is cancelled while the Run is awaited
					}
					return protocol.RunResult{}, tc.awaitErr
				}
				return c, nil
			}
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("err = %v", err)
			}
			done := d.rec.completed[0]
			if done.attempt != tc.wantAttempt {
				t.Fatalf("attempt = %s, want %s", done.attempt, tc.wantAttempt)
			}
			mustContain(t, "summary", done.summary, tc.wantInSum)
			mustNotContain(t, "summary", done.summary, tc.notInSum)
		})
	}
}
