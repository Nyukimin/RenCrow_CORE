package browsertrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
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
	coverage := domaintrace.APICoverageReport{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
		Kind:       modulecore.ArtifactKindReport,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		ObservedFlows:         []string{"network_trace"},
		ObservedEndpoints:     []string{"GET /api/items"},
		MissingFlows:          []string{"error cases"},
		RecommendedNextTraces: []string{"empty result"},
		// Digest of the four body lists in the order the browsertrace domain declares.
		ContentHash:  "sha256:a955188307e1b940fd000e4a418e04cb694a7c3d0c01fe0c988bddd8634c1028",
		SupersededBy: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b"),
		CreatedAt:    now,
	}
	artifact := domaintrace.APIArtifact{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a"),
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		WorkstreamID: "ws_1",
		Type:         "observed_openapi",
		Title:        "Observed OpenAPI",
		Status:       "generated",
		Content:      "openapi: 3.1.0",
		ContentHash:  "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5",
		CreatedAt:    now,
	}
	// The edge is not something a Save may bring in: the successor row is saved as
	// its own row and the edge is established by the owner SupersedeAPIArtifact call.
	successor := artifact
	successor.ArtifactID = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	successor.Title = "Observed OpenAPI revised"
	successor.Content = supersedeContentB
	successor.ContentHash = supersedeHashB

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
	if err := store.SaveAPIArtifact(ctx, successor); err != nil {
		t.Fatalf("SaveAPIArtifact(successor) error = %v", err)
	}
	if err := store.SupersedeAPIArtifact(ctx, artifact.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact() error = %v", err)
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
	if err != nil || len(reports) != 1 || reports[0].ArtifactID != modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003") {
		t.Fatalf("ListAPICoverageReports() = %#v, %v", reports, err)
	}
	// The SQLite payload must keep the whole coverage report, not only its canonical
	// identity: the four body lists, owner fields, content digest and supersession
	// survive the round trip, so a reloaded row is still verifiable against its digest.
	if !reflect.DeepEqual(reports[0], coverage) {
		t.Errorf("ListAPICoverageReports() round trip = %#v, want %#v", reports[0], coverage)
	}
	if !reports[0].CreatedAt.Equal(now) {
		t.Errorf("ListAPICoverageReports() created_at = %v, want %v", reports[0].CreatedAt, now)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("ListAPIArtifacts() = %#v, %v", artifacts, err)
	}
	// Located by ID so the assertion does not depend on list ordering.
	stored := mustFindSupersedeArtifact(t, store, artifact.ArtifactID)
	storedSuccessor := mustFindSupersedeArtifact(t, store, successor.ArtifactID)
	if stored.ArtifactID != modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a") || stored.Kind != modulecore.ArtifactKindSpecification {
		t.Errorf("ListAPIArtifacts() stored the wrong artifact identity: %#v", stored)
	}
	// The stored payload must keep the whole artifact, not only its canonical
	// identity: content role, content body and owner fields survive the round trip,
	// and the supersede leaves every other field of the predecessor alone.
	expectedPredecessor := artifact
	expectedPredecessor.SupersededBy = successor.ArtifactID
	if !reflect.DeepEqual(stored, expectedPredecessor) {
		t.Errorf("ListAPIArtifacts() round trip = %#v, want %#v", stored, expectedPredecessor)
	}
	if !reflect.DeepEqual(storedSuccessor, successor) {
		t.Errorf("supersede did not preserve the successor row: got %#v want %#v", storedSuccessor, successor)
	}
	if stored.Type != "observed_openapi" || stored.Title != "Observed OpenAPI" ||
		stored.Status != "generated" || stored.Content != "openapi: 3.1.0" ||
		stored.TaskID != modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001") ||
		stored.RunID != modulecore.RunID("run_00000000-0000-5000-8000-000000000002") ||
		stored.ActorID != "mio" || stored.WorkstreamID != "ws_1" ||
		stored.ContentHash != "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5" ||
		stored.SupersededBy != modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b") ||
		!stored.CreatedAt.Equal(now) {
		t.Errorf("ListAPIArtifacts() lost artifact fields, got %#v", stored)
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

// TestSQLiteStoreRejectsCoverageReportWithoutItsOwnDigest pins the SQLite write
// boundary: a coverage report whose content digest is missing, or whose digest belongs
// to bytes other than the stored body, never reaches the table. A digest that cannot be
// reproduced from the stored row would make any later verification meaningless.
func TestSQLiteStoreRejectsCoverageReportWithoutItsOwnDigest(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	base := domaintrace.APICoverageReport{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
		Kind:       modulecore.ArtifactKindReport,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		ObservedEndpoints: []string{"GET /api/items"},
		CreatedAt:         now,
	}
	withoutDigest := base
	withoutDigest.ContentHash = ""
	if err := store.SaveAPICoverageReport(ctx, withoutDigest); err == nil {
		t.Error("SaveAPICoverageReport() accepted a report without content_hash")
	}
	otherDigest := base
	otherDigest.ContentHash = domaintrace.ComputeAPICoverageReportContentHash(domaintrace.APICoverageReport{
		ObservedEndpoints: []string{"GET /other"},
	})
	if err := store.SaveAPICoverageReport(ctx, otherDigest); err == nil {
		t.Error("SaveAPICoverageReport() accepted a digest of other bytes")
	}
	if reports, err := store.ListAPICoverageReports(ctx, 10); err != nil || len(reports) != 0 {
		t.Fatalf("ListAPICoverageReports() = %#v, %v, want nothing stored", reports, err)
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

// Establishing a supersession edge is the owner Supersede operation's job. A
// plain Save of a brand new artifact row cannot establish one, because the
// successor row may not exist and nothing has been verified: it must be
// rejected and leave the store unchanged.
func TestSQLiteStoreSaveAPIArtifactRejectsNewRowWithSupersededBy(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	artifact := domaintrace.APIArtifact{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a"),
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		WorkstreamID: "ws_1",
		Type:         "observed_openapi",
		Title:        "Observed OpenAPI",
		Status:       "generated",
		Content:      "openapi: 3.1.0",
		ContentHash:  "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5",
		SupersededBy: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b"),
		CreatedAt:    now,
	}

	if err := store.SaveAPIArtifact(ctx, artifact); err == nil {
		t.Errorf("SaveAPIArtifact() accepted a new artifact row carrying superseded_by %q; only the owner supersede operation may establish that edge", artifact.SupersededBy)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("ListAPIArtifacts() = %d rows after the rejected save, want 0 rows", len(artifacts))
	}
}

// The supersession edge is owned by the store operation, not by a payload that a
// caller happens to write: two artifacts that already exist get the edge from a
// real SupersedeAPIArtifact call, and nothing else about either row moves. The
// predecessor keeps its identity, content role, body, digest and created_at, and
// the successor row survives untouched.
func TestSQLiteStoreSupersedeAPIArtifactEstablishesEdgeAndPreservesRows(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	predecessor := domaintrace.APIArtifact{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a"),
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		WorkstreamID: "ws_1",
		Type:         "observed_openapi",
		Title:        "Observed OpenAPI",
		Status:       "generated",
		Content:      "openapi: 3.1.0",
		ContentHash:  "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5",
		CreatedAt:    now,
	}
	successor := predecessor
	successor.ArtifactID = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	successor.Title = "Observed OpenAPI revised"
	successor.Content = "openapi: 3.1.0\ninfo:\n  title: successor\n"
	successor.ContentHash = "sha256:c22aaaecbfdc8bfafbe86c0a10b78ace43ba9c06bbffd1258b372d0562511580"

	for _, item := range []domaintrace.APIArtifact{predecessor, successor} {
		if err := store.SaveAPIArtifact(ctx, item); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", item.ArtifactID, err)
		}
	}

	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact() error = %v", err)
	}

	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("ListAPIArtifacts() = %d rows after supersede, want 2 rows", len(artifacts))
	}
	var gotPredecessor, gotSuccessor domaintrace.APIArtifact
	for _, item := range artifacts {
		switch item.ArtifactID {
		case predecessor.ArtifactID:
			gotPredecessor = item
		case successor.ArtifactID:
			gotSuccessor = item
		}
	}

	// The edge is the only field that changes on the predecessor.
	wantPredecessor := predecessor
	wantPredecessor.SupersededBy = successor.ArtifactID
	if !reflect.DeepEqual(gotPredecessor, wantPredecessor) {
		t.Errorf("predecessor row after supersede = %#v, want %#v", gotPredecessor, wantPredecessor)
	}
	if gotPredecessor.Kind != modulecore.ArtifactKindSpecification ||
		gotPredecessor.Type != "observed_openapi" ||
		gotPredecessor.Content != "openapi: 3.1.0" ||
		gotPredecessor.ContentHash != "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5" ||
		!gotPredecessor.CreatedAt.Equal(now) {
		t.Errorf("predecessor lost its identity, content or digest: %#v", gotPredecessor)
	}
	// The successor is stored exactly as written and carries no edge of its own.
	if !reflect.DeepEqual(gotSuccessor, successor) {
		t.Errorf("successor row changed = %#v, want %#v", gotSuccessor, successor)
	}
}

// Stage 2 fixtures: three artifacts that all sit in one scope and whose digests
// match their own content, so a rejection can only come from the supersession
// rules and never from an unrelated invalid row.
const (
	supersedeContentA = "openapi: 3.1.0"
	supersedeHashA    = "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5"
	supersedeContentB = "openapi: 3.1.0\ninfo:\n  title: successor\n"
	supersedeHashB    = "sha256:c22aaaecbfdc8bfafbe86c0a10b78ace43ba9c06bbffd1258b372d0562511580"
	supersedeContentC = "openapi: 3.1.0\ninfo:\n  title: third\n"
	supersedeHashC    = "sha256:d599d7890bd52333027ca26b0ee3aa708c724b209433e1af43eae6f999914785"
)

func supersedeFixtureID(suffix string) modulecore.ArtifactID {
	return modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000" + suffix)
}

func supersedeFixtureArtifact(suffix, content, contentHash string) domaintrace.APIArtifact {
	return domaintrace.APIArtifact{
		ArtifactID:   supersedeFixtureID(suffix),
		Kind:         modulecore.ArtifactKindSpecification,
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001",
		RunID:        "run_00000000-0000-5000-8000-000000000002",
		ActorID:      "mio",
		WorkstreamID: "ws_1",
		Type:         "observed_openapi",
		Title:        "Observed OpenAPI",
		Status:       "generated",
		Content:      content,
		ContentHash:  contentHash,
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
}

func newSupersedeStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newSupersedeStoreAt opens an independent store over an existing database file,
// so a test can exercise two writers that share one file rather than one pool.
func newSupersedeStoreAt(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(%s) error = %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustSaveSupersedeArtifacts(t *testing.T, store *SQLiteStore, items ...domaintrace.APIArtifact) {
	t.Helper()
	for _, item := range items {
		if err := store.SaveAPIArtifact(context.Background(), item); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", item.ArtifactID, err)
		}
	}
}

// snapshotAPIArtifactRows captures every api_artifact row including its columns,
// so a rejected supersede can be proven to have changed nothing at all.
func snapshotAPIArtifactRows(t *testing.T, store *SQLiteStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT artifact_id, run_id, created_at, payload FROM api_artifact ORDER BY artifact_id`)
	if err != nil {
		t.Fatalf("snapshot api_artifact: %v", err)
	}
	defer rows.Close()
	snapshot := []string{}
	for rows.Next() {
		var artifactID, runID, createdAt, payload string
		if err := rows.Scan(&artifactID, &runID, &createdAt, &payload); err != nil {
			t.Fatalf("scan api_artifact snapshot: %v", err)
		}
		snapshot = append(snapshot, strings.Join([]string{artifactID, runID, createdAt, payload}, "\x00"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate api_artifact snapshot: %v", err)
	}
	return snapshot
}

// mustFindSupersedeArtifact reads one artifact back by its own ID, so an assertion
// never depends on how the list happens to be ordered.
func mustFindSupersedeArtifact(t *testing.T, store *SQLiteStore, id modulecore.ArtifactID) domaintrace.APIArtifact {
	t.Helper()
	artifacts, err := store.ListAPIArtifacts(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	for _, item := range artifacts {
		if item.ArtifactID == id {
			return item
		}
	}
	t.Fatalf("artifact %s is not in api_artifact: %q", id, artifacts)
	return domaintrace.APIArtifact{}
}

// The predecessor and the successor are two distinct artifacts, so an edge between
// them only holds when they share task, run, actor, workstream and content role.
// The content-role axis is checked on its own as well: two browsertrace roles can
// share one kind (coverage_report and risk_assessment are both reports), so a
// comparison that only looked at the stored kind would accept an edge between two
// different artifacts.
func TestSQLiteStoreSupersedeAPIArtifactRejectsScopeMismatch(t *testing.T) {
	tests := []struct {
		name   string
		adjust func(previous, next *domaintrace.APIArtifact)
	}{
		{"task_id", func(_ *domaintrace.APIArtifact, next *domaintrace.APIArtifact) {
			next.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
		}},
		{"run_id", func(_ *domaintrace.APIArtifact, next *domaintrace.APIArtifact) {
			next.RunID = "run_00000000-0000-5000-8000-000000000008"
		}},
		{"actor_id", func(_ *domaintrace.APIArtifact, next *domaintrace.APIArtifact) { next.ActorID = "shiro" }},
		{"workstream_id", func(_ *domaintrace.APIArtifact, next *domaintrace.APIArtifact) { next.WorkstreamID = "ws_2" }},
		{
			"content_role with its own valid kind",
			func(_ *domaintrace.APIArtifact, next *domaintrace.APIArtifact) {
				next.Type = "endpoint_inventory"
				next.Kind = modulecore.ArtifactKindDocument
			},
		},
		{
			"content_role that shares the same kind",
			func(previous, next *domaintrace.APIArtifact) {
				previous.Type = "coverage_report"
				previous.Kind = modulecore.ArtifactKindReport
				next.Type = "risk_assessment"
				next.Kind = modulecore.ArtifactKindReport
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			tc.adjust(&predecessor, &successor)
			// Both adjusted rows must still be valid artifacts on their own, otherwise
			// the store would reject them for a reason unrelated to scope.
			for _, item := range []domaintrace.APIArtifact{predecessor, successor} {
				if err := domaintrace.ValidateAPIArtifact(item); err != nil {
					t.Fatalf("test fixture row %s became invalid: %v", item.ArtifactID, err)
				}
			}
			if predecessor.Kind != successor.Kind && tc.name == "content_role that shares the same kind" {
				t.Fatalf("test fixture stopped exercising two roles under one kind")
			}
			mustSaveSupersedeArtifacts(t, store, predecessor, successor)
			before := snapshotAPIArtifactRows(t, store)

			if err := store.SupersedeAPIArtifact(context.Background(), predecessor.ArtifactID, successor.ArtifactID); err == nil {
				t.Errorf("SupersedeAPIArtifact() accepted an edge whose %s differs between the two rows", tc.name)
			}

			if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
				t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// Two artifacts that share a content role always share its kind, so a stored row
// whose kind disagrees with its own content role can only be an inconsistent row.
// Such a row cannot be a supersession partner, and the rejection must not touch it.
func TestSQLiteStoreSupersedeAPIArtifactRejectsStoredKindDisagreement(t *testing.T) {
	store := newSupersedeStore(t)
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	mustSaveSupersedeArtifacts(t, store, predecessor, successor)

	corrupt := successor
	corrupt.Kind = modulecore.ArtifactKindReport
	payload, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatalf("encode inconsistent successor: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), successor.ArtifactID); err != nil {
		t.Fatalf("inject mismatched kind: %v", err)
	}
	before := snapshotAPIArtifactRows(t, store)

	if err := store.SupersedeAPIArtifact(context.Background(), predecessor.ArtifactID, successor.ArtifactID); err == nil {
		t.Error("SupersedeAPIArtifact() accepted a successor whose stored kind contradicts its content role")
	}
	if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
		t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
	}
}

// The chain is built with the real operation, so a third edge back to the first
// artifact would close a cycle over three nodes. A cycle is not a supersession
// chain, and refusing it must leave both established edges exactly as they are.
func TestSQLiteStoreSupersedeAPIArtifactRejectsThreeNodeCycle(t *testing.T) {
	store := newSupersedeStore(t)
	first := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	second := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	third := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
	mustSaveSupersedeArtifacts(t, store, first, second, third)
	ctx := context.Background()

	if err := store.SupersedeAPIArtifact(ctx, first.ArtifactID, second.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(first, second) error = %v", err)
	}
	if err := store.SupersedeAPIArtifact(ctx, second.ArtifactID, third.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(second, third) error = %v", err)
	}
	before := snapshotAPIArtifactRows(t, store)

	if err := store.SupersedeAPIArtifact(ctx, third.ArtifactID, first.ArtifactID); err == nil {
		t.Error("SupersedeAPIArtifact(third, first) accepted a cycle over three nodes")
	}
	if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
		t.Errorf("rejected cycle changed rows:\nbefore %q\nafter  %q", before, after)
	}

	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	chains := map[modulecore.ArtifactID]modulecore.ArtifactID{}
	for _, item := range artifacts {
		chains[item.ArtifactID] = item.SupersededBy
	}
	if chains[first.ArtifactID] != second.ArtifactID || chains[second.ArtifactID] != third.ArtifactID || chains[third.ArtifactID] != "" {
		t.Errorf("established chain changed after the rejected cycle: %q", chains)
	}
}

// Every row below the successor belongs to the chain the new edge joins, so a
// chain whose tail row is missing, will not decode, or whose content no longer
// matches its stored digest leaves the whole edge unverifiable. A tail row that is
// perfectly valid on its own yet sits in another task is the same problem: the
// chain no longer describes one artifact. Every case corrupts the deeper node C,
// which a check of the two named rows alone cannot see, and each has to be refused
// without mutating anything.
func TestSQLiteStoreSupersedeAPIArtifactRejectsBrokenSuccessorChain(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, store *SQLiteStore, chainTail domaintrace.APIArtifact)
	}{
		{
			"chain points at a row that does not exist",
			func(t *testing.T, store *SQLiteStore, chainTail domaintrace.APIArtifact) {
				if _, err := store.db.Exec(`DELETE FROM api_artifact WHERE artifact_id = ?`, chainTail.ArtifactID); err != nil {
					t.Fatalf("delete chain tail row: %v", err)
				}
			},
		},
		{
			"chain payload will not decode",
			func(t *testing.T, store *SQLiteStore, chainTail domaintrace.APIArtifact) {
				if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, "{malformed}", chainTail.ArtifactID); err != nil {
					t.Fatalf("inject malformed chain payload: %v", err)
				}
			},
		},
		{
			"chain content no longer matches its stored digest",
			func(t *testing.T, store *SQLiteStore, chainTail domaintrace.APIArtifact) {
				edited := chainTail
				edited.Content = supersedeContentA
				payload, err := json.Marshal(edited)
				if err != nil {
					t.Fatalf("encode edited chain tail: %v", err)
				}
				if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), chainTail.ArtifactID); err != nil {
					t.Fatalf("inject mismatched digest: %v", err)
				}
			},
		},
		{
			"chain row belongs to another task",
			func(t *testing.T, store *SQLiteStore, chainTail domaintrace.APIArtifact) {
				relocated := chainTail
				relocated.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
				// The digest covers the content body only, so this row stays internally
				// valid: only its scope moved, and the row itself is not corrupt.
				if err := domaintrace.ValidateAPIArtifact(relocated); err != nil {
					t.Fatalf("test fixture row should stay valid: %v", err)
				}
				payload, err := json.Marshal(relocated)
				if err != nil {
					t.Fatalf("encode relocated chain tail: %v", err)
				}
				if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), chainTail.ArtifactID); err != nil {
					t.Fatalf("inject relocated chain tail: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			tail := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
			mustSaveSupersedeArtifacts(t, store, predecessor, successor, tail)
			ctx := context.Background()
			// The successor already carries an edge of its own, established by the real
			// operation, and that tail chain is what a new edge above it must keep whole.
			if err := store.SupersedeAPIArtifact(ctx, successor.ArtifactID, tail.ArtifactID); err != nil {
				t.Fatalf("test fixture: SupersedeAPIArtifact(successor, tail) error = %v", err)
			}

			tc.corrupt(t, store, tail)
			before := snapshotAPIArtifactRows(t, store)

			if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err == nil {
				t.Error("SupersedeAPIArtifact() accepted an edge onto a broken successor chain")
			}
			if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
				t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// An edge needs both ends. A missing row, a self reference, an ID that is not a
// canonical artifact ID and an empty successor are all refused, and the two rows
// that do exist keep their exact stored bytes.
func TestSQLiteStoreSupersedeAPIArtifactRejectsMissingAndInvalidTargets(t *testing.T) {
	predecessorID := supersedeFixtureID("a")
	successorID := supersedeFixtureID("b")
	tests := []struct {
		name          string
		predecessorID modulecore.ArtifactID
		successorID   modulecore.ArtifactID
	}{
		{"predecessor row is absent", supersedeFixtureID("e"), successorID},
		{"successor row is absent", predecessorID, supersedeFixtureID("e")},
		{"artifact supersedes itself", predecessorID, predecessorID},
		{"successor id is not canonical", predecessorID, modulecore.ArtifactID("art_1")},
		{"predecessor id is not canonical", modulecore.ArtifactID("art_1"), successorID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			mustSaveSupersedeArtifacts(t, store,
				supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
				supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
			)
			before := snapshotAPIArtifactRows(t, store)

			if err := store.SupersedeAPIArtifact(context.Background(), tc.predecessorID, tc.successorID); err == nil {
				t.Errorf("SupersedeAPIArtifact(%q, %q) succeeded", tc.predecessorID, tc.successorID)
			}
			if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
				t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// The api_artifact columns are what the owner indexes and reads back, so a row
// whose stored payload names another artifact, or whose run_id column disagrees
// with the run inside its payload, cannot be trusted for a chain check. It has to
// fail closed instead of being silently accepted.
func TestSQLiteStoreSupersedeAPIArtifactFailsClosedOnColumnPayloadDisagreement(t *testing.T) {
	t.Run("payload names another artifact", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
		successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
		mustSaveSupersedeArtifacts(t, store, predecessor, successor)

		mislabelled := predecessor
		mislabelled.ArtifactID = successor.ArtifactID
		payload, err := json.Marshal(mislabelled)
		if err != nil {
			t.Fatalf("encode mislabelled payload: %v", err)
		}
		if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), predecessor.ArtifactID); err != nil {
			t.Fatalf("inject mislabelled payload: %v", err)
		}
		before := snapshotAPIArtifactRows(t, store)

		if err := store.SupersedeAPIArtifact(context.Background(), predecessor.ArtifactID, successor.ArtifactID); err == nil {
			t.Error("SupersedeAPIArtifact() accepted a row whose payload holds another artifact_id")
		}
		if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
			t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
		}
	})

	t.Run("run_id column disagrees with payload", func(t *testing.T) {
		store := newSupersedeStore(t)
		predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
		successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
		mustSaveSupersedeArtifacts(t, store, predecessor, successor)

		if _, err := store.db.Exec(`UPDATE api_artifact SET run_id = ? WHERE artifact_id = ?`, "run_00000000-0000-5000-8000-000000000008", predecessor.ArtifactID); err != nil {
			t.Fatalf("inject disagreeing run_id column: %v", err)
		}
		before := snapshotAPIArtifactRows(t, store)

		if err := store.SupersedeAPIArtifact(context.Background(), predecessor.ArtifactID, successor.ArtifactID); err == nil {
			t.Error("SupersedeAPIArtifact() accepted a row whose run_id column disagrees with its payload")
		}
		if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
			t.Errorf("rejected supersede changed rows:\nbefore %q\nafter  %q", before, after)
		}
	})
}

// Retrying the edge that is already there must succeed without rewriting a byte,
// so a client that lost the response cannot drift the chain. A retry aimed at a
// different successor is a conflict: it is refused and leaves the edge alone.
func TestSQLiteStoreSupersedeAPIArtifactRepeatSameEdgeAndRejectsDifferentSuccessor(t *testing.T) {
	store := newSupersedeStore(t)
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	other := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
	mustSaveSupersedeArtifacts(t, store, predecessor, successor, other)
	ctx := context.Background()

	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact() error = %v", err)
	}
	afterEdge := snapshotAPIArtifactRows(t, store)

	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Errorf("repeating the same edge errored: %v", err)
	}
	if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, afterEdge) {
		t.Errorf("repeating the same edge changed rows:\nafter edge %q\nafter retry %q", afterEdge, after)
	}

	before := snapshotAPIArtifactRows(t, store)
	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, other.ArtifactID); err == nil {
		t.Error("SupersedeAPIArtifact() moved an artifact that already had a different successor")
	}
	if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
		t.Errorf("rejected conflicting supersede changed rows:\nbefore %q\nafter  %q", before, after)
	}

	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	for _, item := range artifacts {
		if item.ArtifactID != predecessor.ArtifactID {
			continue
		}
		want := predecessor
		want.SupersededBy = successor.ArtifactID
		if !reflect.DeepEqual(item, want) {
			t.Errorf("predecessor row = %#v, want %#v", item, want)
		}
	}
}

// An ordinary Save carries a whole artifact, so it always carries a supersession
// edge too: the one that is already stored, or a stale/empty one from a caller that
// read the row earlier. Only the owner Supersede operation may add, move or clear
// that edge, otherwise a writer holding an older copy could silently detach a chain
// that a supersede had already established. Keeping exactly the stored edge while
// legitimately updating content or metadata stays allowed, because the APIArtifact
// owner does update rows in place and there is no global immutability rule.
func TestSQLiteStoreSaveAPIArtifactRejectsSupersededByChangesOnExistingRow(t *testing.T) {
	tests := []struct {
		name         string
		establish    bool
		adjust       func(row *domaintrace.APIArtifact)
		wantRejected bool
	}{
		{
			// The row exists with no edge and no supersede has run, so a plain Save
			// carrying a successor cannot establish one.
			name:         "adds an edge to a row that has none",
			adjust:       func(row *domaintrace.APIArtifact) { row.SupersededBy = supersedeFixtureID("b") },
			wantRejected: true,
		},
		{
			// The chain already points at b; a Save pointing it at c is a moved edge.
			name:         "moves an established edge to another successor",
			establish:    true,
			adjust:       func(row *domaintrace.APIArtifact) { row.SupersededBy = supersedeFixtureID("c") },
			wantRejected: true,
		},
		{
			// A caller that read the row before the supersede sends the old empty edge.
			name:         "clears an established edge with a stale empty successor",
			establish:    true,
			adjust:       func(row *domaintrace.APIArtifact) { row.SupersededBy = "" },
			wantRejected: true,
		},
		{
			// Metadata and body updates are the owner's normal in-place updates, and the
			// stored digest is recomputed for the new body.
			name:      "keeps the stored edge while updating body and title",
			establish: true,
			adjust: func(row *domaintrace.APIArtifact) {
				row.Title = "Observed OpenAPI revised"
				row.Status = "draft"
				row.Content = supersedeContentB
				row.ContentHash = supersedeHashB
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSupersedeStore(t)
			ctx := context.Background()
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			mustSaveSupersedeArtifacts(t, store, predecessor, successor)
			if tc.establish {
				if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("test fixture: SupersedeAPIArtifact(predecessor, successor) error = %v", err)
				}
			}

			// Start from the row as it is actually stored, so the only difference between
			// the incoming and the stored row is the edge under test.
			updated := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID)
			tc.adjust(&updated)
			if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
				t.Fatalf("test fixture row became invalid: %v", err)
			}
			before := snapshotAPIArtifactRows(t, store)

			err := store.SaveAPIArtifact(ctx, updated)
			if tc.wantRejected && err == nil {
				t.Errorf("SaveAPIArtifact() accepted superseded_by %q on the existing row %s; only SupersedeAPIArtifact may add, move or clear that edge", updated.SupersededBy, updated.ArtifactID)
			}
			if !tc.wantRejected && err != nil {
				t.Errorf("SaveAPIArtifact() rejected a legitimate update that keeps the stored edge: %v", err)
			}

			after := snapshotAPIArtifactRows(t, store)
			if tc.wantRejected {
				if !reflect.DeepEqual(after, before) {
					t.Errorf("rejected save changed rows:\nbefore %q\nafter  %q", before, after)
				}
				return
			}
			if reflect.DeepEqual(after, before) {
				t.Fatalf("accepted save wrote nothing: %q", after)
			}
			got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID)
			// The edge survives the update, and so do identity, role, digest owner fields.
			if got.SupersededBy != successor.ArtifactID {
				t.Errorf("legitimate save dropped the stored edge: %#v", got)
			}
			if got.ArtifactID != predecessor.ArtifactID || got.Kind != predecessor.Kind ||
				got.TaskID != predecessor.TaskID || got.RunID != predecessor.RunID ||
				got.ActorID != predecessor.ActorID || got.WorkstreamID != predecessor.WorkstreamID ||
				got.Type != predecessor.Type || !got.CreatedAt.Equal(predecessor.CreatedAt) {
				t.Errorf("legitimate save moved the row out of its scope: %#v", got)
			}
			if got.Title != updated.Title || got.Status != updated.Status ||
				got.Content != updated.Content || got.ContentHash != updated.ContentHash {
				t.Errorf("legitimate update did not persist the new body or metadata: got %#v want %#v", got, updated)
			}
		})
	}
}

// A supersession chain is only meaningful while every row in it describes one
// artifact, so an established chain would be invalidated if an ordinary Save moved
// an existing artifact_id into another task, run, actor, workstream, content role or
// kind. Both ends are tried: the row that points onward and the row that already has
// an incoming edge, because either move breaks the same checked chain.
func TestSQLiteStoreSaveAPIArtifactRejectsOwnerScopeChange(t *testing.T) {
	tests := []struct {
		name   string
		init   func(previous, next *domaintrace.APIArtifact)
		adjust func(row *domaintrace.APIArtifact)
	}{
		{"task_id", nil, func(row *domaintrace.APIArtifact) { row.TaskID = "tsk_00000000-0000-5000-8000-000000000009" }},
		{"run_id", nil, func(row *domaintrace.APIArtifact) { row.RunID = "run_00000000-0000-5000-8000-000000000008" }},
		{"actor_id", nil, func(row *domaintrace.APIArtifact) { row.ActorID = "shiro" }},
		{"workstream_id", nil, func(row *domaintrace.APIArtifact) { row.WorkstreamID = "ws_2" }},
		{
			"content role with its own valid kind",
			nil,
			func(row *domaintrace.APIArtifact) {
				row.Type = "endpoint_inventory"
				row.Kind = modulecore.ArtifactKindDocument
			},
		},
		{
			// Both rows start as coverage_report (kind report) so only the content role
			// moves, to risk_assessment, which shares that same kind. A comparison that
			// looked at the kind alone would let the row leave its artifact.
			"content role that shares the same kind",
			func(previous, next *domaintrace.APIArtifact) {
				previous.Type = "coverage_report"
				previous.Kind = modulecore.ArtifactKindReport
				next.Type = "coverage_report"
				next.Kind = modulecore.ArtifactKindReport
			},
			func(row *domaintrace.APIArtifact) {
				row.Type = "risk_assessment"
				row.Kind = modulecore.ArtifactKindReport
			},
		},
	}
	for _, tc := range tests {
		for _, end := range []struct {
			name  string
			token string
		}{{"the row that carries the edge", "a"}, {"the row with an incoming edge", "b"}} {
			t.Run(tc.name+" on "+end.name, func(t *testing.T) {
				store := newSupersedeStore(t)
				ctx := context.Background()
				predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
				successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
				if tc.init != nil {
					tc.init(&predecessor, &successor)
					for _, item := range []domaintrace.APIArtifact{predecessor, successor} {
						if err := domaintrace.ValidateAPIArtifact(item); err != nil {
							t.Fatalf("test fixture row %s became invalid: %v", item.ArtifactID, err)
						}
					}
				}
				mustSaveSupersedeArtifacts(t, store, predecessor, successor)
				if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("test fixture: SupersedeAPIArtifact error = %v", err)
				}

				// Read the stored row back first, so the incoming row differs from it only
				// in the scope field under test and never in the already stored edge.
				targetID := predecessor.ArtifactID
				if end.token == "b" {
					targetID = successor.ArtifactID
				}
				updated := mustFindSupersedeArtifact(t, store, targetID)
				tc.adjust(&updated)
				if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
					t.Fatalf("test fixture row became invalid: %v", err)
				}
				before := snapshotAPIArtifactRows(t, store)

				if err := store.SaveAPIArtifact(ctx, updated); err == nil {
					t.Errorf("SaveAPIArtifact() moved the existing artifact %s to a different %s, which invalidates the checked chain", updated.ArtifactID, tc.name)
				}
				if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
					t.Errorf("rejected save changed rows:\nbefore %q\nafter  %q", before, after)
				}
			})
		}
	}
}

// The guard reads the stored row, so a row that cannot be verified cannot be
// overwritten by a Save that would otherwise look legitimate: the corrupt row fails
// closed instead of being silently replaced.
func TestSQLiteStoreSaveAPIArtifactFailsClosedOnCorruptExistingRow(t *testing.T) {
	store := newSupersedeStore(t)
	ctx := context.Background()
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	mustSaveSupersedeArtifacts(t, store, predecessor)

	if _, err := store.db.Exec(`UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, "{malformed}", predecessor.ArtifactID); err != nil {
		t.Fatalf("inject malformed payload: %v", err)
	}
	before := snapshotAPIArtifactRows(t, store)

	if err := store.SaveAPIArtifact(ctx, predecessor); err == nil {
		t.Error("SaveAPIArtifact() overwrote a stored row it could not read back")
	}
	if after := snapshotAPIArtifactRows(t, store); !reflect.DeepEqual(after, before) {
		t.Errorf("rejected save changed rows:\nbefore %q\nafter  %q", before, after)
	}
}

// The read, the check and the write have to be one transaction, because the stale
// Save above is exactly what a caller races against a supersede: whichever order the
// two arrive in, the established edge must still be there afterwards, and a Save that
// arrives first must not leave a row the supersede then has to refuse.
func TestSQLiteStoreSaveAPIArtifactCannotEraseAnEstablishedEdge(t *testing.T) {
	for i := 0; i < 20; i++ {
		store := newSupersedeStore(t)
		ctx := context.Background()
		predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
		successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
		mustSaveSupersedeArtifacts(t, store, predecessor, successor)

		// The stale copy keeps the empty edge it read before the supersede, and only
		// changes its body, so it is a legitimate update apart from the edge.
		stale := predecessor
		stale.Title = "Observed OpenAPI revised"
		stale.Content = supersedeContentB
		stale.ContentHash = supersedeHashB

		supersedeErr := make(chan error, 1)
		saveErr := make(chan error, 1)
		start := make(chan struct{})
		go func() {
			<-start
			supersedeErr <- store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
		}()
		go func() {
			<-start
			saveErr <- store.SaveAPIArtifact(ctx, stale)
		}()
		close(start)

		if err := <-supersedeErr; err != nil {
			t.Fatalf("iteration %d: SupersedeAPIArtifact() error = %v", i, err)
		}
		<-saveErr

		got := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID)
		if got.SupersededBy != successor.ArtifactID {
			t.Errorf("iteration %d: edge after racing save = %q, want %q", i, got.SupersededBy, successor.ArtifactID)
		}
		// Whichever operation committed first, the surviving row has to stay a row the
		// owner itself would accept: identity, body and digest still agree with each
		// other, so the race cannot leave a half-applied mix of the two writes.
		if err := domaintrace.ValidateAPIArtifact(got); err != nil {
			t.Errorf("iteration %d: row left after the race is invalid: %v (%#v)", i, err, got)
		}
	}
}

// Two independent store instances share one database file and both try to
// supersede the same predecessor, one to B and one to C. Exactly one of them may
// win: the loser has to be refused, and the winner's edge has to be the edge that
// survives. Nothing else moves, so the loser's refused write cannot leave a
// partially updated row, and the state has to be the same after a close and
// reopen, where retrying the winning edge stays idempotent.
func TestSQLiteStoreSupersedeAPIArtifactTwoInstancesOnOneFileHaveOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	first := newSupersedeStoreAt(t, path)
	second := newSupersedeStoreAt(t, path)
	ctx, cancelRace := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRace()
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	b := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	c := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
	mustSaveSupersedeArtifacts(t, first, predecessor, b, c)
	before := snapshotAPIArtifactRows(t, first)

	// Two separate *sql.DB pools, so the only thing serialising them is the
	// database's own write lock.
	start := make(chan struct{})
	type attempt struct {
		successor modulecore.ArtifactID
		err       error
	}
	results := make(chan attempt, 2)
	for _, pair := range []struct {
		store     *SQLiteStore
		successor modulecore.ArtifactID
	}{{first, b.ArtifactID}, {second, c.ArtifactID}} {
		go func(store *SQLiteStore, successor modulecore.ArtifactID) {
			<-start
			results <- attempt{successor: successor, err: store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor)}
		}(pair.store, pair.successor)
	}
	close(start)
	got := []attempt{<-results, <-results}

	var winner, loser attempt
	if got[0].err == nil && got[1].err != nil {
		winner, loser = got[0], got[1]
	} else if got[1].err == nil && got[0].err != nil {
		winner, loser = got[1], got[0]
	} else {
		t.Fatalf("two stores superseding the same artifact returned %+v and %+v; exactly one of them must succeed", got[0], got[1])
	}
	// The refusal has to be the owner's own conflict: the loser has to say that the
	// artifact is already superseded by the winner's successor. Any other error,
	// including a database that was merely busy, says nothing about the edge rule.
	if !strings.Contains(loser.err.Error(), "is already superseded by") || !strings.Contains(loser.err.Error(), string(winner.successor)) {
		t.Errorf("the losing supersede error = %v, want the conflict that artifact %s is already superseded by %s",
			loser.err, predecessor.ArtifactID, winner.successor)
	}
	t.Logf("winner %s -> %s, loser %s -> %s refused: %v", predecessor.ArtifactID, winner.successor, predecessor.ArtifactID, loser.successor, loser.err)

	// The winner edge is what a reader of either instance sees, and the loser's
	// successor row is untouched, because the loser never got to write.
	for _, store := range []*SQLiteStore{first, second} {
		stored := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID)
		if stored.SupersededBy != winner.successor {
			t.Errorf("edge after the race = %q, want the winner %q (loser error %v)", stored.SupersededBy, winner.successor, loser.err)
		}
	}
	wantPredecessor := predecessor
	wantPredecessor.SupersededBy = winner.successor
	if stored := mustFindSupersedeArtifact(t, second, predecessor.ArtifactID); !reflect.DeepEqual(stored, wantPredecessor) {
		t.Errorf("predecessor after the race = %#v, want %#v", stored, wantPredecessor)
	}
	if stored := mustFindSupersedeArtifact(t, second, b.ArtifactID); !reflect.DeepEqual(stored, b) {
		t.Errorf("successor B after the race = %#v, want %#v", stored, b)
	}
	if stored := mustFindSupersedeArtifact(t, second, c.ArtifactID); !reflect.DeepEqual(stored, c) {
		t.Errorf("successor C after the race = %#v, want %#v", stored, c)
	}
	if err := domaintrace.ValidateAPIArtifact(mustFindSupersedeArtifact(t, second, predecessor.ArtifactID)); err != nil {
		t.Errorf("row left after the race is invalid: %v", err)
	}

	// Retrying the winning edge through the other instance is idempotent: no error
	// and no byte of the database changes, so the only write in the whole race is
	// the winner's own.
	// The race wrote exactly one row: the predecessor's payload. Every other row,
	// including the loser's successor, is byte-identical to what it was before.
	afterRace := snapshotAPIArtifactRows(t, first)
	if len(afterRace) != len(before) {
		t.Fatalf("row count after the race = %d, want %d", len(afterRace), len(before))
	}
	changed := []string{}
	for i := range before {
		if before[i] != afterRace[i] {
			changed = append(changed, afterRace[i])
		}
	}
	if len(changed) != 1 || !strings.HasPrefix(changed[0], string(predecessor.ArtifactID)+"\x00") {
		t.Errorf("race changed %d rows %q, want only the predecessor row %s", len(changed), changed, predecessor.ArtifactID)
	}
	if err := second.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, winner.successor); err != nil {
		t.Errorf("retry of the winning edge %s -> %s error = %v", predecessor.ArtifactID, winner.successor, err)
	}
	if after := snapshotAPIArtifactRows(t, first); !reflect.DeepEqual(after, afterRace) {
		t.Errorf("retry of the winning edge changed rows:\nafter race %q\nafter retry %q", afterRace, after)
	}

	// Closing both instances and reopening the same file has to show the same
	// state, and the winning edge stays idempotent there too.
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
	reopened := newSupersedeStoreAt(t, path)
	if stored := mustFindSupersedeArtifact(t, reopened, predecessor.ArtifactID); !reflect.DeepEqual(stored, wantPredecessor) {
		t.Errorf("predecessor after reopen = %#v, want %#v", stored, wantPredecessor)
	}
	if stored := mustFindSupersedeArtifact(t, reopened, b.ArtifactID); !reflect.DeepEqual(stored, b) {
		t.Errorf("successor B after reopen = %#v, want %#v", stored, b)
	}
	if stored := mustFindSupersedeArtifact(t, reopened, c.ArtifactID); !reflect.DeepEqual(stored, c) {
		t.Errorf("successor C after reopen = %#v, want %#v", stored, c)
	}
	reopenBefore := snapshotAPIArtifactRows(t, reopened)
	if err := reopened.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, winner.successor); err != nil {
		t.Errorf("retry of the winning edge after reopen error = %v", err)
	}
	if after := snapshotAPIArtifactRows(t, reopened); !reflect.DeepEqual(after, reopenBefore) {
		t.Errorf("retry after reopen changed rows:\nbefore %q\nafter  %q", reopenBefore, after)
	}
	if err := reopened.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, loser.successor); err == nil {
		t.Errorf("supersede to the losing successor %s succeeded after the race; the artifact is already superseded by %s", loser.successor, winner.successor)
	}
	if after := snapshotAPIArtifactRows(t, reopened); !reflect.DeepEqual(after, reopenBefore) {
		t.Errorf("refused supersede to %s changed rows:\nbefore %q\nafter  %q", loser.successor, reopenBefore, after)
	}
}

// Stage 4B: a stale Save and a Supersede racing over one file from two independent
// store instances. The edge itself is never in question, only which body ends up
// under it: a body update that committed before the edge was established is a
// legitimate update the supersede has to preserve, while a Save that arrives after
// the edge is refused by the owner's own guard and lands nothing. Either way the
// supersede succeeds, the successor row does not move, and the surviving row is a
// row the domain still accepts.
// TestSQLiteStoreSaveAPIArtifactAndSupersedeAcrossTwoInstancesKeepEdge covers the
// two orders the owner has to get right, one at a time, so the semantics below do
// not depend on how the scheduler happens to interleave anything: a body update
// that finishes before the edge is established is preserved by the supersede that
// follows it, and a Save that arrives after the edge is refused by the edge guard
// and lands nothing. Both cases run over two independent store instances sharing
// one file, and both end with the same edge and a row the domain still accepts.
func TestSQLiteStoreSaveAPIArtifactAndSupersedeAcrossTwoInstancesKeepEdge(t *testing.T) {
	newPair := func(t *testing.T) (*SQLiteStore, *SQLiteStore, context.Context, domaintrace.APIArtifact, domaintrace.APIArtifact) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
		writer := newSupersedeStoreAt(t, path)
		racer := newSupersedeStoreAt(t, path)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
		successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
		mustSaveSupersedeArtifacts(t, writer, predecessor, successor)
		return writer, racer, ctx, predecessor, successor
	}
	// The racing Save only rewrites body, digest and title, so apart from the stale
	// empty edge it is the ordinary metadata update the owner allows.
	staleUpdate := func(predecessor domaintrace.APIArtifact) domaintrace.APIArtifact {
		stale := predecessor
		stale.Title = "Observed OpenAPI revised"
		stale.Content = supersedeContentC
		stale.ContentHash = supersedeHashC
		return stale
	}

	t.Run("update commits before the edge", func(t *testing.T) {
		writer, racer, ctx, predecessor, successor := newPair(t)
		stale := staleUpdate(predecessor)

		if err := racer.SaveAPIArtifact(ctx, stale); err != nil {
			t.Fatalf("SaveAPIArtifact(body update with the edge it read) error = %v; nothing had superseded %s yet", err, predecessor.ArtifactID)
		}
		if err := writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
			t.Fatalf("SupersedeAPIArtifact(%s -> %s) after a committed body update error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
		}

		want := stale
		want.SupersededBy = successor.ArtifactID
		assertPredecessorAndSuccessor(t, []*SQLiteStore{writer, racer}, want, successor)
	})

	t.Run("edge commits before the stale update", func(t *testing.T) {
		writer, racer, ctx, predecessor, successor := newPair(t)
		stale := staleUpdate(predecessor)

		if err := writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
			t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
		}
		err := racer.SaveAPIArtifact(ctx, stale)
		if err == nil {
			t.Fatalf("SaveAPIArtifact with the stale empty edge succeeded after the edge was established; the stored edge %s would have been cleared", successor.ArtifactID)
		}
		if !strings.Contains(err.Error(), "does not match the stored edge") {
			t.Errorf("SaveAPIArtifact with the stale empty edge error = %v, want the guard that superseded_by does not match the stored edge %s", err, successor.ArtifactID)
		}

		want := predecessor
		want.SupersededBy = successor.ArtifactID
		assertPredecessorAndSuccessor(t, []*SQLiteStore{writer, racer}, want, successor)
	})
}

// assertPredecessorAndSuccessor checks the row the supersession work has to leave
// behind, through every instance that shares the file: the predecessor exactly as
// wanted, and the successor untouched.
func assertPredecessorAndSuccessor(t *testing.T, stores []*SQLiteStore, wantPredecessor, wantSuccessor domaintrace.APIArtifact) {
	t.Helper()
	for _, store := range stores {
		if stored := mustFindSupersedeArtifact(t, store, wantPredecessor.ArtifactID); !reflect.DeepEqual(stored, wantPredecessor) {
			t.Errorf("predecessor = %#v, want %#v", stored, wantPredecessor)
		}
		if stored := mustFindSupersedeArtifact(t, store, wantSuccessor.ArtifactID); !reflect.DeepEqual(stored, wantSuccessor) {
			t.Errorf("successor %s = %#v, want the untouched %#v", wantSuccessor.ArtifactID, stored, wantSuccessor)
		}
		if err := domaintrace.ValidateAPIArtifact(mustFindSupersedeArtifact(t, store, wantPredecessor.ArtifactID)); err != nil {
			t.Errorf("stored row is invalid: %v", err)
		}
	}
}

// The same two operations genuinely at once: the interleaving is not controlled,
// so which of the two outcomes appears is the scheduler's choice, and both of them
// are the ones the ordered cases above pin down. What must hold every time is that
// the supersede is never refused by a racing save, the edge is always there
// afterwards, and the row underneath it is a whole row from one side or the other
// rather than a half-applied mix of the two writes.
func TestSQLiteStoreSaveAPIArtifactRacingSupersedeAcrossTwoInstancesKeepsEdge(t *testing.T) {
	const attempts = 20
	var saveFirst, supersedeFirst int
	for iteration := 0; iteration < attempts; iteration++ {
		t.Run(fmt.Sprintf("attempt %d", iteration), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
			writer := newSupersedeStoreAt(t, path)
			racer := newSupersedeStoreAt(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			mustSaveSupersedeArtifacts(t, writer, predecessor, successor)

			// The racing Save only rewrites body, digest and title, so apart from the
			// stale empty edge it is the ordinary metadata update the owner allows.
			stale := predecessor
			stale.Title = "Observed OpenAPI revised"
			stale.Content = supersedeContentC
			stale.ContentHash = supersedeHashC

			start := make(chan struct{})
			supersedeErr := make(chan error, 1)
			saveErr := make(chan error, 1)
			go func() {
				<-start
				supersedeErr <- writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
			}()
			go func() {
				<-start
				saveErr <- racer.SaveAPIArtifact(ctx, stale)
			}()
			close(start)
			edgeErr, updateErr := <-supersedeErr, <-saveErr

			if edgeErr != nil {
				t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v; a racing save may delay it but must never refuse it",
					predecessor.ArtifactID, successor.ArtifactID, edgeErr)
			}

			want := predecessor
			want.SupersededBy = successor.ArtifactID
			switch {
			case updateErr == nil:
				saveFirst++
				want.Title, want.Content, want.ContentHash = stale.Title, stale.Content, stale.ContentHash
				t.Logf("iteration %d: the save committed before the edge, so the update survives under it", iteration)
			default:
				supersedeFirst++
				// The refusal has to be the edge guard, not a database that was busy.
				if !strings.Contains(updateErr.Error(), "does not match the stored edge") {
					t.Errorf("SaveAPIArtifact with the stale empty edge error = %v, want the guard that superseded_by does not match the stored edge %s",
						updateErr, successor.ArtifactID)
				}
				t.Logf("iteration %d: the edge was established first and refused the stale save", iteration)
			}

			for _, store := range []*SQLiteStore{writer, racer} {
				if stored := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); !reflect.DeepEqual(stored, want) {
					t.Errorf("predecessor after the race = %#v, want %#v (save error %v, supersede error %v)", stored, want, updateErr, edgeErr)
				}
			}
			assertPredecessorAndSuccessor(t, []*SQLiteStore{writer, racer}, want, successor)
		})
	}
	// Which order the scheduler picks is not a contract, so it is only reported.
	// Both branches are asserted deterministically by the ordered cases above.
	t.Logf("observed interleavings: %d saves committed first, %d refused after the edge", saveFirst, supersedeFirst)
}

// Stage 4C1. The two owner methods are driven against a real database file that
// another connection has already locked, so a call has to wait inside BEGIN
// IMMEDIATE and be cancelled there. This is cancellation BEFORE a successful BEGIN,
// which is a different event from the rollback of a transaction that was already
// open, and the two must not be reported as one case: a call that never started a
// transaction cannot have written anything, while a call that failed after BEGIN
// left a transaction that somebody has to undo. The shared rollback cleanup itself
// is not exercised or changed here.
func TestSQLiteStoreAPIArtifactOwnerMethodsCancelledBeforeSuccessfulBeginLeaveNoWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
	writer := newSupersedeStoreAt(t, path)
	other := newSupersedeStoreAt(t, path)
	locker := newSupersedeStoreAt(t, path)
	// Bounded, so a test that cannot get the write lock eventually stops holding
	// its own connections instead of blocking the cleanup path forever. The caller
	// deadlines below are separate and much shorter on purpose.
	ctx, cancelOuter := context.WithTimeout(context.Background(), 2*time.Minute)
	// Registered before the lock release below so it runs after it: the lock release
	// still needs a live context of its own.
	t.Cleanup(cancelOuter)
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	third := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
	mustSaveSupersedeArtifacts(t, writer, predecessor, successor, third)
	before := snapshotAPIArtifactRows(t, other)

	// A third instance reserves its own connection and takes the write lock, then
	// holds it: every owner method below is a caller that arrives too late.
	conn, err := locker.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve locking connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		t.Fatalf("BEGIN IMMEDIATE on the locking connection: %v", err)
	}
	releaseLock := sync.OnceValue(func() error {
		_, rollbackErr := conn.ExecContext(ctx, `ROLLBACK`)
		return errors.Join(rollbackErr, conn.Close())
	})
	t.Cleanup(func() {
		if err := releaseLock(); err != nil {
			t.Errorf("release the test lock: %v", err)
		}
	})

	update := predecessor
	update.Title = "Observed OpenAPI revised while locked out"
	update.Content = supersedeContentC
	update.ContentHash = supersedeHashC

	for _, attempt := range []struct {
		name string
		call func(context.Context, *SQLiteStore) error
	}{
		{"SaveAPIArtifact", func(ctx context.Context, store *SQLiteStore) error {
			return store.SaveAPIArtifact(ctx, update)
		}},
		{"SupersedeAPIArtifact", func(ctx context.Context, store *SQLiteStore) error {
			return store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
		}},
	} {
		t.Run(attempt.name+" is cancelled before BEGIN succeeds", func(t *testing.T) {
			callerCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			err := attempt.call(callerCtx, writer)
			if err == nil {
				t.Fatalf("%s reported success although another store held the write lock", attempt.name)
			}
			// The cancellation has to surface as the cancellation, not as a domain
			// refusal: the caller was locked out, it did not ask for an illegal edge.
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s error = %v, want a cancelled wait for the write lock", attempt.name, err)
			}
			t.Logf("cancellation before a successful BEGIN: %s -> %v", attempt.name, err)
			if after := snapshotAPIArtifactRows(t, other); !reflect.DeepEqual(after, before) {
				t.Errorf("cancelled %s wrote rows:\nbefore %q\nafter  %q", attempt.name, before, after)
			}
		})
	}

	// Dropping the test lock must leave both stores usable: the cancelled calls
	// reserved a connection, waited, and gave up, so neither may have left anything
	// behind that stops the next transaction on the same file or the same pool.
	if err := releaseLock(); err != nil {
		t.Fatalf("release the test lock: %v", err)
	}
	if err := writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact after the test lock was released error = %v", err)
	}
	edge := predecessor
	edge.SupersededBy = successor.ArtifactID
	edge.Title, edge.Content, edge.ContentHash = update.Title, update.Content, update.ContentHash
	if err := other.SaveAPIArtifact(ctx, edge); err != nil {
		t.Fatalf("SaveAPIArtifact from the other store after the test lock was released error = %v", err)
	}
	if stored := mustFindSupersedeArtifact(t, writer, predecessor.ArtifactID); !reflect.DeepEqual(stored, edge) {
		t.Errorf("predecessor after both stores wrote again = %#v, want %#v", stored, edge)
	}
	if stored := mustFindSupersedeArtifact(t, other, third.ArtifactID); !reflect.DeepEqual(stored, third) {
		t.Errorf("unrelated artifact after the cancelled calls = %#v, want the untouched %#v", stored, third)
	}
}

// Stage 4C1. A write that fails after BEGIN IMMEDIATE had succeeded: a test-local
// trigger raises FAIL inside the row write of each owner method, which aborts the
// statement and leaves the transaction open with an in-transaction change that the
// owner has to undo itself. Two instances share the file, so a successful owner
// operation from either of them after the trigger is removed is the evidence that
// the failed call neither committed nor left its write behind, and gave its write
// lock back. The trigger only injects the failure into existing tables; production
// schema and driver settings are untouched, and the trigger is dropped again.
func TestSQLiteStoreAPIArtifactOwnerMethodsRollbackAStatementFailureAfterBegin(t *testing.T) {
	for _, failure := range []struct {
		name    string
		trigger string
		drop    string
		// marker is the message RAISE(FAIL) puts into the statement error, so the
		// assertion below can tell the injected failure apart from any other error
		// the call might have returned for an unrelated reason.
		marker string
		call   func(context.Context, *SQLiteStore, domaintrace.APIArtifact, domaintrace.APIArtifact) error
		// after reports what a successful owner operation on the same file looks
		// like once the injected failure is gone.
		after func(context.Context, *testing.T, *SQLiteStore, *SQLiteStore, domaintrace.APIArtifact, domaintrace.APIArtifact)
	}{
		{
			name: "SaveAPIArtifact write fails after BEGIN",
			trigger: `CREATE TRIGGER test_fail_api_artifact_insert AFTER INSERT ON api_artifact
				BEGIN SELECT RAISE(FAIL, 'test injected api artifact insert failure'); END`,
			drop:   `DROP TRIGGER IF EXISTS test_fail_api_artifact_insert`,
			marker: "test injected api artifact insert failure",
			call: func(ctx context.Context, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) error {
				updated := predecessor
				updated.Content = supersedeContentC
				updated.ContentHash = supersedeHashC
				return store.SaveAPIArtifact(ctx, updated)
			},
			after: func(ctx context.Context, t *testing.T, writer, reader *SQLiteStore, predecessor, successor domaintrace.APIArtifact) {
				// The edge is the owner operation, and it still has to be reachable
				// through the store whose earlier write failed, then readable from the
				// other instance.
				if err := writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("SupersedeAPIArtifact after the injected write failure error = %v", err)
				}
				want := predecessor
				want.SupersededBy = successor.ArtifactID
				assertPredecessorAndSuccessor(t, []*SQLiteStore{writer, reader}, want, successor)
			},
		},
		{
			name: "SupersedeAPIArtifact write fails after BEGIN",
			trigger: `CREATE TRIGGER test_fail_api_artifact_update AFTER UPDATE ON api_artifact
				BEGIN SELECT RAISE(FAIL, 'test injected api artifact update failure'); END`,
			drop:   `DROP TRIGGER IF EXISTS test_fail_api_artifact_update`,
			marker: "test injected api artifact update failure",
			call: func(ctx context.Context, store *SQLiteStore, predecessor, successor domaintrace.APIArtifact) error {
				return store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
			},
			after: func(ctx context.Context, t *testing.T, writer, reader *SQLiteStore, predecessor, successor domaintrace.APIArtifact) {
				if err := writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
					t.Fatalf("SupersedeAPIArtifact retry after the injected write failure error = %v", err)
				}
				// The other instance, sharing only the file, writes through the ordinary
				// Save path as well, so neither pool kept a transaction of its own.
				moved := successor
				moved.Title = "Successor retitled after the injected failure"
				if err := reader.SaveAPIArtifact(ctx, moved); err != nil {
					t.Fatalf("SaveAPIArtifact from the other store after the injected failure error = %v", err)
				}
				want := predecessor
				want.SupersededBy = successor.ArtifactID
				assertPredecessorAndSuccessor(t, []*SQLiteStore{writer, reader}, want, moved)
			},
		},
	} {
		t.Run(failure.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
			writer := newSupersedeStoreAt(t, path)
			reader := newSupersedeStoreAt(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			mustSaveSupersedeArtifacts(t, writer, predecessor, successor)

			if _, err := writer.db.ExecContext(ctx, failure.trigger); err != nil {
				t.Fatalf("install test trigger %q error = %v", failure.trigger, err)
			}
			t.Cleanup(func() {
				// Bounded: the trigger has to come off even if the test already failed,
				// but a cleanup that cannot get the file lock must not hang the run.
				cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancelCleanup()
				if _, err := writer.db.ExecContext(cleanupCtx, failure.drop); err != nil {
					t.Errorf("drop test trigger: %v", err)
				}
			})
			before := snapshotAPIArtifactRows(t, reader)

			err := failure.call(ctx, writer, predecessor, successor)
			if err == nil {
				t.Fatalf("%s reported success although its own write raised FAIL", failure.name)
			}
			if !strings.Contains(err.Error(), failure.marker) {
				t.Errorf("%s error = %v, want the injected failure %q rather than an unrelated refusal",
					failure.name, err, failure.marker)
			}
			t.Logf("injected post-BEGIN failure surfaced as: %v", err)
			// Uncommitted work is invisible to the other instance either way, so the
			// snapshot alone cannot tell a rollback from an abandoned transaction: the
			// owner operations that succeed below are what show the write was undone
			// and the write lock came back.
			if after := snapshotAPIArtifactRows(t, reader); !reflect.DeepEqual(after, before) {
				t.Errorf("failed %s committed rows:\nbefore %q\nafter  %q", failure.name, before, after)
			}

			if _, err := writer.db.ExecContext(ctx, failure.drop); err != nil {
				t.Fatalf("drop test trigger error = %v", err)
			}
			failure.after(ctx, t, writer, reader, predecessor, successor)
		})
	}
}

// A Save that moves an existing artifact into another task is not a legitimate
// update: it would invalidate a chain that was already checked. So unlike the body
// update above it may never win the race, whichever side reserves a connection
// first, and the supersede it races has to succeed on every attempt.
func TestSQLiteStoreSaveAPIArtifactMovingScopeAcrossTwoInstancesNeverWins(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		t.Run(fmt.Sprintf("attempt %d", iteration), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser_trace.sqlite")
			writer := newSupersedeStoreAt(t, path)
			racer := newSupersedeStoreAt(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			mustSaveSupersedeArtifacts(t, writer, predecessor, successor)

			moved := predecessor
			moved.TaskID = "tsk_00000000-0000-5000-8000-00000000000c"

			start := make(chan struct{})
			supersedeErr := make(chan error, 1)
			saveErr := make(chan error, 1)
			go func() {
				<-start
				supersedeErr <- writer.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
			}()
			go func() {
				<-start
				saveErr <- racer.SaveAPIArtifact(ctx, moved)
			}()
			close(start)
			edgeErr, moveErr := <-supersedeErr, <-saveErr

			if edgeErr != nil {
				t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, edgeErr)
			}
			if moveErr == nil {
				t.Fatalf("SaveAPIArtifact moved artifact %s into task %s while it was being superseded", predecessor.ArtifactID, moved.TaskID)
			}
			if !strings.Contains(moveErr.Error(), "task_id") {
				t.Errorf("SaveAPIArtifact with a moved task_id error = %v, want the scope guard on task_id", moveErr)
			}

			want := predecessor
			want.SupersededBy = successor.ArtifactID
			for _, store := range []*SQLiteStore{writer, racer} {
				if stored := mustFindSupersedeArtifact(t, store, predecessor.ArtifactID); !reflect.DeepEqual(stored, want) {
					t.Errorf("predecessor after the scope race = %#v, want %#v (save error %v)", stored, want, moveErr)
				}
			}
			if stored := mustFindSupersedeArtifact(t, racer, successor.ArtifactID); !reflect.DeepEqual(stored, successor) {
				t.Errorf("successor after the scope race = %#v, want the untouched %#v", stored, successor)
			}
		})
	}
}
