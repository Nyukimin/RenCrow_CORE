package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestCurrentToolDescriptorsIncludesProposalPatch(t *testing.T) {
	tools := CurrentToolDescriptors()
	if len(tools) != 2 {
		t.Fatalf("unexpected worker tool count: got=%d tools=%+v", len(tools), tools)
	}
	tool := tools[0]
	if tool.Name != ToolProposalPatch {
		t.Fatalf("unexpected worker tool: %+v", tool)
	}
	if !containsString(tool.RequiredArgs, "plan") || !containsString(tool.RequiredArgs, "patch") {
		t.Fatalf("proposal patch tool must require plan and patch: %+v", tool)
	}
	if tool.ExecutionPolicy == "" || tool.Description == "" {
		t.Fatalf("proposal patch tool is missing diagnostics metadata: %+v", tool)
	}
	codexTool := tools[1]
	if codexTool.Name != "codex.run" {
		t.Fatalf("unexpected codex tool: %+v", codexTool)
	}
	if !containsString(codexTool.RequiredArgs, "prompt") || !containsString(codexTool.OptionalArgs, "sandbox") {
		t.Fatalf("codex.run tool args missing: %+v", codexTool)
	}
}

func TestBuildDiagnosticsSnapshot(t *testing.T) {
	snapshot := BuildDiagnosticsSnapshot(context.Background(), fakeWorkerExecutor{}, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Health.CheckedAt.IsZero() || len(snapshot.SupportedTools) != 2 {
		t.Fatalf("diagnostics metadata missing: %+v", snapshot)
	}
}

func TestUnavailableExecutorReportsDownAndFailsActions(t *testing.T) {
	executor := UnavailableExecutor{}
	health := executor.Health(context.Background())
	if health.Module != "worker" || health.Status != core.HealthDown || health.Ready || health.Detail != UnavailableExecutorMessage {
		t.Fatalf("unexpected unavailable health: %+v", health)
	}

	result, err := executor.Execute(context.Background(), Action{Tool: ToolProposalPatch})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed || result.Error != UnavailableExecutorMessage {
		t.Fatalf("unexpected unavailable result: %+v", result)
	}
}

type fakeWorkerExecutor struct{}

func (fakeWorkerExecutor) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "worker", Status: core.HealthReady, Ready: true}
}

func (fakeWorkerExecutor) Execute(context.Context, Action) (Result, error) {
	return Result{}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
