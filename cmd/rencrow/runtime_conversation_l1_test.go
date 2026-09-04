package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type countingConversationCloser struct{ calls int }

func (c *countingConversationCloser) Close() error {
	c.calls++
	return nil
}

func TestConversationRuntimeL1SelectsL1AsPrimaryCloser(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage:      config.StorageConfig{Databases: config.DatabasePathsConfig{ConversationL1: filepath.Join(t.TempDir(), "l1.db")}},
	}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.Closer == nil {
		t.Fatal("L1 runtime primary closer is nil")
	}
	l1Closer, ok := runtime.Closer.(*l1sqlite.L1SQLiteStore)
	if !ok || l1Closer != runtime.L1Store {
		t.Fatalf("primary closer=%T %p, want runtime L1 store %p", runtime.Closer, l1Closer, runtime.L1Store)
	}
	if err := runtime.Closer.Close(); err != nil {
		t.Fatalf("primary closer failed: %v", err)
	}
}

func TestConversationRuntimeL1KeepsArchiveAsIndependentCloser(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			ConversationL1:      filepath.Join(dir, "l1.db"),
			ConversationArchive: filepath.Join(dir, "archive.db"),
		}},
	}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.ArchiveStore == nil || runtime.ArchiveCloser != runtime.ArchiveStore {
		t.Fatalf("L1 archive closer=%p store=%p, want independent route archive", runtime.ArchiveCloser, runtime.ArchiveStore)
	}
	if err := runtime.ArchiveCloser.Close(); err != nil {
		t.Fatalf("archive closer failed: %v", err)
	}
	if err := runtime.Closer.Close(); err != nil {
		t.Fatalf("L1 primary closer failed: %v", err)
	}
}

func TestDependenciesShutdownClosesConversationOwnersOnce(t *testing.T) {
	primary := &countingConversationCloser{}
	archive := &countingConversationCloser{}
	deps := &Dependencies{conversationCloser: primary, conversationArchiveCloser: archive}
	deps.Shutdown()
	if primary.calls != 1 || archive.calls != 1 {
		t.Fatalf("conversation closer calls primary=%d archive=%d, want one each", primary.calls, archive.calls)
	}
}

func TestConversationRuntimeArchiveCloserIsOwnedByAdvancedManager(t *testing.T) {
	archive := &archivesqlite.ArchiveSQLiteStore{}
	if got := conversationRuntimeArchiveCloser(&conversationpersistence.RealConversationManager{}, archive); got != nil {
		t.Fatalf("advanced archive closer=%T, want nil because primary manager owns archive shutdown", got)
	}
	if got := conversationRuntimeArchiveCloser(nil, archive); got != archive {
		t.Fatalf("L1-only archive closer=%p, want route archive=%p", got, archive)
	}
}

func TestDependenciesShutdownHonorsConversationArchiveOwnership(t *testing.T) {
	advancedPrimary := &countingConversationCloser{}
	advancedArchive := &countingConversationCloser{}
	advanced := conversationRuntime{Closer: advancedPrimary, ArchiveCloser: nil}
	advancedDeps := &Dependencies{conversationCloser: advanced.Closer, conversationArchiveCloser: advanced.ArchiveCloser}
	advancedDeps.Shutdown()
	if advancedPrimary.calls != 1 || advancedArchive.calls != 0 {
		t.Fatalf("advanced shutdown calls primary=%d independent archive=%d, want 1/0", advancedPrimary.calls, advancedArchive.calls)
	}

	l1Primary := &countingConversationCloser{}
	l1Archive := &countingConversationCloser{}
	l1 := conversationRuntime{Closer: l1Primary, ArchiveCloser: l1Archive}
	l1Deps := &Dependencies{conversationCloser: l1.Closer, conversationArchiveCloser: l1.ArchiveCloser}
	l1Deps.Shutdown()
	if l1Primary.calls != 1 || l1Archive.calls != 1 {
		t.Fatalf("L1 shutdown calls primary=%d independent archive=%d, want 1/1", l1Primary.calls, l1Archive.calls)
	}
}

func TestBuildConversationRuntimeUsesL1ConversationEngineWithoutAdvancedRuntime(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1: filepath.Join(t.TempDir(), "l1.db"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil {
		t.Fatal("L1Store is nil; configured Viewer read store must not depend on Conversation engine")
	}
	defer runtime.L1Store.Close()
	if runtime.Engine == nil {
		t.Fatal("L1 conversation engine is nil; shared Agent context must not depend on advanced conversation runtime")
	}
	if runtime.Manager != nil {
		t.Fatalf("advanced conversation manager unexpectedly enabled: %v", runtime.Manager)
	}
}

func TestConversationRuntimeUserIDUsesConfiguredOwnerWithRenFallback(t *testing.T) {
	if got := conversationRuntimeUserID(&config.Config{LocalAgentOps: config.LocalAgentOpsConfig{UserID: "owner-42"}}); got != "owner-42" {
		t.Fatalf("configured owner id=%q, want owner-42", got)
	}
	if got := conversationRuntimeUserID(&config.Config{}); got != "ren" {
		t.Fatalf("fallback owner id=%q, want ren", got)
	}
}

func TestConversationRuntimeOwnerRecallTraceUsesConfiguredOwnerAndRealL1(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		LocalAgentOps: config.LocalAgentOpsConfig{
			UserID: "owner-42",
		},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1: filepath.Join(t.TempDir(), "l1.db"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil || runtime.Engine == nil {
		t.Fatal("configured runtime must expose the real L1 store and conversation engine")
	}
	defer runtime.L1Store.Close()

	ctx := context.Background()
	proposed, err := runtime.L1Store.OwnerProposeUserMemory(
		ctx,
		"runtime-owner-42-propose",
		"owner-42",
		"owner-42",
		domainmemory.UserMemoryTypePreference,
		"Ren prefers blue",
		"runtime recall E2E fixture",
	)
	if err != nil {
		t.Fatalf("OwnerProposeUserMemory failed: %v", err)
	}
	confirmed, err := runtime.L1Store.OwnerTransitionUserMemory(
		ctx,
		"runtime-owner-42-confirm",
		"owner-42",
		"owner-42",
		proposed.Item.ID,
		domainmemory.UserMemoryOwnerOperationConfirm,
		"",
		"runtime recall E2E confirm",
	)
	if err != nil {
		t.Fatalf("OwnerTransitionUserMemory(confirm) failed: %v", err)
	}

	sessionID := string(modulecore.NewSessionID())
	pack, err := runtime.Engine.BeginTurn(ctx, sessionID, "blue")
	if err != nil {
		t.Fatalf("real L1 ConversationEngine BeginTurn failed: %v", err)
	}
	selected := false
	for _, decision := range pack.UserMemoryRecallDecisions {
		if decision.Item.ID == confirmed.Item.ID {
			selected = decision.Selected && decision.Status == domainmemory.UserMemoryRecallStatusInjected
			break
		}
	}
	if !selected {
		t.Fatalf("confirmed memory was not selected by runtime recall: %+v", pack.UserMemoryRecallDecisions)
	}
	if len(pack.UserProfile.Facts) == 0 || pack.UserProfile.Facts[0] != confirmed.Item.Statement {
		t.Fatalf("selected memory was not injected into UserProfile: %+v", pack.UserProfile.Facts)
	}
	typed, ok := runtime.Engine.(interface {
		CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
	})
	if !ok {
		t.Fatal("runtime engine does not expose typed conversation commit")
	}
	if _, err := typed.CommitConversationTurn(ctx, domconv.ConversationTurnRequest{
		TurnID: string(modulecore.NewTurnID()), SessionID: sessionID, OwnerID: "owner-42", UserMessage: "blue", AgentMessage: "了解", AgentSpeaker: domconv.SpeakerMio,
		RecallTraceItems: pack.ToTraceItems(),
	}); err != nil {
		t.Fatalf("typed conversation commit failed: %v", err)
	}

	readScope, err := domaintool.NewToolExecutionScope(
		"runtime-owner-42-trace-read",
		domaintool.ActorKindUser,
		"owner-42",
		"owner-42",
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("owner trace read scope failed: %v", err)
	}
	readCtx := domaintool.WithToolExecutionScope(ctx, readScope)
	traces, err := runtime.L1Store.OwnerListRecallTraces(readCtx, "owner-42", 20)
	if err != nil {
		t.Fatalf("OwnerListRecallTraces failed: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("owner-42 trace count=%d, want 1: %+v", len(traces), traces)
	}
	trace := traces[0]
	if trace.Status != "completed" || trace.TotalCandidates < 1 || trace.InjectedCount < 1 {
		t.Fatalf("owner-42 trace summary=%+v, want completed with an injected candidate", trace)
	}

	detail, err := runtime.L1Store.OwnerFindRecallTrace(readCtx, "owner-42", trace.ID)
	if err != nil {
		t.Fatalf("OwnerFindRecallTrace failed: %v", err)
	}
	foundInjected := false
	for _, item := range detail.Items {
		if item.MemoryID == confirmed.Item.ID {
			foundInjected = item.Status == domconv.TraceStatusInjected && item.Summary == confirmed.Item.Statement
			break
		}
	}
	if !foundInjected {
		t.Fatalf("owner-42 trace does not contain the injected confirmed memory: %+v", detail.Items)
	}
}

func TestBuildConversationRuntimeConfiguresParquetExportRootAndDelegates(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		LocalAgentOps: config.LocalAgentOpsConfig{
			UserID: "owner-42",
		},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1:      filepath.Join(dir, "l1.db"),
				ConversationArchive: filepath.Join(dir, "archive.db"),
			},
			Memory: config.MemoryStorageConfig{
				ColdExportDir: filepath.Join(dir, "parquet-exports"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil || runtime.ArchiveStore == nil {
		t.Fatal("configured runtime must expose L1 and archive stores")
	}
	defer runtime.L1Store.Close()
	defer runtime.ArchiveStore.Close()

	exportRequestID := "runtime-parquet-export"
	exportScope, err := domaintool.NewToolExecutionScope(
		exportRequestID,
		domaintool.ActorKindUser,
		"owner-42",
		"owner-42",
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("export scope: %v", err)
	}
	export, err := runtime.L1Store.OwnerExportConversationArchiveParquet(
		domaintool.WithToolExecutionScope(context.Background(), exportScope),
		exportRequestID,
		"owner-42",
		"owner-42",
	)
	if err != nil {
		t.Fatalf("configured runtime parquet export failed: %v", err)
	}
	if export.ExportID != exportRequestID || export.Receipt.Operation != domainmemory.UserMemoryOwnerOperationParquetExport {
		t.Fatalf("export result=%+v, want configured-root typed export", export)
	}

	verifyRequestID := "runtime-parquet-verify"
	verifyScope, err := domaintool.NewToolExecutionScope(
		verifyRequestID,
		domaintool.ActorKindUser,
		"owner-42",
		"owner-42",
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("verify scope: %v", err)
	}
	verified, err := runtime.L1Store.OwnerVerifyConversationArchiveParquet(
		domaintool.WithToolExecutionScope(context.Background(), verifyScope),
		verifyRequestID,
		"owner-42",
		"owner-42",
		exportRequestID,
	)
	if err != nil {
		t.Fatalf("configured runtime parquet verify failed: %v", err)
	}
	if verified.ExportID != exportRequestID || verified.Receipt.RequestID != verifyRequestID || verified.Receipt.AuditReference != exportRequestID {
		t.Fatalf("verify result=%+v, want target-bound configured-root verify", verified)
	}
}

func TestBuildConversationRuntimeLeavesParquetUnavailableWithoutColdExportRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		LocalAgentOps: config.LocalAgentOpsConfig{
			UserID: "owner-42",
		},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{
				ConversationL1:      filepath.Join(dir, "l1.db"),
				ConversationArchive: filepath.Join(dir, "archive.db"),
			},
		},
	}

	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
	if runtime.L1Store == nil || runtime.ArchiveStore == nil {
		t.Fatal("runtime with archive path must expose both stores")
	}
	defer runtime.L1Store.Close()
	defer runtime.ArchiveStore.Close()
	scope, err := domaintool.NewToolExecutionScope(
		"runtime-parquet-no-root",
		domaintool.ActorKindUser,
		"owner-42",
		"owner-42",
		[]string{domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	_, err = runtime.L1Store.OwnerExportConversationArchiveParquet(
		domaintool.WithToolExecutionScope(context.Background(), scope),
		"runtime-parquet-no-root",
		"owner-42",
		"owner-42",
	)
	if !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("empty cold export root error=%v, want unavailable", err)
	}
}

func TestBuildConversationRuntimeConfiguresCommonRawSourceRootAndFailsClosedWhenUnset(t *testing.T) {
	t.Run("configured-root", func(t *testing.T) {
		dir := t.TempDir()
		rawRoot := filepath.Join(dir, "raw-source")
		cfg := &config.Config{
			Conversation:  config.ConversationConfig{Enabled: false},
			LocalAgentOps: config.LocalAgentOpsConfig{UserID: "owner-42"},
			Storage: config.StorageConfig{
				Databases: config.DatabasePathsConfig{ConversationL1: filepath.Join(dir, "l1.db")},
				Memory:    config.MemoryStorageConfig{RawSourceDir: rawRoot},
			},
		}
		runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
		if runtime.L1Store == nil {
			t.Fatal("configured runtime must expose L1 store")
		}
		defer runtime.L1Store.Close()
		requestID := "runtime-common-raw-configured"
		scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, "owner-42", "owner-42", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
		if err != nil {
			t.Fatal(err)
		}
		content := bytesOfRuntimeSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'r')
		record := domainmemory.CommonRawRecord{SourceRecordID: "runtime-object", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "runtime-test", Rights: "owner", License: "private"}
		manifest := domainmemory.CommonRawManifest{ContractVersion: domainmemory.CommonRawContractVersion, SourceType: "runtime-test", SourceIdentity: "runtime-export", SourceCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "runtime-test"}
		manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, []domainmemory.CommonRawRecord{record}, nil)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := runtime.L1Store.IntakeCommonRaw(domaintool.WithToolExecutionScope(context.Background(), scope), requestID, "owner-42", "owner-42", domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: []domainmemory.CommonRawRecord{record}})
		if err != nil || receipt.Status != domainmemory.CommonRawStateCompleted || receipt.Records[0].StorageKind != domainmemory.CommonRawStorageObject {
			t.Fatalf("configured raw intake receipt=%+v err=%v", receipt, err)
		}
		if _, err := os.Stat(filepath.Join(rawRoot, filepath.FromSlash(receipt.Records[0].ObjectRef))); err != nil {
			t.Fatalf("configured raw object missing: %v", err)
		}
	})

	t.Run("unset-root", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			Conversation:  config.ConversationConfig{Enabled: false},
			LocalAgentOps: config.LocalAgentOpsConfig{UserID: "owner-42"},
			Storage:       config.StorageConfig{Databases: config.DatabasePathsConfig{ConversationL1: filepath.Join(dir, "l1.db")}},
		}
		runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil)
		if runtime.L1Store == nil {
			t.Fatal("runtime must expose L1 store")
		}
		defer runtime.L1Store.Close()
		requestID := "runtime-common-raw-unset"
		scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, "owner-42", "owner-42", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
		if err != nil {
			t.Fatal(err)
		}
		content := bytesOfRuntimeSize(domainmemory.CommonRawMaxInlinePayloadSize+1, 'u')
		record := domainmemory.CommonRawRecord{SourceRecordID: "runtime-unset", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "user", ContentType: "application/octet-stream", OccurredAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), Content: content, ContentSHA256: domainmemory.SHA256Hex(content), Provenance: "runtime-test", Rights: "owner", License: "private"}
		manifest := domainmemory.CommonRawManifest{ContractVersion: domainmemory.CommonRawContractVersion, SourceType: "runtime-test", SourceIdentity: "runtime-unset-export", SourceCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1", Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "runtime-test"}
		manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, []domainmemory.CommonRawRecord{record}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.L1Store.IntakeCommonRaw(domaintool.WithToolExecutionScope(context.Background(), scope), requestID, "owner-42", "owner-42", domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: []domainmemory.CommonRawRecord{record}})
		code := domainmemory.CommonRawErrorCodeOf(err)
		if code != domainmemory.CommonRawErrorRoot && code != domainmemory.CommonRawErrorUnavailable {
			t.Fatalf("unset raw root code=%q err=%v, want invalid_root/unavailable", code, err)
		}
	})
}

func bytesOfRuntimeSize(size int, value byte) []byte {
	content := make([]byte, size)
	for index := range content {
		content[index] = value
	}
	return content
}
