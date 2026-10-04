package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildDurableStoreRuntimeFromCanonicalManifest(t *testing.T) {
	cfg := &config.Config{DurableStore: config.DurableStoreConfig{Enabled: true, ManifestPath: filepath.Join("..", "..", "config", "durable-stores.json")}, Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{DurableStoreWorkflow: filepath.Join(t.TempDir(), "workflow.db")}}}
	workflow, closer, err := buildDurableStoreRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	result, handled, err := workflow.Handle(context.Background(), appstore.Input{ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"), RequestedBy: "ren", Message: "XのBookmarkを保存するDBの設計を確認して"})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if result.Status != domainstore.StatusCompleted || result.Classification.StoreID != "core.conversation_l1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBuildDurableStoreRuntimeRejectsUnknownManifestFields(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "config", "durable-stores.json"))
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(canonical), `"module_id":`, `"unexpected": true, "module_id":`, 1)
	if _, err := decodeDurableStoreManifest([]byte(withUnknown)); err == nil {
		t.Fatal("unknown manifest field must be rejected")
	}
}

func TestBuildDurableStoreRuntimeUsesSelectedOwnerWithoutLocalFallback(t *testing.T) {
	root := t.TempDir()
	store := &runtimeDurableWorkflowStoreStub{}
	cfg := &config.Config{
		DurableStore: config.DurableStoreConfig{Enabled: true, ManifestPath: filepath.Join("..", "..", "config", "durable-stores.json")},
		Storage:      config.StorageConfig{Databases: config.DatabasePathsConfig{DurableStoreWorkflow: filepath.Join(root, "missing-parent", "workflow.db")}},
	}
	workflow, closer, err := buildDurableStoreRuntime(cfg, store)
	if err != nil {
		t.Fatalf("buildDurableStoreRuntime with selected remote Store: %v", err)
	}
	defer closer.Close()
	selected, ok := closer.(*runtimeBorrowedDurableWorkflowStore)
	if !ok || selected.Store != store {
		t.Fatalf("selected durable Store wrapper = %T, want borrowed injected owner", closer)
	}
	_, handled, err := workflow.Handle(context.Background(), appstore.Input{
		ActionID:    modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		RequestedBy: "agent:mio",
		UserScope:   "user:authenticated-test",
		Message:     "XのBookmarkを保存するDBの設計を確認して",
	})
	if err != nil || !handled {
		t.Fatalf("workflow.Handle handled=%v err=%v", handled, err)
	}
	if store.findActionCalls != 1 || store.findDedupeCalls != 1 || store.saveCalls != 1 {
		t.Fatalf("injected Store calls action/dedupe/save=%d/%d/%d, want 1/1/1", store.findActionCalls, store.findDedupeCalls, store.saveCalls)
	}
}

type runtimeDurableWorkflowStoreStub struct {
	findActionCalls int
	findDedupeCalls int
	saveCalls       int
}

func (store *runtimeDurableWorkflowStoreStub) FindByDedupeKey(context.Context, string) (*domainstore.WorkflowResult, error) {
	store.findDedupeCalls++
	return nil, nil
}

func (store *runtimeDurableWorkflowStoreStub) FindByActionID(context.Context, modulecore.ActionID) (*domainstore.RequestReceipt, error) {
	store.findActionCalls++
	return nil, nil
}

func (*runtimeDurableWorkflowStoreStub) FindByRequirementID(context.Context, string) (*domainstore.WorkflowResult, error) {
	return nil, nil
}

func (store *runtimeDurableWorkflowStoreStub) SaveWithReceipt(context.Context, *domainstore.WorkflowResult, domainstore.RequestReceipt) error {
	store.saveCalls++
	return nil
}
