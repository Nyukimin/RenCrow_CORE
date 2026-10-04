package conversation

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRealConversationManagerFlushAndHistoryUseSelectedArchiveRPC(t *testing.T) {
	root := t.TempDir()
	archiveOwner, err := archivesqlite.NewArchiveSQLiteStore(filepath.Join(root, "archive.sqlite"))
	if err != nil {
		t.Fatalf("open SQLite Archive owner: %v", err)
	}
	t.Cleanup(func() { _ = archiveOwner.Close() })
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{
		Token: "real-manager-archive-test-token", JournalDir: filepath.Join(root, "journal"),
	})
	if err != nil {
		t.Fatalf("create storage-host Handler: %v", err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if err := storagehost.RegisterArchiveGroup(handler, archiveOwner); err != nil {
		t.Fatalf("register Archive group: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := storagehost.NewClient(storagehost.ClientConfig{
		Endpoint: server.URL, Token: "real-manager-archive-test-token", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create storage-host client: %v", err)
	}
	ctx := context.Background()
	if err := client.Handshake(ctx); err != nil {
		t.Fatalf("storage-host handshake: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	thread := &domconv.Thread{
		ID: modulecore.NewThreadID(), ThreadSeq: 1, ThreadKind: domconv.ThreadKindUserConversation,
		SessionID: string(modulecore.NewSessionID()), Domain: "general", Status: domconv.ThreadActive,
		StartTime: now.Add(-time.Minute), Turns: []domconv.Message{
			{Speaker: domconv.SpeakerUser, Msg: "Please keep the response concise.", Timestamp: now.Add(-time.Minute)},
			{Speaker: domconv.SpeakerMio, Msg: "Understood; I will be concise.", Timestamp: now},
		},
	}
	redis := newMockRedisStore()
	redis.threads[thread.ID] = thread
	manager := &RealConversationManager{
		redisStore: redis, archiveStore: storagehost.NewArchiveStoreClient(client),
	}

	summary, err := manager.FlushThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("RealConversationManager FlushThread through selected Archive client: %v", err)
	}
	if summary == nil || summary.ThreadID != thread.ID || summary.SessionID != thread.SessionID || summary.Receipt == nil {
		t.Fatalf("RealConversationManager archived summary identity/receipt mismatch: %+v", summary)
	}
	readBack, err := manager.GetSessionHistory(ctx, thread.SessionID, 10)
	if err != nil {
		t.Fatalf("RealConversationManager history through selected Archive client: %v", err)
	}
	if len(readBack) != 1 || readBack[0].ThreadID != thread.ID || readBack[0].SessionID != thread.SessionID ||
		readBack[0].Receipt == nil || readBack[0].Receipt.EvidenceSHA256 != summary.Receipt.EvidenceSHA256 {
		t.Fatalf("RealConversationManager Archive history identity/receipt mismatch: %+v", readBack)
	}
}
