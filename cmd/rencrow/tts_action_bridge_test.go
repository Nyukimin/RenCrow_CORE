package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type actionTTSBridgeStub struct {
	pushErr      error
	receiptBound bool
}

type actionSpeechPlanBridgeStub struct {
	actionTTSBridgeStub
	pushCalls         int
	transport         *transportmanager.Manager
	transportRequest  domaintransport.Request
	transportResponse domaintransport.Response
	transportErr      error
	gotPlan           speech.SpeechPlan
	gotTTSSessionID   string
	gotSessionID      string
	gotTurnID         string
	gotSpeakerID      string
	receiptBoundPlan  bool
}

func (s *actionSpeechPlanBridgeStub) PushSpeechPlan(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	s.pushCalls++
	_, s.receiptBoundPlan = domaintransport.ReceiptOwnerFromContext(ctx)
	s.gotTTSSessionID = ttsSessionID
	s.gotSessionID = conversationSessionID
	s.gotTurnID = turnID
	s.gotSpeakerID = speakerID
	s.gotPlan = plan
	if s.transport != nil {
		request, err := s.transport.BeginRequest(ctx, "tts.speech_plan")
		if err != nil {
			s.transportErr = err
			return err
		}
		s.transportRequest = request
		response, err := s.transport.CompleteResponse(ctx, request.RequestID, "tts-response")
		if err != nil {
			s.transportErr = err
			return err
		}
		s.transportResponse = response
	}
	return s.pushErr
}

func (s *actionTTSBridgeStub) StartSession(context.Context, orchestrator.TTSSessionStart) error {
	return nil
}

func (s *actionTTSBridgeStub) PushText(ctx context.Context, _ string, _ string, _ *moduletts.EmotionState) error {
	_, s.receiptBound = domaintransport.ReceiptOwnerFromContext(ctx)
	return s.pushErr
}

func (s *actionTTSBridgeStub) EndSession(context.Context, string) error {
	return nil
}

func TestActionTTSBridgeCompletesSessionTaskRunActionAttempt(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	inner := &actionTTSBridgeStub{}
	bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-session-1"}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "tts-session-1", "こんにちは", nil); err != nil {
		t.Fatal(err)
	}
	if !inner.receiptBound {
		t.Fatal("TTS transport receipt owner was not bound to synthesis context")
	}
	if err := bridge.EndSession(context.Background(), "tts-session-1"); err != nil {
		t.Fatal(err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded, domaintask.StatusSucceeded)
}

func TestActionTTSBridgeForwardsSpeechPlanWithTransportReceiptOwner(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	inner := &actionSpeechPlanBridgeStub{}
	bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-speech-plan"}); err != nil {
		t.Fatal(err)
	}
	pusher, ok := bridge.(interface {
		PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
	})
	if !ok {
		t.Fatal("actionTTSBridge does not forward optional PushSpeechPlan")
	}
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "契約確認。"}}}}}
	if err := pusher.PushSpeechPlan(context.Background(), "tts-speech-plan", "ses_0123456789abcdef0123456789abcdef", "turn_0123456789abcdef0123456789abcdef", "mio", plan); err != nil {
		t.Fatalf("PushSpeechPlan() error = %v", err)
	}
	if !inner.receiptBoundPlan {
		t.Fatal("TTS transport receipt owner was not bound to SpeechPlan synthesis context")
	}
	if inner.gotTTSSessionID != "tts-speech-plan" || inner.gotSessionID != "ses_0123456789abcdef0123456789abcdef" || inner.gotTurnID != "turn_0123456789abcdef0123456789abcdef" || inner.gotSpeakerID != "mio" {
		t.Fatalf("SpeechPlan identity changed by decorator: tts=%q session=%q turn=%q speaker=%q", inner.gotTTSSessionID, inner.gotSessionID, inner.gotTurnID, inner.gotSpeakerID)
	}
	if len(inner.gotPlan.Utterances) != 1 || inner.gotPlan.Utterances[0].Segments[0].Text != "契約確認。" {
		t.Fatalf("SpeechPlan changed by decorator: %#v", inner.gotPlan)
	}
	if err := bridge.EndSession(context.Background(), "tts-speech-plan"); err != nil {
		t.Fatal(err)
	}
}

func TestActionTTSBridgeSpeechPlanTransportUsesMatchingExecutionIdentity(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	transport := newTTSTransportTestOwner(t, actions)
	inner := &actionSpeechPlanBridgeStub{transport: transport}
	bridge, err := newActionTTSBridge(inner, actions, tasks, transport, "mio")
	if err != nil {
		t.Fatal(err)
	}
	task, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		Title:    "canonical chat turn",
		Route:    domaintask.RouteCHAT,
		OwnerID:  "mio",
		Assignee: "mio",
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "synthesize the response"})
	if err != nil {
		t.Fatal(err)
	}
	traceID := modulecore.NewTraceID()
	processCtx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, traceID)
	if err != nil {
		t.Fatal(err)
	}
	const ttsSessionID = "tts-canonical-speech-plan"
	if err := bridge.StartSession(processCtx, orchestrator.TTSSessionStart{SessionID: ttsSessionID}); err != nil {
		t.Fatalf("StartSession(): %v", err)
	}
	pusher := bridge.(interface {
		PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
	})
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "canonical transport lineage"}}}}}
	conversationSessionID := string(modulecore.NewSessionID())
	turnID := string(modulecore.NewTurnID())
	if err := pusher.PushSpeechPlan(processCtx, ttsSessionID, conversationSessionID, turnID, "mio", plan); err != nil {
		t.Fatalf("PushSpeechPlan(): %v", err)
	}
	if inner.transportErr != nil {
		t.Fatalf("TTS transport request/response: %v", inner.transportErr)
	}
	request := inner.transportRequest
	response := inner.transportResponse
	if request.TaskID != task.TaskID || request.RunID != run.RunID || request.TraceID != traceID {
		t.Fatalf("transport request identity = task=%s run=%s trace=%s, want task=%s run=%s trace=%s", request.TaskID, request.RunID, request.TraceID, task.TaskID, run.RunID, traceID)
	}
	if request.Operation != "tts.speech_plan" || request.ActionID == "" || request.AttemptID == "" {
		t.Fatalf("transport request lineage = %#v", request)
	}
	if response.RequestID != request.RequestID || response.TaskID != task.TaskID || response.RunID != run.RunID || response.TraceID != traceID || response.ActionID != request.ActionID || response.AttemptID != request.AttemptID {
		t.Fatalf("transport response lineage = %#v, request=%#v", response, request)
	}
	if err := bridge.EndSession(processCtx, ttsSessionID); err != nil {
		t.Fatalf("EndSession(): %v", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded, domaintask.StatusRunning)
	if err := tasks.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "mio", taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: "chat completed"}, nil); err != nil {
		t.Fatalf("close canonical chat run: %v", err)
	}
}

func TestActionTTSBridgeRejectsMismatchedSpeechPlanIdentityWithoutClosingSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(modulecore.TaskID, modulecore.RunID, modulecore.TraceID) (modulecore.TaskID, modulecore.RunID, modulecore.TraceID)
	}{
		{
			name: "task",
			edit: func(taskID modulecore.TaskID, runID modulecore.RunID, traceID modulecore.TraceID) (modulecore.TaskID, modulecore.RunID, modulecore.TraceID) {
				return modulecore.NewTaskID(), runID, traceID
			},
		},
		{
			name: "run",
			edit: func(taskID modulecore.TaskID, _ modulecore.RunID, traceID modulecore.TraceID) (modulecore.TaskID, modulecore.RunID, modulecore.TraceID) {
				return taskID, modulecore.NewRunID(), traceID
			},
		},
		{
			name: "trace",
			edit: func(taskID modulecore.TaskID, runID modulecore.RunID, _ modulecore.TraceID) (modulecore.TaskID, modulecore.RunID, modulecore.TraceID) {
				return taskID, runID, modulecore.NewTraceID()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks, actions := newTTSActionTestOwners(t)
			inner := &actionSpeechPlanBridgeStub{}
			bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
			if err != nil {
				t.Fatal(err)
			}
			task, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{
				Title:    "canonical chat turn",
				Route:    domaintask.RouteCHAT,
				OwnerID:  "mio",
				Assignee: "mio",
				ReadOnly: true,
			}, domaintask.SharedRoleContext{CurrentPlan: "synthesize the response"})
			if err != nil {
				t.Fatal(err)
			}
			traceID := modulecore.NewTraceID()
			processCtx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, traceID)
			if err != nil {
				t.Fatal(err)
			}
			const ttsSessionID = "tts-reject-mismatch"
			if err := bridge.StartSession(processCtx, orchestrator.TTSSessionStart{SessionID: ttsSessionID}); err != nil {
				t.Fatalf("StartSession(): %v", err)
			}
			pusher := bridge.(interface {
				PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
			})
			wrongTaskID, wrongRunID, wrongTraceID := tc.edit(task.TaskID, run.RunID, traceID)
			foreignCtx, err := domainexecution.WithIdentity(context.Background(), wrongTaskID, wrongRunID, wrongTraceID)
			if err != nil {
				t.Fatal(err)
			}
			plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "foreign lineage rejected"}}}}}
			conversationSessionID := string(modulecore.NewSessionID())
			turnID := string(modulecore.NewTurnID())
			if err := pusher.PushSpeechPlan(foreignCtx, ttsSessionID, conversationSessionID, turnID, "mio", plan); err == nil {
				t.Fatal("PushSpeechPlan() error = nil, want mismatched execution identity rejection")
			}
			if inner.pushCalls != 0 {
				t.Fatalf("inner synthesis calls after rejection = %d, want 0", inner.pushCalls)
			}
			if err := pusher.PushSpeechPlan(processCtx, ttsSessionID, conversationSessionID, turnID, "mio", plan); err != nil {
				t.Fatalf("valid PushSpeechPlan() after rejection: %v", err)
			}
			if inner.pushCalls != 1 {
				t.Fatalf("inner synthesis calls after valid retry = %d, want 1", inner.pushCalls)
			}
			if err := bridge.EndSession(processCtx, ttsSessionID); err != nil {
				t.Fatalf("EndSession() after valid retry: %v", err)
			}
			assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded, domaintask.StatusRunning)
			if err := tasks.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "mio", taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: "chat completed"}, nil); err != nil {
				t.Fatalf("close canonical chat run: %v", err)
			}
		})
	}
}

func TestActionTTSBridgeClosesSpeechPlanFailureAtSessionEnd(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	sentinel := errors.New("SpeechPlan synthesis canceled")
	inner := &actionSpeechPlanBridgeStub{actionTTSBridgeStub: actionTTSBridgeStub{pushErr: sentinel}}
	bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-speech-plan-failure"}); err != nil {
		t.Fatal(err)
	}
	pusher := bridge.(interface {
		PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
	})
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "途中で止める。"}}}}}
	if err := pusher.PushSpeechPlan(context.Background(), "tts-speech-plan-failure", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan); !errors.Is(err, sentinel) {
		t.Fatalf("PushSpeechPlan error = %v, want %v", err, sentinel)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusFailed, domainaction.AttemptStatusFailed, domaintask.StatusFailed)
	if err := bridge.EndSession(context.Background(), "tts-speech-plan-failure"); err != nil {
		t.Fatalf("EndSession repeated SpeechPlan close: %v", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusFailed, domainaction.AttemptStatusFailed, domaintask.StatusFailed)
}

func TestActionTTSBridgeCancelsSpeechPlanAttemptBeforeProcessReturns(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	inner := &actionSpeechPlanBridgeStub{actionTTSBridgeStub: actionTTSBridgeStub{pushErr: context.Canceled}}
	bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-speech-plan-cancel"}); err != nil {
		t.Fatal(err)
	}
	pusher := bridge.(interface {
		PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
	})
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "取消する。"}}}}}
	if err := pusher.PushSpeechPlan(context.Background(), "tts-speech-plan-cancel", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("PushSpeechPlan cancellation = %v, want context.Canceled", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusCancelled, domainaction.AttemptStatusCancelled, domaintask.StatusCancelled)
	if err := bridge.EndSession(context.Background(), "tts-speech-plan-cancel"); err != nil {
		t.Fatalf("EndSession repeated cancellation close: %v", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusCancelled, domainaction.AttemptStatusCancelled, domaintask.StatusCancelled)
}

func TestActionTTSBridgePreCanceledSpeechPlanClosesAttemptWithoutStartingSynthesis(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	inner := &actionSpeechPlanBridgeStub{}
	bridge, err := newActionTTSBridge(inner, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	task, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		Title:    "Chat turn",
		Route:    domaintask.RouteCHAT,
		OwnerID:  "mio",
		Assignee: "mio",
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "respond while speech is canceled"})
	if err != nil {
		t.Fatal(err)
	}
	processCtx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(processCtx, orchestrator.TTSSessionStart{SessionID: "tts-speech-plan-pre-cancel"}); err != nil {
		t.Fatal(err)
	}
	pusher := bridge.(interface {
		PushSpeechPlan(context.Context, string, string, string, string, speech.SpeechPlan) error
	})
	speechCtx, cancelSpeech := context.WithCancel(processCtx)
	cancelSpeech()
	plan := speech.SpeechPlan{SchemaVersion: speech.SchemaVersion, Utterances: []speech.Utterance{{Segments: []speech.Segment{{Text: "合成を開始しない。"}}}}}
	err = pusher.PushSpeechPlan(speechCtx, "tts-speech-plan-pre-cancel", string(modulecore.NewSessionID()), string(modulecore.NewTurnID()), "mio", plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled PushSpeechPlan error = %v, want context.Canceled", err)
	}
	if inner.pushCalls != 0 {
		t.Fatalf("pre-canceled synthesis calls = %d, want 0", inner.pushCalls)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusCancelled, domainaction.AttemptStatusCancelled, domaintask.StatusRunning)
	if err := bridge.EndSession(processCtx, "tts-speech-plan-pre-cancel"); err != nil {
		t.Fatalf("EndSession after pre-canceled SpeechPlan: %v", err)
	}
	if processCtx.Err() != nil {
		t.Fatalf("speech cancellation canceled the ProcessMessage context: %v", processCtx.Err())
	}
	if err := tasks.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "mio", taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: "Chat turn completed"}, nil); err != nil {
		t.Fatalf("close still-live Chat Run: %v", err)
	}
}

func TestActionTTSBridgeClosesFailureDuringPush(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	sentinel := errors.New("TTS gateway unavailable")
	bridge, err := newActionTTSBridge(&actionTTSBridgeStub{pushErr: sentinel}, actions, tasks, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-session-2"}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "tts-session-2", "失敗", nil); !errors.Is(err, sentinel) {
		t.Fatalf("PushText error=%v", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusFailed, domainaction.AttemptStatusFailed, domaintask.StatusFailed)
}

func newTTSTransportTestOwner(t *testing.T, actions *actionmanager.Manager) *transportmanager.Manager {
	t.Helper()
	store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	return transportmanager.New(store, actions)
}

func newTTSActionTestOwners(t *testing.T) (*taskmanager.Manager, *actionmanager.Manager) {
	t.Helper()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	return taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), actionmanager.New(actionStore)
}

func assertTTSActionLifecycle(t *testing.T, tasks *taskmanager.Manager, actions *actionmanager.Manager, actionStatus domainaction.Status, attemptStatus domainaction.AttemptStatus, taskStatus domaintask.Status) {
	t.Helper()
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(storedActions) != 1 {
		t.Fatalf("actions=%#v err=%v", storedActions, err)
	}
	action := storedActions[0]
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(context.Background(), action.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.GetRun(context.Background(), action.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != domainaction.KindTTS || action.Status != actionStatus || len(attempts) != 1 || attempts[0].Status != attemptStatus {
		t.Fatalf("TTS action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != taskStatus || run.Status != domaintask.RunStatus(taskStatus) {
		t.Fatalf("TTS Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}
