package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type ContextBudgetRunnerConfig struct {
	Agent      string
	Model      string
	Policy     domainai.ContextBudgetPolicy
	Recorder   ContextBudgetUsageRecorder
	OffloadDir string
}

type ContextBudgetUsageRecorder interface {
	SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error
	SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error
}

// ContextBudgetRunner enforces the AI Native context budget on tool results
// before they are appended back into the next LLM tool-loop context.
type ContextBudgetRunner struct {
	inner      tool.RunnerV2
	agent      string
	model      string
	policy     domainai.ContextBudgetPolicy
	rec        ContextBudgetUsageRecorder
	offloadDir string
}

func NewContextBudgetRunner(inner tool.RunnerV2, cfg ContextBudgetRunnerConfig) *ContextBudgetRunner {
	agent := cfg.Agent
	if agent == "" {
		agent = "ToolRunner"
	}
	return &ContextBudgetRunner{
		inner:      inner,
		agent:      agent,
		model:      cfg.Model,
		policy:     cfg.Policy,
		rec:        cfg.Recorder,
		offloadDir: cfg.OffloadDir,
	}
}

func (r *ContextBudgetRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if r.inner == nil {
		return nil, fmt.Errorf("inner runner is required")
	}
	resp, err := r.inner.ExecuteV2(ctx, toolName, args)
	if err != nil || resp == nil || resp.IsError() {
		return resp, err
	}
	usage := domainai.ContextUsage{
		EventID:       fmt.Sprintf("ctx_tool_%d", time.Now().UTC().UnixNano()),
		Agent:         r.agent,
		Model:         r.model,
		ContextTokens: estimateToolResultTokens(resp),
		ToolCallCount: 1,
		CreatedAt:     time.Now().UTC(),
	}
	decision, evalErr := domainai.EvaluateContextBudget(usage, r.policy)
	if evalErr != nil {
		return nil, fmt.Errorf("tool context budget evaluation failed: %w", evalErr)
	}
	if err := r.recordContextBudget(ctx, usage, decision, toolName); err != nil {
		return nil, err
	}
	if decision.MaxContextTokens <= 0 {
		return resp, nil
	}
	if decision.Status == domainai.ContextBudgetStatusStop {
		metadata := contextBudgetMetadata(decision)
		if r.offloadDir != "" {
			offload, offloadErr := r.offloadToolResult(toolName, usage.EventID, resp)
			if offloadErr != nil {
				return nil, offloadErr
			}
			for k, v := range offload {
				metadata[k] = v
			}
		}
		return tool.NewError(tool.ErrValidationFailed, "tool result exceeds context budget", metadata), nil
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]any{}
	}
	for k, v := range contextBudgetMetadata(decision) {
		resp.Metadata[k] = v
	}
	if decision.Status == domainai.ContextBudgetStatusWarn {
		log.Printf("Tool context budget warning agent=%s tool=%s tokens=%d max=%d reason=%s",
			r.agent, toolName, decision.ContextTokens, decision.MaxContextTokens, decision.Reason)
	}
	return resp, nil
}

func (r *ContextBudgetRunner) offloadToolResult(toolName string, eventID string, resp *tool.ToolResponse) (map[string]any, error) {
	if err := os.MkdirAll(r.offloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("tool result offload mkdir failed: %w", err)
	}
	filename := fmt.Sprintf("%s_%s_%d.json", safeToolResultName(toolName), eventID, time.Now().UTC().UnixNano())
	path := filepath.Join(r.offloadDir, filename)
	data, err := resp.JSON()
	if err != nil {
		return nil, fmt.Errorf("tool result offload marshal failed: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("tool result offload write failed: %w", err)
	}
	return map[string]any{
		"context_budget_offloaded":     true,
		"context_budget_offload_path":  path,
		"context_budget_offload_bytes": len(data),
	}, nil
}

func (r *ContextBudgetRunner) recordContextBudget(ctx context.Context, usage domainai.ContextUsage, decision domainai.ContextBudgetDecision, toolName string) error {
	if r.rec == nil {
		return nil
	}
	if err := r.rec.SaveContextUsage(ctx, usage); err != nil {
		return fmt.Errorf("tool context usage save failed: %w", err)
	}
	eventType := ""
	switch decision.Status {
	case domainai.ContextBudgetStatusWarn:
		eventType = "context_budget_warning"
	case domainai.ContextBudgetStatusStop:
		eventType = "context_budget_exceeded"
	default:
		return nil
	}
	now := time.Now().UTC()
	event := domainai.WorkflowEvent{
		EventID:       fmt.Sprintf("evt_tool_context_budget_%d", now.UnixNano()),
		EventType:     eventType,
		Agent:         r.agent,
		CommandName:   toolName,
		Status:        decision.Status,
		CreatedAt:     now,
		CompletedAt:   now,
		Summary:       decision.Reason,
		ParentEventID: usage.EventID,
	}
	if err := r.rec.SaveWorkflowEvent(ctx, event); err != nil {
		return fmt.Errorf("tool context budget event save failed: %w", err)
	}
	return nil
}

func (r *ContextBudgetRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	if r.inner == nil {
		return nil, fmt.Errorf("inner runner is required")
	}
	return r.inner.ListTools(ctx)
}

func estimateToolResultTokens(resp *tool.ToolResponse) int {
	if resp == nil {
		return 0
	}
	text := resp.String()
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if tokens == 0 {
		return 1
	}
	return tokens
}

func contextBudgetMetadata(decision domainai.ContextBudgetDecision) map[string]any {
	return map[string]any{
		"context_budget_status":             decision.Status,
		"context_budget_reason":             decision.Reason,
		"context_budget_context_tokens":     decision.ContextTokens,
		"context_budget_max_context_tokens": decision.MaxContextTokens,
		"context_budget_usage_ratio":        decision.UsageRatio,
	}
}

func safeToolResultName(value string) string {
	if value == "" {
		return "tool"
	}
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out = append(out, r)
			continue
		}
		out = append(out, '_')
	}
	return string(out)
}
