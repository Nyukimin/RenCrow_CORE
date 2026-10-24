package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestConversationRuntimePreparedRecallWarmsConfiguredLocalOwner(t *testing.T) {
	cfg := configuredPreparedRecallLocalConfig(t)
	seed, err := l1sqlite.NewL1SQLiteStore(cfg.Storage.Databases.ConversationL1)
	if err != nil {
		t.Fatalf("open temporary L1 for startup seed: %v", err)
	}
	proposed, err := seed.OwnerProposeUserMemory(
		context.Background(), "prepared-recall-seed-propose", "owner-42", "owner-42",
		domainmemory.UserMemoryTypePreference, "Owner prefers concise answers", "prepared recall startup seed",
	)
	if err != nil {
		_ = seed.Close()
		t.Fatalf("seed owner UserMemory candidate: %v", err)
	}
	confirmed, err := seed.OwnerTransitionUserMemory(
		context.Background(), "prepared-recall-seed-confirm", "owner-42", "owner-42", proposed.Item.ID,
		domainmemory.UserMemoryOwnerOperationConfirm, "", "prepared recall startup seed review",
	)
	if err != nil {
		_ = seed.Close()
		t.Fatalf("confirm owner UserMemory seed: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close temporary L1 seed store: %v", err)
	}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, runtimeStorageOwnerBundle{})
	closeConversationRuntimeOnCleanup(t, runtime)

	prepared := runtime.PreparedRecallRuntime
	if prepared == nil {
		t.Fatal("conversation runtime does not expose PreparedRecallRuntime")
	}
	capability, ready := prepared.CurrentCapability()
	if !ready {
		t.Fatal("configured local owner prepared recall capability is not ready after startup warm")
	}
	if capability.OwnerID() != "owner-42" || capability.UserScope() != "user:owner-42" {
		t.Fatalf("prepared recall owner=%q scope=%q, want configured authenticated user scope", capability.OwnerID(), capability.UserScope())
	}

	option, ok := capability.ReadyOption(nil)
	if !ok {
		t.Fatal("current prepared recall capability did not produce its ready option")
	}
	oldLease, status, err := option.ProjectionOwner.Acquire(capability.OwnerID(), capability.UserScope(), capability.SourceVersion())
	if err != nil || oldLease == nil || status != domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("acquire warmed prepared recall snapshot: lease=%v status=%q err=%v", oldLease, status, err)
	}
	defer oldLease.Release()
	selection, err := oldLease.Select(domainPreparedRecallSelectInput(capability.SourceVersion()))
	if err != nil || selection.PreparationStatus != domainconversation.PreparedRecallPreparationReady || len(selection.UserMemories) != 1 {
		t.Fatalf("prepared startup snapshot selection=%+v err=%v, want the one confirmed L1 memory", selection, err)
	}
	if got := selection.UserMemories[0].Decision.Item.ID; got != confirmed.Item.ID {
		t.Fatalf("prepared startup selected memory id=%q, want %q", got, confirmed.Item.ID)
	}
	if _, ok := runtime.Closer.(*preparedRecallConversationCloser); !ok {
		t.Fatalf("runtime primary closer=%T, want prepared recall lifecycle wrapper", runtime.Closer)
	}

	if _, err := runtime.L1Store.OwnerTransitionUserMemory(
		context.Background(), "prepared-recall-forget", "owner-42", "owner-42", confirmed.Item.ID,
		domainmemory.UserMemoryOwnerOperationForget, "", "prepared recall mutation test",
	); err != nil {
		t.Fatalf("OwnerTransitionUserMemory(forget) failed: %v", err)
	}
	if _, status, err := option.ProjectionOwner.Acquire(capability.OwnerID(), capability.UserScope(), capability.SourceVersion()); err == nil || status == domainconversation.PreparedRecallPreparationReady {
		t.Fatalf("owner mutation left the previous prepared recall source version usable: status=%q err=%v", status, err)
	}
	if _, err := oldLease.Select(domainPreparedRecallSelectInput(capability.SourceVersion())); !errors.Is(err, conversationpersistence.ErrPreparedRecallProjectionStale) {
		t.Fatalf("owner mutation did not synchronously revoke the acquired snapshot: err=%v", err)
	}
}

func TestConversationRuntimeDoesNotPrepareRecallWithoutConfiguredLocalPrincipal(t *testing.T) {
	cfg := &config.Config{
		Conversation: config.ConversationConfig{Enabled: false},
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			ConversationL1: filepath.Join(t.TempDir(), "conversation-l1.db"),
		}},
	}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, runtimeStorageOwnerBundle{})
	closeConversationRuntimeOnCleanup(t, runtime)

	if runtime.PreparedRecallRuntime != nil {
		t.Fatal("prepared recall was enabled using the legacy default conversation owner without a configured local principal")
	}
	if got := conversationRuntimeUserID(cfg); got != "ren" {
		t.Fatalf("legacy conversation owner=%q, want ren for unrelated existing runtime wiring", got)
	}
}

func TestConversationRuntimeRemoteStorageHostHasNoPreparedRecallCapability(t *testing.T) {
	root := t.TempDir()
	fixture := newRuntimeConversationStorageHostFixture(t, root)
	cfg := &config.Config{
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "remote-owner"},
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			ConversationL1: filepath.Join(root, "must-not-open", "conversation-l1.db"),
		}},
	}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, fixture.owners)
	closeConversationRuntimeOnCleanup(t, runtime)

	if runtime.PreparedRecallRuntime != nil {
		t.Fatal("remote StorageHost runtime exposed prepared recall without local owner invalidation")
	}
	if runtime.L1Store != nil {
		t.Fatalf("remote StorageHost runtime unexpectedly opened local L1 store: %p", runtime.L1Store)
	}
}

func TestPreparedRecallConversationCloserClosesWorkerBeforeOwnerStore(t *testing.T) {
	var order []string
	preparedErr := errors.New("prepared close")
	prepared := &recordingConversationRuntimeCloser{name: "prepared", order: &order, err: preparedErr}
	store := &recordingConversationRuntimeCloser{name: "store", order: &order}
	archive := &recordingConversationRuntimeCloser{name: "archive", order: &order}
	closer := conversationRuntimeCloserWithPreparedRecall(prepared, store)
	deps := &Dependencies{conversationArchiveCloser: archive, conversationCloser: closer}
	deps.Shutdown()

	if err := closer.Close(); !errors.Is(err, preparedErr) {
		t.Fatalf("prepared recall closer error=%v, want wrapped prepared close error", err)
	}
	if err := closer.Close(); !errors.Is(err, preparedErr) {
		t.Fatalf("repeated closer error=%v, want same prepared close error", err)
	}
	if !reflect.DeepEqual(order, []string{"archive", "prepared", "store"}) {
		t.Fatalf("close order=%v, want existing archive order followed by prepared worker and owner store exactly once", order)
	}
}

type recordingConversationRuntimeCloser struct {
	name  string
	order *[]string
	err   error
}

func (c *recordingConversationRuntimeCloser) Close() error {
	*c.order = append(*c.order, c.name)
	return c.err
}

func configuredPreparedRecallLocalConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Conversation:  config.ConversationConfig{Enabled: false},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "owner-42"},
		Storage: config.StorageConfig{
			Databases: config.DatabasePathsConfig{ConversationL1: filepath.Join(t.TempDir(), "conversation-l1.db")},
			Memory:    config.MemoryStorageConfig{RawSourceDir: filepath.Join(t.TempDir(), "raw-source")},
		},
	}
}

func closeConversationRuntimeOnCleanup(t *testing.T, runtime conversationRuntime) {
	t.Helper()
	t.Cleanup(func() {
		if runtime.ArchiveCloser != nil {
			_ = runtime.ArchiveCloser.Close()
		}
		if runtime.Closer != nil {
			_ = runtime.Closer.Close()
		}
	})
}

func domainPreparedRecallSelectInput(sourceVersion string) domainconversation.PreparedRecallSelectInput {
	return domainconversation.PreparedRecallSelectInput{
		AuthenticatedOwnerID:  "owner-42",
		UserScope:             "user:owner-42",
		ExpectedSourceVersion: sourceVersion,
		Persona:               "mio",
		Message:               "Owner prefers concise answers",
		Now:                   time.Now().UTC(),
	}
}
