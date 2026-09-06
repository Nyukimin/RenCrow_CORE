package browsertrace

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreBrowserTraceToAPI(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	run := domaintrace.TraceRun{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", TracePath: "traces/trace_1", CreatedAt: now}
	candidate := domaintrace.APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}
	schema := domaintrace.APICandidateSchema{
		SchemaID:    "schema_1",
		CandidateID: "api_cand_1",
		SchemaType:  "response",
		SchemaJSON:  `{"type":"object"}`,
		SampleCount: 1,
		CreatedAt:   now,
	}
	validation := domaintrace.APICandidateValidationResult{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status: "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		CreatedAt: now,
	}
	coverage := domaintrace.APICoverageReport{ReportID: "coverage_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", CreatedAt: now}
	artifact := domaintrace.APIArtifact{
		ArtifactID: "art_openapi_1",
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Type:      "observed_openapi",
		Title:     "Observed OpenAPI",
		Status:    "generated",
		Content:   "openapi: 3.1.0",
		CreatedAt: now,
	}

	if err := store.SaveTraceRun(ctx, run); err != nil {
		t.Fatalf("SaveTraceRun() error = %v", err)
	}
	if err := store.SaveAPICandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveAPICandidate() error = %v", err)
	}
	if err := store.SaveAPICandidateSchema(ctx, schema); err != nil {
		t.Fatalf("SaveAPICandidateSchema() error = %v", err)
	}
	if err := store.SaveAPICandidateValidationResult(ctx, validation); err != nil {
		t.Fatalf("SaveAPICandidateValidationResult() error = %v", err)
	}
	if err := store.SaveAPICoverageReport(ctx, coverage); err != nil {
		t.Fatalf("SaveAPICoverageReport() error = %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, artifact); err != nil {
		t.Fatalf("SaveAPIArtifact() error = %v", err)
	}

	runs, err := store.ListTraceRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].RunID != "run_00000000-0000-5000-8000-000000000002" {
		t.Fatalf("ListTraceRuns() = %#v, %v", runs, err)
	}
	candidates, err := store.ListAPICandidates(ctx, 10)
	if err != nil || len(candidates) != 1 || candidates[0].CandidateID != "api_cand_1" {
		t.Fatalf("ListAPICandidates() = %#v, %v", candidates, err)
	}
	schemas, err := store.ListAPICandidateSchemas(ctx, 10)
	if err != nil || len(schemas) != 1 || schemas[0].SchemaID != "schema_1" {
		t.Fatalf("ListAPICandidateSchemas() = %#v, %v", schemas, err)
	}
	validations, err := store.ListAPICandidateValidationResults(ctx, 10)
	if err != nil || len(validations) != 1 || validations[0].ValidationID != "api_val_1" {
		t.Fatalf("ListAPICandidateValidationResults() = %#v, %v", validations, err)
	}
	reports, err := store.ListAPICoverageReports(ctx, 10)
	if err != nil || len(reports) != 1 || reports[0].ReportID != "coverage_1" {
		t.Fatalf("ListAPICoverageReports() = %#v, %v", reports, err)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_openapi_1" {
		t.Fatalf("ListAPIArtifacts() = %#v, %v", artifacts, err)
	}
}

func TestSQLiteStoreUsesCanonicalRunIDColumns(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tables := []string{"browser_trace_run", "api_candidate", "api_candidate_validation", "api_coverage_report", "api_artifact"}
	for _, table := range tables {
		rows, err := store.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		hasRunID := false
		hasLegacyID := false
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			hasRunID = hasRunID || name == "run_id"
			hasLegacyID = hasLegacyID || name == "trace_run_id"
		}
		rows.Close()
		if !hasRunID || hasLegacyID {
			t.Fatalf("%s columns: run_id=%t trace_run_id=%t", table, hasRunID, hasLegacyID)
		}
	}
}

func TestSQLiteStoreFindBrowserTraceByIDUsesExactPrimaryKeysAndPreservesOwnerAuditFields(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	candidate := domaintrace.APICandidate{
		CandidateID: "candidate-exact",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}
	validation := domaintrace.APICandidateValidationResult{
		ValidationID: "validation-exact",
		CandidateID:  candidate.CandidateID,
		TaskID:       candidate.TaskID,
		RunID:        candidate.RunID,
		ActorID:      candidate.ActorID,
		Passed:       false,
		Status:       "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		Reviewer:   "ren",
		ReviewNote: "terms checked",
		CreatedAt:  now,
	}
	if err := store.SaveAPICandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveAPICandidate() failed: %v", err)
	}
	if err := store.SaveAPICandidateValidationResult(ctx, validation); err != nil {
		t.Fatalf("SaveAPICandidateValidationResult() failed: %v", err)
	}

	gotCandidate, found, err := store.FindAPICandidateByID(ctx, candidate.CandidateID)
	if err != nil || !found || !reflect.DeepEqual(gotCandidate, candidate) {
		t.Fatalf("FindAPICandidateByID() = %#v, found=%v, err=%v", gotCandidate, found, err)
	}
	gotValidation, found, err := store.FindAPICandidateValidationResultByID(ctx, validation.ValidationID)
	if err != nil || !found || gotValidation.Reviewer != "ren" || gotValidation.ReviewNote != "terms checked" {
		t.Fatalf("FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", gotValidation, found, err)
	}
	if got, found, err := store.FindAPICandidateByID(ctx, "candidate-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidate{}) {
		t.Fatalf("missing FindAPICandidateByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindAPICandidateValidationResultByID(ctx, "validation-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidateValidationResult{}) {
		t.Fatalf("missing FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestSQLiteStoreFindBrowserTraceByIDRejectsMalformedPayload(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	candidate := domaintrace.APICandidate{
		CandidateID: "candidate-malformed",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC),
	}
	validation := domaintrace.APICandidateValidationResult{
		ValidationID: "validation-malformed",
		CandidateID:  candidate.CandidateID,
		TaskID:       candidate.TaskID,
		RunID:        candidate.RunID,
		ActorID:      candidate.ActorID,
		Passed:       false,
		Status:       "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		CreatedAt: candidate.CreatedAt,
	}
	if err := store.SaveAPICandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveAPICandidate() failed: %v", err)
	}
	if err := store.SaveAPICandidateValidationResult(ctx, validation); err != nil {
		t.Fatalf("SaveAPICandidateValidationResult() failed: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE api_candidate SET payload = ? WHERE candidate_id = ?`, "{malformed}", candidate.CandidateID); err != nil {
		t.Fatalf("corrupt candidate payload: %v", err)
	}
	if _, found, err := store.FindAPICandidateByID(ctx, candidate.CandidateID); err == nil || found {
		t.Fatalf("expected malformed candidate payload error, found=%v err=%v", found, err)
	}
	if _, err := store.db.Exec(`UPDATE api_candidate_validation SET payload = ? WHERE validation_id = ?`, "{malformed}", validation.ValidationID); err != nil {
		t.Fatalf("corrupt validation payload: %v", err)
	}
	if _, found, err := store.FindAPICandidateValidationResultByID(ctx, validation.ValidationID); err == nil || found {
		t.Fatalf("expected malformed validation payload error, found=%v err=%v", found, err)
	}
}

func TestSQLiteStoreConfiguresSingleConnectionAndBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections=%d want=1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout=%d want=5000", busyTimeout)
	}
}

func TestSQLiteStoreConcurrentWritesAreSerialized(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const writes = 64
	var wg sync.WaitGroup
	errs := make(chan error, writes)
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			err := store.SaveTraceRun(context.Background(), domaintrace.TraceRun{
				TaskID:    "tsk_00000000-0000-5000-8000-000000000001",
				RunID:     modulecore.RunID(fmt.Sprintf("run_00000000-0000-5000-8000-%012d", index)),
				ActorID:   "mio",
				TracePath: "traces/concurrent",
				CreatedAt: time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}
}
