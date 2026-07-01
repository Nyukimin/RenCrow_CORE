package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	domainexecution "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/execution"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	execrepo "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/execution"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

type fakeRunner struct {
	metas []tool.ToolMetadata
}

func (f *fakeRunner) ExecuteV2(_ context.Context, _ string, _ map[string]any) (*tool.ToolResponse, error) {
	return tool.NewSuccess("ok"), nil
}

func (f *fakeRunner) ListTools(_ context.Context) ([]tool.ToolMetadata, error) {
	return f.metas, nil
}

func TestPolicyRunner_DenyBlockedCommand(t *testing.T) {
	repo, err := execrepo.NewJSONLRepository(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("repo init failed: %v", err)
	}

	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{DenyCommands: []string{"rm -rf"}})
	runner, err := NewPolicyRunner(inner, engine, repo, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{"command": "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %+v", resp)
	}

	counts, err := repo.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountByStatus failed: %v", err)
	}
	if counts[domainexecution.StatusDenied] == 0 {
		t.Fatalf("expected denied count > 0, got %v", counts)
	}
}

func TestPolicyRunner_DeniesMediatedFileWriteOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	inner := tools.NewToolRunner(tools.ToolRunnerConfig{DisableToolHarness: true})
	engine := NewPolicyEngine(PolicyConfig{
		Workspace:         workspace,
		WorkspaceEnforced: true,
	})
	policyRunner, err := NewPolicyRunner(inner, engine, nil, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}
	runner := tools.NewToolHarnessRunner(policyRunner, nil)

	resp, err := runner.ExecuteV2(context.Background(), "file_write", map[string]any{
		"args": map[string]any{
			"path":    outside,
			"content": "blocked",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %+v", resp)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file should not be created, stat err=%v", err)
	}
}

func TestPolicyRunner_RefreshesToolMetadataAfterDynamicRegistration(t *testing.T) {
	inner := &fakeRunner{metas: []tool.ToolMetadata{{ToolID: "shell"}}}
	engine := NewPolicyEngine(PolicyConfig{})
	runner, err := NewPolicyRunner(inner, engine, nil, "test")
	if err != nil {
		t.Fatalf("NewPolicyRunner failed: %v", err)
	}

	inner.metas = append(inner.metas, tool.ToolMetadata{ToolID: "subagent"})
	resp, err := runner.ExecuteV2(context.Background(), "subagent", map[string]any{})
	if err != nil {
		t.Fatalf("ExecuteV2 should refresh dynamic metadata: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %+v", resp.Error)
	}
}
