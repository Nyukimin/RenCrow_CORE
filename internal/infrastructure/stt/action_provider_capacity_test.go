package stt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
)

// A refused STT admission must not leave a Run-less queued Task behind.
func TestActionProviderLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	inner := &receiptAwareSTTProvider{MockProvider: MockProvider{Text: "unused"}}
	provider, err := NewActionProvider(inner, actions, saturated.Manager, transportmanager.New(transportStore, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}
	wav := make([]byte, 44)
	copy(wav[0:4], "RIFF")
	copy(wav[8:12], "WAVE")

	if _, err := provider.Transcribe(context.Background(), wav); !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("Transcribe error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
