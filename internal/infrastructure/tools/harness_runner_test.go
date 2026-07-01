package tools

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/toolharness"
)

type captureRunnerV2 struct {
	args map[string]any
}

func (r *captureRunnerV2) ExecuteV2(_ context.Context, _ string, args map[string]any) (*tool.ToolResponse, error) {
	r.args = args
	return tool.NewSuccess("ok"), nil
}

func (r *captureRunnerV2) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "file_read"}}, nil
}

func TestToolHarnessRunner_MediatesBeforeInner(t *testing.T) {
	inner := &captureRunnerV2{}
	runner := NewToolHarnessRunner(inner, nil)

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"args": map[string]any{
			"path":  "testdata/a.txt",
			"limit": float64(10),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %+v", resp.Error)
	}
	if inner.args["path"] != "testdata/a.txt" {
		t.Fatalf("inner received unmediated args: %#v", inner.args)
	}
	if inner.args["offset"] != 0 {
		t.Fatalf("expected offset default before inner execution, got %#v", inner.args)
	}
	if resp.Metadata["tool_harness_status"] != "repaired" {
		t.Fatalf("expected repaired metadata, got %#v", resp.Metadata)
	}
}

func TestToolHarnessRunner_RecordsMediationEvent(t *testing.T) {
	recorder := &mockToolHarnessRecorder{}
	runner := NewToolHarnessRunner(&captureRunnerV2{}, recorder)

	_, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"path":  "testdata/a.txt",
		"limit": float64(10),
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("expected one event, got %d", len(recorder.events))
	}
	if recorder.events[0].ValidationStatus != toolharness.ValidationStatusRepaired {
		t.Fatalf("expected repaired event, got %#v", recorder.events[0])
	}
}

func TestToolHarnessRunner_StrictModeRejectsRepairableInput(t *testing.T) {
	inner := &captureRunnerV2{}
	runner := NewToolHarnessRunnerWithConfig(inner, ToolHarnessRunnerConfig{
		Mode: ToolHarnessModeStrict,
	})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"args": map[string]any{"path": "testdata/a.txt"},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("expected validation error, got %+v", resp)
	}
	if inner.args != nil {
		t.Fatalf("strict mode should not execute inner runner, got %#v", inner.args)
	}
}

func TestToolHarnessRunner_LogOnlyModeDoesNotRepairExecutionInput(t *testing.T) {
	inner := &captureRunnerV2{}
	runner := NewToolHarnessRunnerWithConfig(inner, ToolHarnessRunnerConfig{
		Mode: ToolHarnessModeLogOnly,
	})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{
		"args": map[string]any{"path": "testdata/a.txt"},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected tool error: %+v", resp.Error)
	}
	if _, ok := inner.args["args"]; !ok {
		t.Fatalf("log_only mode should pass raw input to inner runner, got %#v", inner.args)
	}
	if _, ok := resp.Metadata["tool_harness_status"]; ok {
		t.Fatalf("log_only mode should not attach repaired metadata to executed raw input, got %#v", resp.Metadata)
	}
}
