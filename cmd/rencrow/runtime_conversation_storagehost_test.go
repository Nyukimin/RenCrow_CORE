package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildConversationRuntimeUsesSelectedRemoteOwnersWithoutLocalFallback(t *testing.T) {
	root := t.TempDir()
	fixture := newRuntimeConversationStorageHostFixture(t, root)
	ctx := context.Background()
	createdMemory, err := fixture.userMemory.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "chat-user", Type: domainmemory.UserMemoryTypePreference,
		Statement: "User prefers concise direct answers", State: domainmemory.MemoryStateConfirmed,
		EvidenceEventIDs: []string{"memory-evidence-1"}, Confidence: 0.95,
		Sensitivity: "normal", Scope: "all_personas", Source: "runtime-test",
	})
	if err != nil {
		t.Fatalf("seed remote user-memory owner: %v", err)
	}

	localL1 := filepath.Join(root, "must-not-open", "conversation-l1.sqlite")
	localArchive := filepath.Join(root, "must-not-open", "conversation-archive.sqlite")
	cfg := &config.Config{Storage: config.StorageConfig{
		Databases: config.DatabasePathsConfig{
			ConversationL1:      localL1,
			ConversationArchive: localArchive,
		},
	}}
	cfg.LocalAgentOps.UserID = "chat-user"
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, fixture.owners)
	if runtime.Closer != nil {
		t.Cleanup(func() { _ = runtime.Closer.Close() })
	}
	if runtime.L1Store != nil || runtime.ArchiveStore != nil {
		t.Fatalf("remote runtime exposed local durable owners: L1=%T Archive=%T", runtime.L1Store, runtime.ArchiveStore)
	}
	if runtime.ChatL1Store != fixture.owners.ConversationStore || runtime.ChatArchiveStore != fixture.owners.ArchiveStore || runtime.UserMemoryStore != fixture.userMemory {
		t.Fatalf("remote Chat owner selection mismatch: L1=%T Archive=%T UserMemory=%T", runtime.ChatL1Store, runtime.ChatArchiveStore, runtime.UserMemoryStore)
	}
	for _, localPath := range []string{localL1, localArchive} {
		if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remote Conversation runtime opened local path %q (stat err=%v)", localPath, err)
		}
	}

	sessionID := string(modulecore.NewSessionID())
	pack, err := runtime.Engine.BeginTurn(ctx, sessionID, "How should answers be phrased?")
	if err != nil {
		t.Fatalf("remote Conversation BeginTurn: %v", err)
	}
	selectedMemory := false
	for _, decision := range pack.UserMemoryRecallDecisions {
		if decision.Item.ID == createdMemory.ID {
			selectedMemory = decision.Selected && decision.Status == domainmemory.UserMemoryRecallStatusInjected
			break
		}
	}
	if !selectedMemory || len(pack.UserProfile.Facts) == 0 || pack.UserProfile.Facts[0] != createdMemory.Statement {
		t.Fatalf("remote UserMemory was not selected and injected: decisions=%+v profile=%+v", pack.UserMemoryRecallDecisions, pack.UserProfile)
	}

	committer, ok := runtime.Engine.(interface {
		CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
	})
	if !ok {
		t.Fatal("remote Conversation engine does not expose typed EndTurn commit")
	}
	request := domconv.ConversationTurnRequest{
		TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(), RootTaskID: modulecore.NewTaskID(),
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		SessionID: sessionID, OwnerID: "chat-user", UserMessage: "How should answers be phrased?",
		AgentMessage: "Concise and direct.", AgentSpeaker: domconv.SpeakerMio,
		RecallTraceItems: pack.ToTraceItems(),
	}
	committed, err := committer.CommitConversationTurn(ctx, request)
	if err != nil {
		t.Fatalf("remote EndTurn commit: %v", err)
	}
	if committed.TurnID != request.TurnID || committed.TraceID != request.TraceID || committed.RootTaskID != request.RootTaskID ||
		committed.SessionID != sessionID || committed.UserMessageID != request.UserMessageID || committed.AgentMessageID != request.AgentMessageID {
		t.Fatalf("remote EndTurn receipt IDs mismatch: got=%+v request=%+v", committed, request)
	}
	storedReceipt, err := fixture.turn.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil {
		t.Fatalf("read remote durable Turn receipt: %v", err)
	}
	if storedReceipt.TurnID != committed.TurnID || storedReceipt.PayloadSHA256 != committed.PayloadSHA256 || storedReceipt.ThreadID != committed.ThreadID {
		t.Fatalf("remote durable Turn receipt mismatch: stored=%+v committed=%+v", storedReceipt, committed)
	}
	events, err := fixture.l1.RecentBySession(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("read remote L1 conversation after EndTurn: %v", err)
	}
	if len(events) != 2 || events[0].Message != request.UserMessage && events[0].Message != request.AgentMessage || events[1].Message != request.UserMessage && events[1].Message != request.AgentMessage {
		t.Fatalf("remote L1 messages=%+v, want committed user/agent message payloads", events)
	}

	restarted := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, fixture.owners)
	restartedPack, err := restarted.Engine.BeginTurn(ctx, sessionID, "Continue from that answer")
	if err != nil {
		t.Fatalf("remote Conversation BeginTurn after runtime rebuild: %v", err)
	}
	if len(restartedPack.ShortContext) != 2 || restartedPack.ShortContext[0].Msg != request.UserMessage || restartedPack.ShortContext[1].Msg != request.AgentMessage {
		t.Fatalf("remote L1 history after runtime rebuild=%+v, want committed canonical messages", restartedPack.ShortContext)
	}

	threadID := modulecore.NewThreadID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	receipt := &domconv.ThreadSummaryReceipt{
		SchemaVersion:  domconv.ThreadSummaryReceiptSchemaVersion,
		GenerationMode: domconv.ThreadSummaryGenerationLLM, Provider: "runtime-test",
		EvidenceSHA256: strings.Repeat("a", 64), SourceTurnCount: 1,
		Roles: []string{"user", "mio"}, CreatedAt: now,
	}
	summary := &domconv.ThreadSummary{
		ThreadID: threadID, ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation,
		SessionID: sessionID, Domain: "general", Summary: "Concise answer preference was used.",
		Keywords: []string{"concise", "answer", "preference"}, Roles: []string{"user", "mio"}, Receipt: receipt,
		StartTime: now.Add(-time.Minute), EndTime: now,
	}
	if err := restarted.ChatArchiveStore.SaveThreadSummaryWithReceipt(ctx, summary, receipt); err != nil {
		t.Fatalf("save remote Chat archive summary: %v", err)
	}
	archived, err := restarted.ChatArchiveStore.GetThreadSummary(ctx, threadID)
	if err != nil {
		t.Fatalf("read remote Chat archive summary: %v", err)
	}
	if archived == nil || archived.ThreadID != threadID || archived.SessionID != sessionID || archived.Summary != summary.Summary || archived.Receipt == nil || archived.Receipt.EvidenceSHA256 != receipt.EvidenceSHA256 {
		t.Fatalf("remote Chat archive summary DTO mismatch: %+v", archived)
	}
	history, err := restarted.ChatArchiveStore.GetSessionHistory(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("read remote Chat archive history: %v", err)
	}
	if len(history) != 1 || history[0].ThreadID != threadID || history[0].SessionID != sessionID || history[0].Receipt == nil || history[0].Receipt.Roles[1] != "mio" {
		t.Fatalf("remote Chat archive history DTO mismatch: %+v", history)
	}
}

type runtimeConversationStorageHostFixture struct {
	owners     runtimeStorageOwnerBundle
	userMemory *storagehost.UserMemoryStoreClient
	l1         *storagehost.L1StoreClient
	turn       *storagehost.TurnStoreClient
}

func newRuntimeConversationStorageHostFixture(t *testing.T, root string) *runtimeConversationStorageHostFixture {
	t.Helper()
	ownerDir := filepath.Join(root, "storage-host-owner")
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		t.Fatalf("create storage-host owner directory: %v", err)
	}
	l1Owner, err := l1sqlite.NewL1SQLiteStore(filepath.Join(ownerDir, "conversation-l1.sqlite"))
	if err != nil {
		t.Fatalf("open storage-host L1 owner: %v", err)
	}
	t.Cleanup(func() { _ = l1Owner.Close() })
	archiveOwner, err := archivesqlite.NewArchiveSQLiteStore(filepath.Join(ownerDir, "conversation-archive.sqlite"))
	if err != nil {
		t.Fatalf("open storage-host Archive owner: %v", err)
	}
	t.Cleanup(func() { _ = archiveOwner.Close() })
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{
		Token: "runtime-conversation-test-token", JournalDir: filepath.Join(root, "storage-host-journal"),
	})
	if err != nil {
		t.Fatalf("create storage-host Handler: %v", err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if err := storagehost.RegisterL1Group(handler, l1Owner); err != nil {
		t.Fatalf("register L1 group: %v", err)
	}
	if err := storagehost.RegisterTurnGroup(handler, l1Owner); err != nil {
		t.Fatalf("register Turn group: %v", err)
	}
	if err := storagehost.RegisterUserMemoryGroup(handler, l1Owner); err != nil {
		t.Fatalf("register UserMemory group: %v", err)
	}
	if err := storagehost.RegisterArchiveGroup(handler, archiveOwner); err != nil {
		t.Fatalf("register Archive group: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := storagehost.NewClient(storagehost.ClientConfig{
		Endpoint: server.URL, Token: "runtime-conversation-test-token", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create storage-host client: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("storage-host handshake: %v", err)
	}
	userMemory := storagehost.NewUserMemoryStoreClient(client)
	l1Client := storagehost.NewL1StoreClient(client)
	turnClient := storagehost.NewTurnStoreClient(client)
	archiveClient := storagehost.NewArchiveStoreClient(client)
	return &runtimeConversationStorageHostFixture{
		owners: runtimeStorageOwnerBundle{
			Remote:            true,
			Client:            client,
			ArchiveStore:      archiveClient,
			SessionRepository: nil,
			ConversationStore: &runtimeRemoteConversationStorageOwner{
				L1StoreClient:   l1Client,
				TurnStoreClient: turnClient,
			},
			UserMemoryStore: userMemory,
		},
		userMemory: userMemory, l1: l1Client, turn: turnClient,
	}
}
