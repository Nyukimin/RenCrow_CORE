package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// A refused DCI identity acceptance admission must leave neither a queued nor
// a failed Task behind.
func TestAgentOpsDCIAdmissionLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	handler := &agentOpsHandler{taskOwner: saturated.Manager}

	_, _, _, err := handler.admitAgentOpsDCIExecution(context.Background())
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("admitAgentOpsDCIExecution error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
