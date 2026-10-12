package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
// Each test runs the shared scenarios of taskmanagertest.VerifyStartedRunClosure
// against one function of this package that starts a Run itself.

func TestPlaybackActionRecorderClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		WorkFailure: taskmanagertest.WorkFailureIsRecorded,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			recorder, err := newPlaybackActionRecorder(c.Actions, c.Tasks, "mio")
			if err != nil {
				t.Fatal(err)
			}
			ack := ttsPlaybackAckRequest{
				PublicPlaybackRef: "response-1",
				SessionID:         "session-1",
				UtteranceID:       "utterance-1",
				Status:            "ended",
			}
			if workErr != nil {
				ack.Status, ack.Error = "failed", workErr.Error()
				if errors.Is(workErr, context.Canceled) {
					ack.Status = "cancelled"
				}
			}
			return func(ctx context.Context) error { return recorder.Record(ctx, ack) }
		},
	})
}

func TestActionTTSBridgeClosesTheRunItStarted(t *testing.T) {
	taskmanagertest.VerifyStartedRunClosure(t, taskmanagertest.StartedRunPath{
		BindsReceiptOwner: true,
		WorkFailure:       taskmanagertest.WorkFailureReturnsInjected,
		Start: func(t *testing.T, c *taskmanagertest.Closure, workErr error) func(context.Context) error {
			bridge, err := newActionTTSBridge(&actionTTSBridgeStub{pushErr: workErr}, c.Actions, c.Tasks, c.NewTransport(t), "mio")
			if err != nil {
				t.Fatal(err)
			}
			return func(ctx context.Context) error {
				if err := bridge.StartSession(ctx, orchestrator.TTSSessionStart{SessionID: "tts-session"}); err != nil {
					return err
				}
				if err := bridge.PushText(ctx, "tts-session", "text", nil); err != nil {
					return err
				}
				return bridge.EndSession(ctx, "tts-session")
			}
		},
	})
}
