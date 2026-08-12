package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	_ "modernc.org/sqlite"
)

type recordingSummaryRunner struct {
	calls chan struct{}
	step  time.Duration
}

func (r *recordingSummaryRunner) RunOnce(context.Context) (runtimePersonRelatedSummaryRunResult, error) {
	r.calls <- struct{}{}
	return runtimePersonRelatedSummaryRunResult{}, nil
}

func (r *recordingSummaryRunner) Interval() time.Duration { return r.step }

func TestStartRuntimePersonRelatedSummaryWorkerRunsImmediatelyAndStops(t *testing.T) {
	runner := &recordingSummaryRunner{calls: make(chan struct{}, 4), step: 10 * time.Millisecond}
	cancel := startRuntimePersonRelatedSummaryWorker(runner, backgroundJobFailureReporter{})
	select {
	case <-runner.calls:
	case <-time.After(time.Second):
		t.Fatal("summary worker did not run immediately")
	}
	cancel()
	before := len(runner.calls)
	time.Sleep(30 * time.Millisecond)
	if got := len(runner.calls); got != before {
		t.Fatalf("summary worker continued after cancel: before=%d after=%d", before, got)
	}
}

type fakeRuntimeSummaryCollector struct {
	result personrelatedcatalog.SummaryCollectionResult
	err    error
	got    personrelatedcatalog.SummaryCollectionRequest
}

type fakeSummaryTranslator struct{ got string }

func (t *fakeSummaryTranslator) TranslateDescription(_ context.Context, original, _ string) (string, error) {
	t.got = original
	return "日本語の概要", nil
}

func (f *fakeRuntimeSummaryCollector) CollectSummaries(_ context.Context, request personrelatedcatalog.SummaryCollectionRequest) (personrelatedcatalog.SummaryCollectionResult, error) {
	f.got = request
	return f.result, f.err
}

func TestRuntimePersonRelatedSummaryWorkerTranslatesDescriptionOnly(t *testing.T) {
	path := seedRuntimeSummaryJob(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	collector := &fakeRuntimeSummaryCollector{result: personrelatedcatalog.SummaryCollectionResult{Status: "ready", Patches: []personrelatedcatalog.SummaryPatch{{
		Category: personrelatedcatalog.CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1",
		CanonicalURL: "https://jpsearch.go.jp/item/1", EvidenceURL: "https://jpsearch.go.jp/item/1",
		DescriptionOriginal: "English description", DescriptionLanguage: "en",
		SourceStatus: "ready", TranslationStatus: "not_attempted", RetrievedAt: now.Format(time.RFC3339),
	}}}}
	translator := &fakeSummaryTranslator{}
	worker, err := prepareRuntimePersonRelatedSummaryWorker(path, collector, config.PersonRelatedCatalogSummaryWorkerConfig{Interval: "5m", BatchSize: 20, Lease: "2m", MaxAttempts: 3}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	worker.translator = translator
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if translator.got != "English description" {
		t.Fatalf("translated input=%q", translator.got)
	}
	db := openRuntimeSummaryDB(t, path)
	defer db.Close()
	var name, descriptionJA string
	if err := db.QueryRow(`SELECT display_name FROM hobby_related_items WHERE item_id='d1'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT description_ja FROM hobby_item_summaries WHERE category='drama' AND item_id='d1'`).Scan(&descriptionJA); err != nil {
		t.Fatal(err)
	}
	if name != "作品" || descriptionJA != "日本語の概要" {
		t.Fatalf("name=%q description_ja=%q", name, descriptionJA)
	}
}

func TestRuntimePersonRelatedSummaryWorkerAppliesExactPatch(t *testing.T) {
	path := seedRuntimeSummaryJob(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	collector := &fakeRuntimeSummaryCollector{result: personrelatedcatalog.SummaryCollectionResult{Status: "ready", Patches: []personrelatedcatalog.SummaryPatch{{
		Category: personrelatedcatalog.CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1",
		CanonicalURL: "https://jpsearch.go.jp/item/1", EvidenceURL: "https://jpsearch.go.jp/item/1",
		DescriptionOriginal: "日本語概要", DescriptionLanguage: "ja", DescriptionJA: "日本語概要",
		SourceStatus: "ready", TranslationStatus: "not_required", RetrievedAt: now.Format(time.RFC3339),
	}}}}
	worker, err := prepareRuntimePersonRelatedSummaryWorker(path, collector, config.PersonRelatedCatalogSummaryWorkerConfig{Interval: "5m", BatchSize: 20, Lease: "2m", MaxAttempts: 3}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 1 || result.Ready != 1 || len(collector.got.Targets) != 1 || collector.got.Targets[0].ItemID != "d1" {
		t.Fatalf("result=%#v request=%#v", result, collector.got)
	}
	db := openRuntimeSummaryDB(t, path)
	defer db.Close()
	job, err := personrelatedcatalog.GetSummaryJob(context.Background(), db, personrelatedcatalog.CategoryDrama, "d1")
	if err != nil || job.State != personrelatedcatalog.SummaryJobReady {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}

func TestRuntimePersonRelatedSummaryWorkerMarksDeadAfterBoundedFailure(t *testing.T) {
	path := seedRuntimeSummaryJob(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	collector := &fakeRuntimeSummaryCollector{err: errors.New("provider unavailable")}
	worker, err := prepareRuntimePersonRelatedSummaryWorker(path, collector, config.PersonRelatedCatalogSummaryWorkerConfig{Interval: "5m", BatchSize: 20, Lease: "2m", MaxAttempts: 1}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err == nil || result.Dead != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	db := openRuntimeSummaryDB(t, path)
	defer db.Close()
	job, getErr := personrelatedcatalog.GetSummaryJob(context.Background(), db, personrelatedcatalog.CategoryDrama, "d1")
	if getErr != nil || job.State != personrelatedcatalog.SummaryJobDead {
		t.Fatalf("job=%#v err=%v", job, getErr)
	}
}

func seedRuntimeSummaryJob(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hobby.sqlite")
	db := openRuntimeSummaryDB(t, path)
	defer db.Close()
	ctx := context.Background()
	if err := personrelatedcatalog.EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO hobby_related_items(item_id,category,item_type,display_name,name_original,name_state,source_record_id,canonical_url,source,description_translation_state) VALUES('d1','drama','series','作品','作品','source_ja','jp:1','https://jpsearch.go.jp/item/1','jpsearch','not_attempted')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := personrelatedcatalog.EnqueueSummaryJob(ctx, db, personrelatedcatalog.SummaryJob{Category: personrelatedcatalog.CategoryDrama, ItemID: "d1", Source: "jpsearch", SourceRecordID: "jp:1", CanonicalURL: "https://jpsearch.go.jp/item/1", AvailableAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	return path
}

func openRuntimeSummaryDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
