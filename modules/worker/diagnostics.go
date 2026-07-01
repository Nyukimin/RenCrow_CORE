package worker

import (
	"context"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type DiagnosticsSnapshot struct {
	UpdatedAt      string            `json:"updated_at"`
	Health         core.HealthReport `json:"health"`
	SupportedTools []ToolDescriptor  `json:"supported_tools"`
}

type ToolDescriptor struct {
	Name            string   `json:"name"`
	RequiredArgs    []string `json:"required_args"`
	OptionalArgs    []string `json:"optional_args,omitempty"`
	ExecutionPolicy string   `json:"execution_policy"`
	Description     string   `json:"description"`
}

const UnavailableExecutorMessage = "worker executor unavailable"

type UnavailableExecutor struct{}

func (UnavailableExecutor) Health(context.Context) core.HealthReport {
	return core.HealthReport{
		Module: "worker",
		Status: core.HealthDown,
		Ready:  false,
		Detail: UnavailableExecutorMessage,
	}
}

func (UnavailableExecutor) Execute(context.Context, Action) (Result, error) {
	return Result{
		Status: StatusFailed,
		Error:  UnavailableExecutorMessage,
	}, nil
}

func CurrentToolDescriptors() []ToolDescriptor {
	return []ToolDescriptor{
		{
			Name:            ToolProposalPatch,
			RequiredArgs:    []string{"plan", "patch"},
			OptionalArgs:    []string{"risk", "cost_hint"},
			ExecutionPolicy: "WorkerExecutionService validates and executes proposal patches; diagnostics endpoint does not execute actions.",
			Description:     "Execute a Coder-generated proposal patch through the Worker module contract.",
		},
	}
}

func BuildDiagnosticsSnapshot(ctx context.Context, executor Executor, updatedAt time.Time) DiagnosticsSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	health := executor.Health(ctx)
	if health.CheckedAt.IsZero() {
		health.CheckedAt = updatedAt
	}
	return DiagnosticsSnapshot{
		UpdatedAt:      updatedAt.UTC().Format(time.RFC3339),
		Health:         health,
		SupportedTools: CurrentToolDescriptors(),
	}
}
