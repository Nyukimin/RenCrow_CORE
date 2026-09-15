package knowledgememory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestNewsKnowledgeReceiptOverlaySurvivesRestartAndKeepsImportedReadiness(t *testing.T) {
	ctx := context.Background()
	path := importNewsReceiptFixture(t, domainkm.NewsKnowledgeItem{
		ItemID: "imported-news", Source: "wire", Topic: "imported topic", Status: "candidate", CreatedAt: newsReceiptTestTime(),
	})
	store, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatal(err)
	}

	candidate := receiptNewsItem("candidate-news", "private candidate", "candidate")
	reviewed := receiptNewsItem("reviewed-news", "private reviewed", "reviewed")
	first, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, candidate, "gmail:message:candidate", "shiro")
	if err != nil || first {
		_ = store.Close()
		t.Fatalf("first candidate write = reused %v, err %v", first, err)
	}
	first, err = store.SaveNewsKnowledgeItemWithReceipt(ctx, reviewed, "gmail:message:reviewed", "shiro")
	if err != nil || first {
		_ = store.Close()
		t.Fatalf("first reviewed write = reused %v, err %v", first, err)
	}
	var sourceCount, importedCount int
	var sourceHash, importedHash string
	if err := store.db.QueryRow(`SELECT source_count, imported_count, source_hash, imported_hash FROM knowledge_memory_import_manifest WHERE manifest_id = 'active'`).Scan(&sourceCount, &importedCount, &sourceHash, &importedHash); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := readOnly.Readiness(ctx)
	if err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	if readiness.IntegrityState != KnowledgeMemoryIntegrityReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady {
		_ = readOnly.Close()
		t.Fatalf("readiness after receipt overlays = %#v", readiness)
	}
	var gotSourceCount, gotImportedCount int
	var gotSourceHash, gotImportedHash string
	if err := readOnly.db.QueryRow(`SELECT source_count, imported_count, source_hash, imported_hash FROM knowledge_memory_import_manifest WHERE manifest_id = 'active'`).Scan(&gotSourceCount, &gotImportedCount, &gotSourceHash, &gotImportedHash); err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	if gotSourceCount != sourceCount || gotImportedCount != importedCount || gotSourceHash != sourceHash || gotImportedHash != importedHash {
		_ = readOnly.Close()
		t.Fatalf("import manifest changed: before %d/%d %s/%s after %d/%d %s/%s", sourceCount, importedCount, sourceHash, importedHash, gotSourceCount, gotImportedCount, gotSourceHash, gotImportedHash)
	}
	results, err := readOnly.Search(ctx, appkm.SearchRequest{
		Query: "private candidate", Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: candidate.UserID}, RecordType: newsKnowledgeRecordType, Limit: 20,
	})
	if err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("candidate overlay is searchable: %#v", results)
	}
	results, err = readOnly.Search(ctx, appkm.SearchRequest{
		Query: "private reviewed", Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: reviewed.UserID}, RecordType: newsKnowledgeRecordType, Limit: 20,
	})
	if err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].RecordID != reviewed.ItemID {
		t.Fatalf("reviewed overlay search = %#v", results)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewsKnowledgeReceiptReplayPreservesReviewedOwnerState(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := receiptNewsItem("replay-news", "replay topic", "candidate")
	reused, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:thread:replay", "shiro")
	if err != nil || reused {
		t.Fatalf("first write = reused %v, err %v", reused, err)
	}
	reviewed := item
	reviewed.Status = "reviewed"
	if err := store.SaveNewsKnowledgeItem(ctx, reviewed); err != nil {
		t.Fatalf("owner review update: %v", err)
	}
	reused, err = store.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:thread:replay", "shiro")
	if err != nil || !reused {
		t.Fatalf("replay = reused %v, err %v", reused, err)
	}
	items, err := store.ListNewsKnowledgeItems(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "reviewed" {
		t.Fatalf("replay overwrote reviewed state: %#v", items)
	}
}

func TestNewsKnowledgeReceiptRejectsSourceAndOwnerCollisions(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := receiptNewsItem("collision-news", "collision", "candidate")
	if _, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:collision", "shiro"); err != nil {
		t.Fatal(err)
	}
	other := receiptNewsItem("other-news", "collision", "candidate")
	if _, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, other, "gmail:collision", "shiro"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("source collision error = %v", err)
	}
	other = item
	other.Topic = "changed owner"
	other.UserID = "owner-2"
	if _, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, other, "gmail:collision-2", "shiro"); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("item collision error = %v", err)
	}
	other = item
	other.Source = "different source"
	if err := store.SaveNewsKnowledgeItem(ctx, other); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("source update error = %v", err)
	}
}

func TestNewsKnowledgeReceiptReadinessDetectsPayloadTampering(t *testing.T) {
	ctx := context.Background()
	path := importNewsReceiptFixture(t, domainkm.NewsKnowledgeItem{
		ItemID: "imported-news", Source: "wire", Topic: "imported topic", Status: "candidate", CreatedAt: newsReceiptTestTime(),
	})
	store, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	item := receiptNewsItem("tampered-news", "tamper target", "reviewed")
	if _, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:tamper", "shiro"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE news_knowledge SET payload = ? WHERE item_id = ?`, `{"item_id":"tampered-news","user_id":"owner-1","source":"wire","topic":"changed","status":"reviewed","visibility":"private","created_at":"2026-09-14T12:00:00Z"}`, item.ItemID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	readiness, err := readOnly.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.IntegrityState != KnowledgeMemoryIntegrityFailed {
		t.Fatalf("tampered receipt readiness = %#v", readiness)
	}
}

func TestNewsKnowledgeReceiptLegacyDatabaseReadinessBeforeOwnerSchemaUpgrade(t *testing.T) {
	ctx := context.Background()
	path := importNewsReceiptFixture(t, domainkm.NewsKnowledgeItem{
		ItemID: "imported-news", Source: "wire", Topic: "imported topic", Status: "candidate", CreatedAt: newsReceiptTestTime(),
	})
	legacyWriter, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyWriter.db.Exec(`DROP TABLE knowledge_memory_news_receipts`); err != nil {
		_ = legacyWriter.Close()
		t.Fatal(err)
	}
	if err := legacyWriter.Close(); err != nil {
		t.Fatal(err)
	}
	legacyReadOnly, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := legacyReadOnly.Readiness(ctx)
	if err != nil {
		_ = legacyReadOnly.Close()
		t.Fatal(err)
	}
	if readiness.IntegrityState != KnowledgeMemoryIntegrityReady {
		_ = legacyReadOnly.Close()
		t.Fatalf("legacy database readiness = %#v", readiness)
	}
	if err := legacyReadOnly.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err := OpenSQLiteStoreWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.EnsureOwnerRouteSchema(ctx); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	item := receiptNewsItem("upgraded-news", "upgraded private news", "reviewed")
	if reused, err := writer.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:upgraded", "shiro"); err != nil || reused {
		_ = writer.Close()
		t.Fatalf("upgraded write = reused %v, err %v", reused, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	readiness, err = readOnly.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.IntegrityState != KnowledgeMemoryIntegrityReady || readiness.Coverage.State != KnowledgeMemoryCoverageReady {
		t.Fatalf("readiness after owner schema upgrade = %#v", readiness)
	}
}

func TestNewsKnowledgeReceiptConcurrentReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := receiptNewsItem("race-news", "race topic", "candidate")
	const calls = 16
	results := make(chan struct {
		reused bool
		err    error
	}, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reused, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:race", "shiro")
			results <- struct {
				reused bool
				err    error
			}{reused: reused, err: err}
		}()
	}
	wg.Wait()
	close(results)
	first := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent write error: %v", result.err)
		}
		if !result.reused {
			first++
		}
	}
	if first != 1 {
		t.Fatalf("first-write count = %d, want 1", first)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_memory_news_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("receipt count = %d, want 1", count)
	}
}

func TestNewsKnowledgeReceiptValidatesPrivateOwnerAndBounds(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := receiptNewsItem("validation-news", "validation", "candidate")
	cases := []struct {
		name      string
		item      domainkm.NewsKnowledgeItem
		sourceKey string
		actorID   string
	}{
		{name: "missing owner", item: func() domainkm.NewsKnowledgeItem { item := base; item.UserID = ""; return item }(), sourceKey: "gmail:missing-owner", actorID: "shiro"},
		{name: "public", item: func() domainkm.NewsKnowledgeItem {
			item := base
			item.ItemID = "validation-public"
			item.Visibility = "public"
			return item
		}(), sourceKey: "gmail:public", actorID: "shiro"},
		{name: "promoted initial", item: func() domainkm.NewsKnowledgeItem {
			item := base
			item.ItemID = "validation-promoted"
			item.Status = "promoted"
			return item
		}(), sourceKey: "gmail:promoted", actorID: "shiro"},
		{name: "source key bound", item: func() domainkm.NewsKnowledgeItem { item := base; item.ItemID = "validation-source"; return item }(), sourceKey: strings.Repeat("s", maxNewsReceiptSourceKeyRunes+1), actorID: "shiro"},
		{name: "actor bound", item: func() domainkm.NewsKnowledgeItem { item := base; item.ItemID = "validation-actor"; return item }(), sourceKey: "gmail:actor", actorID: strings.Repeat("a", maxNewsReceiptActorIDRunes+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := store.SaveNewsKnowledgeItemWithReceipt(ctx, testCase.item, testCase.sourceKey, testCase.actorID); err == nil {
				t.Fatal("invalid receipt input unexpectedly succeeded")
			}
		})
	}
}

func importNewsReceiptFixture(t *testing.T, item domainkm.NewsKnowledgeItem) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	source := NewJSONLStore(root)
	if err := source.SaveNewsKnowledgeItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "knowledge_memory.db")
	if _, err := ImportJSONLToSQLite(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func receiptNewsItem(id, topic, status string) domainkm.NewsKnowledgeItem {
	return domainkm.NewsKnowledgeItem{
		ItemID: id, UserID: "owner-1", Source: "wire", Topic: topic, URL: "https://example.com/" + id,
		Summary: "summary for " + id, Status: status, Visibility: "private", CreatedAt: newsReceiptTestTime(),
	}
}

func newsReceiptTestTime() time.Time {
	return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
}

type newsReceiptFailingL1Store struct {
	failStage bool
	staging   []l1sqlite.L1StagingItem
	registry  []l1sqlite.L1SourceRegistryEntry
}

func (s *newsReceiptFailingL1Store) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	if s.failStage {
		return nil, errors.New("staging projection failed")
	}
	s.staging = append(s.staging, item)
	return &item, nil
}

func (s *newsReceiptFailingL1Store) SaveSourceRegistryEntry(_ context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error) {
	s.registry = append(s.registry, entry)
	return &entry, nil
}

func TestL1ConnectedPrivateNewsReceiptDoesNotUseGenericL1Projection(t *testing.T) {
	ctx := context.Background()
	base, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	l1 := &newsReceiptFailingL1Store{failStage: true}
	store := WithL1Connection(base, l1)
	writer, ok := store.(NewsKnowledgeReceiptWriter)
	if !ok {
		t.Fatal("L1-connected store does not expose news receipt writer")
	}
	item := receiptNewsItem("l1-private", "private canonical", "candidate")
	reused, err := writer.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:l1-private", "shiro")
	if err != nil || reused {
		t.Fatalf("private receipt result = reused %v, err %v", reused, err)
	}
	items, err := base.ListNewsKnowledgeItems(ctx, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("durable private row = %#v, err %v", items, err)
	}
	reused, err = writer.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:l1-private", "shiro")
	if err != nil || !reused {
		t.Fatalf("private replay result = reused %v, err %v", reused, err)
	}
	if len(l1.staging) != 0 || len(l1.registry) != 0 {
		t.Fatalf("private receipt used generic L1 projection: staging=%#v registry=%#v", l1.staging, l1.registry)
	}
}

func TestL1ConnectedPrivateNewsReceiptUsesCanonicalOwnerSearchWithoutL1Projection(t *testing.T) {
	ctx := context.Background()
	base, err := NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	l1 := &newsReceiptFailingL1Store{}
	store := WithL1Connection(base, l1)
	writer, ok := store.(NewsKnowledgeReceiptWriter)
	if !ok {
		t.Fatal("L1-connected store does not expose news receipt writer")
	}
	item := receiptNewsItem("private-l1", "private owner note", "candidate")
	if reused, err := writer.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:private-l1", "shiro"); err != nil || reused {
		t.Fatalf("private receipt write = reused %v, err %v", reused, err)
	}
	if len(l1.staging) != 0 || len(l1.registry) != 0 {
		t.Fatalf("private receipt used generic L1 projection: staging=%#v registry=%#v", l1.staging, l1.registry)
	}

	reviewed := item
	reviewed.Status = "reviewed"
	reviewed.Topic = "owner edited topic"
	reviewed.Summary = "owner edited summary"
	if err := store.SaveNewsKnowledgeItem(ctx, reviewed); err != nil {
		t.Fatalf("owner review update: %v", err)
	}
	if len(l1.staging) != 0 || len(l1.registry) != 0 {
		t.Fatalf("private review used generic L1 projection: staging=%#v registry=%#v", l1.staging, l1.registry)
	}
	ownerResults, err := base.Search(ctx, appkm.SearchRequest{
		Query:      reviewed.Topic,
		Scope:      appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: item.UserID},
		RecordType: newsKnowledgeRecordType,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("owner News search: %v", err)
	}
	if len(ownerResults) != 1 || ownerResults[0].RecordID != item.ItemID {
		t.Fatalf("owner News search = %#v", ownerResults)
	}
	otherResults, err := base.Search(ctx, appkm.SearchRequest{
		Query:      reviewed.Topic,
		Scope:      appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "other-owner"},
		RecordType: newsKnowledgeRecordType,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("other-owner News search: %v", err)
	}
	if len(otherResults) != 0 {
		t.Fatalf("private News visible to another owner: %#v", otherResults)
	}
	publicResults, err := base.Search(ctx, appkm.SearchRequest{
		Query:      reviewed.Topic,
		Scope:      appkm.SearchScope{Scope: appkm.SearchScopePublic},
		RecordType: newsKnowledgeRecordType,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("public News search: %v", err)
	}
	if len(publicResults) != 0 {
		t.Fatalf("private News visible in public search: %#v", publicResults)
	}
	reused, err := writer.SaveNewsKnowledgeItemWithReceipt(ctx, item, "gmail:private-l1", "shiro")
	if err != nil || !reused {
		t.Fatalf("private replay = reused %v, err %v", reused, err)
	}
	items, err := base.ListNewsKnowledgeItems(ctx, 20)
	if err != nil || len(items) != 1 || items[0].Status != "reviewed" {
		t.Fatalf("private replay changed current review: %#v, err %v", items, err)
	}
	if len(l1.staging) != 0 || len(l1.registry) != 0 {
		t.Fatalf("private replay used generic L1 projection: staging=%#v registry=%#v", l1.staging, l1.registry)
	}
}
