package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestCriteriaProofOwnerReadFailuresKeepTheActualResultUnadopted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeClient)
	}{
		{
			name: "criteria mismatch",
			setup: func(c *fakeClient) {
				c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
					result := c.runResult("completed", "passed")
					wrong := strings.Repeat("b", 64)
					result.Verification.CriteriaRevision = &wrong
					return result, nil
				}
			},
		},
		{
			name: "RunGet identifies another Run",
			setup: func(c *fakeClient) {
				c.onRunGet = func(protocol.RunGetInput) (protocol.RunInfo, error) {
					result := c.runResult("completed", "passed")
					return protocol.RunInfo{RunID: string(harnessID("other-run")), TaskID: c.task, ThreadID: c.thread,
						Terminal: true, Result: &result, LastEventSeq: 4}, nil
				}
			},
		},
		{
			name: "event sequence gap",
			setup: func(c *fakeClient) {
				c.onEventsRead = func(in protocol.EventsReadInput) (protocol.EventsReadResult, error) {
					page := fakeEventsPage(c, in)
					if len(page.Events) > 0 {
						page.Events[0].EventSeq++
					}
					return page, nil
				}
			},
		},
		{
			name: "evidence unavailable",
			setup: func(c *fakeClient) {
				c.onEvidenceRead = func(protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error) {
					return protocol.EvidenceReadResult{}, errors.New("owner evidence unavailable")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				tc.setup(c)
				return c, nil
			}
			_, err, _ := delegate(t, d, "proof must fail closed")
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("unconfirmed owner proof must return unknown: %v", err)
			}
			if len(d.rec.completed) != 1 || d.rec.completed[0].action != domainaction.StatusFailed || d.rec.completed[0].attempt != domainaction.AttemptStatusFailed {
				t.Fatalf("unconfirmed owner proof must close one failed action and attempt: err=%v completed=%+v action=%+v attempt=%+v delegation=%+v client=%+v", err, d.rec.completed, d.rec.action, d.rec.attempt, d.rec.attempt.NativeDelegation, d.clients)
			}
			if d.rec.attempt.NativeDelegation == nil || len(d.rec.attempt.NativeDelegation.RunResult) == 0 || d.rec.attempt.NativeDelegation.Proof != nil ||
				d.rec.attempt.Status != domainaction.AttemptStatusFailed || d.rec.action.Status != domainaction.StatusFailed {
				t.Fatalf("unadopted result was not retained as a failed terminal pair: action=%+v attempt=%+v", d.rec.action, d.rec.attempt)
			}
			var retained protocol.RunResult
			if err := json.Unmarshal(d.rec.attempt.NativeDelegation.RunResult, &retained); err != nil || retained.Status != "completed" || retained.Verification.Status != "passed" {
				t.Fatalf("the actual completed+passed RunResult must remain immutable and visible: %+v err=%v", retained, err)
			}
		})
	}
}

func fakeEventsPage(c *fakeClient, in protocol.EventsReadInput) protocol.EventsReadResult {
	return fakeEventsPageFrom(c.runEvents(), in)
}

func fakeEventsPageFrom(events []protocol.Event, in protocol.EventsReadInput) protocol.EventsReadResult {
	page := protocol.EventsReadResult{Events: []protocol.Event{}}
	for _, event := range events {
		if event.EventSeq > in.AfterSeq && int64(len(page.Events)) < in.Limit {
			page.Events = append(page.Events, event)
		}
	}
	if len(page.Events) > 0 {
		page.NextAfterSeq = page.Events[len(page.Events)-1].EventSeq
	} else {
		page.NextAfterSeq = in.AfterSeq
	}
	page.HasMore = page.NextAfterSeq < int64(len(events))
	return page
}

func TestCriteriaProofRequiresTheInitialAcceptedPrefixAndUniqueStart(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*[]protocol.Event)
	}{
		{
			name: "initial acceptance has another Task",
			mutate: func(events *[]protocol.Event) {
				(*events)[0].TaskID = protocol.Str(harnessID("tsk"))
			},
		},
		{
			name: "duplicate initial acceptance",
			mutate: func(events *[]protocol.Event) {
				duplicate := (*events)[0]
				duplicate.EventID = harnessID("evt")
				*events = append(*events, protocol.Event{})
				copy((*events)[2:], (*events)[1:])
				(*events)[1] = duplicate
			},
		},
		{
			name: "missing run started",
			mutate: func(events *[]protocol.Event) {
				for index, event := range *events {
					if event.Type == protocol.EventRunStarted {
						*events = append((*events)[:index], (*events)[index+1:]...)
						return
					}
				}
			},
		},
		{
			name: "duplicate run started",
			mutate: func(events *[]protocol.Event) {
				for index, event := range *events {
					if event.Type == protocol.EventRunStarted {
						duplicate := event
						duplicate.EventID = harnessID("evt")
						*events = append(*events, protocol.Event{})
						copy((*events)[index+2:], (*events)[index+1:])
						(*events)[index+1] = duplicate
						return
					}
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				var events []protocol.Event
				loadEvents := func() []protocol.Event {
					if events == nil {
						events = c.runEvents()
						tc.mutate(&events)
						for index := range events {
							events[index].EventSeq = int64(index + 1)
						}
					}
					return events
				}
				c.onRunGet = func(protocol.RunGetInput) (protocol.RunInfo, error) {
					currentEvents := loadEvents()
					c.mu.Lock()
					result := c.lastResult
					c.mu.Unlock()
					return protocol.RunInfo{RunID: result.RunID, TaskID: result.TaskID, ThreadID: c.thread,
						Terminal: true, Result: &result, LastEventSeq: int64(len(currentEvents))}, nil
				}
				c.onEventsRead = func(in protocol.EventsReadInput) (protocol.EventsReadResult, error) {
					return fakeEventsPageFrom(loadEvents(), in), nil
				}
				return c, nil
			}
			_, err, _ := delegate(t, d, "malformed initial events must not prove success")
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("invalid initial event ordering must remain unknown: %v", err)
			}
			if d.rec.attempt.NativeDelegation == nil || d.rec.attempt.NativeDelegation.Proof != nil || len(d.rec.attempt.NativeDelegation.RunResult) == 0 {
				t.Fatalf("unproven result must remain recorded without an adoption proof: %+v", d.rec.attempt.NativeDelegation)
			}
		})
	}
}

func TestCriteriaPinMismatchBlocksBeforeActionOrHarnessOpen(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "wrong Task criteria")
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.tasks.task = &domaintask.Task{
		TaskID: identity.TaskID, OwnerID: "shiro", Assignee: "shiro", Route: domaintask.RouteOperations,
		Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("b", 64),
	}
	_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("Task/deployment criteria mismatch must block: %v", err)
	}
	if len(d.rec.created) != 0 || d.startCalls() != 0 {
		t.Fatalf("criteria mismatch must block before Action creation or Harness Open: actions=%d clients=%d", len(d.rec.created), d.startCalls())
	}
}

func TestCriteriaProofReadCancellationOrTimeoutFinalizesExactResultWithinTaskScope(t *testing.T) {
	for _, read := range []string{"run/get", "events/read", "evidence/read"} {
		for _, outcome := range []string{"caller cancellation", "owner-read deadline"} {
			t.Run(read+"/"+outcome, func(t *testing.T) {
				d := newDeployment(t)
				setDeploymentStepTimeout(t, d, 300*time.Millisecond)
				entered := make(chan struct{}, 1)
				var c *fakeClient
				var expectedRaw []byte
				d.startFn = func(int, client.Config) (*fakeClient, error) {
					c = newFakeClient()
					c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
						result := c.runResult("completed", "passed")
						var err error
						expectedRaw, err = protocol.Encode(result)
						return result, err
					}
					block := func(ctx context.Context) error {
						select {
						case entered <- struct{}{}:
						default:
						}
						<-ctx.Done()
						return ctx.Err()
					}
					switch read {
					case "run/get":
						c.onRunGetContext = func(ctx context.Context, _ protocol.RunGetInput) (protocol.RunInfo, error) {
							return protocol.RunInfo{}, block(ctx)
						}
					case "events/read":
						c.onEventsReadContext = func(ctx context.Context, _ protocol.EventsReadInput) (protocol.EventsReadResult, error) {
							return protocol.EventsReadResult{}, block(ctx)
						}
					case "evidence/read":
						c.onEvidenceReadContext = func(ctx context.Context, _ protocol.EvidenceReadInput) (protocol.EvidenceReadResult, error) {
							return protocol.EvidenceReadResult{}, block(ctx)
						}
					}
					return c, nil
				}

				ctx, input, messages := newTurn(t, "preserve terminal Harness result")
				ctx = context.WithValue(ctx, delegationTestScopeKey{}, "scope-preserved")
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				identity, err := domainexecution.IdentityFromContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("proof owner read did not begin")
				}
				if outcome == "caller cancellation" {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
						t.Fatalf("unconfirmed proof result error = %v, want unknown", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("bounded proof/finalization did not return")
				}

				if c == nil || len(expectedRaw) == 0 {
					t.Fatal("fixture did not receive the terminal raw RunResult")
				}
				if d.rec.attempt.NativeDelegation == nil || !bytes.Equal(d.rec.attempt.NativeDelegation.RunResult, expectedRaw) ||
					d.rec.attempt.NativeDelegation.Proof != nil || d.rec.attempt.Status != domainaction.AttemptStatusFailed || d.rec.action.Status != domainaction.StatusFailed {
					t.Fatalf("failed proof did not retain the exact raw result without adoption: action=%+v attempt=%+v", d.rec.action, d.rec.attempt)
				}
				d.tasks.mu.Lock()
				defer d.tasks.mu.Unlock()
				if d.tasks.lastTaskID != identity.TaskID || d.tasks.lastRunID != identity.RunID || d.tasks.lastActor != shiroActorID ||
					d.tasks.lastScopeValue != "scope-preserved" || !d.tasks.lastHadDeadline || d.tasks.lastContextErr != nil {
					t.Fatalf("finalization lost bounded Task scope/fence context: task=%s run=%s actor=%q marker=%v deadline=%v ctxerr=%v",
						d.tasks.lastTaskID, d.tasks.lastRunID, d.tasks.lastActor, d.tasks.lastScopeValue, d.tasks.lastHadDeadline, d.tasks.lastContextErr)
				}
				if d.tasks.lastToolScope.ActorID != shiroActorID || d.tasks.lastToolScope.AgentRole != "worker" || d.tasks.lastToolScope.Purpose != "ops" {
					t.Fatalf("finalization did not preserve the actual Shiro OPS execution scope: %+v", d.tasks.lastToolScope)
				}
			})
		}
	}
}

func TestCriteriaProofFinalizationStillRequiresCurrentTaskRunFence(t *testing.T) {
	d := newDeployment(t)
	setDeploymentStepTimeout(t, d, 300*time.Millisecond)
	entered := make(chan struct{}, 1)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onRunGetContext = func(ctx context.Context, _ protocol.RunGetInput) (protocol.RunInfo, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return protocol.RunInfo{}, ctx.Err()
		}
		return c, nil
	}
	ctx, input, messages := newTurn(t, "stale Task fence refuses finalization")
	ctx = context.WithValue(ctx, delegationTestScopeKey{}, "scope-preserved")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.tasks.fenceError = errors.New("Task writer generation is stale")
	d.tasks.fenceErrorAtCall = 4 // initial Action, Open, Start, then terminal finalization
	done := make(chan error, 1)
	go func() {
		_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		done <- err
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("proof owner read did not begin")
	}
	select {
	case err := <-done:
		if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
			t.Fatalf("stale Task fence error = %v, want unknown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("finalization did not return after stale Task fence refusal")
	}
	if len(d.rec.completed) != 0 || d.rec.attempt.NativeDelegation.RunResult != nil || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("finalization bypassed stale Task/Run fence: completions=%+v action=%s attempt=%s", d.rec.completed, d.rec.action.Status, d.rec.attempt.Status)
	}
	d.tasks.mu.Lock()
	defer d.tasks.mu.Unlock()
	if d.tasks.lastTaskID != identity.TaskID || d.tasks.lastRunID != identity.RunID || d.tasks.lastActor != shiroActorID ||
		d.tasks.lastScopeValue != "scope-preserved" || !d.tasks.lastHadDeadline {
		t.Fatalf("finalization fence used a different scope: task=%s run=%s actor=%q marker=%v deadline=%v",
			d.tasks.lastTaskID, d.tasks.lastRunID, d.tasks.lastActor, d.tasks.lastScopeValue, d.tasks.lastHadDeadline)
	}
}

func setDeploymentStepTimeout(t *testing.T, d *testDeployment, timeout time.Duration) {
	t.Helper()
	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("close default Runtime: %v", err)
	}
	d.settings.StepTimeout = timeout
	runtime, err := NewRuntime(d.settings, d.rec, d.tasks, Options{
		Start:   d.start,
		Now:     func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
		Log:     d.log.write,
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewRuntime with test step timeout: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Errorf("close bounded Runtime: %v", err)
		}
	})
}
