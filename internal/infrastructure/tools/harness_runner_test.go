package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
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

type failedMediationRecorder struct{}

func (failedMediationRecorder) RecordToolMediationEvent(context.Context, toolharness.Event) error {
	return fmt.Errorf("private persistence detail")
}

func TestMediationFailurePreventsToolEffects(t *testing.T) {
	for _, mode := range []string{ToolHarnessModeStrict, ToolHarnessModeLogOnly, ToolHarnessModeValidateThenRepair} {
		t.Run(mode, func(t *testing.T) {
			inner := &captureRunnerV2{}
			runner := NewToolHarnessRunnerWithConfig(inner, ToolHarnessRunnerConfig{Mode: mode, Recorder: failedMediationRecorder{}})
			response, err := runner.ExecuteV2(context.Background(), "unknown_fixture", map[string]any{"x": "input"})
			if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrInternalError {
				t.Fatalf("persistence failure hidden: %#v %v", response, err)
			}
			if inner.args != nil {
				t.Fatal("tool executed without receipt")
			}
			if strings.Contains(response.Error.Message, "private") {
				t.Fatal("persistence details leaked")
			}
		})
	}
	runner := NewToolRunner(ToolRunnerConfig{ToolHarnessRecorder: failedMediationRecorder{}})
	called := false
	runner.tools["fixture"] = func(context.Context, map[string]any) (string, error) { called = true; return "effect", nil }
	runner.toolsV2["fixture"] = func(context.Context, map[string]any) (*tool.ToolResponse, error) {
		called = true
		return tool.NewSuccess("effect"), nil
	}
	if _, err := runner.Execute(context.Background(), "fixture", nil); err == nil {
		t.Fatal("V1 discarded recorder failure")
	}
	response, err := runner.ExecuteV2(context.Background(), "fixture", nil)
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("V2 discarded recorder failure: %#v %v", response, err)
	}
	if called {
		t.Fatal("base runner executed without receipt")
	}
}

type contextMediationRecorder struct{ got context.Context }

func (r *contextMediationRecorder) RecordToolMediationEvent(ctx context.Context, _ toolharness.Event) error {
	r.got = ctx
	return nil
}
func TestMediationRecorderReceivesExactContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &contextMediationRecorder{}
	runner := NewToolHarnessRunner(&captureRunnerV2{}, recorder)
	if _, err := runner.ExecuteV2(ctx, "file_read", map[string]any{"path": "file"}); err != nil {
		t.Fatal(err)
	}
	if recorder.got != ctx {
		t.Fatal("execution context replaced before recorder")
	}
	base := NewToolRunner(ToolRunnerConfig{ToolHarnessRecorder: recorder})
	base.tools["fixture"] = func(context.Context, map[string]any) (string, error) { return "ok", nil }
	recorder.got = nil
	if _, err := base.Execute(ctx, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if recorder.got != ctx {
		t.Fatal("V1 execution context replaced")
	}
}
