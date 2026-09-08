package main

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	aiworkflowpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeContextBudgetRecorderStub struct {
	usages []domainai.ContextUsage
	events []modulecore.EventEnvelope
}

func TestBuildToolRuntimeSharesSecurityActionManagerWithPolicyRunner(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		WorkspaceDir: workspace,
		Security: config.SecurityConfig{
			Enabled:    true,
			PolicyMode: "balanced",
		},
	}
	owner, ctx := runtimeToolOwnerFixture(t, workspace, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	if runtime.ActionManager == nil {
		t.Fatal("security-enabled runtime must expose its ActionManager")
	}

	path := filepath.Join(workspace, "read.txt")
	if err := os.WriteFile(path, []byte("shared-action-manager"), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(ctx, "file_read", map[string]any{"path": path})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("permitted tool failed: response=%#v error=%v", response, err)
	}

	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := runtime.ActionManager.ListActions(ctx, domainaction.Filter{TaskID: identity.TaskID, RunID: identity.RunID})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("want one PolicyRunner action, got %d", len(actions))
	}
	action := actions[0]
	if action.TaskID != identity.TaskID || action.RunID != identity.RunID || action.Kind != domainaction.KindTool || action.Name != "file_read" {
		t.Fatalf("unexpected action: %+v", action)
	}
	if action.CurrentAttemptID == "" {
		t.Fatal("PolicyRunner action has no current attempt")
	}
	attempts, err := runtime.ActionManager.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list PolicyRunner attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("want one PolicyRunner attempt, got %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.AttemptID != action.CurrentAttemptID || attempt.ActionID != action.ActionID || attempt.StartReason != domainaction.AttemptStartReasonFirst {
		t.Fatalf("unexpected attempt: %+v", attempt)
	}
}

func TestBuildToolRuntimeSecurityPreservesRegisteredWorkerRoute(t *testing.T) {
	// CompositeRunner executes registered .sh entries through a POSIX shell.
	// Windows shell resolution is covered by internal/infrastructure/tools' OS-specific tests.
	if runtime.GOOS == "windows" {
		t.Skip("registered .sh execution requires POSIX shell; Windows resolution is tested separately")
	}

	workspace := t.TempDir()
	const toolName = "registry_only_runtime_tool"
	toolsDir := filepath.Join(workspace, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(toolsDir, toolName+".sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf 'registry-route-ok\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	registry := &runtimeCapabilityToolRegistryStub{entries: []capdomain.ToolEntry{{
		Name:        toolName,
		Description: "registry-only runtime tool",
		SchemaJSON:  `{"type":"object","properties":{"input":{"type":"string"}}}`,
		Platforms:   []string{"linux", "darwin"},
	}}}
	disabled := false
	cfg := &config.Config{
		WorkspaceDir: workspace,
		Security: config.SecurityConfig{
			Enabled:    true,
			PolicyMode: "balanced",
		},
		ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, workspace, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, registry, nil, nil, nil, testCanonicalMediationStore(t))

	metadata, err := runtime.WorkerRuntimeRunnerV2.ListTools(executionCtx)
	if err != nil {
		t.Fatalf("secured Worker ListTools failed: %v", err)
	}
	if !hasToolMetadata(metadata, toolName) {
		t.Fatalf("registry-only tool is missing from secured Worker metadata: %#v", metadata)
	}
	rawMetadata, err := runtime.WorkerRunnerV2.ListTools(executionCtx)
	if err != nil {
		t.Fatalf("base Worker ListTools failed: %v", err)
	}
	if hasToolMetadata(rawMetadata, toolName) {
		t.Fatalf("registry-only tool unexpectedly bypassed the runtime registry wrapper: %#v", rawMetadata)
	}

	response, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, toolName, map[string]any{"input": "first"})
	if err != nil || response == nil || response.IsError() || response.String() != "registry-route-ok\n" {
		t.Fatalf("secured registry route failed: response=%#v error=%v", response, err)
	}
	assertRuntimeToolActionTerminal(t, runtime.ActionManager, executionCtx, toolName, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)

	if err := os.Remove(scriptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, toolName, map[string]any{"input": "after-delete"}); err == nil {
		t.Fatal("deleted registry script must be rejected")
	}
	assertRuntimeToolActionTerminal(t, runtime.ActionManager, executionCtx, toolName, domainaction.StatusFailed, domainaction.AttemptStatusFailed)
}

func assertRuntimeToolActionTerminal(t *testing.T, actions *actionmanager.Manager, ctx context.Context, toolName string, wantAction domainaction.Status, wantAttempt domainaction.AttemptStatus) {
	t.Helper()
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	items, err := actions.ListActions(ctx, domainaction.Filter{TaskID: identity.TaskID, RunID: identity.RunID})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	var action domainaction.Action
	for _, item := range items {
		if item.Name == toolName && item.Status == wantAction {
			action = item
		}
	}
	if action.ActionID == "" {
		t.Fatalf("no %s action for %q in %#v", wantAction, toolName, items)
	}
	attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != wantAttempt || !domainaction.IsAttemptTerminal(attempts[0].Status) {
		t.Fatalf("unexpected terminal attempt for %q: %#v", toolName, attempts)
	}
}

func TestBuildToolRuntimeLeavesActionManagerNilWhenSecurityAndSubagentDisabled(t *testing.T) {
	cfg := &config.Config{WorkspaceDir: t.TempDir()}
	runtime := buildToolRuntimeForTest(t, cfg)
	if runtime.ActionManager != nil {
		t.Fatal("runtime must not create an ActionManager when Security and Subagent are disabled")
	}
}

func TestViewerRuntimeToolsUsesProductionWorkerRunner(t *testing.T) {
	disabled := false
	cfg := &config.Config{ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	runtime := buildToolRuntimeForTest(t, cfg)
	metas, err := viewerRuntimeTools(&Dependencies{workerToolRunner: runtime.WorkerRuntimeRunnerV2})(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range metas {
		if meta.ToolID == "shell" {
			return
		}
	}
	t.Fatal("production Worker runner metadata is not reachable from Viewer")
}

func TestBuildToolRuntimeInstallsSkillReadOnlyForWorker(t *testing.T) {
	disabled := false
	cfg := &config.Config{ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	catalog := toolsinfra.NewSkillCatalog([]domaincontext.SkillMetadata{{Name: "review", BodyText: "trusted body"}})
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = t.TempDir()
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, catalog, nil, testCanonicalMediationStore(t))

	workerMetas, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools failed: %v", err)
	}
	chatMetas, err := runtime.ChatRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Chat ListTools failed: %v", err)
	}
	if !hasToolMetadata(workerMetas, "skill.read") {
		t.Fatalf("Worker metadata missing skill.read: %#v", workerMetas)
	}
	if hasToolMetadata(chatMetas, "skill.read") {
		t.Fatalf("Chat runner must not receive skill.read: %#v", chatMetas)
	}
	resp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, "skill.read", map[string]any{"name": "review"})
	if err != nil || resp == nil || resp.IsError() || resp.String() != "trusted body" {
		t.Fatalf("Worker skill.read failed: resp=%#v err=%v", resp, err)
	}
}

func TestBuildToolRuntimeRegistersKnowledgeSearchOnlyAfterSQLiteIndexGate(t *testing.T) {
	disabled := false
	workspace := t.TempDir()
	dbPath := filepath.Join(workspace, "knowledge_memory.db")
	source := knowledgememorypersistence.NewJSONLStore(filepath.Join(workspace, "knowledge_jsonl"))
	if err := source.SaveCreativeKnowledgeItem(context.Background(), domainkm.CreativeKnowledgeItem{
		ItemID: "runtime-creative-1", Title: "日本語の公開作品", WorkType: "映画", Status: "reviewed", CreatedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := knowledgememorypersistence.ImportJSONLToSQLite(context.Background(), filepath.Join(workspace, "knowledge_jsonl"), dbPath); err != nil {
		t.Fatalf("prepare promoted SQLite database: %v", err)
	}
	cfg := &config.Config{
		WorkspaceDir:    workspace,
		Storage:         config.StorageConfig{Databases: config.DatabasePathsConfig{KnowledgeMemory: dbPath}},
		KnowledgeMemory: config.KnowledgeMemoryConfig{Storage: "sqlite", SQLitePath: dbPath},
		ToolHarness:     config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
	}
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = t.TempDir()
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "mio")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	if runtime.KnowledgeMemoryToolStore == nil {
		t.Fatal("SQLite Knowledge Memory Tool store was not initialized")
	}
	defer runtime.KnowledgeMemoryToolStore.Close()
	entry, describeErr := runtime.DataCapabilityCatalog.catalog.Describe("knowledge_memory")
	if describeErr != nil || entry.Status != "available" || entry.ToolID != "knowledge.search" || entry.Reason != "scope_unavailable" {
		t.Fatalf("promoted public catalog entry = %#v err=%v", entry, describeErr)
	}
	if strings.Join(entry.SafeOperations, "\x00") != "public_search" {
		t.Fatalf("private operation was exposed without trusted auth: %#v", entry.SafeOperations)
	}
	publicScope, err := domaintool.NewToolExecutionScope(
		"runtime-knowledge-test",
		domaintool.ActorKindAgent,
		"mio",
		"",
		[]string{domaintool.DataScopePublic},
		domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	for name, runner := range map[string]domaintool.RunnerV2{
		"chat":   runtime.ChatRuntimeRunnerV2,
		"worker": runtime.WorkerRuntimeRunnerV2,
	} {
		metadata, listErr := runner.ListTools(context.Background())
		if listErr != nil {
			t.Fatalf("%s ListTools() error = %v", name, listErr)
		}
		if !hasToolMetadata(metadata, "knowledge.search") {
			t.Fatalf("%s knowledge.search is not exposed after SQLite schema gate: %#v", name, metadata)
		}
		response, executeErr := runner.ExecuteV2(
			domaintool.WithToolExecutionScope(executionCtx, publicScope),
			"knowledge.search",
			map[string]any{"query": "日本語", "record_type": "creative_knowledge"},
		)
		if executeErr != nil || response == nil || response.Error != nil {
			t.Fatalf("%s knowledge.search E2E response=%#v err=%v", name, response, executeErr)
		}
	}
	disabledMemory := false
	disabledCfg := *cfg
	disabledCfg.KnowledgeMemory.Enabled = &disabledMemory
	disabledRuntime := buildToolRuntimeForTest(t, &disabledCfg)
	disabledMetadata, metadataErr := disabledRuntime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if metadataErr != nil {
		t.Fatalf("disabled Worker ListTools() error = %v", metadataErr)
	}
	if disabledRuntime.KnowledgeMemoryToolStore != nil || hasToolMetadata(disabledMetadata, "knowledge.search") {
		t.Fatal("disabled Knowledge Memory must not initialize or expose knowledge.search")
	}
}

func TestBuildToolRuntimeAdvertisesPrivateKnowledgeSearchOnlyWithCompleteLineIngress(t *testing.T) {
	disabled := false
	workspace := t.TempDir()
	dbPath := filepath.Join(workspace, "knowledge_memory.db")
	source := knowledgememorypersistence.NewJSONLStore(filepath.Join(workspace, "knowledge_jsonl"))
	if err := source.SaveCreativeKnowledgeItem(context.Background(), domainkm.CreativeKnowledgeItem{
		ItemID: "runtime-private-creative-1", Title: "日本語の非公開境界テスト", WorkType: "映画", Status: "reviewed", CreatedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := knowledgememorypersistence.ImportJSONLToSQLite(context.Background(), filepath.Join(workspace, "knowledge_jsonl"), dbPath); err != nil {
		t.Fatalf("prepare promoted SQLite database: %v", err)
	}
	cases := []struct {
		name        string
		secret      string
		token       string
		wantPrivate bool
	}{
		{name: "none"},
		{name: "secret-only", secret: "line-channel-secret"},
		{name: "token-only", token: "line-access-token"},
		{name: "both", secret: "line-channel-secret", token: "line-access-token", wantPrivate: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				WorkspaceDir:    workspace,
				Storage:         config.StorageConfig{Databases: config.DatabasePathsConfig{KnowledgeMemory: dbPath}},
				KnowledgeMemory: config.KnowledgeMemoryConfig{Storage: "sqlite", SQLitePath: dbPath},
				Line:            config.LineConfig{ChannelSecret: tt.secret, AccessToken: tt.token},
				ToolHarness:     config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
			}
			runtime := buildToolRuntimeForTest(t, cfg)
			if runtime.KnowledgeMemoryToolStore != nil {
				t.Cleanup(func() { _ = runtime.KnowledgeMemoryToolStore.Close() })
			}
			entry, err := runtime.DataCapabilityCatalog.catalog.Describe("knowledge_memory")
			if err != nil {
				t.Fatalf("knowledge_memory catalog entry: %v", err)
			}
			if entry.Status != "available" || entry.ToolID != "knowledge.search" {
				t.Fatalf("knowledge_memory catalog readiness = %#v", entry)
			}
			wantOperations := "public_search"
			wantReason := "scope_unavailable"
			if tt.wantPrivate {
				wantOperations = "public_search\x00user_private_search"
				wantReason = ""
			}
			if strings.Join(entry.SafeOperations, "\x00") != wantOperations || entry.Reason != wantReason {
				t.Fatalf("knowledge_memory catalog scope = operations=%#v reason=%q", entry.SafeOperations, entry.Reason)
			}
		})
	}
}

func TestBuildToolRuntimeDoesNotCreateOrExposeUnreadyKnowledgeDatabase(t *testing.T) {
	disabled := false
	workspace := t.TempDir()
	dbPath := filepath.Join(workspace, "not-created.db")
	cfg := &config.Config{
		WorkspaceDir: workspace,
		Storage:      config.StorageConfig{Databases: config.DatabasePathsConfig{KnowledgeMemory: dbPath}},
		KnowledgeMemory: config.KnowledgeMemoryConfig{
			Storage:    "sqlite",
			SQLitePath: dbPath,
		},
		ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled},
	}
	runtime := buildToolRuntimeForTest(t, cfg)
	if runtime.KnowledgeMemoryToolStore != nil {
		t.Fatal("unready knowledge database must not be retained as a runtime store")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("runtime created or retained missing database path: stat err=%v", err)
	}
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "knowledge.search") {
		t.Fatal("knowledge.search must remain unregistered before promotion")
	}
	entry, err := runtime.DataCapabilityCatalog.catalog.Describe("knowledge_memory")
	if err != nil || entry.Status != "unavailable" || entry.Reason != "database_unavailable" {
		t.Fatalf("unready catalog entry = %#v err=%v", entry, err)
	}
}

func hasToolMetadata(metas []domaintool.ToolMetadata, name string) bool {
	for _, metadata := range metas {
		if metadata.ToolID == name {
			return true
		}
	}
	return false
}

func (s *runtimeContextBudgetRecorderStub) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	s.usages = append(s.usages, item)
	return nil
}

func (s *runtimeContextBudgetRecorderStub) Append(_ context.Context, item modulecore.EventEnvelope) error {
	s.events = append(s.events, item)
	return nil
}

func TestBuildToolMediationRecorderPreservesLegacyPathWithoutWriting(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tool_mediation.jsonl")
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		ToolHarness: config.ToolHarnessConfig{
			LogPath: logPath,
		},
	}

	recorder, err := buildToolMediationRecorder(cfg, testCanonicalMediationStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if recorder == nil {
		t.Fatal("expected recorder")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("legacy JSONL destination used: %v", err)
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

	if recorder, err := buildToolMediationRecorder(cfg, testCanonicalMediationStore(t)); recorder != nil || err != nil {
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

	if recorder, err := buildToolMediationRecorder(cfg, testCanonicalMediationStore(t)); recorder != nil || err != nil {
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

	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = t.TempDir()
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	resp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, "file_read", map[string]any{"path": path})
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

	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = t.TempDir()
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, recorder, nil, nil, testCanonicalMediationStore(t))
	resp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, "file_read", map[string]any{"path": path})
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
	if recorder.events[0].CausationEventID != "" || recorder.events[0].Payload["context_usage_record_id"] != recorder.usages[0].EventID {
		t.Fatalf("event must reference non-event usage only through payload: event=%#v usage=%#v", recorder.events[0], recorder.usages[0])
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
	stateStore := aiworkflowpersistence.NewJSONLStore(filepath.Join(workspace, "logs", "ai_workflow"))
	eventsStore, err := eventpersistence.NewSQLiteStore(filepath.Join(workspace, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer eventsStore.Close()
	store := composeRuntimeAIWorkflowStore(stateStore, eventsStore)
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

	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = t.TempDir()
	}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, store, nil, nil, testCanonicalMediationStore(t))
	warnResp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, "file_read", map[string]any{"path": warnPath})
	if err != nil {
		t.Fatalf("warning ExecuteV2 returned err: %v", err)
	}
	if warnResp == nil || warnResp.IsError() {
		t.Fatalf("expected warning success response, got %#v", warnResp)
	}
	if warnResp.Metadata["context_budget_status"] != domainai.ContextBudgetStatusWarn {
		t.Fatalf("expected warning metadata, got %#v", warnResp.Metadata)
	}

	stopResp, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(executionCtx, "file_read", map[string]any{"path": stopPath})
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
	events, err := store.ListByComponent(ctx, "ai_workflow", 10)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("expected two persisted context usages, got %#v", usages)
	}
	identity, err := domainexecution.IdentityFromContext(executionCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, usage := range usages {
		if usage.TaskID != identity.TaskID || usage.RunID != identity.RunID {
			t.Fatalf("persisted context usage lost execution identity: %#v", usage)
		}
	}
	byType := map[string]modulecore.EventEnvelope{}
	for _, event := range events {
		if event.TaskID != identity.TaskID || event.RunID != identity.RunID || event.TraceID != identity.TraceID || event.ActorKind != "agent" || event.ActorID != "shiro" {
			t.Fatalf("persisted budget event lost owner lineage: %#v", event)
		}
		byType[event.EventType] = event
	}
	for _, want := range []string{"context_budget_warning", "context_budget_exceeded"} {
		event, ok := byType[want]
		if !ok {
			t.Fatalf("expected persisted %s event, got %#v", want, events)
		}
		if event.Payload["command_name"] != "file_read" || event.Payload["agent_label"] != "Worker" || event.CausationEventID != "" || event.Payload["context_usage_record_id"] == "" {
			t.Fatalf("unexpected persisted event for %s: %#v", want, event)
		}
	}
}

type taskExecutionRunnerMarker struct{}

type countingTaskExecutionRunner struct {
	executeCalls int
	listCalls    int
	lastToolName string
	lastArgs     map[string]any
	sawMarker    bool
	sawIdentity  bool
	sawScope     bool
}

func (r *countingTaskExecutionRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*domaintool.ToolResponse, error) {
	r.executeCalls++
	r.lastToolName = toolName
	r.lastArgs = args
	r.sawMarker = ctx != nil && ctx.Value(taskExecutionRunnerMarker{}) == "preserved"
	if ctx != nil {
		_, identityErr := domainexecution.IdentityFromContext(ctx)
		r.sawIdentity = identityErr == nil
		_, r.sawScope = domaintool.ToolExecutionScopeFromContext(ctx)
	}
	return domaintool.NewSuccess("fixture success"), nil
}

func (r *countingTaskExecutionRunner) ListTools(context.Context) ([]domaintool.ToolMetadata, error) {
	r.listCalls++
	return []domaintool.ToolMetadata{{ToolID: "fixture.tool", Version: "test"}}, nil
}

func newTaskExecutionRunnerFixture(t *testing.T) (*Dependencies, domaintask.Task, domaintask.Run) {
	t.Helper()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initializeRuntimeTaskOwner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	ctx := context.Background()
	task, err := deps.taskManager.Create(ctx, domaintask.Task{
		Title:    "tool execution admission fixture",
		Route:    domaintask.RouteGeneral,
		Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create admission Task: %v", err)
	}
	run, err := deps.taskManager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start admission Run: %v", err)
	}
	return deps, task, run
}

func newTaskExecutionRunnerContext(t *testing.T, task domaintask.Task, run domaintask.Run, actorKind domaintool.ActorKind, actorID string, includeIdentity, includeScope bool) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), taskExecutionRunnerMarker{}, "preserved")
	if includeIdentity {
		var err error
		ctx, err = domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
		if err != nil {
			t.Fatalf("bind execution identity: %v", err)
		}
	}
	if includeScope {
		authenticatedUserID := ""
		authenticationSource := domaintool.AuthenticationSourceAgentOrchestrator
		if actorKind == domaintool.ActorKindUser {
			authenticatedUserID = actorID
			authenticationSource = domaintool.AuthenticationSourceHTTP
		}
		scope, err := domaintool.NewToolExecutionScope(
			"runtime-tool-admission",
			actorKind,
			actorID,
			authenticatedUserID,
			[]string{domaintool.DataScopePublic},
			authenticationSource,
		)
		if err != nil {
			t.Fatalf("create execution scope: %v", err)
		}
		ctx = domaintool.WithToolExecutionScope(ctx, scope)
	}
	return ctx
}

func TestTaskExecutionRunnerAdmitsCurrentAgentRunAndPreservesContext(t *testing.T) {
	deps, task, run := newTaskExecutionRunnerFixture(t)
	inner := &countingTaskExecutionRunner{}
	runner := &taskExecutionRunner{owner: deps.taskManager, inner: inner}
	ctx := newTaskExecutionRunnerContext(t, task, run, domaintool.ActorKindAgent, "shiro", true, true)
	args := map[string]any{"fixture": "unchanged"}

	response, err := runner.ExecuteV2(ctx, "fixture.tool", args)
	if err != nil {
		t.Fatalf("admitted ExecuteV2: %v", err)
	}
	if response == nil || response.String() != "fixture success" {
		t.Fatalf("response = %#v", response)
	}
	if inner.executeCalls != 1 {
		t.Fatalf("inner ExecuteV2 calls = %d, want 1", inner.executeCalls)
	}
	if inner.lastToolName != "fixture.tool" || inner.lastArgs["fixture"] != "unchanged" {
		t.Fatalf("inner arguments = tool=%q args=%#v", inner.lastToolName, inner.lastArgs)
	}
	if !inner.sawMarker || !inner.sawIdentity || !inner.sawScope {
		t.Fatalf("inner did not receive unchanged execution context: marker=%t identity=%t scope=%t", inner.sawMarker, inner.sawIdentity, inner.sawScope)
	}
}

func TestTaskExecutionRunnerRejectsInvalidAdmissionBeforeInner(t *testing.T) {
	cases := []struct {
		name            string
		actorKind       domaintool.ActorKind
		actorID         string
		includeIdentity bool
		includeScope    bool
		before          func(*testing.T, *Dependencies, domaintask.Task)
	}{
		{name: "missing_identity", actorKind: domaintool.ActorKindAgent, actorID: "shiro", includeScope: true},
		{name: "missing_scope", actorKind: domaintool.ActorKindAgent, actorID: "shiro", includeIdentity: true},
		{name: "user_scope", actorKind: domaintool.ActorKindUser, actorID: "shiro", includeIdentity: true, includeScope: true},
		{name: "wrong_agent", actorKind: domaintool.ActorKindAgent, actorID: "mio", includeIdentity: true, includeScope: true},
		{
			name:            "closed_owner",
			actorKind:       domaintool.ActorKindAgent,
			actorID:         "shiro",
			includeIdentity: true,
			includeScope:    true,
			before: func(t *testing.T, deps *Dependencies, _ domaintask.Task) {
				if err := deps.taskManager.Close(); err != nil {
					t.Fatalf("close owner: %v", err)
				}
			},
		},
		{
			name:            "terminal_task_and_run",
			actorKind:       domaintool.ActorKindAgent,
			actorID:         "shiro",
			includeIdentity: true,
			includeScope:    true,
			before: func(t *testing.T, deps *Dependencies, task domaintask.Task) {
				if _, err := deps.taskManager.Succeed(context.Background(), task.TaskID, "fixture terminal"); err != nil {
					t.Fatalf("finish fixture Task: %v", err)
				}
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			deps, task, run := newTaskExecutionRunnerFixture(t)
			if test.before != nil {
				test.before(t, deps, task)
			}
			inner := &countingTaskExecutionRunner{}
			runner := &taskExecutionRunner{owner: deps.taskManager, inner: inner}
			ctx := newTaskExecutionRunnerContext(t, task, run, test.actorKind, test.actorID, test.includeIdentity, test.includeScope)

			response, err := runner.ExecuteV2(ctx, "fixture.tool", map[string]any{"fixture": test.name})
			if err == nil {
				t.Fatalf("invalid admission was accepted: response=%#v", response)
			}
			if response != nil {
				t.Fatalf("rejected admission returned response: %#v", response)
			}
			if inner.executeCalls != 0 {
				t.Fatalf("inner ExecuteV2 calls = %d, want 0", inner.executeCalls)
			}
		})
	}
}

func TestTaskExecutionRunnerRejectsStaleRunAfterWriterRestart(t *testing.T) {
	root := t.TempDir()
	first := &Dependencies{}
	if err := initializeRuntimeTaskOwner(first, root); err != nil {
		t.Fatalf("initialize first owner: %v", err)
	}
	task, err := first.taskManager.Create(context.Background(), domaintask.Task{
		Title:    "stale Run fixture",
		Route:    domaintask.RouteGeneral,
		Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create stale Task: %v", err)
	}
	oldRun, err := first.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start stale Run: %v", err)
	}
	if err := first.taskManager.Close(); err != nil {
		t.Fatalf("close first owner: %v", err)
	}

	second := &Dependencies{}
	if err := initializeRuntimeTaskOwner(second, root); err != nil {
		t.Fatalf("initialize restarted owner: %v", err)
	}
	t.Cleanup(func() {
		if err := second.taskManager.Close(); err != nil {
			t.Errorf("close restarted owner: %v", err)
		}
	})
	inner := &countingTaskExecutionRunner{}
	runner := &taskExecutionRunner{owner: second.taskManager, inner: inner}
	ctx := newTaskExecutionRunnerContext(t, task, oldRun, domaintool.ActorKindAgent, "shiro", true, true)

	response, err := runner.ExecuteV2(ctx, "fixture.tool", map[string]any{"fixture": "stale"})
	if err == nil {
		t.Fatalf("stale Run was accepted: response=%#v", response)
	}
	if response != nil || inner.executeCalls != 0 {
		t.Fatalf("stale Run reached inner: response=%#v calls=%d", response, inner.executeCalls)
	}
}

func TestTaskExecutionRunnerRejectsNilDependenciesBeforeInner(t *testing.T) {
	inner := &countingTaskExecutionRunner{}
	var nilRunner *taskExecutionRunner
	if response, err := nilRunner.ExecuteV2(context.Background(), "fixture.tool", nil); err == nil || response != nil {
		t.Fatalf("nil receiver result = response=%#v err=%v", response, err)
	}
	if response, err := (&taskExecutionRunner{inner: inner}).ExecuteV2(context.Background(), "fixture.tool", nil); err == nil || response != nil {
		t.Fatalf("nil owner result = response=%#v err=%v", response, err)
	}
	if inner.executeCalls != 0 {
		t.Fatalf("nil owner reached inner: calls=%d", inner.executeCalls)
	}

	deps, _, _ := newTaskExecutionRunnerFixture(t)
	if response, err := (&taskExecutionRunner{owner: deps.taskManager}).ExecuteV2(context.Background(), "fixture.tool", nil); err == nil || response != nil {
		t.Fatalf("nil inner result = response=%#v err=%v", response, err)
	}
	if inner.executeCalls != 0 {
		t.Fatalf("nil inner changed fake calls: %d", inner.executeCalls)
	}
}

func TestTaskExecutionRunnerListsMetadataWithoutExecutionIdentity(t *testing.T) {
	inner := &countingTaskExecutionRunner{}
	runner := &taskExecutionRunner{inner: inner}
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(metadata) != 1 || metadata[0].ToolID != "fixture.tool" || inner.listCalls != 1 {
		t.Fatalf("metadata=%#v listCalls=%d", metadata, inner.listCalls)
	}
	if _, err := (&taskExecutionRunner{}).ListTools(context.Background()); err == nil {
		t.Fatal("nil inner ListTools was accepted")
	}
}

func TestBuildToolMediationRecorderCannotSilentlyDisableAfterFailure(t *testing.T) {
	recorder, err := buildToolMediationRecorder(&config.Config{}, nil)
	if err == nil || recorder != nil {
		t.Fatal("missing canonical store did not fail closed")
	}
}

func TestToolRuntimeAdmitsOwnerBeforeMediationPersistence(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{WorkspaceDir: workspace}
	owner, valid := runtimeToolOwnerFixture(t, workspace, "shiro")
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	if runtime.ToolMediationRecorder == nil {
		t.Fatal("fixture recorder unavailable")
	}
	canceled, cancel := context.WithCancel(valid)
	cancel()
	for _, ctx := range []context.Context{context.Background(), canceled} {
		response, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(ctx, "file_read", map[string]any{"path": filepath.Join(workspace, "missing")})
		if err == nil && (response == nil || !response.IsError()) {
			t.Fatalf("unowned request accepted: %#v", response)
		}
		events, err := runtime.ToolMediationRecorder.ListRecent(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 0 {
			t.Fatalf("unadmitted request wrote %d mediation events", len(events))
		}
	}
	file := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(valid, "file_read", map[string]any{"path": file})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("valid owner failed: %#v %v", response, err)
	}
	events, err := runtime.ToolMediationRecorder.ListRecent(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("valid request not recorded: %d %v", len(events), err)
	}
}

func testCanonicalMediationStore(t *testing.T) *eventpersistence.SQLiteStore {
	t.Helper()
	store, err := eventpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
func buildToolRuntimeForTest(t *testing.T, cfg *config.Config) toolRuntime {
	t.Helper()
	return buildToolRuntimeWithCapabilities(nil, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
}

func TestToolRuntimeMediationViewerUsesCanonicalEvent(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{WorkspaceDir: workspace}
	owner, ctx := runtimeToolOwnerFixture(t, workspace, "shiro")
	store := testCanonicalMediationStore(t)
	runtime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, store)
	path := filepath.Join(workspace, "read.txt")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := runtime.WorkerRuntimeRunnerV2.ExecuteV2(ctx, "file_read", map[string]any{"path": path})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("tool failed: %#v %v", response, err)
	}
	events, err := store.ListByComponent(ctx, "tool_harness", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("canonical record missing: %d %v", len(events), err)
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	event := events[0]
	if event.TaskID != identity.TaskID || event.RunID != identity.RunID || event.TraceID != identity.TraceID || event.ActorID != "shiro" || event.EventSeq <= 0 {
		t.Fatalf("lineage mismatch: %#v", event)
	}
	request := httptest.NewRequest("GET", "/viewer/tool-harness/recent", nil)
	result := httptest.NewRecorder()
	viewer.HandleToolHarnessRecent(runtime.ToolMediationRecorder).ServeHTTP(result, request)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
		t.Fatalf("Viewer projection failed: %s", result.Body.String())
	}
	if body.Items[0]["event_id"] != string(event.EventID) || body.Items[0]["task_id"] != string(event.TaskID) || body.Items[0]["run_id"] != string(event.RunID) {
		t.Fatalf("Viewer disagrees with canonical event: %#v", body.Items)
	}
}
