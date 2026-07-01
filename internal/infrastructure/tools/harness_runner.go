package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/toolharness"
)

const (
	ToolHarnessModeValidateThenRepair = "validate_then_repair"
	ToolHarnessModeLogOnly            = "log_only"
	ToolHarnessModeStrict             = "strict"
)

type ToolHarnessRunnerConfig struct {
	Mode     string
	Recorder toolharness.Recorder
}

// ToolHarnessRunner は RunnerV2 の前段で tool call 入力を契約調停する。
// PolicyRunner より外側に置くことで、修復後の入力を安全ポリシーへ渡せる。
type ToolHarnessRunner struct {
	inner    tool.RunnerV2
	harness  *toolharness.Harness
	recorder toolharness.Recorder
	mode     string
}

func NewToolHarnessRunner(inner tool.RunnerV2, recorder toolharness.Recorder) *ToolHarnessRunner {
	return NewToolHarnessRunnerWithConfig(inner, ToolHarnessRunnerConfig{Recorder: recorder})
}

func NewToolHarnessRunnerWithConfig(inner tool.RunnerV2, cfg ToolHarnessRunnerConfig) *ToolHarnessRunner {
	return &ToolHarnessRunner{
		inner:    inner,
		harness:  toolharness.New(),
		recorder: cfg.Recorder,
		mode:     normalizeToolHarnessMode(cfg.Mode),
	}
}

func (r *ToolHarnessRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if r.inner == nil {
		return nil, fmt.Errorf("inner runner is required")
	}
	mediated := r.mediate(toolName, args)
	if r.mode == ToolHarnessModeStrict && mediated.Repaired() {
		return tool.NewError(tool.ErrValidationFailed, "tool input requires repair under strict tool harness mode", mediated.Metadata()), nil
	}
	if r.mode == ToolHarnessModeLogOnly {
		return r.inner.ExecuteV2(ctx, toolName, args)
	}
	resp, err := r.inner.ExecuteV2(ctx, toolName, mediated.Input)
	if err != nil || resp == nil || !mediated.Repaired() {
		return resp, err
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]any{}
	}
	for k, v := range mediated.Metadata() {
		resp.Metadata[k] = v
	}
	return resp, nil
}

func (r *ToolHarnessRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	if r.inner == nil {
		return nil, fmt.Errorf("inner runner is required")
	}
	return r.inner.ListTools(ctx)
}

func (r *ToolHarnessRunner) mediate(toolName string, args map[string]any) toolharness.Result {
	if r.harness == nil {
		return toolharness.Result{Input: args}
	}
	result := r.harness.Mediate(toolName, args)
	r.record(toolName, args, result)
	return result
}

func (r *ToolHarnessRunner) record(toolName string, args map[string]any, result toolharness.Result) {
	if r.recorder == nil {
		return
	}
	now := time.Now().UTC()
	event := toolharness.NewEvent(
		fmt.Sprintf("evt_tool_%d", now.UnixNano()),
		toolName,
		args,
		result,
		now,
	)
	_ = r.recorder.RecordToolMediationEvent(event)
}

func normalizeToolHarnessMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ToolHarnessModeLogOnly:
		return ToolHarnessModeLogOnly
	case ToolHarnessModeStrict:
		return ToolHarnessModeStrict
	default:
		return ToolHarnessModeValidateThenRepair
	}
}
