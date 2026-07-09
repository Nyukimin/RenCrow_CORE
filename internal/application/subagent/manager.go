package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const defaultSystemPrompt = "You are a helpful assistant. Use the provided tools to complete the task."

type superAgentContextKey struct{}

type superAgentRuntimeContext struct {
	ParentRunID          string
	Scope                []string
	Tools                []string
	TerminationCondition string
}

// SuperAgentRecorder は subagent 実行を SuperAgent Harness 台帳へ記録する最小インターフェース。
type SuperAgentRecorder interface {
	SaveSubagentTask(ctx context.Context, item domainsuperagent.SubagentTask) error
	SaveTraceEvent(ctx context.Context, item domainsuperagent.TraceEvent) error
}

// ManagerOption は Manager の追加設定オプション
type ManagerOption func(*Manager)

// WithToolRegistry は ToolRegistry を Manager に注入する（Phase 4）
// RunSync 毎に承認済みツールを toolDefs に動的マージする
func WithToolRegistry(reg capability.ToolRegistry) ManagerOption {
	return func(m *Manager) {
		m.registry = reg
	}
}

// WithSuperAgentRecorder は subagent 実行の開始 / 完了 / 失敗を SuperAgent 台帳へ保存する。
func WithSuperAgentRecorder(recorder SuperAgentRecorder) ManagerOption {
	return func(m *Manager) {
		m.superAgentRecorder = recorder
	}
}

// WithSuperAgentRuntime は親 Lead Agent run と subagent task の接続情報を context に載せる。
func WithSuperAgentRuntime(ctx context.Context, parentRunID string, scope []string, tools []string, terminationCondition string) context.Context {
	return context.WithValue(ctx, superAgentContextKey{}, superAgentRuntimeContext{
		ParentRunID:          strings.TrimSpace(parentRunID),
		Scope:                cleanStrings(scope),
		Tools:                cleanStrings(tools),
		TerminationCondition: strings.TrimSpace(terminationCondition),
	})
}

// Manager はサブエージェントタスクの実行を管理する
type Manager struct {
	provider           llm.ToolCallingProvider
	toolRunner         tool.RunnerV2
	toolDefs           []llm.ToolDefinition
	loopConfig         toolloop.Config
	registry           capability.ToolRegistry // Phase 4: 動的ツール読込用（nil = 無効）
	onRegistryError    func(error)             // Phase 4: ToolRegistry エラー通知用（nil を許容）
	superAgentRecorder SuperAgentRecorder
}

// NewManager は新しい Manager を作成する
func NewManager(
	provider llm.ToolCallingProvider,
	toolRunner tool.RunnerV2,
	toolDefs []llm.ToolDefinition,
	loopConfig toolloop.Config,
	opts ...ManagerOption,
) *Manager {
	m := &Manager{
		provider:   provider,
		toolRunner: toolRunner,
		toolDefs:   toolDefs,
		loopConfig: loopConfig,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// SetRegistryErrorHandler は ToolRegistry エラー発生時に呼ばれるコールバックを設定する。
// 主に Viewer SSE でエラーを通知するために orchestrator から注入する。
func (m *Manager) SetRegistryErrorHandler(fn func(error)) {
	m.onRegistryError = fn
}

// SetSuperAgentRecorder は runtime 初期化後に SuperAgent 台帳レコーダーを注入する。
func (m *Manager) SetSuperAgentRecorder(recorder SuperAgentRecorder) {
	m.superAgentRecorder = recorder
}

// RunSync はサブエージェントタスクを同期実行する
func (m *Manager) RunSync(ctx context.Context, task agent.SubagentTask) (agent.SubagentResult, error) {
	if task.Instruction == "" {
		return agent.SubagentResult{}, fmt.Errorf("instruction is required")
	}
	log.Printf("[Subagent] start agent=%s instruction_len=%d", task.AgentName, len(task.Instruction))
	record, recordCtx := m.newSuperAgentExecutionRecord(ctx, task)
	if record != nil {
		if err := m.recordSuperAgentSubagentStarted(ctx, *record, recordCtx); err != nil {
			return agent.SubagentResult{}, err
		}
	}

	systemPrompt := task.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	messages := []llm.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task.Instruction},
	}

	mergedDefs := m.mergeToolDefs(ctx)
	output, err := toolloop.Run(ctx, m.provider, m.toolRunner, mergedDefs, messages, m.loopConfig)
	if err != nil {
		log.Printf("[Subagent] error agent=%s err=%v", task.AgentName, err)
		if record != nil {
			if recordErr := m.recordSuperAgentSubagentFinished(ctx, *record, recordCtx, "failed", err.Error()); recordErr != nil {
				return agent.SubagentResult{}, fmt.Errorf("subagent %s failed: %w; failed to record superagent subagent failure: %v", task.AgentName, err, recordErr)
			}
		}
		return agent.SubagentResult{}, fmt.Errorf("subagent %s failed: %w", task.AgentName, err)
	}
	log.Printf("[Subagent] complete agent=%s output_len=%d", task.AgentName, len(output))
	if record != nil {
		if err := m.recordSuperAgentSubagentFinished(ctx, *record, recordCtx, "completed", "Subagent completed"); err != nil {
			return agent.SubagentResult{}, err
		}
	}

	return agent.SubagentResult{
		AgentName: task.AgentName,
		Output:    output,
	}, nil
}

// mergeToolDefs は base toolDefs と ToolRegistry の承認済みツールをマージする
// base のツールが優先される（名前重複は base が勝つ）
func (m *Manager) mergeToolDefs(ctx context.Context) []llm.ToolDefinition {
	if m.registry == nil {
		return m.toolDefs
	}

	entries, err := m.registry.ListForPlatform(ctx, runtime.GOOS)
	if err != nil {
		log.Printf("[Subagent] WARN: registry list failed: %v", err)
		if m.onRegistryError != nil {
			m.onRegistryError(err)
		}
		return m.toolDefs
	}

	if len(entries) == 0 {
		return m.toolDefs
	}

	// base ツールの名前セット（重複チェック用）
	existing := make(map[string]bool, len(m.toolDefs))
	for _, d := range m.toolDefs {
		existing[d.Function.Name] = true
	}

	merged := make([]llm.ToolDefinition, len(m.toolDefs))
	copy(merged, m.toolDefs)

	for _, entry := range entries {
		if existing[entry.Name] {
			continue // base ツールが優先
		}
		var toolDef llm.ToolDefinition
		if err := json.Unmarshal([]byte(entry.SchemaJSON), &toolDef); err != nil {
			log.Printf("[Subagent] WARN: skip registry tool %q: invalid schema: %v", entry.Name, err)
			continue
		}
		merged = append(merged, toolDef)
		existing[entry.Name] = true
	}

	return merged
}

type superAgentExecutionRecord struct {
	SubagentID string
	StartedAt  time.Time
}

func (m *Manager) newSuperAgentExecutionRecord(ctx context.Context, task agent.SubagentTask) (*superAgentExecutionRecord, superAgentRuntimeContext) {
	if m.superAgentRecorder == nil {
		return nil, superAgentRuntimeContext{}
	}
	runtimeCtx, ok := ctx.Value(superAgentContextKey{}).(superAgentRuntimeContext)
	if !ok || runtimeCtx.ParentRunID == "" {
		return nil, superAgentRuntimeContext{}
	}
	startedAt := time.Now().UTC()
	agentName := sanitizeIDPart(task.AgentName)
	if agentName == "" {
		agentName = "worker"
	}
	return &superAgentExecutionRecord{
		SubagentID: fmt.Sprintf("sub_%s_%d", agentName, startedAt.UnixNano()),
		StartedAt:  startedAt,
	}, runtimeCtx
}

func (m *Manager) recordSuperAgentSubagentStarted(ctx context.Context, record superAgentExecutionRecord, runtimeCtx superAgentRuntimeContext) error {
	item := domainsuperagent.SubagentTask{
		SubagentID:           record.SubagentID,
		ParentRunID:          runtimeCtx.ParentRunID,
		AgentType:            "Subagent",
		Task:                 "runtime delegated subagent task",
		Scope:                fallbackStrings(runtimeCtx.Scope, []string{"runtime:subagent"}),
		Tools:                runtimeCtx.Tools,
		TerminationCondition: fallbackString(runtimeCtx.TerminationCondition, "return final subagent result to parent run"),
		Status:               "running",
		CreatedAt:            record.StartedAt,
	}
	if err := m.superAgentRecorder.SaveSubagentTask(ctx, item); err != nil {
		return fmt.Errorf("failed to record superagent subagent start: %w", err)
	}
	trace := domainsuperagent.TraceEvent{
		EventID:        fmt.Sprintf("evt_subagent_started_%s_%d", record.SubagentID, record.StartedAt.UnixNano()),
		ParentEventID:  runtimeCtx.ParentRunID,
		RunID:          runtimeCtx.ParentRunID,
		EventType:      "subagent_started",
		Actor:          "Subagent",
		PayloadSummary: "runtime delegated subagent task",
		Status:         "running",
		CreatedAt:      record.StartedAt,
	}
	if err := m.superAgentRecorder.SaveTraceEvent(ctx, trace); err != nil {
		return fmt.Errorf("failed to record superagent subagent start trace: %w", err)
	}
	return nil
}

func (m *Manager) recordSuperAgentSubagentFinished(ctx context.Context, record superAgentExecutionRecord, runtimeCtx superAgentRuntimeContext, status string, summary string) error {
	completedAt := time.Now().UTC()
	item := domainsuperagent.SubagentTask{
		SubagentID:           record.SubagentID,
		ParentRunID:          runtimeCtx.ParentRunID,
		AgentType:            "Subagent",
		Task:                 "runtime delegated subagent task",
		Scope:                fallbackStrings(runtimeCtx.Scope, []string{"runtime:subagent"}),
		Tools:                runtimeCtx.Tools,
		TerminationCondition: fallbackString(runtimeCtx.TerminationCondition, "return final subagent result to parent run"),
		Status:               status,
		CreatedAt:            record.StartedAt,
		CompletedAt:          completedAt,
	}
	if err := m.superAgentRecorder.SaveSubagentTask(ctx, item); err != nil {
		return fmt.Errorf("failed to record superagent subagent %s: %w", status, err)
	}
	trace := domainsuperagent.TraceEvent{
		EventID:        fmt.Sprintf("evt_subagent_%s_%s_%d", status, record.SubagentID, completedAt.UnixNano()),
		ParentEventID:  runtimeCtx.ParentRunID,
		RunID:          runtimeCtx.ParentRunID,
		EventType:      "subagent_" + status,
		Actor:          "Subagent",
		PayloadSummary: summary,
		Status:         status,
		CreatedAt:      completedAt,
	}
	if err := m.superAgentRecorder.SaveTraceEvent(ctx, trace); err != nil {
		return fmt.Errorf("failed to record superagent subagent %s trace: %w", status, err)
	}
	return nil
}

func cleanStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func fallbackStrings(values []string, fallback []string) []string {
	if len(values) > 0 {
		return values
	}
	return fallback
}

func fallbackString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func sanitizeIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if r == '_' || r == '-' {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
