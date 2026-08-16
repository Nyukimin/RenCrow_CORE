package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/chatgptimport"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestReconcileChatGPTImportStartupRemovesStagesBeforeRawAndBlocksActiveLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-source")
	store := startupTestStore(t)
	defer store.Close()

	if _, err := chatgptimport.CreateUploadStage(root, "stale-stage"); err != nil {
		t.Fatalf("CreateUploadStage: %v", err)
	}
	appendStartupLedgerEvent(t, store, "validating-request", "export-validating", domainmemory.ChatGPTImportStateValidating)
	appendStartupLedgerEvent(t, store, "committing-request", "export-committing", domainmemory.ChatGPTImportStateValidating)
	appendStartupLedgerEvent(t, store, "committing-request", "export-committing", domainmemory.ChatGPTImportStateCommitting)
	appendStartupLedgerEvent(t, store, "terminal-request", "export-terminal", domainmemory.ChatGPTImportStateValidating)
	appendStartupLedgerEvent(t, store, "terminal-request", "export-terminal", domainmemory.ChatGPTImportStateCommitting)
	appendStartupLedgerEvent(t, store, "terminal-request", "export-terminal", domainmemory.ChatGPTImportStateCompleted)

	result, err := reconcileChatGPTImportStartup(context.Background(), store, root)
	if err != nil {
		t.Fatalf("reconcileChatGPTImportStartup: %v", err)
	}
	if result.RemovedStages != 1 || result.BlockedImports != 2 {
		t.Fatalf("reconcile result=%+v, want one removed stage and two blocked imports", result)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, chatgptimport.UploadStagingDirectoryName)); readErr != nil || len(entries) != 0 {
		t.Fatalf("staging entries=%v err=%v, want empty staging namespace", entries, readErr)
	}
	if got := startupImportStatus(t, store, "export-validating"); got.State != domainmemory.ChatGPTImportStateBlocked {
		t.Fatalf("validating import state=%q, want blocked", got.State)
	}
	if got := startupImportStatus(t, store, "export-committing"); got.State != domainmemory.ChatGPTImportStateBlocked {
		t.Fatalf("committing import state=%q, want blocked", got.State)
	}
	terminal := startupImportStatus(t, store, "export-terminal")
	if terminal.State != domainmemory.ChatGPTImportStateCompleted || terminal.ErrorCode != "" || terminal.FailureReason != "" {
		t.Fatalf("terminal import changed=%+v, want completed without failure", terminal)
	}
}

func TestReconcileChatGPTImportStartupRejectsUnsafeStageBeforeRawOrLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-source")
	store := startupTestStore(t)
	defer store.Close()

	if _, err := chatgptimport.CreateUploadStage(root, "valid-stage"); err != nil {
		t.Fatalf("CreateUploadStage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, chatgptimport.UploadStagingDirectoryName, "unknown-entry"), []byte("unsafe"), 0o600); err != nil {
		t.Fatalf("write unknown staging entry: %v", err)
	}
	appendStartupLedgerEvent(t, store, "unsafe-stage-request", "export-unsafe-stage", domainmemory.ChatGPTImportStateValidating)

	_, err := reconcileChatGPTImportStartup(context.Background(), store, root)
	if err == nil {
		t.Fatal("unsafe staging entry unexpectedly reconciled")
	}
	if !errors.Is(err, chatgptimport.ErrUploadStageUnsafe) {
		t.Fatalf("unsafe stage error=%v, want ErrUploadStageUnsafe", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, chatgptimport.UploadStagingDirectoryName, "unknown-entry")); statErr != nil {
		t.Fatalf("unknown staging entry changed or disappeared: %v", statErr)
	}
	if got := startupImportStatus(t, store, "export-unsafe-stage"); got.State != domainmemory.ChatGPTImportStateValidating {
		t.Fatalf("ledger state=%q, want validating because raw and ledger steps must not run", got.State)
	}
}

func TestReconcileChatGPTImportStartupRejectsRawCorruptionAfterStageCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-source")
	store := startupTestStore(t)
	defer store.Close()

	if _, err := chatgptimport.CreateUploadStage(root, "stale-stage"); err != nil {
		t.Fatalf("CreateUploadStage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "unknown-root-entry"), []byte("unexpected"), 0o600); err != nil {
		t.Fatalf("write unknown raw root entry: %v", err)
	}
	appendStartupLedgerEvent(t, store, "raw-corruption-request", "export-raw-corruption", domainmemory.ChatGPTImportStateValidating)

	_, err := reconcileChatGPTImportStartup(context.Background(), store, root)
	if err == nil {
		t.Fatal("unknown Common Raw root entry unexpectedly reconciled")
	}
	if domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorObject {
		t.Fatalf("raw corruption error code=%q err=%v, want object", domainmemory.CommonRawErrorCodeOf(err), err)
	}
	if _, statErr := os.Stat(filepath.Join(root, chatgptimport.UploadStagingDirectoryName, "stale-stage")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale stage remains after stage reconciliation: %v", statErr)
	}
	if got := startupImportStatus(t, store, "export-raw-corruption"); got.State != domainmemory.ChatGPTImportStateValidating {
		t.Fatalf("ledger state=%q, want validating because ledger reconciliation must not run after raw failure", got.State)
	}
}

func TestReconcileChatGPTImportStartupMissingRootConvergesOnFirstObjectIntake(t *testing.T) {
	root := filepath.Join(t.TempDir(), "raw-source")
	store := startupTestStore(t)
	defer store.Close()

	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw root exists before startup reconcile: %v", err)
	}
	result, err := reconcileChatGPTImportStartup(context.Background(), store, root)
	if err != nil {
		t.Fatalf("reconcile missing root: %v", err)
	}
	if result != (chatGPTImportStartupReconcileResult{}) {
		t.Fatalf("missing root result=%+v, want zero counts", result)
	}

	content := bytes.Repeat([]byte("object"), (domainmemory.CommonRawMaxInlinePayloadSize/len("object"))+1)
	record := domainmemory.CommonRawRecord{
		SourceRecordID: "missing-root-record",
		Sensitivity:    domainmemory.CommonRawPrivateSensitivity,
		Role:           "user",
		ContentType:    "application/octet-stream",
		OccurredAt:     time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		Content:        content,
		ContentSHA256:  domainmemory.SHA256Hex(content),
		Provenance:     "startup-test",
		Rights:         "owner",
		License:        "private",
	}
	manifest := domainmemory.CommonRawManifest{
		ContractVersion:  domainmemory.CommonRawContractVersion,
		SourceType:       "startup-test",
		SourceIdentity:   "missing-root-export",
		SourceCount:      1,
		SchemaVersion:    "schema-1",
		ConverterVersion: "converter-1",
		Sensitivity:      domainmemory.CommonRawPrivateSensitivity,
		Rights:           "owner",
		License:          "private",
		Provenance:       "startup-test",
	}
	manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, []domainmemory.CommonRawRecord{record}, nil)
	if err != nil {
		t.Fatalf("CommonRawInputHash: %v", err)
	}
	requestID := "missing-root-intake"
	receipt, err := store.IntakeCommonRaw(
		startupToolContext(t, requestID, "owner-startup"),
		requestID,
		"owner-startup",
		"owner-startup",
		domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: []domainmemory.CommonRawRecord{record}},
	)
	if err != nil || receipt.Status != domainmemory.CommonRawStateCompleted {
		t.Fatalf("first object intake receipt=%+v err=%v, want completed", receipt, err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		t.Fatalf("missing root did not converge on object intake: info=%v err=%v", info, statErr)
	}
}

func startupTestStore(t *testing.T) *l1sqlite.L1SQLiteStore {
	t.Helper()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "conversation-l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	return store
}

func appendStartupLedgerEvent(t *testing.T, store *l1sqlite.L1SQLiteStore, requestID, exportID string, state domainmemory.ChatGPTImportState) domainmemory.ChatGPTImportEvent {
	t.Helper()
	input := domainmemory.ChatGPTImportEventInput{
		RequestID: requestID,
		OwnerID:   "owner-startup",
		ActorID:   "owner-startup",
		Binding:   startupTestBinding(exportID),
		State:     state,
		Counts: domainmemory.ChatGPTImportCounts{
			SourceCount: 1, FileCount: 1, ChunkCount: 1, MessageCount: 1, BatchCount: 1,
		},
	}
	event, err := store.AppendChatGPTImportEvent(startupToolContext(t, requestID, input.OwnerID), input)
	if err != nil {
		t.Fatalf("AppendChatGPTImportEvent(%s,%s): %v", requestID, state, err)
	}
	return event
}

func startupTestBinding(exportID string) domainmemory.ChatGPTImportBinding {
	return domainmemory.ChatGPTImportBinding{
		ExportID:          exportID,
		ManifestSHA256:    strings.Repeat("a", 64),
		ArtifactSHA256:    strings.Repeat("b", 64),
		ArtifactBytes:     1,
		Format:            domainmemory.ChatGPTImportBundleFormat,
		SchemaVersion:     domainmemory.ChatGPTImportRecordSchema,
		ConverterVersion:  domainmemory.ChatGPTImportConverterVersion,
		SourceFileCount:   1,
		SourceChunkCount:  1,
		SourceObjectCount: 0,
		MessageCount:      1,
	}
}

func startupImportStatus(t *testing.T, store *l1sqlite.L1SQLiteStore, exportID string) domainmemory.ChatGPTImportView {
	t.Helper()
	requestID := "status-" + exportID
	status, err := store.GetChatGPTImportStatus(startupToolContext(t, requestID, "owner-startup"), requestID, "owner-startup", "owner-startup", exportID)
	if err != nil {
		t.Fatalf("GetChatGPTImportStatus(%s): %v", exportID, err)
	}
	return status
}

func startupToolContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		ownerID,
		ownerID,
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
