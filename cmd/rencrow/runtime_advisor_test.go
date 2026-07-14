package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type advisorRuntimeToolRunner struct{}

func (advisorRuntimeToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "codex.run"}}, nil
}

func (advisorRuntimeToolRunner) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	return tool.NewSuccess("recorded advice"), nil
}

func TestBuildAdvisorRuntimeWiresRecordingAndPolicy(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{Enabled: true},
		Advisor: config.AdvisorConfig{
			Storage: "jsonl",
			LogPath: filepath.Join(t.TempDir(), "advisor"),
		},
	}
	runtime, err := buildAdvisorRuntime(cfg, advisorRuntimeToolRunner{})
	if err != nil {
		t.Fatalf("buildAdvisorRuntime failed: %v", err)
	}
	if runtime.Service == nil || runtime.Store == nil || runtime.Policy == nil || len(runtime.Profiles) != 1 {
		t.Fatalf("runtime not fully wired: %#v", runtime)
	}
	decision, err := runtime.Policy.Decide("shiro", "ask_advisor")
	if err != nil || decision.Decision != "allowed" {
		t.Fatalf("unexpected policy decision=%#v err=%v", decision, err)
	}
	_, err = runtime.Service.RequestAdvice(context.Background(), domainadvisor.AdviceRequest{
		ID: "req-1", RequestedByAgent: "shiro", AdvisorID: domainadvisor.AdvisorCodex,
		Purpose: "review", Prompt: "inspect", ApprovalMode: "advice_only",
	})
	if err != nil {
		t.Fatalf("RequestAdvice failed: %v", err)
	}
	runs, err := runtime.Store.ListAdviceRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("advisor run was not persisted: runs=%#v err=%v", runs, err)
	}
}
