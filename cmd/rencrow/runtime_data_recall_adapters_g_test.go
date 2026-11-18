package main

import (
	"context"
	"path/filepath"
	"strings"
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
	archive := domainkm.PersonalArchiveEntry{
		EntryID: "user-one-lumina-diary", UserID: "user-1", SourceRef: "gmail://gmail.com/messages/a1",
		Title: "Lumina diary", SourceKind: "lumina_diary_email", AuthorID: "lumina", DiaryDate: "2026-08-14",
		OriginalText: "ScopeDiary original private conversation", Protected: true, CreatedAt: now.Add(time.Minute),
	}
	if _, err := store.SavePersonalArchiveEntryWithReceipt(context.Background(), archive, archive.SourceRef, "lumina"); err != nil {
		t.Fatalf("SavePersonalArchiveEntryWithReceipt failed: %v", err)
	}

	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallKnowledgeMemory(registry, store, store); err != nil {
		t.Fatalf("register knowledge memory recall: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 8 {
		t.Fatalf("knowledge memory recall routes = %#v", routes)
	}
	if routeContext := renderRuntimeDataRouteContext(registry, nil); !strings.Contains(routeContext, "data.recall knowledge_memory/read_user_personal_archive_entry access=user") {
		t.Fatalf("exact personal archive route is absent from dynamic runtime capability snapshot: %s", routeContext)
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
	userArchive := runtimeKnowledgeMemoryExecuteRecall(t, worker, userOne, "search_user_personal_archive", "ScopeDiary", 10)
	assertKnowledgeMemorySearchRecord(t, userArchive, "user-one-lumina-diary", "user", "user-1")
	diaryEntry := runtimeKnowledgeMemoryExecuteRecall(t, worker, userOne, "read_user_personal_archive_entry", "user-one-lumina-diary", 1)
	if len(diaryEntry.Records) != 1 {
		t.Fatalf("exact diary read = %#v", diaryEntry.Records)
	}
	diaryRecord := diaryEntry.Records[0]
	if diaryRecord["entry_id"] != userArchive.Records[0]["record_id"] || diaryRecord["user_id"] != "user-1" || diaryRecord["source_ref"] != archive.SourceRef || diaryRecord["original_text"] != archive.OriginalText || diaryRecord["author_id"] != "lumina" || diaryRecord["diary_date"] != "2026-08-14" || diaryRecord["protected"] != true {
		t.Fatalf("exact diary record lost source/provenance: %#v", diaryRecord)
	}

	userTwo := runtimeKnowledgeMemoryUserContext(t, "knowledge-memory-user-two-search", "user-2", "mio")
	userTwoCreative := runtimeKnowledgeMemoryExecuteRecall(t, worker, userTwo, "search_user_creative", "ScopeCreative", 10)
	assertKnowledgeMemorySearchRecord(t, userTwoCreative, "user-two-creative", "user", "user-2")
	userTwoArchive := runtimeKnowledgeMemoryExecuteRecall(t, worker, userTwo, "search_user_personal_archive", "ScopeDiary", 10)
	if len(userTwoArchive.Records) != 0 {
		t.Fatalf("private archive leaked to another user: %#v", userTwoArchive)
	}
	userTwoDetail := runtimeKnowledgeMemoryExecuteRecall(t, worker, userTwo, "read_user_personal_archive_entry", "user-one-lumina-diary", 1)
	if len(userTwoDetail.Records) != 0 {
		t.Fatalf("exact private archive read leaked to another user: %#v", userTwoDetail)
	}
	publicDetail, err := worker.ExecuteV2(public, "data.recall", map[string]any{
		"store": "knowledge_memory", "operation": "read_user_personal_archive_entry", "query": "user-one-lumina-diary",
	})
	if err != nil || publicDetail == nil || !publicDetail.IsError() {
		t.Fatalf("public exact archive read response=%#v err=%v", publicDetail, err)
	}
	for _, result := range []runtimeDataRecallResult{publicCreative, publicNews, userCreative, userNews, userArchive, userTwoCreative, userTwoArchive} {
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
	if record["record_type"] != "creative_knowledge" && record["record_type"] != "news_knowledge" && record["record_type"] != "personal_archive" {
		t.Fatalf("unexpected record type = %#v", record)
	}
}
