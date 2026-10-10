package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

func newCapacityTestActionOwner(t *testing.T) *actionmanager.Manager {
	t.Helper()
	store, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	return actionmanager.New(store)
}

// A refused Memory Promotion admission must not leave a Run-less queued Task behind.
func TestActionMemoryPromotionRunnerLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	inner := &memoryPromotionActionRunnerStub{}
	runner, err := newActionMemoryPromotionRunner(inner, newCapacityTestActionOwner(t), saturated.Manager, "midori")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runner.RunOne(context.Background()); !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("RunOne error=%v, want ErrParallelLimit", err)
	}
	if inner.identityBound || inner.actionBound {
		t.Fatal("the promotion ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}

// A refused TTS session admission must not leave a Run-less queued Task behind.
func TestActionTTSBridgeLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actions := newCapacityTestActionOwner(t)
	bridge, err := newActionTTSBridge(&actionTTSBridgeStub{}, actions, saturated.Manager, newTTSTransportTestOwner(t, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}

	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-session-capacity"}); !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("StartSession error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}

// A refused playback receipt admission must not leave a Run-less queued Task behind.
func TestPlaybackActionRecorderLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	recorder, err := newPlaybackActionRecorder(newCapacityTestActionOwner(t), saturated.Manager, "mio")
	if err != nil {
		t.Fatal(err)
	}

	err = recorder.Record(context.Background(), ttsPlaybackAckRequest{
		PublicPlaybackRef: "response-capacity", SessionID: "session-capacity", UtteranceID: "utterance-capacity", Status: "ended",
	})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("Record error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
