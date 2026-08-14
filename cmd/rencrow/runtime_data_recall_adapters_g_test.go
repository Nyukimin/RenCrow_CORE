package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataRecallKnowledgeMemoryIndexedPublicAndUserScopes(t *testing.T) {
	store, err := knowledgememorypersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge_memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for _, item := range []domainkm.CreativeKnowledgeItem{
		{ItemID: "public-creative", Title: "ScopeCreative Public", WorkType: "novel", Status: "reviewed", CreatedAt: now},
		{ItemID: "user-one-creative", UserID: "user-1", Title: "ScopeCreative User One", WorkType: "film", Status: "reviewed", Visibility: "private", CreatedAt: now.Add(time.Minute)},
		{ItemID: "user-two-creative", UserID: "user-2", Title: "ScopeCreative User Two", WorkType: "film", Status: "reviewed", Visibility: "private", CreatedAt: now.Add(2 * time.Minute)},
	} {
		if err := store.SaveCreativeKnowledgeItem(context.Background(), item); err != nil {
			t.Fatalf("SaveCreativeKnowledgeItem(%s): %v", item.ItemID, err)
		}
	}
	for _, item := range []domainkm.NewsKnowledgeItem{
		{ItemID: "public-news", Source: "public-source", Topic: "ScopeNews Public", Summary: "public summary", Status: "promoted", CreatedAt: now},
		{ItemID: "user-one-news", UserID: "user-1", Source: "private-source", Topic: "ScopeNews User One", Summary: "private summary", Status: "promoted", Visibility: "private", CreatedAt: now.Add(time.Minute)},
		{ItemID: "user-two-news", UserID: "user-2", Source: "private-source", Topic: "ScopeNews User Two", Summary: "other private summary", Status: "promoted", Visibility: "private", CreatedAt: now.Add(2 * time.Minute)},
	} {
		if err := store.SaveNewsKnowledgeItem(context.Background(), item); err != nil {
			t.Fatalf("SaveNewsKnowledgeItem(%s): %v", item.ItemID, err)
		}
	}

	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallKnowledgeMemory(registry, store, store); err != nil {
		t.Fatalf("register knowledge memory recall: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 6 {
		t.Fatalf("knowledge memory recall routes = %#v", routes)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})

	public := runtimeKnowledgeMemoryPublicContext(t, "knowledge-memory-public-search")
	publicCreative := runtimeKnowledgeMemoryExecuteRecall(t, worker, public, "search_public_creative", "ScopeCreative", 10)
	assertKnowledgeMemorySearchRecord(t, publicCreative, "public-creative", "public", "")
	publicNews := runtimeKnowledgeMemoryExecuteRecall(t, worker, public, "search_public_news", "ScopeNews", 10)
	assertKnowledgeMemorySearchRecord(t, publicNews, "public-news", "public", "")

	userOne := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-user-search", "user-1", "mio")
	userCreative := runtimeKnowledgeMemoryExecuteRecall(t, worker, userOne, "search_user_creative", "ScopeCreative", 10)
	assertKnowledgeMemorySearchRecord(t, userCreative, "user-one-creative", "user", "user-1")
	userNews := runtimeKnowledgeMemoryExecuteRecall(t, worker, userOne, "search_user_news", "ScopeNews", 10)
	assertKnowledgeMemorySearchRecord(t, userNews, "user-one-news", "user", "user-1")

	userTwo := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-user-two-search", "user-2", "mio")
	userTwoCreative := runtimeKnowledgeMemoryExecuteRecall(t, worker, userTwo, "search_user_creative", "ScopeCreative", 10)
	assertKnowledgeMemorySearchRecord(t, userTwoCreative, "user-two-creative", "user", "user-2")
	for _, result := range []runtimeDataRecallResult{publicCreative, publicNews, userCreative, userNews, userTwoCreative} {
		for _, record := range result.Records {
			if _, leaked := record["payload"]; leaked {
				t.Fatalf("indexed search exposed payload: %#v", record)
			}
			if _, leaked := record["content_hints"]; leaked {
				t.Fatalf("indexed search exposed private content hints: %#v", record)
			}
		}
	}
}

func TestRuntimeDataRecallKnowledgeMemoryRejectsUntrustedScopeAndBoundsLimit(t *testing.T) {
	store, err := knowledgememorypersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge_memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallKnowledgeMemory(registry, store, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	public := runtimeKnowledgeMemoryPublicContext(t, "knowledge-memory-limit-public")
	response, err := worker.ExecuteV2(public, "data.recall", map[string]any{
		"store": "knowledge_memory", "operation": "search_public_creative", "query": "needle", "limit": 51,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("invalid limit response=%#v err=%v", response, err)
	}
	internal := dataRecallInternalContext(t)
	response, err = worker.ExecuteV2(internal, "data.recall", map[string]any{
		"store": "knowledge_memory", "operation": "search_user_creative", "query": "needle", "limit": 1,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("missing authenticated user response=%#v err=%v", response, err)
	}
}

func runtimeKnowledgeMemoryExecuteRecall(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, operation, query string, limit int) runtimeDataRecallResult {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "knowledge_memory", "operation": operation, "query": query, "limit": limit,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("knowledge memory data.recall response=%#v err=%v", response, err)
	}
	result, ok := response.Result.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("knowledge memory data.recall result type=%T value=%#v", response.Result, response.Result)
	}
	return result
}

func assertKnowledgeMemorySearchRecord(t *testing.T, result runtimeDataRecallResult, recordID, scope, userID string) {
	t.Helper()
	if len(result.Records) != 1 {
		t.Fatalf("search result = %#v, want one record", result)
	}
	record := result.Records[0]
	if record["record_id"] != recordID || record["scope"] != scope || record["user_id"] != userID {
		t.Fatalf("search record = %#v", record)
	}
	if record["record_type"] != "creative_knowledge" && record["record_type"] != "news_knowledge" {
		t.Fatalf("unexpected record type = %#v", record)
	}
}
