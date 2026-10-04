package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	persistencestore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
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

func TestDurableStoreRuntimeRemoteOwnerReopensAndReplaysWithoutDuplicateRows(t *testing.T) {
	root := t.TempDir()
	storageHostDB := filepath.Join(root, "storage-host", "durable-store-workflow.db")
	journalDir := filepath.Join(root, "storage-host", "journal")
	executionHostDB := filepath.Join(root, "execution-host", "durable-store-workflow.db")
	manifestPath := filepath.Join("..", "..", "config", "durable-stores.json")
	cfg := &config.Config{
		DurableStore: config.DurableStoreConfig{Enabled: true, ManifestPath: manifestPath},
		Storage:      config.StorageConfig{Databases: config.DatabasePathsConfig{DurableStoreWorkflow: executionHostDB}},
	}
	const token = "durable-store-runtime-integration-token"
	actionID := modulecore.ActionID("act_00000000-0000-5000-8000-000000000101")
	userScope := "user:authenticated-101"
	message := "XのBookmarkを保存するDBの設計を確認して"

	owner1, handler1, server1, client1 := openDurableStoreRuntimeRemoteOwner(t, storageHostDB, journalDir, token)
	workflow1, closer1, err := buildDurableStoreRuntime(cfg, client1)
	if err != nil {
		t.Fatalf("buildDurableStoreRuntime(remote owner): %v", err)
	}
	writeRegistry1 := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDurableStoreWorkflow(writeRegistry1, workflow1); err != nil {
		t.Fatalf("register remote durable workflow data.write: %v", err)
	}
	worker1 := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry1, DisableToolHarness: true})
	requestContext := runtimeDurableWriteTestContext(t, string(actionID), userScope)
	receipt1 := runtimeDataWriteOwnerExecuteWrite(t, worker1, requestContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if receipt1.Owner != "durable_store_workflow" || receipt1.OwnerRoute != "durable_store_workflow/handle_storage_intent" || receipt1.ActorID != "shiro" || receipt1.DataScope != string(dataRecallAccessUser) || receipt1.AuditRef == "" || receipt1.IdempotentReplay || receipt1.IdempotencyKey != string(actionID) {
		t.Fatalf("remote data.write receipt lost route/scope/identity: owner=%q route=%q actor=%q scope=%q audit_ref=%q replay=%t", receipt1.Owner, receipt1.OwnerRoute, receipt1.ActorID, receipt1.DataScope, receipt1.AuditRef, receipt1.IdempotentReplay)
	}
	requirementID := receipt1.AuditRef
	result1, err := client1.FindByRequirementID(context.Background(), requirementID)
	if err != nil || result1 == nil || result1.Requirement.ActionID != actionID || result1.Requirement.UserScope != userScope || result1.Requirement.RequirementID != requirementID {
		t.Fatalf("remote result identity/content mismatch: requirement_id=%q err=%v", receipt1.AuditRef, err)
	}
	assertDurableStoreRuntimePersistence(t, storageHostDB, actionID, userScope, requirementID)
	assertDurableStoreRuntimeSingleRows(t, countDurableStoreRuntimeRows(t, storageHostDB), "after first write")
	actionReceipt1, err := client1.FindByActionID(context.Background(), actionID)
	if err != nil || actionReceipt1 == nil || actionReceipt1.ActionID != actionID || actionReceipt1.UserScope != userScope || actionReceipt1.RequirementID != result1.Requirement.RequirementID {
		t.Fatalf("remote action receipt lost identity: action_id=%q requirement_id=%q err=%v", actionID, requirementID, err)
	}
	if err := closer1.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}
	closeDurableStoreRuntimeRemoteOwner(t, owner1, handler1, server1)
	if _, err := os.Stat(executionHostDB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execution-host durable DB path stat=%v, want not-exist", err)
	}

	countsBefore := countDurableStoreRuntimeRows(t, storageHostDB)
	assertDurableStoreRuntimeSingleRows(t, countsBefore, "before replay after owner reopen")
	owner2, handler2, server2, client2 := openDurableStoreRuntimeRemoteOwner(t, storageHostDB, journalDir, token)
	workflow2, closer2, err := buildDurableStoreRuntime(cfg, client2)
	if err != nil {
		t.Fatalf("buildDurableStoreRuntime(remote owner after reopen): %v", err)
	}
	defer func() {
		_ = closer2.Close()
		closeDurableStoreRuntimeRemoteOwner(t, owner2, handler2, server2)
	}()
	writeRegistry2 := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDurableStoreWorkflow(writeRegistry2, workflow2); err != nil {
		t.Fatalf("register reopened durable workflow data.write: %v", err)
	}
	worker2 := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry2, DisableToolHarness: true})
	receipt2 := runtimeDataWriteOwnerExecuteWrite(t, worker2, requestContext, "durable_store_workflow", "handle_storage_intent", map[string]any{"message": message})
	if !receipt2.IdempotentReplay || receipt2.AuditRef != requirementID || receipt2.IdempotencyKey != string(actionID) {
		t.Fatalf("remote replay data.write receipt changed binding: audit_ref=%q replay=%t idempotency_key=%q", receipt2.AuditRef, receipt2.IdempotentReplay, receipt2.IdempotencyKey)
	}
	result2, err := client2.FindByRequirementID(context.Background(), receipt2.AuditRef)
	if err != nil || result2 == nil || result2.Requirement.ActionID != actionID || result2.Requirement.UserScope != userScope || result2.Requirement.RequirementID != result1.Requirement.RequirementID {
		t.Fatalf("remote replay result identity/content mismatch: requirement_id=%q err=%v", receipt2.AuditRef, err)
	}
	canonical1, canonical2 := result1, result2
	canonical1.Deduplicated, canonical1.RequestReplay = false, false
	canonical2.Deduplicated, canonical2.RequestReplay = false, false
	if !reflect.DeepEqual(canonical2, canonical1) {
		t.Fatalf("remote replay changed canonical result: before=%+v after=%+v", canonical1, canonical2)
	}
	actionReceipt2, err := client2.FindByActionID(context.Background(), actionID)
	if err != nil || actionReceipt2 == nil || *actionReceipt2 != *actionReceipt1 {
		t.Fatalf("remote replay changed action receipt: action_id=%q requirement_id=%q err=%v", actionID, requirementID, err)
	}
	countsAfter := countDurableStoreRuntimeRows(t, storageHostDB)
	assertDurableStoreRuntimeSingleRows(t, countsAfter, "after replay")
	if countsAfter != countsBefore {
		t.Fatalf("remote replay changed canonical/receipt rows: before=%+v after=%+v", countsBefore, countsAfter)
	}
	assertDurableStoreRuntimePersistence(t, storageHostDB, actionID, userScope, requirementID)
}

type durableStoreRuntimeRowCounts struct {
	Workflow, ActionReceipt, StorageHostReceipt int
}

func countDurableStoreRuntimeRows(t *testing.T, path string) durableStoreRuntimeRowCounts {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open row-count database: %v", err)
	}
	defer db.Close()
	var counts durableStoreRuntimeRowCounts
	for _, query := range []struct {
		dest *int
		sql  string
	}{
		{&counts.Workflow, "SELECT COUNT(*) FROM durable_store_workflow"},
		{&counts.ActionReceipt, "SELECT COUNT(*) FROM durable_store_workflow_receipt"},
		{&counts.StorageHostReceipt, "SELECT COUNT(*) FROM durable_store_workflow_storagehost_receipt"},
	} {
		if err := db.QueryRow(query.sql).Scan(query.dest); err != nil {
			t.Fatalf("count %q: %v", query.sql, err)
		}
	}
	return counts
}

func assertDurableStoreRuntimeSingleRows(t *testing.T, counts durableStoreRuntimeRowCounts, phase string) {
	t.Helper()
	want := durableStoreRuntimeRowCounts{Workflow: 1, ActionReceipt: 1, StorageHostReceipt: 1}
	if counts != want {
		t.Fatalf("durable workflow row counts %s = %+v, want exactly one canonical, action-receipt, and storage-host receipt row", phase, counts)
	}
}

func assertDurableStoreRuntimePersistence(t *testing.T, path string, actionID modulecore.ActionID, userScope, requirementID string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open persistence assertion database: %v", err)
	}
	defer db.Close()

	var workflowRequirementID, workflowPayload string
	if err := db.QueryRow(`SELECT requirement_id, payload FROM durable_store_workflow WHERE requirement_id = ?`, requirementID).Scan(&workflowRequirementID, &workflowPayload); err != nil {
		t.Fatalf("read durable workflow canonical row: %v", err)
	}
	var persisted domainstore.WorkflowResult
	if err := json.Unmarshal([]byte(workflowPayload), &persisted); err != nil {
		t.Fatalf("decode durable workflow canonical row: %v", err)
	}
	if workflowRequirementID != requirementID || persisted.Requirement.RequirementID != requirementID || persisted.Requirement.ActionID != actionID || persisted.Requirement.UserScope != userScope || persisted.Requirement.DedupeKey == "" || persisted.Classification.StoreID != "core.conversation_l1" {
		t.Fatalf("durable workflow canonical identity/content mismatch: row_requirement_id=%q requirement_id=%q action_id=%q user_scope=%q", workflowRequirementID, persisted.Requirement.RequirementID, persisted.Requirement.ActionID, persisted.Requirement.UserScope)
	}

	wantPayloadHash := domainstore.HashStorageRequirement(persisted.Requirement)
	var receiptActionID, receiptUserScope, receiptPayloadHash, receiptRequirementID string
	if err := db.QueryRow(`SELECT action_id, user_scope, payload_hash, requirement_id FROM durable_store_workflow_receipt WHERE action_id = ?`, string(actionID)).Scan(&receiptActionID, &receiptUserScope, &receiptPayloadHash, &receiptRequirementID); err != nil {
		t.Fatalf("read durable workflow action receipt: %v", err)
	}
	if receiptActionID != string(actionID) || receiptUserScope != userScope || receiptPayloadHash != wantPayloadHash || receiptRequirementID != requirementID {
		t.Fatalf("durable workflow action receipt binding mismatch: action_id=%q user_scope=%q requirement_id=%q", receiptActionID, receiptUserScope, receiptRequirementID)
	}

	var opID, opPayloadHash, opActionID, opRequirementID, effectHash string
	var writerGeneration int64
	if err := db.QueryRow(`SELECT op_id, payload_sha256, writer_generation, action_id, requirement_id, effect_sha256 FROM durable_store_workflow_storagehost_receipt WHERE action_id = ? AND requirement_id = ?`, string(actionID), requirementID).Scan(&opID, &opPayloadHash, &writerGeneration, &opActionID, &opRequirementID, &effectHash); err != nil {
		t.Fatalf("read durable workflow storage-host receipt: %v", err)
	}
	if opID == "" || len(opPayloadHash) != 64 || writerGeneration <= 0 || opActionID != string(actionID) || opRequirementID != requirementID || len(effectHash) != 64 {
		t.Fatalf("durable workflow storage-host receipt binding mismatch: op_id=%q writer_generation=%d action_id=%q requirement_id=%q", opID, writerGeneration, opActionID, opRequirementID)
	}
}

func openDurableStoreRuntimeRemoteOwner(t *testing.T, dbPath, journalDir, token string) (*persistencestore.SQLiteStore, *storagehost.Handler, *httptest.Server, *storagehost.DurableStoreWorkflowClient) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("create durable owner parent: %v", err)
	}
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatalf("create storage host journal parent: %v", err)
	}
	owner, err := persistencestore.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open durable owner: %v", err)
	}
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: token, JournalDir: journalDir})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("open storage host handler: %v", err)
	}
	if err := storagehost.RegisterDurableStoreWorkflowGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("register durable workflow group: %v", err)
	}
	server := httptest.NewServer(handler)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: token, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("new storage host client: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("storage host handshake: %v", err)
	}
	return owner, handler, server, storagehost.NewDurableStoreWorkflowClient(client)
}

func closeDurableStoreRuntimeRemoteOwner(t *testing.T, owner *persistencestore.SQLiteStore, handler *storagehost.Handler, server *httptest.Server) {
	t.Helper()
	server.Close()
	if err := handler.Close(); err != nil {
		t.Errorf("close storage host handler: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Errorf("close durable owner: %v", err)
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
