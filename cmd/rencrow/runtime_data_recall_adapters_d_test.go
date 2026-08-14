package main

import (
	"context"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestRuntimeDataRecallConversationL1UserMemoryScopesE2E(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	createdA, replay, err := store.CreateUserMemoryCandidateWithRequest(context.Background(), "conversation-l1-recall-a", "shiro", domainmemory.CreateUserMemoryInput{
		UserID: "user-a", Type: domainmemory.UserMemoryTypePreference, Statement: "User A prefers concise explanations",
		EvidenceEventIDs: []string{"evt-a"}, Confidence: 0.9, Sensitivity: "normal", Scope: "all_personas",
	})
	if err != nil || replay {
		t.Fatalf("create user A candidate=%#v replay=%v err=%v", createdA, replay, err)
	}
	createdB, replay, err := store.CreateUserMemoryCandidateWithRequest(context.Background(), "conversation-l1-recall-b", "shiro", domainmemory.CreateUserMemoryInput{
		UserID: "user-b", Type: domainmemory.UserMemoryTypeProject, Statement: "User B project note",
		EvidenceEventIDs: []string{"evt-b"}, Confidence: 0.7, Sensitivity: "normal", Scope: "all_personas",
	})
	if err != nil || replay {
		t.Fatalf("create user B candidate=%#v replay=%v err=%v", createdB, replay, err)
	}
	worker := runtimeConversationL1OwnerWorker(t, store)
	userA := conversationL1UserContext(t, "conversation-l1-recall-read-a", "user-a", "shiro")
	exact := runtimeDataWriteOwnerExecuteRecall(t, worker, userA, "conversation_l1", "user_memory", createdA.ID)
	assertConversationL1RecallResult(t, exact, "conversation-l1-recall-read-a", "conversation_l1/user_memory", 1)
	assertConversationL1SafeRecord(t, exact.Records[0])
	if exact.Records[0]["memory_id"] != createdA.ID || exact.Records[0]["statement"] != createdA.Statement {
		t.Fatalf("exact user A record=%#v", exact.Records[0])
	}
	list := runtimeDataWriteOwnerExecuteRecall(t, worker, userA, "conversation_l1", "user_memories", "User A")
	assertConversationL1RecallResult(t, list, "conversation-l1-recall-read-a", "conversation_l1/user_memories", 1)
	if list.Records[0]["memory_id"] != createdA.ID {
		t.Fatalf("listed user A record=%#v", list.Records[0])
	}
	userB := conversationL1UserContext(t, "conversation-l1-recall-read-b", "user-b", "shiro")
	crossUser := runtimeDataWriteOwnerExecuteRecall(t, worker, userB, "conversation_l1", "user_memory", createdA.ID)
	assertConversationL1RecallResult(t, crossUser, "conversation-l1-recall-read-b", "conversation_l1/user_memory", 0)
	userBList := runtimeDataWriteOwnerExecuteRecall(t, worker, userB, "conversation_l1", "user_memories", "User A")
	assertConversationL1RecallResult(t, userBList, "conversation-l1-recall-read-b", "conversation_l1/user_memories", 0)
}
