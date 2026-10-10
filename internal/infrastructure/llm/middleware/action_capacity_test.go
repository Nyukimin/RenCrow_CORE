package middleware

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
)

// A refused LLM provider admission must not leave a Run-less queued Task behind.
func TestActionProviderLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	actions := newLLMActionTestManager(t)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	inner := &actionTestProvider{}
	wrapped, err := WithActionExecutionOwners(inner, actions, saturated.Manager, transportmanager.New(transportStore, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := wrapped.Generate(context.Background(), llm.GenerateRequest{}); !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("Generate error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
