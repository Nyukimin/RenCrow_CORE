package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolregistrypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteToolRegistryOwnerThroughWorkerAndExactRecall(t *testing.T) {
	workspace := t.TempDir()
	toolsDir := filepath.Join(workspace, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatalf("create tools directory: %v", err)
	}
	writeRuntimeToolRegistryScript(t, toolsDir, "report_tool")

	dbPath := filepath.Join(t.TempDir(), "tool-registry.db")
	store, err := toolregistrypersistence.NewSQLiteToolRegistryStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteToolRegistry(writeRegistry, workspace, store); err != nil {
		t.Fatalf("register write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallToolRegistry(recallRegistry, store); err != nil {
		t.Fatalf("register recall: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	payload := map[string]any{
		"name": " report_tool ", "description": "  Generate a report  ",
		"schema_json": "{\"type\": \"object\", \"properties\": {}}",
		"platforms":   []any{"windows", "linux"},
	}
	ctx := runtimeToolRegistryOwnerContext(t, "tool-owner-1", "mio")
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "tool_registry", "register_existing_script", payload)
	if first.IdempotentReplay || first.SchemaVersion != "tool-registry/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.OwnerRoute != "tool_registry/register_existing_script" || first.AuditRef != "tool-owner-1" || first.IdempotencyKey != "tool-owner-1" {
		t.Fatalf("first receipt = %#v", first)
	}
	entry, err := store.Get(ctx, "report_tool")
	if err != nil {
		t.Fatalf("Get registered tool: %v", err)
	}
	canonicalScript, err := filepath.Abs(filepath.Join(workspace, "tools", "report_tool.sh"))
	if err != nil || entry.Source != capdomain.ToolSource(canonicalScript) || entry.CreatedBy != "mio" || entry.SchemaJSON != `{"properties":{},"type":"object"}` || len(entry.Platforms) != 2 || entry.Platforms[0] != "linux" || entry.Platforms[1] != "windows" {
		t.Fatalf("registered entry = %+v canonical=%s err=%v", entry, canonicalScript, err)
	}

	exact := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "tool_registry", "tool", "report_tool")
	if len(exact.Records) != 1 || exact.Records[0]["name"] != "report_tool" || exact.Evidence.OwnerRoute != "tool_registry/tool" {
		t.Fatalf("exact tool recall = %#v", exact)
	}
	listResponse, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "tool_registry", "operation": "list_tools", "query": "linux", "limit": 1,
	})
	if err != nil || listResponse == nil || listResponse.IsError() {
		t.Fatalf("list tools response=%#v err=%v", listResponse, err)
	}
	list, ok := listResponse.Result.(runtimeDataRecallResult)
	if !ok || len(list.Records) != 1 || list.Records[0]["name"] != "report_tool" {
		t.Fatalf("list tools = %#v", listResponse.Result)
	}
	receiptRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "tool_registry", "requests", first.AuditRef)
	if len(receiptRecall.Records) != 1 || receiptRecall.Records[0]["request_id"] != first.AuditRef || receiptRecall.Records[0]["actor_id"] != "mio" || receiptRecall.Records[0]["tool_name"] != "report_tool" || receiptRecall.Records[0]["payload_hash"] == "" {
		t.Fatalf("receipt recall = %#v", receiptRecall)
	}

	// Canonical JSON and sorted platforms make equivalent payload formatting a
	// true exact-request replay rather than a new mutation.
	replayPayload := map[string]any{
		"name": "report_tool", "description": "Generate a report",
		"schema_json": `{"properties": {}, "type":"object"}`, "platforms": []any{"linux", "windows"},
	}
	replay := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "tool_registry", "register_existing_script", replayPayload)
	if !replay.IdempotentReplay || replay.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt = %#v first=%#v", replay, first)
	}
}

func TestRuntimeDataWriteToolRegistryOwnerReopenConflictAndSemanticDedupe(t *testing.T) {
	workspace := t.TempDir()
	toolsDir := filepath.Join(workspace, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatalf("create tools directory: %v", err)
	}
	writeRuntimeToolRegistryScript(t, toolsDir, "reopen_tool")
	dbPath := filepath.Join(t.TempDir(), "tool-registry.db")
	store, err := toolregistrypersistence.NewSQLiteToolRegistryStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataWriteToolRegistry(writeRegistry, workspace, store); err != nil {
		t.Fatal(err)
	}
	if err := registerRuntimeDataRecallToolRegistry(recallRegistry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true})
	payload := runtimeToolRegistryPayload("reopen_tool")
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, runtimeToolRegistryOwnerContext(t, "reopen-1", "shiro"), "tool_registry", "register_existing_script", payload)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := toolregistrypersistence.NewSQLiteToolRegistryStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	writeRegistry = newRuntimeDataWriteRegistry()
	recallRegistry = newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataWriteToolRegistry(writeRegistry, workspace, reopened); err != nil {
		t.Fatal(err)
	}
	if err := registerRuntimeDataRecallToolRegistry(recallRegistry, reopened); err != nil {
		t.Fatal(err)
	}
	worker = toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true})
	replay := runtimeDataWriteOwnerExecuteWrite(t, worker, runtimeToolRegistryOwnerContext(t, "reopen-1", "shiro"), "tool_registry", "register_existing_script", payload)
	if !replay.IdempotentReplay || replay.AuditRef != first.AuditRef {
		t.Fatalf("reopened replay = %#v first=%#v", replay, first)
	}
	changed := runtimeToolRegistryPayload("reopen_tool")
	changed["description"] = "changed"
	if response, err := worker.ExecuteV2(runtimeToolRegistryOwnerContext(t, "reopen-1", "shiro"), "data.write", map[string]any{"store": "tool_registry", "operation": "register_existing_script", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request payload conflict response=%#v err=%v", response, err)
	}
	if response, err := worker.ExecuteV2(runtimeToolRegistryOwnerContext(t, "reopen-1", "mio"), "data.write", map[string]any{"store": "tool_registry", "operation": "register_existing_script", "payload": payload}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("same-request actor conflict response=%#v err=%v", response, err)
	}
	semantic := runtimeDataWriteOwnerExecuteWrite(t, worker, runtimeToolRegistryOwnerContext(t, "reopen-2", "mio"), "tool_registry", "register_existing_script", payload)
	if semantic.IdempotentReplay || semantic.AuditRef != "reopen-2" {
		t.Fatalf("semantic dedupe receipt = %#v", semantic)
	}
	if entries, err := reopened.ListForPlatform(context.Background(), "linux"); err != nil || len(entries) != 1 {
		t.Fatalf("semantic dedupe mutated tools = %#v err=%v", entries, err)
	}
	if receipt, found, err := reopened.FindRequestReceipt(context.Background(), "reopen-2"); err != nil || !found || receipt.ToolName != "reopen_tool" {
		t.Fatalf("semantic receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestRuntimeDataWriteToolRegistryOwnerRejectsUnsafeScriptPaths(t *testing.T) {
	workspace := t.TempDir()
	toolsDir := filepath.Join(workspace, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatalf("create tools directory: %v", err)
	}
	writeRuntimeToolRegistryScript(t, toolsDir, "safe_tool")
	if _, err := resolveRuntimeToolRegistryScriptPath(workspace, "../escape"); err == nil {
		t.Fatal("traversal name was accepted")
	}
	if _, err := resolveRuntimeToolRegistryScriptPath(workspace, "missing_tool"); err == nil {
		t.Fatal("missing script was accepted")
	}
	if err := os.Mkdir(filepath.Join(toolsDir, "directory_tool.sh"), 0755); err != nil {
		t.Fatalf("create directory script: %v", err)
	}
	if _, err := resolveRuntimeToolRegistryScriptPath(workspace, "directory_tool"); err == nil {
		t.Fatal("directory script was accepted")
	}
	target := filepath.Join(toolsDir, "safe_tool.sh")
	symlink := filepath.Join(toolsDir, "symlink_tool.sh")
	if err := os.Symlink(target, symlink); err != nil {
		if errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "not supported") {
			t.Skipf("symlink unsupported: %v", err)
		}
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := resolveRuntimeToolRegistryScriptPath(workspace, "symlink_tool"); err == nil {
		t.Fatal("symlink script was accepted")
	}
}

func writeRuntimeToolRegistryScript(t *testing.T, toolsDir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(toolsDir, name+".sh"), []byte("#!/bin/sh\nprintf '%s' ok\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

func runtimeToolRegistryPayload(name string) map[string]any {
	return map[string]any{
		"name": name, "description": "A reusable tool", "schema_json": `{"type":"object"}`, "platforms": []any{"linux"},
	}
}

func runtimeToolRegistryOwnerContext(t *testing.T, requestID, actorID string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID,
		AllowedDataScopes: []string{domaintool.DataScopeInternal}, AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole: "worker", Purpose: "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("tool registry scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

var _ capdomain.ToolRegistry = (*toolregistrypersistence.SQLiteToolRegistryStore)(nil)
