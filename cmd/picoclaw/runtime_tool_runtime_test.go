package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	aiworkflowpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/aiworkflow"
)

type runtimeContextBudgetRecorderStub struct {
	usages []domainai.ContextUsage
	events []domainai.WorkflowEvent
}

func (s *runtimeContextBudgetRecorderStub) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	s.usages = append(s.usages, item)
	return nil
}

func (s *runtimeContextBudgetRecorderStub) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	s.events = append(s.events, item)
	return nil
}

func TestBuildToolMediationRecorderUsesConfiguredLogPath(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tool_mediation.jsonl")
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness: config.ToolHarnessConfig{
			LogPath: logPath,
		},
	}

	recorder := buildToolMediationRecorder(cfg)
	if recorder == nil {
		t.Fatal("expected recorder")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected recorder file at configured path: %v", err)
	}
}

func TestBuildToolMediationRecorderDisabledByConfig(t *testing.T) {
	enabled := false
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness: config.ToolHarnessConfig{
			Enabled: &enabled,
		},
	}

	if recorder := buildToolMediationRecorder(cfg); recorder != nil {
		t.Fatal("disabled tool harness should not create recorder")
	}
}

func TestBuildToolMediationRecorderRecordEventsDisabled(t *testing.T) {
	recordEvents := false
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness: config.ToolHarnessConfig{
			RecordEvents: &recordEvents,
		},
	}

	if recorder := buildToolMediationRecorder(cfg); recorder != nil {
		t.Fatal("record_events=false should not create recorder")
	}
}

func TestBuildToolRuntimeWrapsToolContextBudget(t *testing.T) {
	recordEvents := false
	workspace := t.TempDir()
	path := filepath.Join(workspace, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 400)), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg := &config.Config{
		WorkspaceDir: workspace,
		ToolHarness: config.ToolHarnessConfig{
			RecordEvents: &recordEvents,
		},
		AIWorkflow: config.AIWorkflowConfig{
			ContextBudgetTokens:    50,
			ContextBudgetWarnRatio: 0.8,
			ContextBudgetStopRatio: 0.95,
		},
	}

	runtime := buildToolRuntime(cfg, nil, nil, nil)
	resp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(context.Background(), "file_read", map[string]any{"path": path})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected context budget error response, got %#v", resp)
	}
	if resp.Error.Details["context_budget_status"] != domainai.ContextBudgetStatusStop {
		t.Fatalf("expected stop metadata, got %#v", resp.Error.Details)
	}
}

func TestBuildToolRuntimeRecordsToolContextBudgetUsage(t *testing.T) {
	recordEvents := false
	workspace := t.TempDir()
	path := filepath.Join(workspace, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 340)), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg := &config.Config{
		WorkspaceDir: workspace,
		ToolHarness: config.ToolHarnessConfig{
			RecordEvents: &recordEvents,
		},
		AIWorkflow: config.AIWorkflowConfig{
			ContextBudgetTokens:    100,
			ContextBudgetWarnRatio: 0.8,
			ContextBudgetStopRatio: 0.95,
		},
	}
	recorder := &runtimeContextBudgetRecorderStub{}

	runtime := buildToolRuntime(cfg, nil, nil, recorder)
	resp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(context.Background(), "file_read", map[string]any{"path": path})
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp == nil || resp.IsError() {
		t.Fatalf("expected warning success response, got %#v", resp)
	}
	if len(recorder.usages) != 1 {
		t.Fatalf("expected one usage record, got %#v", recorder.usages)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("expected one workflow event, got %#v", recorder.events)
	}
	if recorder.events[0].EventType != "context_budget_warning" {
		t.Fatalf("expected context budget warning event, got %#v", recorder.events[0])
	}
	if recorder.events[0].ParentEventID != recorder.usages[0].EventID {
		t.Fatalf("event should link to usage: event=%#v usage=%#v", recorder.events[0], recorder.usages[0])
	}
}

func TestBuildToolRuntimePersistsToolContextBudgetToAIWorkflowStore(t *testing.T) {
	recordEvents := false
	ctx := context.Background()
	workspace := t.TempDir()
	warnPath := filepath.Join(workspace, "warn.txt")
	stopPath := filepath.Join(workspace, "stop.txt")
	if err := os.WriteFile(warnPath, []byte(strings.Repeat("w", 340)), 0644); err != nil {
		t.Fatalf("write warning fixture: %v", err)
	}
	if err := os.WriteFile(stopPath, []byte(strings.Repeat("s", 520)), 0644); err != nil {
		t.Fatalf("write stop fixture: %v", err)
	}
	store := aiworkflowpersistence.NewJSONLStore(filepath.Join(workspace, "logs", "ai_workflow"))
	cfg := &config.Config{
		WorkspaceDir: workspace,
		ToolHarness: config.ToolHarnessConfig{
			RecordEvents: &recordEvents,
		},
		AIWorkflow: config.AIWorkflowConfig{
			ContextBudgetTokens:    100,
			ContextBudgetWarnRatio: 0.8,
			ContextBudgetStopRatio: 0.95,
		},
	}

	runtime := buildToolRuntime(cfg, nil, nil, store)
	warnResp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(ctx, "file_read", map[string]any{"path": warnPath})
	if err != nil {
		t.Fatalf("warning ExecuteV2 returned err: %v", err)
	}
	if warnResp == nil || warnResp.IsError() {
		t.Fatalf("expected warning success response, got %#v", warnResp)
	}
	if warnResp.Metadata["context_budget_status"] != domainai.ContextBudgetStatusWarn {
		t.Fatalf("expected warning metadata, got %#v", warnResp.Metadata)
	}

	stopResp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(ctx, "file_read", map[string]any{"path": stopPath})
	if err != nil {
		t.Fatalf("stop ExecuteV2 returned err: %v", err)
	}
	if stopResp == nil || !stopResp.IsError() {
		t.Fatalf("expected stop error response, got %#v", stopResp)
	}
	if stopResp.Error.Details["context_budget_status"] != domainai.ContextBudgetStatusStop {
		t.Fatalf("expected stop metadata, got %#v", stopResp.Error.Details)
	}
	if offloaded, _ := stopResp.Error.Details["context_budget_offloaded"].(bool); !offloaded {
		t.Fatalf("expected stopped tool result to be offloaded, got %#v", stopResp.Error.Details)
	}

	usages, err := store.ListContextUsages(ctx, 10)
	if err != nil {
		t.Fatalf("ListContextUsages() error = %v", err)
	}
	events, err := store.ListWorkflowEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListWorkflowEvents() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("expected two persisted context usages, got %#v", usages)
	}
	byType := map[string]domainai.WorkflowEvent{}
	for _, event := range events {
		byType[event.EventType] = event
	}
	for _, want := range []string{"context_budget_warning", "context_budget_exceeded"} {
		event, ok := byType[want]
		if !ok {
			t.Fatalf("expected persisted %s event, got %#v", want, events)
		}
		if event.CommandName != "file_read" || event.Agent != "Worker" || event.ParentEventID == "" {
			t.Fatalf("unexpected persisted event for %s: %#v", want, event)
		}
	}
}
