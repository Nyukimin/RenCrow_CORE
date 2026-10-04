package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	toolregistrypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildRuntimeToolRegistryUsesSelectedRemoteOwnerWithoutLocalFallback(t *testing.T) {
	root := t.TempDir()
	owner, err := toolregistrypersistence.NewSQLiteToolRegistryStore(filepath.Join(root, "host-registry.db"))
	if err != nil {
		t.Fatalf("open host ToolRegistry owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-tool-registry-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if err := storagehost.RegisterToolRegistryGroup(handler, owner); err != nil {
		t.Fatalf("register ToolRegistry group: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-tool-registry-test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("ToolRegistry client handshake: %v", err)
	}
	remote := storagehost.NewToolRegistryClient(client)
	localPath := filepath.Join(root, "missing-parent", "local-registry.db")
	cfg := &config.Config{Capability: config.CapabilityConfig{ToolRegistryDB: localPath}}
	selected := buildRuntimeToolRegistry(cfg, remote)
	if selected != remote {
		t.Fatalf("selected ToolRegistry=%T, want shared remote client", selected)
	}
	entry := capdomain.ToolEntry{
		Name: "runtime_remote_registry", Description: "remote registry test",
		SchemaJSON: `{"type":"object","properties":{}}`, Platforms: []string{"linux"},
		Source: capdomain.ToolSourceBuiltin, CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), CreatedBy: "builtin",
	}
	if err := selected.Register(context.Background(), entry); err != nil {
		t.Fatalf("selected ToolRegistry.Register: %v", err)
	}
	items, err := selected.ListForPlatform(context.Background(), "linux")
	if err != nil || len(items) != 1 || items[0].Name != entry.Name {
		t.Fatalf("selected ToolRegistry.ListForPlatform=%+v err=%v", items, err)
	}
	toolsDir := filepath.Join(root, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRuntimeToolRegistryScript(t, toolsDir, "remote_script")
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteToolRegistry(writeRegistry, root, selected); err != nil {
		t.Fatalf("register remote ToolRegistry data.write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallToolRegistry(recallRegistry, selected); err != nil {
		t.Fatalf("register remote ToolRegistry data.recall: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	actionID := modulecore.ActionID("act_00000000-0000-5000-8000-000000000021")
	ctx := runtimeToolRegistryOwnerContext(t, string(actionID), "mio")
	writeResult := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "tool_registry", "register_existing_script", map[string]any{
		"name": "remote_script", "description": "remote write",
		"schema_json": `{"type":"object","properties":{}}`, "platforms": []any{"linux"},
	})
	if writeResult.OwnerRoute != "tool_registry/register_existing_script" || writeResult.IdempotentReplay {
		t.Fatalf("remote ToolRegistry data.write result=%+v", writeResult)
	}
	recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "tool_registry", "tool", "remote_script")
	if len(recalled.Records) != 1 || recalled.Records[0]["name"] != "remote_script" {
		t.Fatalf("remote ToolRegistry data.recall result=%+v", recalled)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote runtime opened local ToolRegistry path %q (stat err=%v)", localPath, err)
	}
}
