package browsertrace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreBrowserTraceToAPI(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
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
		Passed: false,
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
		ObservedEndpoints: []string{"GET /api/items"},
		// Only one list is set, so the row omits the other three. The digest still
		// covers them as empty lists, which is what the reloaded row has to reproduce.
		ContentHash:  "sha256:3bdfdc18a461fb89e90fdb6ddd80d4a26ec6ec088fa38fb2ee6c11b1b0b80aba",
		SupersededBy: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b"),
		CreatedAt:    now,
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
	// The two rows start with no edge, because an ordinary Save may not establish
	// one: the edge below is established by the owner Supersede operation, the same
	// way a runtime route will establish it.
	successor := domaintrace.APIArtifact{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b"),
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		WorkstreamID: "ws_1",
		Type:         "observed_openapi",
		Title:        "Observed OpenAPI successor",
		Status:       "generated",
		Content:      supersedeContentB,
		ContentHash:  supersedeHashB,
		CreatedAt:    now,
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
	// The JSONL row must keep the whole coverage report, not only its canonical
	// identity: the body lists, owner fields, content digest and supersession survive,
	// and the reloaded lists still reproduce the stored digest.
	if !reflect.DeepEqual(reports[0], coverage) {
		t.Errorf("ListAPICoverageReports() round trip = %#v, want %#v", reports[0], coverage)
	}
	if got := domaintrace.ComputeAPICoverageReportContentHash(reports[0]); got != reports[0].ContentHash {
		t.Errorf("reloaded coverage digest %s does not reproduce its body digest %s", reports[0].ContentHash, got)
	}
	if !reports[0].CreatedAt.Equal(now) {
		t.Errorf("ListAPICoverageReports() created_at = %v, want %v", reports[0].CreatedAt, now)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("ListAPIArtifacts() = %#v, %v", artifacts, err)
	}
	// The superseded row is located by artifact id rather than by position: the
	// history keeps both the record written before the edge and the record that
	// carries it, so only the reader's latest-record-per-id resolution says which
	// one is current.
	var roundTripped domaintrace.APIArtifact
	for _, candidate := range artifacts {
		if candidate.ArtifactID == modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a") {
			roundTripped = candidate
		}
	}
	if roundTripped.Kind != modulecore.ArtifactKindSpecification {
		t.Fatalf("ListAPIArtifacts() did not return artifact %s with its kind: %#v", artifact.ArtifactID, artifacts)
	}
	// The JSONL row must keep the whole artifact, not only its canonical identity:
	// content role, content body and owner fields survive the round trip, together
	// with the edge that the owner operation established.
	wantRoundTripped := artifact
	wantRoundTripped.SupersededBy = successor.ArtifactID
	if !reflect.DeepEqual(roundTripped, wantRoundTripped) {
		t.Errorf("ListAPIArtifacts() round trip = %#v, want %#v", roundTripped, wantRoundTripped)
	}
	if roundTripped.Type != "observed_openapi" || roundTripped.Title != "Observed OpenAPI" ||
		roundTripped.Status != "generated" || roundTripped.Content != "openapi: 3.1.0" ||
		roundTripped.TaskID != modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001") ||
		roundTripped.RunID != modulecore.RunID("run_00000000-0000-5000-8000-000000000002") ||
		roundTripped.ActorID != "mio" || roundTripped.WorkstreamID != "ws_1" ||
		roundTripped.ContentHash != "sha256:2fb0d1c2b023895b7bf1b743fa8554f9572cfd63667a21703a7ea57bc0fdd4f5" ||
		roundTripped.SupersededBy != modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b") ||
		!roundTripped.CreatedAt.Equal(now) {
		t.Errorf("ListAPIArtifacts() lost artifact fields, got %#v", roundTripped)
	}
	// The successor keeps its own record untouched: establishing an edge above it
	// is not a reason to rewrite the row that is pointed at.
	var storedSuccessor domaintrace.APIArtifact
	for _, candidate := range artifacts {
		if candidate.ArtifactID == successor.ArtifactID {
			storedSuccessor = candidate
		}
	}
	if !reflect.DeepEqual(storedSuccessor, successor) {
		t.Errorf("successor record changed = %#v, want %#v", storedSuccessor, successor)
	}
}

// TestJSONLStoreRejectsCoverageReportWithoutItsOwnDigest pins the JSONL write boundary:
// a coverage report whose content digest is missing, or whose digest belongs to bytes
// other than the appended body, never becomes a row. A digest that the row cannot
// reproduce would leave the appended history unverifiable.
func TestJSONLStoreRejectsCoverageReportWithoutItsOwnDigest(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
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

func TestJSONLStoreRejectsWriteMethodCandidate(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	err := store.SaveAPICandidate(context.Background(), domaintrace.APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "DELETE",
		ObservedURL:          "https://example.com/api/items/1",
		ContainsPersonalData: "unknown",
		Status:               "candidate",
	})
	if err == nil {
		t.Fatal("expected DELETE candidate to fail")
	}
}

func TestJSONLStoreFindAPICandidateByIDReturnsLatestExactRecord(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	first := domaintrace.APICandidate{
		CandidateID: "candidate-exact",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}
	latest := first
	latest.ObservedURL = "https://example.com/api/items?latest=true"
	latest.CreatedAt = now.Add(time.Minute)
	suffix := first
	suffix.CandidateID = "candidate-exact-suffix"

	for _, item := range []domaintrace.APICandidate{first, suffix, latest} {
		if err := store.SaveAPICandidate(ctx, item); err != nil {
			t.Fatalf("SaveAPICandidate(%q) failed: %v", item.CandidateID, err)
		}
	}

	got, found, err := store.FindAPICandidateByID(ctx, "candidate-exact")
	if err != nil || !found || got.CandidateID != "candidate-exact" || got.ObservedURL != latest.ObservedURL {
		t.Fatalf("FindAPICandidateByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindAPICandidateByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidate{}) {
		t.Fatalf("missing FindAPICandidateByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestJSONLStoreFindAPICandidateValidationResultPreservesOwnerAuditFields(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	first := domaintrace.APICandidateValidationResult{
		ValidationID: "validation-exact",
		CandidateID:  "candidate-1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Passed: false,
		Status: "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		Reviewer:   "ren",
		ReviewNote: "initial review",
		CreatedAt:  now,
	}
	latest := first
	latest.ReviewNote = "latest review"
	latest.CreatedAt = now.Add(time.Minute)
	suffix := first
	suffix.ValidationID = "validation-exact-suffix"

	for _, item := range []domaintrace.APICandidateValidationResult{first, suffix, latest} {
		if err := store.SaveAPICandidateValidationResult(ctx, item); err != nil {
			t.Fatalf("SaveAPICandidateValidationResult(%q) failed: %v", item.ValidationID, err)
		}
	}

	got, found, err := store.FindAPICandidateValidationResultByID(ctx, "validation-exact")
	if err != nil || !found || got.Reviewer != "ren" || got.ReviewNote != "latest review" {
		t.Fatalf("FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindAPICandidateValidationResultByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidateValidationResult{}) {
		t.Fatalf("missing FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestJSONLStoreFindBrowserTraceByIDRejectsMalformedRecord(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	if err := os.WriteFile(filepath.Join(root, "api_candidate.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed candidate: %v", err)
	}
	if _, found, err := store.FindAPICandidateByID(context.Background(), "candidate"); err == nil || found {
		t.Fatalf("expected malformed candidate error, found=%v err=%v", found, err)
	}
	if err := os.WriteFile(filepath.Join(root, "api_candidate_validation.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed validation: %v", err)
	}
	if _, found, err := store.FindAPICandidateValidationResultByID(context.Background(), "validation"); err == nil || found {
		t.Fatalf("expected malformed validation error, found=%v err=%v", found, err)
	}
}

// TestJSONLStoreSaveAPIArtifactRejectsNewRowWithSuccessor pins the JSONL write boundary
// for the supersession edge: an ordinary Save of a NEW artifact cannot establish that
// edge. Only the owner Supersede operation may do so, because a row appended with a
// successor that no operation has checked (existence, scope, cycle) would make the
// appended history unverifiable. Both the rejection and the empty result are recorded so
// the Red shows every part of the contract that the current write path is missing.
func TestJSONLStoreSaveAPIArtifactRejectsNewRowWithSuccessor(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	// The body and digest match, so the only reason to reject this row is the edge.
	newcomer := domaintrace.APIArtifact{
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
		CreatedAt:    time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}

	if err := store.SaveAPIArtifact(ctx, newcomer); err == nil {
		t.Errorf("SaveAPIArtifact() accepted a new artifact that declares successor %s; only the owner Supersede operation may establish an edge", newcomer.SupersededBy)
	}

	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("ListAPIArtifacts() returned %d rows after a rejected Save, want 0: %#v", len(artifacts), artifacts)
	}
}

// TestJSONLStoreListAPIArtifactsReturnsLatestRecordPerID pins the read side of the
// appended artifact history. Supersede and content updates append a new record for an
// id that already exists, so a reader that returned the first occurrence, or that mixed
// two occurrences into one row, would show a stale superseded_by edge and a body that no
// longer matches its stored digest. The newest record per id wins, and because the file
// is append-only, "newest" is the last occurrence: a limit therefore has to start from
// the most recently written id rather than the oldest.
func TestJSONLStoreListAPIArtifactsReturnsLatestRecordPerID(t *testing.T) {
	store, newStoreErr := NewJSONLStore(t.TempDir())
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	first := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	second := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	if err := store.SaveAPIArtifact(ctx, first); err != nil {
		t.Fatalf("SaveAPIArtifact(first) failed: %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, second); err != nil {
		t.Fatalf("SaveAPIArtifact(second) failed: %v", err)
	}
	// Same artifact id, later record: a new body, digest and title.
	updated := first
	updated.Content = supersedeContentC
	updated.ContentHash = supersedeHashC
	updated.Title = "Observed OpenAPI revised"
	if err := store.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("SaveAPIArtifact(updated first) failed: %v", err)
	}

	got, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	want := []domaintrace.APIArtifact{updated, second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListAPIArtifacts(limit 10) resolved the wrong rows\n got=%#v\nwant=%#v", got, want)
	}

	limited, err := store.ListAPIArtifacts(ctx, 1)
	if err != nil {
		t.Fatalf("ListAPIArtifacts(limit 1) error = %v", err)
	}
	if !reflect.DeepEqual(limited, []domaintrace.APIArtifact{updated}) {
		t.Fatalf("ListAPIArtifacts(limit 1) = %#v, want only the most recently written artifact", limited)
	}
}

// TestJSONLStoreAPIArtifactPreservesALargeRecord pins the size boundary of the
// artifact reader. A body larger than the default bufio scanner token is a valid
// record: the batch writer already accepts it, so a reader that could not load it
// would turn a persisted artifact into unreadable history, and a reader that sized
// its buffer by the whole file would let one record allocate freely. Both the live
// store and a store reopened over the same file have to reproduce the record exactly,
// including the digest that has to keep matching its own body.
func TestJSONLStoreAPIArtifactPreservesALargeRecord(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	// 100 KiB of body, so the record is well past the 64 KiB default token size.
	body := strings.Repeat("openapi: 3.1.0\n", 1024*100/15+1)
	if len(body) < 100*1024 {
		t.Fatalf("test body is only %d bytes, want at least %d", len(body), 100*1024)
	}
	large := supersedeFixtureArtifact("a", body, modulecore.ContentHashOf([]byte(body)))
	if err := store.SaveAPIArtifact(ctx, large); err != nil {
		t.Fatalf("SaveAPIArtifact(large) failed: %v", err)
	}

	got, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	if !reflect.DeepEqual(got, []domaintrace.APIArtifact{large}) {
		t.Fatalf("ListAPIArtifacts() lost the large record\n got=%d rows\nwant=1 row", len(got))
	}

	reopened, newErr := NewJSONLStore(root)
	if newErr != nil {
		t.Fatalf("NewJSONLStore(%s) after a large write error = %v", root, newErr)
	}
	reloaded, err := reopened.ListAPIArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() after reopen error = %v", err)
	}
	if !reflect.DeepEqual(reloaded, []domaintrace.APIArtifact{large}) {
		t.Fatalf("reopened store returned %#v, want the large record byte-for-byte", reloaded)
	}
}

// TestJSONLStoreNewRejectsUnverifiableArtifactRecord pins the constructor boundary:
// a persisted artifact record that the domain contract will not accept has to stop the
// store from opening at all. A digest that does not reproduce its own body is exactly
// as unusable as a line that is not JSON, because either one would let the store serve
// an artifact that cannot be used as evidence, and a later append would silently build
// on top of it. The constructor reports the error and hands back no store.
func TestJSONLStoreNewRejectsUnverifiableArtifactRecord(t *testing.T) {
	row := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	// Guarded before the deliberate defect below: the untouched fixture is a record the
	// contract does accept, so a rejection can only come from the injected defect.
	if err := domaintrace.ValidateAPIArtifact(row); err != nil {
		t.Fatalf("valid fixture stopped being valid: %v", err)
	}
	// The digest form itself stays valid, so the only defect in the record below is
	// that the hash belongs to another body.
	if err := modulecore.ValidateContentHash(supersedeHashB, "content_hash"); err != nil {
		t.Fatalf("fixture digest %s stopped being a valid digest form: %v", supersedeHashB, err)
	}
	row.ContentHash = supersedeHashB
	wrongDigest, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal mismatched fixture: %v", err)
	}

	tests := []struct {
		name   string
		record string
	}{
		{"malformed json", "{not json}\n"},
		{"content hash that is not its own body", string(wrongDigest) + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, artifactFilename), []byte(tc.record), 0644); err != nil {
				t.Fatalf("prewrite %s: %v", artifactFilename, err)
			}
			store, newErr := NewJSONLStore(root)
			if newErr == nil {
				t.Fatalf("NewJSONLStore(%s) opened a store over the record %q", root, tc.record)
			}
			if store != nil {
				t.Errorf("NewJSONLStore returned a non-nil store alongside error %v", newErr)
			}
		})
	}
}

// snapshotAPIArtifactFile returns the exact appended artifact file so a rejected
// operation can be compared against the bytes that were already persisted. The
// appended history is the evidence, so a rejection that silently added a record is a
// defect even when the reader happens to hide it.
func snapshotAPIArtifactFile(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, artifactFilename))
	if err != nil {
		t.Fatalf("read %s: %v", artifactFilename, err)
	}
	return string(raw)
}

// mustAPIArtifactRecords reads back the current logical state through the owner
// reader, so a guard test asserts what a caller actually observes and not only the
// raw file.
func mustAPIArtifactRecords(t *testing.T, store *JSONLStore) []domaintrace.APIArtifact {
	t.Helper()
	items, err := store.ListAPIArtifacts(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListAPIArtifacts() error = %v", err)
	}
	return items
}

// TestJSONLStoreSaveAPIArtifactRejectsScopeMoveOnExistingRow pins the other half of
// the write boundary: an ordinary Save may not move an artifact that is already
// persisted into another scope. The supersession checks compare the scope of the two
// rows an edge connects, so a Save that relocated one of them afterwards would leave a
// stored edge pointing at a row that no longer shares task, run, actor, workstream or
// content role. The content-role axis is included on its own because two browsertrace
// roles can share one kind, so a comparison that only looked at kind would not notice
// coverage_report becoming risk_assessment.
func TestJSONLStoreSaveAPIArtifactRejectsScopeMoveOnExistingRow(t *testing.T) {
	tests := []struct {
		name   string
		adjust func(row *domaintrace.APIArtifact)
	}{
		{"task_id", func(row *domaintrace.APIArtifact) {
			row.TaskID = "tsk_00000000-0000-5000-8000-000000000009"
		}},
		{"run_id", func(row *domaintrace.APIArtifact) {
			row.RunID = "run_00000000-0000-5000-8000-000000000008"
		}},
		{"actor_id", func(row *domaintrace.APIArtifact) { row.ActorID = "shiro" }},
		{"workstream_id", func(row *domaintrace.APIArtifact) { row.WorkstreamID = "ws_2" }},
		{
			"content_role that shares the same kind",
			func(row *domaintrace.APIArtifact) {
				row.Type = domaintrace.APIArtifactTypeRiskAssessment
				row.Kind = modulecore.ArtifactKindReport
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, newStoreErr := NewJSONLStore(root)
			if newStoreErr != nil {
				t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
			}
			ctx := context.Background()

			// The stored row starts in the content-role case with its own valid role, so
			// the only difference below is the relocation of an id that already exists.
			original := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			if tc.name == "content_role that shares the same kind" {
				original.Type = domaintrace.APIArtifactTypeCoverageReport
				original.Kind = modulecore.ArtifactKindReport
			}
			if err := domaintrace.ValidateAPIArtifact(original); err != nil {
				t.Fatalf("stored fixture row became invalid: %v", err)
			}
			if err := store.SaveAPIArtifact(ctx, original); err != nil {
				t.Fatalf("SaveAPIArtifact(original) failed: %v", err)
			}
			if original.SupersededBy != "" {
				t.Fatalf("stored fixture row stopped having an empty edge: %s", original.SupersededBy)
			}
			beforeRecords := mustAPIArtifactRecords(t, store)
			if !reflect.DeepEqual(beforeRecords, []domaintrace.APIArtifact{original}) {
				t.Fatalf("stored state before the rejected Save = %#v, want the original row", beforeRecords)
			}
			beforeFile := snapshotAPIArtifactFile(t, root)

			moved := original
			tc.adjust(&moved)
			// The moved row has to be a valid artifact on its own, otherwise the store
			// would reject it for a reason unrelated to the scope move.
			if err := domaintrace.ValidateAPIArtifact(moved); err != nil {
				t.Fatalf("moved fixture row %s is not independently valid: %v", moved.ArtifactID, err)
			}
			if moved.ArtifactID != original.ArtifactID {
				t.Fatalf("test fixture changed the artifact id instead of moving an existing row")
			}
			if tc.name == "content_role that shares the same kind" {
				if moved.Kind != original.Kind || moved.Type == original.Type {
					t.Fatalf("test fixture stopped exercising two content roles under one kind")
				}
			}

			if err := store.SaveAPIArtifact(ctx, moved); err == nil {
				t.Errorf("SaveAPIArtifact() accepted a Save that moved existing artifact %s into another %s", original.ArtifactID, tc.name)
			}

			if after := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(after, beforeRecords) {
				t.Errorf("rejected Save changed the current records:\n before=%#v\n after =%#v", beforeRecords, after)
			}
			if afterFile := snapshotAPIArtifactFile(t, root); afterFile != beforeFile {
				t.Errorf("rejected Save appended to %s:\n before=%q\n after =%q", artifactFilename, beforeFile, afterFile)
			}
		})
	}
}

// TestJSONLStoreSaveAPIArtifactRejectsAddingEdgeToExistingRow pins that the
// supersession edge itself is not an ordinary Save field. The successor of an
// existing row has to be checked for existence, shared scope and cycles by the owner
// Supersede operation; an append that simply carried a well-formed successor id would
// record an edge that nothing verified, and the appended history would stop being
// evidence for the current supersession state.
func TestJSONLStoreSaveAPIArtifactRejectsAddingEdgeToExistingRow(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	original := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	if err := store.SaveAPIArtifact(ctx, original); err != nil {
		t.Fatalf("SaveAPIArtifact(original) failed: %v", err)
	}
	beforeRecords := mustAPIArtifactRecords(t, store)
	beforeFile := snapshotAPIArtifactFile(t, root)

	added := original
	added.SupersededBy = supersedeFixtureID("b")
	if err := domaintrace.ValidateAPIArtifact(added); err != nil {
		t.Fatalf("row with an added edge %s is not independently valid: %v", added.ArtifactID, err)
	}

	if err := store.SaveAPIArtifact(ctx, added); err == nil {
		t.Errorf("SaveAPIArtifact() accepted an ordinary Save that added successor %s to existing artifact %s; only the owner Supersede operation may establish an edge", added.SupersededBy, original.ArtifactID)
	}

	if after := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(after, beforeRecords) {
		t.Errorf("rejected Save changed the current records:\n before=%#v\n after =%#v", beforeRecords, after)
	}
	if afterFile := snapshotAPIArtifactFile(t, root); afterFile != beforeFile {
		t.Errorf("rejected Save appended to %s:\n before=%q\n after =%q", artifactFilename, beforeFile, afterFile)
	}
}

// findAPIArtifactRecord picks one artifact out of a current-state read by artifact id.
// The appended history keeps several records per id, so a supersession test has to look
// at the record the reader resolved for a given id instead of assuming where in the
// read that record landed.
func findAPIArtifactRecord(t *testing.T, records []domaintrace.APIArtifact, id modulecore.ArtifactID) domaintrace.APIArtifact {
	t.Helper()
	for _, candidate := range records {
		if candidate.ArtifactID == id {
			return candidate
		}
	}
	t.Fatalf("current records do not contain artifact %s: %#v", id, records)
	return domaintrace.APIArtifact{}
}

// TestJSONLStoreSupersedeAPIArtifactEstablishesTheEdgeAndRepeatsWithoutAnAppend pins
// the JSONL owner operation that establishes a supersession edge. Two artifacts that
// share one scope are stored edge-free, the owner operation points the predecessor at
// the successor, and the read-back has to reproduce both whole records: only
// superseded_by changes on the predecessor, while the successor record is not rewritten
// at all. Repeating the stored edge is idempotent and, because the appended history is
// the evidence, a repeat must add no record to the file. Finally a store opened again on
// the same root has to resolve the same current state and stay just as idempotent, which
// is what a restarted process will observe.
func TestJSONLStoreSupersedeAPIArtifactEstablishesTheEdgeAndRepeatsWithoutAnAppend(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	if err := store.SaveAPIArtifact(ctx, predecessor); err != nil {
		t.Fatalf("SaveAPIArtifact(predecessor) failed: %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, successor); err != nil {
		t.Fatalf("SaveAPIArtifact(successor) failed: %v", err)
	}
	for _, item := range mustAPIArtifactRecords(t, store) {
		if item.SupersededBy != "" {
			t.Fatalf("artifact %s was stored with an edge %s before the owner operation ran", item.ArtifactID, item.SupersededBy)
		}
	}
	before := snapshotAPIArtifactFile(t, root)

	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}

	current := mustAPIArtifactRecords(t, store)
	wantPredecessor := predecessor
	wantPredecessor.SupersededBy = successor.ArtifactID
	if got := findAPIArtifactRecord(t, current, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
		t.Errorf("predecessor record after supersede = %#v, want %#v", got, wantPredecessor)
	}
	if got := findAPIArtifactRecord(t, current, successor.ArtifactID); !reflect.DeepEqual(got, successor) {
		t.Errorf("successor record changed = %#v, want %#v", got, successor)
	}
	after := snapshotAPIArtifactFile(t, root)
	if after == before {
		t.Errorf("SupersedeAPIArtifact() appended nothing to %s, so the edge is not durable", artifactFilename)
	}
	if !strings.HasPrefix(after, before) {
		t.Errorf("SupersedeAPIArtifact() rewrote the appended history:\n before=%q\n after =%q", before, after)
	}

	// The same edge again: the operation is idempotent, and an idempotent repeat must
	// not grow the appended history.
	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("repeat SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}
	if repeated := snapshotAPIArtifactFile(t, root); repeated != after {
		t.Errorf("repeat of the stored edge appended to %s:\n before=%q\n after =%q", artifactFilename, after, repeated)
	}
	if repeated := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(repeated, current) {
		t.Errorf("repeat of the stored edge changed the current records:\n before=%#v\n after =%#v", current, repeated)
	}

	// A store opened again on the same root reads the same current state and stays
	// idempotent, because the edge lives in the appended history and not in memory.
	reopened, reopenErr := NewJSONLStore(root)
	if reopenErr != nil {
		t.Fatalf("NewJSONLStore(reopen) error = %v", reopenErr)
	}
	if got := mustAPIArtifactRecords(t, reopened); !reflect.DeepEqual(got, current) {
		t.Errorf("reopened current records = %#v, want %#v", got, current)
	}
	if err := reopened.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact() after reopen error = %v", err)
	}
	if afterReopen := snapshotAPIArtifactFile(t, root); afterReopen != after {
		t.Errorf("SupersedeAPIArtifact() after reopen appended to %s:\n before=%q\n after =%q", artifactFilename, after, afterReopen)
	}
}

// saveSupersededFixturePair writes the A and B artifacts with no edge and establishes
// the owner edge A -> B, so a test can start from an edge that a real owner operation
// rather than a fixture literal put there.
func saveSupersededFixturePair(t *testing.T, store *JSONLStore) (domaintrace.APIArtifact, domaintrace.APIArtifact) {
	t.Helper()
	ctx := context.Background()
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	if err := store.SaveAPIArtifact(ctx, predecessor); err != nil {
		t.Fatalf("SaveAPIArtifact(predecessor) failed: %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, successor); err != nil {
		t.Fatalf("SaveAPIArtifact(successor) failed: %v", err)
	}
	if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}
	return predecessor, successor
}

// TestJSONLStoreSaveAPIArtifactRejectsEdgeChangesOnAnEstablishedEdge pins the write
// boundary once the edge really exists. An ordinary Save of the predecessor may neither
// clear that edge nor point it at a different artifact, because only the owner operation
// checks successor existence, shared scope and cycles: a write that quietly moved the
// edge would leave a stored supersession chain that nothing verified, and the appended
// history would stop being evidence for the current state. Both candidates below are
// independently valid artifacts, so a rejection can only come from the edge change, and a
// rejection has to leave both the resolved current records and the appended bytes alone.
func TestJSONLStoreSaveAPIArtifactRejectsEdgeChangesOnAnEstablishedEdge(t *testing.T) {
	tests := []struct {
		name   string
		adjust func(row *domaintrace.APIArtifact)
	}{
		{"clears_the_established_edge", func(row *domaintrace.APIArtifact) { row.SupersededBy = "" }},
		{"moves_it_to_a_valid_distinct_successor", func(row *domaintrace.APIArtifact) {
			row.SupersededBy = supersedeFixtureID("c")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, newStoreErr := NewJSONLStore(root)
			if newStoreErr != nil {
				t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
			}
			ctx := context.Background()

			predecessor, successor := saveSupersededFixturePair(t, store)
			beforeRecords := mustAPIArtifactRecords(t, store)
			established := findAPIArtifactRecord(t, beforeRecords, predecessor.ArtifactID)
			if established.SupersededBy != successor.ArtifactID {
				t.Fatalf("owner operation left predecessor edge %q, want %q", established.SupersededBy, successor.ArtifactID)
			}
			beforeFile := snapshotAPIArtifactFile(t, root)

			moved := established
			test.adjust(&moved)
			if err := domaintrace.ValidateAPIArtifact(moved); err != nil {
				t.Fatalf("candidate row %s with edge %q is not independently valid: %v", moved.ArtifactID, moved.SupersededBy, err)
			}

			err := store.SaveAPIArtifact(ctx, moved)
			if err == nil {
				t.Errorf("SaveAPIArtifact() accepted an ordinary Save that moved artifact %s from stored edge %q to %q; only SupersedeAPIArtifact may add, change or clear that edge",
					predecessor.ArtifactID, established.SupersededBy, moved.SupersededBy)
			} else if !strings.Contains(err.Error(), "superseded_by") || !strings.Contains(err.Error(), string(established.SupersededBy)) {
				// A rejection that came from anything else would hide the guard, so the
				// reason is checked rather than just the fact that something failed.
				t.Errorf("SaveAPIArtifact() rejected the edge move for an unrelated reason, want the stored edge %q to be named: %v", established.SupersededBy, err)
			}
			if after := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(after, beforeRecords) {
				t.Errorf("rejected Save changed the current records:\n before=%#v\n after =%#v", beforeRecords, after)
			}
			if afterFile := snapshotAPIArtifactFile(t, root); afterFile != beforeFile {
				t.Errorf("rejected Save changed %s:\n before=%q\n after =%q", artifactFilename, beforeFile, afterFile)
			}
		})
	}
}

// TestJSONLStoreSaveAPIArtifactAllowsABodyUpdateThatRetainsTheEstablishedEdge keeps the
// other side of the same boundary: the edge guard must not turn the artifact into an
// immutable row. A new body with its own matching digest and a new title, carried
// alongside the unchanged stored edge, is the owner's normal in-place update and has to be
// appended. The updated record then becomes the current record for that id, the successor
// record is left alone, and the history only grows at its end, because the file is
// append-only evidence.
func TestJSONLStoreSaveAPIArtifactAllowsABodyUpdateThatRetainsTheEstablishedEdge(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	predecessor, successor := saveSupersededFixturePair(t, store)
	beforeRecords := mustAPIArtifactRecords(t, store)
	beforeFile := snapshotAPIArtifactFile(t, root)

	updated := findAPIArtifactRecord(t, beforeRecords, predecessor.ArtifactID)
	updated.Content = supersedeContentC
	updated.ContentHash = supersedeHashC
	updated.Title = "Observed OpenAPI revised"
	if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
		t.Fatalf("updated body %s with its own digest is not a valid row: %v", updated.ArtifactID, err)
	}

	if err := store.SaveAPIArtifact(ctx, updated); err != nil {
		t.Fatalf("SaveAPIArtifact() rejected a body update that kept the stored edge %q: %v", updated.SupersededBy, err)
	}

	after := mustAPIArtifactRecords(t, store)
	if got := findAPIArtifactRecord(t, after, predecessor.ArtifactID); !reflect.DeepEqual(got, updated) {
		t.Errorf("current record for updated artifact = %#v, want %#v", got, updated)
	}
	if got := findAPIArtifactRecord(t, after, successor.ArtifactID); !reflect.DeepEqual(got, successor) {
		t.Errorf("successor record changed by a predecessor body update = %#v, want %#v", got, successor)
	}
	afterFile := snapshotAPIArtifactFile(t, root)
	if afterFile == beforeFile {
		t.Errorf("accepted body update appended nothing to %s", artifactFilename)
	}
	if !strings.HasPrefix(afterFile, beforeFile) {
		t.Errorf("accepted body update rewrote the appended history:\n before=%q\n after =%q", beforeFile, afterFile)
	}
}

// mustSaveJSONLArtifacts writes rows through the ordinary owner Save after checking
// each one is a valid artifact on its own, so a supersede refusal below can only come
// from the supersession rules and never from a row the store would have refused anyway.
func mustSaveJSONLArtifacts(t *testing.T, store *JSONLStore, items ...domaintrace.APIArtifact) {
	t.Helper()
	ctx := context.Background()
	for _, item := range items {
		if err := domaintrace.ValidateAPIArtifact(item); err != nil {
			t.Fatalf("test fixture row %s is not a valid artifact: %v", item.ArtifactID, err)
		}
		if err := store.SaveAPIArtifact(ctx, item); err != nil {
			t.Fatalf("SaveAPIArtifact(%s) error = %v", item.ArtifactID, err)
		}
	}
}

// TestJSONLStoreSupersedeAPIArtifactRefusesEdgesThatWouldBreakTheChain pins the
// refusals of the owner supersede operation: a missing end of the edge, an edge that
// points an artifact at itself, a partner in another workstream, a partner that is a
// different content role under one kind, a successor that contradicts the edge already
// stored for that predecessor, and an edge that would close a cycle under an established
// chain. Each case uses rows that are individually valid artifacts, so a refusal can
// only come from the supersession rules, and every refusal has to name its reason while
// leaving the resolved current records and the appended file bytes untouched, because a
// refused operation must not leave a half-written chain behind.
func TestJSONLStoreSupersedeAPIArtifactRefusesEdgesThatWouldBreakTheChain(t *testing.T) {
	tests := []struct {
		name string
		// setup stores the rows, and any edge it wants really established, through the
		// owner operations only, then reports the two ids of the refused attempt.
		setup   func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID)
		wantErr []string
	}{
		{
			// The edge needs a stored predecessor row: nothing was verified about an id
			// that the appended history does not hold.
			name: "predecessor row is absent",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
				)
				return supersedeFixtureID("e"), supersedeFixtureID("b")
			},
			wantErr: []string{"artifact " + string(supersedeFixtureID("e")), "is not in " + artifactFilename},
		},
		{
			// The same holds for the successor: an edge to an id that was never stored
			// cannot be checked for existence, scope or cycles now or later.
			name: "successor row is absent",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
				)
				return supersedeFixtureID("a"), supersedeFixtureID("e")
			},
			wantErr: []string{"artifact " + string(supersedeFixtureID("e")), "is not in " + artifactFilename},
		},
		{
			// A self reference is not a supersession: it would claim that a stored
			// artifact already sits below itself.
			name: "artifact supersedes itself",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
				)
				return supersedeFixtureID("a"), supersedeFixtureID("a")
			},
			wantErr: []string{"must not reference the artifact itself"},
		},
		{
			// The two artifacts are distinct valid rows that merely sit in another
			// workstream, so an edge between them would join two chains of ownership.
			name: "successor is in another workstream",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				otherWorkstream := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
				otherWorkstream.WorkstreamID = "ws_2"
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					otherWorkstream,
				)
				return supersedeFixtureID("a"), supersedeFixtureID("b")
			},
			wantErr: []string{"workstream_id", "ws_1", "ws_2"},
		},
		{
			// coverage_report and risk_assessment are both reports, so this pair shares
			// one kind and only a comparison that also reads the content role refuses it.
			name: "successor is another content role under the same kind",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
				predecessor.Type = "coverage_report"
				predecessor.Kind = modulecore.ArtifactKindReport
				successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
				successor.Type = "risk_assessment"
				successor.Kind = modulecore.ArtifactKindReport
				mustSaveJSONLArtifacts(t, store, predecessor, successor)
				return predecessor.ArtifactID, successor.ArtifactID
			},
			wantErr: []string{"content role", "coverage_report", "risk_assessment"},
		},
		{
			// The predecessor already sits under B, so pointing it at a second successor
			// would fork the chain rather than supersede it.
			name: "predecessor is already superseded by a different successor",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
					supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC),
				)
				if err := store.SupersedeAPIArtifact(context.Background(), supersedeFixtureID("a"), supersedeFixtureID("b")); err != nil {
					t.Fatalf("test fixture SupersedeAPIArtifact(a, b) error = %v", err)
				}
				return supersedeFixtureID("a"), supersedeFixtureID("c")
			},
			wantErr: []string{"is already superseded by", string(supersedeFixtureID("b")), string(supersedeFixtureID("c"))},
		},
		{
			// A -> B is really established, so B -> A would close a cycle over the two
			// nodes: the walk below the new successor leads back to the predecessor.
			name: "edge would close a cycle under an established chain",
			setup: func(t *testing.T, store *JSONLStore) (modulecore.ArtifactID, modulecore.ArtifactID) {
				t.Helper()
				mustSaveJSONLArtifacts(t, store,
					supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
					supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
				)
				if err := store.SupersedeAPIArtifact(context.Background(), supersedeFixtureID("a"), supersedeFixtureID("b")); err != nil {
					t.Fatalf("test fixture SupersedeAPIArtifact(a, b) error = %v", err)
				}
				return supersedeFixtureID("b"), supersedeFixtureID("a")
			},
			wantErr: []string{"cycle", string(supersedeFixtureID("a")), string(supersedeFixtureID("b"))},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, newStoreErr := NewJSONLStore(root)
			if newStoreErr != nil {
				t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
			}
			predecessorID, successorID := test.setup(t, store)
			beforeRecords := mustAPIArtifactRecords(t, store)
			beforeFile := snapshotAPIArtifactFile(t, root)

			err := store.SupersedeAPIArtifact(context.Background(), predecessorID, successorID)
			if err == nil {
				t.Fatalf("SupersedeAPIArtifact(%s -> %s) succeeded, want a refusal: %s", predecessorID, successorID, test.name)
			}
			for _, want := range test.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("SupersedeAPIArtifact(%s -> %s) refused for %q, want the reason to state %q", predecessorID, successorID, err, want)
				}
			}

			if after := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(after, beforeRecords) {
				t.Errorf("refused supersede changed the current records:\n before=%#v\n after =%#v", beforeRecords, after)
			}
			if afterFile := snapshotAPIArtifactFile(t, root); afterFile != beforeFile {
				t.Errorf("refused supersede changed %s:\n before=%q\n after =%q", artifactFilename, beforeFile, afterFile)
			}
		})
	}
}

// appendAPIArtifactInjectedRecord writes a record straight into the artifact file and
// returns the file bytes that follow the injection. This is corruption injection in an
// isolated test fixture, not an owner operation and not runtime evidence: an ordinary
// Save would refuse to move a stored row's scope or digest, so the row below is placed
// by hand the way an interrupted write or a hand-edited history would leave it. Only the
// newest record for that id matters to a reader, because the loader resolves each id to
// its last occurrence.
func appendAPIArtifactInjectedRecord(t *testing.T, root string, row domaintrace.APIArtifact) string {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal injected record: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(root, artifactFilename), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open %s to inject corruption: %v", artifactFilename, err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		t.Fatalf("inject record for %s into %s: %v", row.ArtifactID, artifactFilename, err)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("sync injected record: %v", err)
	}
	return snapshotAPIArtifactFile(t, root)
}

// TestJSONLStoreSupersedeAPIArtifactRefusesACorruptedDeeperChainRow covers the chain
// rules below the row an edge touches directly. The setup is owner operations only: three
// valid edge-free artifacts are saved and B -> C is really established. Afterwards a newer
// C record is injected by hand, so the deeper row points at an artifact that was never
// stored, points back up into a cycle, leaves the scope the established edge was checked
// against, or no longer reproduces its own digest. A new edge A -> B has to be refused by
// walking that chain rather than trusting the rows it first reads, and the refusal may not
// append behind the injected row: the file bytes have to stay exactly as the injection left
// them. Where every row is still individually verifiable the resolved records have to be
// unchanged as well; where a row cannot verify its own digest, the reader failing closed is
// the accepted behaviour, so only the file bytes are compared there.
func TestJSONLStoreSupersedeAPIArtifactRefusesACorruptedDeeperChainRow(t *testing.T) {
	tests := []struct {
		name string
		// injected is the hand-written newest row for C, built after the owner
		// operations above have established B -> C.
		injected func(t *testing.T) domaintrace.APIArtifact
		wantErr  []string
		// verifiable says whether every stored row can still check its own digest,
		// which is what decides whether a resolved-record snapshot can be read at all.
		verifiable bool
	}{
		{
			// The deeper row reaches below the history: nothing was ever stored for the
			// id it claims to sit under, so the chain has an unverifiable end.
			name:       "deeper row points at an artifact that was never stored",
			verifiable: true,
			injected: func(t *testing.T) domaintrace.APIArtifact {
				t.Helper()
				deeper := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
				deeper.SupersededBy = supersedeFixtureID("d")
				return deeper
			},
			wantErr: []string{
				"supersession chain of artifact " + string(supersedeFixtureID("c")),
				string(supersedeFixtureID("d")) + " is not in " + artifactFilename,
			},
		},
		{
			// C is stored below B, and the injected C claims to sit under B, so the
			// proposed A -> B edge would close a cycle two rows down rather than one.
			name:       "deeper row points back up and closes a cycle",
			verifiable: true,
			injected: func(t *testing.T) domaintrace.APIArtifact {
				t.Helper()
				deeper := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
				deeper.SupersededBy = supersedeFixtureID("b")
				return deeper
			},
			wantErr: []string{
				"supersession chain of artifact " + string(supersedeFixtureID("a")),
				"revisits artifact " + string(supersedeFixtureID("b")),
			},
		},
		{
			// The injected row is a valid artifact on its own, so the refusal can only
			// come from the scope the established edge was checked against no longer
			// holding for the row below it.
			name:       "deeper row left the scope of the established chain",
			verifiable: true,
			injected: func(t *testing.T) domaintrace.APIArtifact {
				t.Helper()
				deeper := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
				deeper.WorkstreamID = "ws_2"
				return deeper
			},
			wantErr: []string{"workstream_id", "ws_1", "ws_2"},
		},
		{
			// The digest form stays valid while the body belongs to another row, so the
			// deeper node cannot vouch for itself. A reader that refuses to trust an
			// unverifiable row fails closed here, and that is the accepted outcome.
			name:       "deeper row digest does not reproduce its own body",
			verifiable: false,
			injected: func(t *testing.T) domaintrace.APIArtifact {
				t.Helper()
				deeper := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)
				deeper.ContentHash = supersedeHashA
				return deeper
			},
			wantErr: []string{artifactFilename, "content_hash " + supersedeHashA},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, newStoreErr := NewJSONLStore(root)
			if newStoreErr != nil {
				t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			mustSaveJSONLArtifacts(t, store,
				supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA),
				supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB),
				supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC),
			)
			if err := store.SupersedeAPIArtifact(ctx, supersedeFixtureID("b"), supersedeFixtureID("c")); err != nil {
				t.Fatalf("owner fixture SupersedeAPIArtifact(b, c) error = %v", err)
			}

			injected := test.injected(t)
			if injected.ArtifactID != supersedeFixtureID("c") {
				t.Fatalf("injected row replaced %s, want the deeper artifact %s", injected.ArtifactID, supersedeFixtureID("c"))
			}
			if test.verifiable {
				if err := domaintrace.ValidateAPIArtifact(injected); err != nil {
					t.Fatalf("injected row %s stopped being an individually valid artifact: %v", injected.ArtifactID, err)
				}
			} else if err := modulecore.ValidateContentHash(injected.ContentHash, "content_hash"); err != nil {
				t.Fatalf("injected digest %s stopped being a valid digest form: %v", injected.ContentHash, err)
			}
			injectedFile := appendAPIArtifactInjectedRecord(t, root, injected)
			var injectedRecords []domaintrace.APIArtifact
			if test.verifiable {
				injectedRecords = mustAPIArtifactRecords(t, store)
			}

			err := store.SupersedeAPIArtifact(ctx, supersedeFixtureID("a"), supersedeFixtureID("b"))
			if err == nil {
				t.Fatalf("SupersedeAPIArtifact(a -> b) succeeded over the injected deeper row, want a refusal: %s", test.name)
			}
			for _, want := range test.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("SupersedeAPIArtifact(a -> b) refused for %q, want the reason to state %q", err, want)
				}
			}
			if afterFile := snapshotAPIArtifactFile(t, root); afterFile != injectedFile {
				t.Errorf("refused supersede appended behind the injected row in %s:\n injected=%q\n after   =%q", artifactFilename, injectedFile, afterFile)
			}
			if test.verifiable {
				if after := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(after, injectedRecords) {
					t.Errorf("refused supersede changed the current records:\n injected=%#v\n after   =%#v", injectedRecords, after)
				}
			}
		})
	}
}

// largestAPIArtifactRecordBytes is the per-record ceiling the shared batch writer was
// independently verified to enforce for api_artifact.jsonl: an appended record is
// rejected only when it is LONGER than this. The regression is sized from that writer
// contract directly rather than from the reader's own buffer constant, so lowering the
// reader ceiling cannot quietly shrink the record this test asks for and make a reader
// that lost the ability to read the largest accepted record look like it passes.
const largestAPIArtifactRecordBytes = 16 << 20

// largestAPIArtifactFixture sizes an artifact whose encoded JSONL record is exactly the
// per-record ceiling the batch writer already enforces, which is the same number the
// reader names as its buffer ceiling. The writer rejects a record only when it is
// LONGER than that ceiling, so a record of exactly that length is evidence the owner
// itself appended: a reader that cannot load it turns an accepted artifact into
// unreadable history. The body is ASCII and its digest is computed for it, so the row
// stays a valid artifact, and the size is derived from the marshaled fixture rather than
// guessed, because the record also carries ids, the content role and the created_at.
func largestAPIArtifactFixture(t *testing.T) domaintrace.APIArtifact {
	t.Helper()
	probe := supersedeFixtureArtifact("a", "x", modulecore.ContentHashOf([]byte("x")))
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal probe fixture: %v", err)
	}
	// The probe body is one ASCII byte and the record has no escaped characters, so the
	// remaining bytes are the framing that every field name and id contributes.
	overhead := len(encoded) - 1
	// The body is ASCII with nothing that JSON escapes, so the encoded record is exactly
	// the framing plus the body length and the ceiling can be hit precisely.
	content := strings.Repeat("a", largestAPIArtifactRecordBytes-overhead)
	largest := supersedeFixtureArtifact("a", content, modulecore.ContentHashOf([]byte(content)))

	record, err := json.Marshal(largest)
	if err != nil {
		t.Fatalf("marshal largest fixture: %v", err)
	}
	if len(record) != largestAPIArtifactRecordBytes {
		t.Fatalf("largest fixture encodes to %d bytes, want exactly the writer ceiling %d", len(record), largestAPIArtifactRecordBytes)
	}
	if err := domaintrace.ValidateAPIArtifact(largest); err != nil {
		t.Fatalf("largest fixture is not a valid artifact: %v", err)
	}
	return largest
}

// TestJSONLStoreAPIArtifactReaderAcceptsTheLargestRecordTheWriterAppends pins the size
// boundary between the append path and the read path at the ceiling itself, not below it.
// The store has to accept the record, resolve it through ListAPIArtifacts and reproduce it
// byte-for-byte after a reopen, because the appended history is the only evidence of a
// superseded artifact. Failures report ids, digests and lengths instead of the body: a
// 16 MiB body printed in a failure would bury the reason.
func TestJSONLStoreAPIArtifactReaderAcceptsTheLargestRecordTheWriterAppends(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()
	largest := largestAPIArtifactFixture(t)

	if err := store.SaveAPIArtifact(ctx, largest); err != nil {
		t.Fatalf("SaveAPIArtifact() rejected a record of exactly %d bytes, the ceiling the writer itself enforces: %v", maxArtifactRecordBytes, err)
	}

	got, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil {
		// The reopen below still runs, so one report shows both that the appended record
		// could not be read back and that a fresh process cannot open over it.
		t.Errorf("ListAPIArtifacts() returned %d records and error %v for the %d byte record the store just appended, want that one record exactly", len(got), err, largestAPIArtifactRecordBytes)
	} else if len(got) != 1 || got[0].ArtifactID != largest.ArtifactID || got[0].ContentHash != largest.ContentHash || len(got[0].Content) != len(largest.Content) {
		first := domaintrace.APIArtifact{}
		if len(got) > 0 {
			first = got[0]
		}
		t.Errorf("ListAPIArtifacts() returned %d records, first artifact %s digest %s body length %d, want 1 record %s digest %s body length %d",
			len(got), first.ArtifactID, first.ContentHash, len(first.Content), largest.ArtifactID, largest.ContentHash, len(largest.Content))
	} else if !reflect.DeepEqual(got, []domaintrace.APIArtifact{largest}) {
		t.Errorf("ListAPIArtifacts() did not reproduce the largest record exactly: artifact %s digest %s body length %d",
			got[0].ArtifactID, got[0].ContentHash, len(got[0].Content))
	}

	reloaded, reopenErr := readAPIArtifactsAfterReopen(t, root, ctx)
	if reopenErr != nil {
		t.Errorf("reopened store returned error %v instead of the %d byte record it was asked to open", reopenErr, largestAPIArtifactRecordBytes)
	} else if len(reloaded) != 1 || !reflect.DeepEqual(reloaded, []domaintrace.APIArtifact{largest}) {
		// Read defensively: an empty read has to report its length, not panic on index 0.
		first := domaintrace.APIArtifact{}
		if len(reloaded) > 0 {
			first = reloaded[0]
		}
		t.Errorf("reopened store returned %d records, first artifact %s digest %s body length %d, want 1 record %s digest %s body length %d",
			len(reloaded), first.ArtifactID, first.ContentHash, len(first.Content), largest.ArtifactID, largest.ContentHash, len(largest.Content))
	}
}

// readAPIArtifactsAfterReopen opens the store again over an existing root and returns
// what a fresh process would read, so the largest-record test can report a failed open
// and a failed read from one run without dereferencing a store it did not get.
func readAPIArtifactsAfterReopen(t *testing.T, root string, ctx context.Context) ([]domaintrace.APIArtifact, error) {
	t.Helper()
	reopened, err := NewJSONLStore(root)
	if err != nil {
		return nil, fmt.Errorf("NewJSONLStore(%s): %w", root, err)
	}
	items, err := reopened.ListAPIArtifacts(ctx, 10)
	if err != nil {
		return nil, fmt.Errorf("ListAPIArtifacts() after reopen: %w", err)
	}
	return items, nil
}

// TestJSONLStoreNewRejectsAnUnterminatedAPIArtifactRecord pins the constructor boundary
// for a history whose last record has no terminating newline, which is what a write torn
// by an interruption leaves behind. The record itself is valid JSON, so a reader that only
// decodes records one at a time will accept it, yet the next append has no line break to
// start after and concatenates two records into one unreadable line. Opening the store
// therefore has to stop with an error and hand back no store, rather than hand out a store
// whose next write silently corrupts the evidence.
func TestJSONLStoreNewRejectsAnUnterminatedAPIArtifactRecord(t *testing.T) {
	root := t.TempDir()
	row := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	if err := domaintrace.ValidateAPIArtifact(row); err != nil {
		t.Fatalf("fixture stopped being a valid artifact: %v", err)
	}
	terminated, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	unterminated := append([]byte{}, terminated...)
	if unterminated[len(unterminated)-1] == '\n' {
		t.Fatalf("fixture record already ends with a newline, so this is not a torn record")
	}
	if err := os.WriteFile(filepath.Join(root, artifactFilename), unterminated, 0644); err != nil {
		t.Fatalf("prewrite unterminated %s: %v", artifactFilename, err)
	}

	store, newErr := NewJSONLStore(root)
	if newErr == nil {
		t.Errorf("NewJSONLStore(%s) opened a store over %s whose last record is not newline terminated, so the next append would concatenate two records", root, artifactFilename)
	}
	if store != nil {
		t.Errorf("NewJSONLStore returned a non-nil store alongside error %v", newErr)
	}
}

// TestJSONLStoreSaveAPIArtifactRejectsAFileWhoseLastRecordIsUnterminated covers the same
// torn history when it appears AFTER a clean open: the store was built over an empty
// history and then a write was interrupted, so the file ends inside a record. A Save has
// to refuse to append behind that record and leave the file alone, because appending here
// would fuse the torn record with the new one and lose both. The record that follows is a
// valid artifact on its own, so a rejection can only come from the unterminated tail.
func TestJSONLStoreSaveAPIArtifactRejectsAFileWhoseLastRecordIsUnterminated(t *testing.T) {
	root := t.TempDir()
	store, newStoreErr := NewJSONLStore(root)
	if newStoreErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", newStoreErr)
	}
	ctx := context.Background()

	torn, err := json.Marshal(supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA))
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, artifactFilename), torn, 0644); err != nil {
		t.Fatalf("write unterminated %s: %v", artifactFilename, err)
	}

	next := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	if saveErr := store.SaveAPIArtifact(ctx, next); saveErr == nil {
		t.Errorf("SaveAPIArtifact(%s) accepted a file whose last record is unterminated in %s, which concatenates the torn record with the new one", next.ArtifactID, artifactFilename)
	}
	if afterFile := snapshotAPIArtifactFile(t, root); !reflect.DeepEqual([]byte(afterFile), torn) {
		t.Errorf("rejected save still changed %s: %d bytes, want the %d bytes that were already there", artifactFilename, len(afterFile), len(torn))
	}
}

// TestJSONLStoreSupersedeAPIArtifactTwoInstancesOnOneRootHaveOneWinner races the owner
// supersede operation across two independently opened JSONL stores that share one root,
// which is what two processes over the same appended history look like. Both try to
// supersede the same stored artifact, one toward B and one toward C, released together
// from a start barrier under a bounded context. Exactly one may succeed and the loser has
// to be refused by the owner's own already-superseded conflict: an error that merely says
// the lock was busy would say nothing about the edge rule. The winner's edge is the state
// both instances resolve, only the predecessor's superseded_by moves while B and C stay
// byte-identical, and the whole race leaves exactly one new record behind, so the loser
// appended nothing. Retrying the winning edge through the other instance adds no record,
// and a store opened again over the same root resolves the same state, stays idempotent on
// that edge, and still refuses the losing successor. The roots are repeated a fixed number
// of times to exercise the race more than once, but both interleavings are legal outcomes,
// so no round ever requires a particular winner order to appear.
func TestJSONLStoreSupersedeAPIArtifactTwoInstancesOnOneRootHaveOneWinner(t *testing.T) {
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	b := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	c := supersedeFixtureArtifact("c", supersedeContentC, supersedeHashC)

	for round := 1; round <= 10; round++ {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			root := t.TempDir()
			// Two store instances built separately over one root: neither shares an
			// in-process lock object with the other, so only the batch OS lock and WAL
			// order the two writers.
			first, firstErr := NewJSONLStore(root)
			if firstErr != nil {
				t.Fatalf("NewJSONLStore(first) error = %v", firstErr)
			}
			second, secondErr := NewJSONLStore(root)
			if secondErr != nil {
				t.Fatalf("NewJSONLStore(second) error = %v", secondErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			mustSaveJSONLArtifacts(t, first, predecessor, b, c)
			for _, item := range mustAPIArtifactRecords(t, first) {
				if item.SupersededBy != "" {
					t.Fatalf("artifact %s was stored with an edge %s before the race", item.ArtifactID, item.SupersededBy)
				}
			}
			beforeFile := snapshotAPIArtifactFile(t, root)

			type attempt struct {
				successor modulecore.ArtifactID
				err       error
			}
			attempts := []struct {
				store     *JSONLStore
				successor modulecore.ArtifactID
			}{{first, b.ArtifactID}, {second, c.ArtifactID}}
			start := make(chan struct{})
			results := make(chan attempt, len(attempts))
			var wg sync.WaitGroup
			for _, entry := range attempts {
				wg.Add(1)
				go func(store *JSONLStore, successor modulecore.ArtifactID) {
					defer wg.Done()
					<-start
					results <- attempt{successor: successor, err: store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor)}
				}(entry.store, entry.successor)
			}
			close(start)
			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatalf("the two supersede calls did not both return within the bounded context: %v", ctx.Err())
			}
			got := []attempt{<-results, <-results}

			var winner, loser attempt
			switch {
			case got[0].err == nil && got[1].err != nil:
				winner, loser = got[0], got[1]
			case got[1].err == nil && got[0].err != nil:
				winner, loser = got[1], got[0]
			default:
				t.Fatalf("two stores superseding the same artifact returned %+v and %+v; exactly one of them must succeed", got[0], got[1])
			}
			if !strings.Contains(loser.err.Error(), "is already superseded by") || !strings.Contains(loser.err.Error(), string(winner.successor)) {
				t.Errorf("the losing supersede error = %v, want the conflict that artifact %s is already superseded by %s",
					loser.err, predecessor.ArtifactID, winner.successor)
			}
			t.Logf("round %d: winner %s -> %s, loser %s -> %s refused: %v",
				round, predecessor.ArtifactID, winner.successor, predecessor.ArtifactID, loser.successor, loser.err)

			// Both instances have to resolve one identical current state, and that
			// state is the winner's edge over otherwise unchanged rows.
			wantPredecessor := predecessor
			wantPredecessor.SupersededBy = winner.successor
			firstRecords := mustAPIArtifactRecords(t, first)
			secondRecords := mustAPIArtifactRecords(t, second)
			if !reflect.DeepEqual(firstRecords, secondRecords) {
				t.Errorf("the two instances resolved different records:\n first =%#v\n second=%#v", firstRecords, secondRecords)
			}
			if got := findAPIArtifactRecord(t, secondRecords, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
				t.Errorf("predecessor after the race = %#v, want %#v", got, wantPredecessor)
			}
			for _, unchanged := range []domaintrace.APIArtifact{b, c} {
				if got := findAPIArtifactRecord(t, secondRecords, unchanged.ArtifactID); !reflect.DeepEqual(got, unchanged) {
					t.Errorf("artifact %s after the race = %#v, want the stored record %#v", unchanged.ArtifactID, got, unchanged)
				}
			}
			if err := domaintrace.ValidateAPIArtifact(findAPIArtifactRecord(t, secondRecords, predecessor.ArtifactID)); err != nil {
				t.Errorf("record left after the race is invalid: %v", err)
			}

			// The race is append-only and wrote exactly one record: the winner's
			// predecessor. A loser that appended anything behind the winner would show
			// up as a second record here.
			afterRace := snapshotAPIArtifactFile(t, root)
			if !strings.HasPrefix(afterRace, beforeFile) {
				t.Errorf("the race rewrote the appended history:\n before=%q\n after =%q", beforeFile, afterRace)
			}
			if gained := strings.Count(afterRace, "\n") - strings.Count(beforeFile, "\n"); gained != 1 {
				t.Errorf("the race appended %d records to %s, want exactly the winner's 1 (loser error %v)", gained, artifactFilename, loser.err)
			}

			// Retrying the winning edge is idempotent for either instance, so both store
			// instances ask for the same edge here. Which of them won the race is the
			// scheduler's choice, so covering both is what guarantees that the instance
			// that lost the race also finds the stored edge idempotent rather than
			// erroring or appending.
			for name, store := range map[string]*JSONLStore{"first instance": first, "second instance": second} {
				if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, winner.successor); err != nil {
					t.Errorf("retry of the winning edge %s -> %s through the %s error = %v", predecessor.ArtifactID, winner.successor, name, err)
				}
				if afterRetry := snapshotAPIArtifactFile(t, root); afterRetry != afterRace {
					t.Errorf("retry of the winning edge through the %s appended to %s:\nafter race =%q\nafter retry=%q", name, artifactFilename, afterRace, afterRetry)
				}
				if afterRetry := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(afterRetry, secondRecords) {
					t.Errorf("retry of the winning edge through the %s changed the current records:\n before=%#v\n after =%#v", name, secondRecords, afterRetry)
				}
			}
			// The successor that lost the race is refused for either instance, including
			// the instance that asked for it, because the winning edge is what the shared
			// history now says. The refusal is deterministic, so both instances are
			// covered and the loser's own retry is always exercised.
			for name, store := range map[string]*JSONLStore{"first instance": first, "second instance": second} {
				err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, loser.successor)
				if err == nil {
					t.Errorf("a supersede to %s through the %s succeeded after the race, want the conflict that the artifact is already superseded by %s",
						loser.successor, name, winner.successor)
				} else if !strings.Contains(err.Error(), "is already superseded by") || !strings.Contains(err.Error(), string(winner.successor)) {
					t.Errorf("a supersede to %s through the %s refused with %v, want the conflict naming the winning edge %s",
						loser.successor, name, err, winner.successor)
				}
				if afterLoser := snapshotAPIArtifactFile(t, root); afterLoser != afterRace {
					t.Errorf("refused supersede to %s through the %s appended to %s:\n before=%q\n after =%q",
						loser.successor, name, artifactFilename, afterRace, afterLoser)
				}
			}

			// A store opened again over the same root sees the same state, stays
			// idempotent on the winning edge, and still refuses the losing successor
			// without appending anything.
			reopened, reopenErr := NewJSONLStore(root)
			if reopenErr != nil {
				t.Fatalf("NewJSONLStore(reopen) error = %v", reopenErr)
			}
			if got := mustAPIArtifactRecords(t, reopened); !reflect.DeepEqual(got, secondRecords) {
				t.Errorf("records after reopen = %#v, want %#v", got, secondRecords)
			}
			if err := reopened.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, winner.successor); err != nil {
				t.Errorf("retry of the winning edge after reopen error = %v", err)
			}
			if afterReopen := snapshotAPIArtifactFile(t, root); afterReopen != afterRace {
				t.Errorf("retry of the winning edge after reopen appended to %s:\n before=%q\n after =%q", artifactFilename, afterRace, afterReopen)
			}
			if err := reopened.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, loser.successor); err == nil {
				t.Errorf("supersede to the losing successor %s succeeded after the race; the artifact is already superseded by %s", loser.successor, winner.successor)
			}
			if afterLoser := snapshotAPIArtifactFile(t, root); afterLoser != afterRace {
				t.Errorf("refused supersede to %s appended to %s:\n before=%q\n after =%q", loser.successor, artifactFilename, afterRace, afterLoser)
			}
		})
	}
}

// TestJSONLStoreSaveAPIArtifactRacingSupersedeKeepsEstablishedEdge pins what happens
// when an ordinary Save of the predecessor races the owner supersede of the same
// artifact across two store instances over one root. The two operations are ordered
// only by the batch OS lock, so either may commit first, and the contract has to hold
// for both orders: the supersede always succeeds, and the Save either lands before the
// edge exists - in which case its new body, digest and title are the current record and
// the supersede then points that record at the successor - or arrives after the edge is
// stored, in which case it carries no edge and must be refused by the established-edge
// guard rather than silently clearing it. Whichever order the scheduler picks, the
// predecessor ends up carrying the successor edge and nothing else changes, so the race
// cannot lose an established edge by reordering. Only the observed order is logged; no
// round requires a particular order to appear.
func TestJSONLStoreSaveAPIArtifactRacingSupersedeKeepsEstablishedEdge(t *testing.T) {
	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	b := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	// The ordinary in-place update: a new body with its own matching digest and a new
	// title, and deliberately no edge, so a Save racing the supersede looks stale.
	refreshedContent := "openapi: 3.1.0\ninfo:\n  title: refreshed\n"
	updated := predecessor
	updated.Content = refreshedContent
	updated.ContentHash = modulecore.ContentHashOf([]byte(refreshedContent))
	updated.Title = "Refreshed OpenAPI"
	if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
		t.Fatalf("updated fixture row is not a valid artifact: %v", err)
	}

	for round := 1; round <= 10; round++ {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			root := t.TempDir()
			first, firstErr := NewJSONLStore(root)
			if firstErr != nil {
				t.Fatalf("NewJSONLStore(first) error = %v", firstErr)
			}
			second, secondErr := NewJSONLStore(root)
			if secondErr != nil {
				t.Fatalf("NewJSONLStore(second) error = %v", secondErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			mustSaveJSONLArtifacts(t, first, predecessor, b)
			for _, item := range mustAPIArtifactRecords(t, first) {
				if item.SupersededBy != "" {
					t.Fatalf("artifact %s was stored with edge %s before the race", item.ArtifactID, item.SupersededBy)
				}
			}
			beforeFile := snapshotAPIArtifactFile(t, root)

			// One Save and one supersede of the same artifact, released together on two
			// instances that share nothing but the root and the OS lock.
			start := make(chan struct{})
			saved := make(chan error, 1)
			superseded := make(chan error, 1)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				saved <- first.SaveAPIArtifact(ctx, updated)
			}()
			go func() {
				defer wg.Done()
				<-start
				superseded <- second.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, b.ArtifactID)
			}()
			close(start)
			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatalf("the save and supersede calls did not both return within the bounded context: %v", ctx.Err())
			}
			saveErr, supersedeErr := <-saved, <-superseded

			// The owner operation is never the one that fails here: whichever order the
			// two writers took, the edge it establishes has to be accepted.
			if supersedeErr != nil {
				t.Fatalf("SupersedeAPIArtifact(%s -> %s) error = %v, want success regardless of the racing Save",
					predecessor.ArtifactID, b.ArtifactID, supersedeErr)
			}

			// The Save either committed before the edge existed or was refused by the
			// established-edge guard. Anything else, an ignore of the stored edge or an
			// unrelated rejection, is a defect.
			wantPredecessor := predecessor
			if saveErr != nil {
				if !strings.Contains(saveErr.Error(), "does not match the stored edge") ||
					!strings.Contains(saveErr.Error(), string(b.ArtifactID)) {
					t.Errorf("SaveAPIArtifact(refreshed %s) error = %v, want the established-edge guard refusing to clear the stored edge %s",
						predecessor.ArtifactID, saveErr, b.ArtifactID)
				}
			} else {
				wantPredecessor.Content = updated.Content
				wantPredecessor.ContentHash = updated.ContentHash
				wantPredecessor.Title = updated.Title
			}
			wantPredecessor.SupersededBy = b.ArtifactID
			t.Logf("round %d: save committed first = %v (save error %v); final predecessor body is %q and the edge is %s",
				round, saveErr == nil, saveErr, wantPredecessor.Content, wantPredecessor.SupersededBy)

			// Both instances resolve one identical current state, and the predecessor in
			// that state carries the successor edge over otherwise unchanged rows.
			firstRecords := mustAPIArtifactRecords(t, first)
			secondRecords := mustAPIArtifactRecords(t, second)
			if !reflect.DeepEqual(firstRecords, secondRecords) {
				t.Errorf("the two instances resolved different records:\n first =%#v\n second=%#v", firstRecords, secondRecords)
			}
			for name, records := range map[string][]domaintrace.APIArtifact{"first instance": firstRecords, "second instance": secondRecords} {
				if got := findAPIArtifactRecord(t, records, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
					t.Errorf("predecessor seen by the %s = %#v (body len %d digest %s edge %q), want %#v (body len %d digest %s)",
						name, got, len(got.Content), got.ContentHash, got.SupersededBy,
						wantPredecessor, len(wantPredecessor.Content), wantPredecessor.ContentHash)
				}
				if got := findAPIArtifactRecord(t, records, b.ArtifactID); !reflect.DeepEqual(got, b) {
					t.Errorf("successor seen by the %s = %#v, want the stored record %#v", name, got, b)
				}
			}
			if err := domaintrace.ValidateAPIArtifact(findAPIArtifactRecord(t, secondRecords, predecessor.ArtifactID)); err != nil {
				t.Errorf("record left after the race is invalid: %v", err)
			}

			// The race only appends: a committed Save adds its own record plus the
			// supersede record, and a refused Save adds nothing but the supersede record.
			// The record that ends the file is always the one carrying the edge.
			afterRace := snapshotAPIArtifactFile(t, root)
			if !strings.HasPrefix(afterRace, beforeFile) {
				t.Errorf("the race rewrote the appended history:\n before=%q\n after =%q", beforeFile, afterRace)
			}
			wantAppended := 1
			if saveErr == nil {
				wantAppended = 2
			}
			if gained := strings.Count(afterRace, "\n") - strings.Count(beforeFile, "\n"); gained != wantAppended {
				t.Errorf("the race appended %d records to %s, want %d (save error %v, supersede error %v)",
					gained, artifactFilename, wantAppended, saveErr, supersedeErr)
			}

			// The established edge now refuses a further stale Save on either instance,
			// and the stored edge itself is idempotent for the owner operation without
			// appending anything.
			for name, store := range map[string]*JSONLStore{"first instance": first, "second instance": second} {
				if err := store.SaveAPIArtifact(ctx, updated); err == nil {
					t.Errorf("a stale Save through the %s succeeded after the race, want the established-edge guard refusing to clear the edge %s", name, b.ArtifactID)
				} else if !strings.Contains(err.Error(), "does not match the stored edge") {
					t.Errorf("a stale Save through the %s refused with %v, want the established-edge guard naming the stored edge %s", name, err, b.ArtifactID)
				}
				if err := store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, b.ArtifactID); err != nil {
					t.Errorf("retry of the stored edge %s -> %s through the %s error = %v", predecessor.ArtifactID, b.ArtifactID, name, err)
				}
			}
			afterRetry := snapshotAPIArtifactFile(t, root)
			if afterRetry != afterRace {
				t.Errorf("post-race retries changed %s:\nafter race =%q\nafter retry=%q", artifactFilename, afterRace, afterRetry)
			}

			// A store opened again over the same root sees the same edge-bearing state.
			reopened, reopenErr := NewJSONLStore(root)
			if reopenErr != nil {
				t.Fatalf("NewJSONLStore(reopen) error = %v", reopenErr)
			}
			if got := mustAPIArtifactRecords(t, reopened); !reflect.DeepEqual(got, secondRecords) {
				t.Errorf("records after reopen = %#v, want %#v", got, secondRecords)
			}
		})
	}
}

// batchWalFilename is the write-ahead log name the shared batch helper owns for one
// root. It is named here, rather than reached into, so a cancellation test can compare
// the log bytes around an interrupted operation.
const batchWalFilename = ".jsonlbatch.wal"

func snapshotAPIArtifactWal(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, batchWalFilename))
	if err != nil {
		t.Fatalf("read %s: %v", batchWalFilename, err)
	}
	return string(raw)
}

// lockWaitContext is a test-only context wrapper used to prove that an owner operation
// really reached the batch lock wait before it was canceled. The shared lock helper
// calls tryLockExclusive first and only evaluates ctx.Done in its select when that
// attempt reported the lock busy, so the first Done call is the observable moment the
// operation was locked out. This wrapper closes a channel exactly on that first Done
// call and otherwise delegates to the embedded context, so cancellation itself stays
// entirely the production path: there is no fake lock and no production fault hook.
type lockWaitContext struct {
	context.Context
	entered   chan struct{}
	enteredAt sync.Once
}

func (c *lockWaitContext) Done() <-chan struct{} {
	c.enteredAt.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// TestJSONLStoreAPIArtifactCancellationWhileWaitingForOwnerLock pins what a canceled
// owner operation leaves behind on the JSONL side. Another writer on the same root holds
// the batch write lock through the real batch API, so a Save and a Supersede issued
// meanwhile cannot obtain it and have to give up with context.Canceled. The wrapper above
// shows that the canceled call was locked out before the cancellation is issued, and the
// holder is still inside its critical section when the call returns, so the operation
// provably never reached the append: the artifact file and the WAL it journals through
// stay byte-for-byte as they were. The pre-canceled variants pin the same no-write
// outcome for a context already canceled at the call. Every subcase then releases the
// holder and joins it while the bounded context is still live, and both original store
// instances have to write and supersede normally afterwards, which is what shows the
// shared lock came back healthy rather than leaked.
func TestJSONLStoreAPIArtifactCancellationWhileWaitingForOwnerLock(t *testing.T) {
	tests := []struct {
		name string
		// preCancel cancels the caller context before the owner call is issued
		// instead of while it is waiting for the lock.
		preCancel bool
		op        func(ctx context.Context, store *JSONLStore, predecessor, successor domaintrace.APIArtifact) error
	}{
		{
			name: "SaveAPIArtifact canceled while it waits for the owner lock",
			op: func(ctx context.Context, store *JSONLStore, predecessor, successor domaintrace.APIArtifact) error {
				refreshed := predecessor
				refreshed.Content = "openapi: 3.1.0\ninfo:\n  title: refreshed\n"
				refreshed.ContentHash = modulecore.ContentHashOf([]byte(refreshed.Content))
				refreshed.Title = "Refreshed OpenAPI"
				return store.SaveAPIArtifact(ctx, refreshed)
			},
		},
		{
			name: "SupersedeAPIArtifact canceled while it waits for the owner lock",
			op: func(ctx context.Context, store *JSONLStore, predecessor, successor domaintrace.APIArtifact) error {
				return store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
			},
		},
		{
			name:      "SaveAPIArtifact with a context canceled before the call",
			preCancel: true,
			op: func(ctx context.Context, store *JSONLStore, predecessor, successor domaintrace.APIArtifact) error {
				refreshed := predecessor
				refreshed.Content = "openapi: 3.1.0\ninfo:\n  title: refreshed\n"
				refreshed.ContentHash = modulecore.ContentHashOf([]byte(refreshed.Content))
				refreshed.Title = "Refreshed OpenAPI"
				return store.SaveAPIArtifact(ctx, refreshed)
			},
		},
		{
			name:      "SupersedeAPIArtifact with a context canceled before the call",
			preCancel: true,
			op: func(ctx context.Context, store *JSONLStore, predecessor, successor domaintrace.APIArtifact) error {
				return store.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			holder, holderErr := NewJSONLStore(root)
			if holderErr != nil {
				t.Fatalf("NewJSONLStore(holder) error = %v", holderErr)
			}
			canceled, canceledErr := NewJSONLStore(root)
			if canceledErr != nil {
				t.Fatalf("NewJSONLStore(canceled) error = %v", canceledErr)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
			successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
			mustSaveJSONLArtifacts(t, holder, predecessor, successor)

			beforeRecords := mustAPIArtifactRecords(t, holder)
			for _, item := range beforeRecords {
				if item.SupersededBy != "" {
					t.Fatalf("artifact %s was stored with edge %s before the canceled operation", item.ArtifactID, item.SupersededBy)
				}
			}
			beforeFile := snapshotAPIArtifactFile(t, root)
			beforeWal := snapshotAPIArtifactWal(t, root)

			// The holder takes the real batch write lock and stays inside its callback
			// until this test releases it, so the lock is genuinely contended and no
			// fake lock or production fault hook is involved. An empty append map means
			// the holder itself commits nothing to the artifact file.
			held := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseHolder := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseHolder()
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- holder.batch.Write(ctx, func() (map[string][]byte, error) {
					close(held)
					select {
					case <-release:
						return nil, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				})
			}()
			select {
			case <-held:
			case err := <-holderDone:
				t.Fatalf("the holder released the batch lock before the operation ran: %v", err)
			case <-ctx.Done():
				t.Fatalf("the holder never acquired the batch lock: %v", ctx.Err())
			}

			opCtx, opCancel := context.WithCancel(context.Background())
			defer opCancel()
			if tc.preCancel {
				opCancel()
			}
			// The wrapper records the moment the operation finds the lock busy, which is
			// the point where a cancellation is meaningful for this test.
			lockedOut := make(chan struct{})
			waiting := &lockWaitContext{Context: opCtx, entered: lockedOut}

			opDone := make(chan error, 1)
			go func() { opDone <- tc.op(waiting, canceled, predecessor, successor) }()
			if !tc.preCancel {
				// Wait until the operation actually tried the lock and was refused, so the
				// cancellation is issued on a call that is provably waiting for the lock and
				// not on one that gave up before it ever got there.
				select {
				case <-lockedOut:
				case err := <-holderDone:
					t.Fatalf("the holder released the batch lock before the operation reached the lock wait: %v", err)
				case <-ctx.Done():
					t.Fatalf("the operation never reached the batch lock wait: %v", ctx.Err())
				}
				opCancel()
			}
			var opErr error
			select {
			case opErr = <-opDone:
			case err := <-holderDone:
				t.Fatalf("the holder finished before the canceled operation returned, so the wait was not bounded by the lock: %v", err)
			case <-ctx.Done():
				t.Fatalf("the canceled operation did not return within the bounded context: %v", ctx.Err())
			}

			if !errors.Is(opErr, context.Canceled) {
				t.Errorf("canceled operation error = %v, want context.Canceled", opErr)
			}
			t.Logf("canceled operation returned %v while the batch lock was still held", opErr)

			// The canceled call gave up while the holder was still inside its critical
			// section, so it never held the lock and never appended: both the current
			// records and the raw artifact and WAL bytes have to be exactly what they were.
			if afterFile := snapshotAPIArtifactFile(t, root); afterFile != beforeFile {
				t.Errorf("the canceled operation changed %s:\nbefore=%d bytes\nafter =%d bytes", artifactFilename, len(beforeFile), len(afterFile))
			}
			if afterWal := snapshotAPIArtifactWal(t, root); afterWal != beforeWal {
				t.Errorf("the canceled operation changed %s:\nbefore=%d bytes\nafter =%d bytes", batchWalFilename, len(beforeWal), len(afterWal))
			}

			// Releasing the holder has to complete its own transaction cleanly, and the
			// shared lock has to be healthy afterwards: both original store instances
			// still write and supersede, with no leftover half-written change.
			releaseHolder()
			select {
			case err := <-holderDone:
				if err != nil {
					t.Fatalf("the holder released the batch lock with %v, want the empty append map to commit nothing", err)
				}
			case <-ctx.Done():
				t.Fatalf("the holder never released the batch lock: %v", ctx.Err())
			}

			// Now that the lock is free again, the current state is readable through the
			// instance whose own call was canceled, and it is still the pre-cancellation
			// state: the canceled operation contributed no record.
			if afterRecords, err := canceled.ListAPIArtifacts(ctx, 50); err != nil {
				t.Errorf("ListAPIArtifacts after the canceled operation error = %v", err)
			} else if !reflect.DeepEqual(afterRecords, beforeRecords) {
				t.Errorf("current records after the canceled operation =\n%#v\nwant unchanged\n%#v", afterRecords, beforeRecords)
			}

			updated := predecessor
			updated.Content = supersedeContentC
			updated.ContentHash = supersedeHashC
			updated.Title = "Rewritten after the cancellation"
			if err := canceled.SaveAPIArtifact(ctx, updated); err != nil {
				t.Fatalf("SaveAPIArtifact through the store whose call was canceled error = %v, want the store still usable", err)
			}
			if err := holder.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
				t.Fatalf("SupersedeAPIArtifact through the store whose call was canceled error = %v, want the owner operation still usable", err)
			}

			wantPredecessor := updated
			wantPredecessor.SupersededBy = successor.ArtifactID
			for name, store := range map[string]*JSONLStore{"holding instance": holder, "canceled instance": canceled} {
				records := mustAPIArtifactRecords(t, store)
				if got := findAPIArtifactRecord(t, records, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
					t.Errorf("predecessor seen by the %s after recovery = %#v, want %#v", name, got, wantPredecessor)
				}
				if got := findAPIArtifactRecord(t, records, successor.ArtifactID); !reflect.DeepEqual(got, successor) {
					t.Errorf("successor seen by the %s after recovery = %#v, want the stored record %#v", name, got, successor)
				}
			}
			if afterFile := snapshotAPIArtifactFile(t, root); len(afterFile) <= len(beforeFile) {
				t.Errorf("%s held %d bytes before and %d bytes after the writes that followed the cancellation, want the two committed changes appended",
					artifactFilename, len(beforeFile), len(afterFile))
			}
		})
	}
}

// batchJournalSchemaVersion is the journal version the shared batch helper persists in
// its own record schema. It is restated here, rather than reached into, because the
// interrupted-WAL fixture below has to be written as raw journal records, exactly the way
// the helper writes them.
const batchJournalSchemaVersion = 1

// interruptedWALFile and interruptedWALRecord mirror the journal record schema the batch
// helper persists (version, kind, tx_id and per-file name, offset, append_length,
// prefix_sha256, payload_sha256). Only those fields exist: the helper decodes journal
// records with unknown fields rejected, so this fixture cannot invent a field of its own.
type interruptedWALFile struct {
	Name          string `json:"name"`
	Offset        int64  `json:"offset"`
	AppendLength  int64  `json:"append_length"`
	PrefixSHA256  string `json:"prefix_sha256"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type interruptedWALRecord struct {
	Version int                  `json:"version"`
	Kind    string               `json:"kind"`
	TxID    string               `json:"tx_id"`
	Files   []interruptedWALFile `json:"files,omitempty"`
}

// decodeInterruptedWALRecords splits a journal snapshot into its records. A fixture
// only ever appends behind records the store already committed, so assertions about
// the fixture decode the appended suffix, while a check that one transaction never
// committed scans the whole journal for that transaction id.
func decodeInterruptedWALRecords(t *testing.T, wal string) []interruptedWALRecord {
	t.Helper()
	var records []interruptedWALRecord
	for _, line := range strings.Split(wal, "\n") {
		if line == "" {
			continue
		}
		var record interruptedWALRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode journal record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func sha256HexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// appendToStoreFile appends raw bytes the way an interrupted append would have left them:
// straight to the file, with no journal record and no fsync bookkeeping.
func appendToStoreFile(t *testing.T, path string, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open %s for a fixture append: %v", filepath.Base(path), err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatalf("append to %s: %v", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s after a fixture append: %v", filepath.Base(path), err)
	}
}

// TestJSONLStoreRecoversPendingArtifactWALOnReopen pins the owner integration around an
// interrupted artifact append. Two artifacts are saved through the real store, then a
// fixture reproduces the exact state an interruption leaves behind: the file carries a
// part of a new record, the journal carries a prepare that describes the whole intended
// record, and no commit record was written. That is a deterministic interrupted state
// built from the persisted journal schema, not a claim about real power loss, native
// Windows behavior or a live deployment.
//
// A reader has to refuse that state, so ListAPIArtifacts fails closed with the batch
// recovery error rather than showing a half-written row, and the reader must not repair
// anything on its own. Opening the store again runs the owner-side recovery, which rolls
// the data file back to the prepared offset: the artifact bytes and the current A and B
// rows come back byte-for-byte as they were before the interruption, and the rolled-back
// row never surfaces afterwards. Both store instances then have to take real Save and
// Supersede operations again, and a second reopen has to preserve the resulting final
// state without changing the artifact file any further.
func TestJSONLStoreRecoversPendingArtifactWALOnReopen(t *testing.T) {
	root := t.TempDir()
	store, storeErr := NewJSONLStore(root)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	predecessor := supersedeFixtureArtifact("a", supersedeContentA, supersedeHashA)
	successor := supersedeFixtureArtifact("b", supersedeContentB, supersedeHashB)
	mustSaveJSONLArtifacts(t, store, predecessor, successor)

	beforeRecords := mustAPIArtifactRecords(t, store)
	if len(beforeRecords) != 2 {
		t.Fatalf("saved %d artifact rows before the interruption, want 2: %#v", len(beforeRecords), beforeRecords)
	}
	beforeFile := snapshotAPIArtifactFile(t, root)
	beforeWal := snapshotAPIArtifactWal(t, root)

	// The row whose append is interrupted. It is a fully valid artifact with its own
	// body and matching digest, so the only reason a reader cannot show it is the WAL
	// shape, and its digest is distinctive enough to prove afterwards that the rolled
	// back row never surfaced.
	inFlight := predecessor
	inFlight.Content = "openapi: 3.1.0\ninfo:\n  title: interrupted mid-append\n"
	inFlight.ContentHash = modulecore.ContentHashOf([]byte(inFlight.Content))
	inFlight.Title = "Interrupted Mid-Append"
	if err := domaintrace.ValidateAPIArtifact(inFlight); err != nil {
		t.Fatalf("interrupted fixture row is not a valid artifact: %v", err)
	}
	line, err := json.Marshal(inFlight)
	if err != nil {
		t.Fatalf("marshal the interrupted fixture row: %v", err)
	}
	payload := append(line, '\n')
	if len(payload) < 4 {
		t.Fatalf("interrupted payload of %d bytes is too short to tear", len(payload))
	}
	partial := payload[:len(payload)/2]
	if strings.ContainsRune(string(partial), '\n') {
		t.Fatalf("the fixture tear landed on a delimiter, so it would not look interrupted")
	}

	// The interruption itself, written in the order the batch helper writes it: the
	// journal prepare describing the whole intended record, then part of that record
	// in the data file, with no commit behind the prepare.
	prepareTxID := "test-interrupted-artifact-append"
	prepare := interruptedWALRecord{
		Version: batchJournalSchemaVersion,
		Kind:    "prepare",
		TxID:    prepareTxID,
		Files: []interruptedWALFile{{
			Name:          artifactFilename,
			Offset:        int64(len(beforeFile)),
			AppendLength:  int64(len(payload)),
			PrefixSHA256:  sha256HexOf([]byte(beforeFile)),
			PayloadSHA256: sha256HexOf(payload),
		}},
	}
	record, err := json.Marshal(prepare)
	if err != nil {
		t.Fatalf("marshal the fixture prepare record: %v", err)
	}
	appendToStoreFile(t, filepath.Join(root, batchWalFilename), append(record, '\n'))
	appendToStoreFile(t, filepath.Join(root, artifactFilename), partial)

	// The fixture really is the interrupted shape: the data file ends inside a record
	// behind the prepared offset, and no commit record was written.
	tornFile := snapshotAPIArtifactFile(t, root)
	if len(tornFile) != len(beforeFile)+len(partial) {
		t.Fatalf("fixture left %s at %d bytes, want %d", artifactFilename, len(tornFile), len(beforeFile)+len(partial))
	}
	if !strings.HasPrefix(tornFile, beforeFile) || !strings.HasSuffix(tornFile, string(partial)) {
		t.Errorf("fixture did not append the torn record behind the committed history in %s", artifactFilename)
	}
	if strings.HasSuffix(tornFile, "\n") {
		t.Errorf("the torn fixture record ends on a delimiter, so the interruption would not be observable")
	}
	// The journal is append-only, so the earlier committed Saves leave their own
	// records behind. The fixture has to add exactly one prepare for this
	// transaction, and nothing may have committed it.
	afterPrepareWal := snapshotAPIArtifactWal(t, root)
	if !strings.HasPrefix(afterPrepareWal, beforeWal) {
		t.Fatalf("fixture WAL is not the committed journal plus an appended prepare:\nbefore=%q\nafter =%q", beforeWal, afterPrepareWal)
	}
	if appended := decodeInterruptedWALRecords(t, afterPrepareWal[len(beforeWal):]); len(appended) != 1 ||
		appended[0].Kind != "prepare" || appended[0].TxID != prepareTxID || len(appended[0].Files) != 1 ||
		appended[0].Files[0].Name != artifactFilename {
		t.Fatalf("fixture appended %#v behind the committed journal, want a lone prepare for %s on %s",
			appended, prepareTxID, artifactFilename)
	}
	for _, committed := range decodeInterruptedWALRecords(t, afterPrepareWal) {
		if committed.TxID == prepareTxID && committed.Kind != "prepare" {
			t.Fatalf("fixture WAL already holds a %q record for %s", committed.Kind, prepareTxID)
		}
	}

	// A reader refuses the pending transaction and does not quietly repair it.
	if rows, err := store.ListAPIArtifacts(ctx, 50); !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
		t.Fatalf("ListAPIArtifacts() with a pending prepare returned (%d rows, %v), want the batch recovery error", len(rows), err)
	} else {
		t.Logf("reader refused the pending prepare as expected: %v", err)
	}
	if stillTorn := snapshotAPIArtifactFile(t, root); stillTorn != tornFile {
		t.Errorf("the refusing reader changed %s: before=%d bytes after=%d bytes", artifactFilename, len(tornFile), len(stillTorn))
	}
	if stillWal := snapshotAPIArtifactWal(t, root); stillWal != afterPrepareWal {
		t.Errorf("the refusing reader changed %s: before=%d bytes after=%d bytes", batchWalFilename, len(afterPrepareWal), len(stillWal))
	}

	// Opening the store again runs the owner-side recovery over the same journal.
	recovered, recoveredErr := NewJSONLStore(root)
	if recoveredErr != nil {
		t.Fatalf("NewJSONLStore() over a pending prepare error = %v, want the constructor to recover it", recoveredErr)
	}
	if restored := snapshotAPIArtifactFile(t, root); restored != beforeFile {
		t.Errorf("artifact file after recovery = %d bytes, want the exact pre-interruption %d bytes:\nbefore=%q\nafter =%q",
			len(restored), len(beforeFile), restored, beforeFile)
	}
	if rows := mustAPIArtifactRecords(t, recovered); !reflect.DeepEqual(rows, beforeRecords) {
		t.Errorf("current records after recovery =\n%#v\nwant the pre-interruption rows\n%#v", rows, beforeRecords)
	}
	if wal := snapshotAPIArtifactWal(t, root); !strings.Contains(wal, `"kind":"rollback"`) || !strings.Contains(wal, prepareTxID) {
		t.Errorf("journal does not record the rollback of %s:\n%s", prepareTxID, wal)
	} else if len(wal) <= len(beforeWal) {
		t.Errorf("journal held %d bytes before recovery and %d after, want the rollback appended", len(beforeWal), len(wal))
	}
	if strings.Contains(snapshotAPIArtifactFile(t, root), inFlight.ContentHash) {
		t.Errorf("the rolled back record %s survived recovery in %s", inFlight.ContentHash, artifactFilename)
	}

	// Both store instances are usable again: the instance that saw the pending journal
	// reads and writes, and the freshly opened one performs the owner supersede.
	if rows, err := store.ListAPIArtifacts(ctx, 50); err != nil {
		t.Fatalf("ListAPIArtifacts() on the original instance after recovery error = %v", err)
	} else if !reflect.DeepEqual(rows, beforeRecords) {
		t.Errorf("original instance read\n%#v\nafter recovery, want\n%#v", rows, beforeRecords)
	}
	refreshed := predecessor
	refreshed.Content = supersedeContentC
	refreshed.ContentHash = supersedeHashC
	refreshed.Title = "Rewritten after recovery"
	if err := store.SaveAPIArtifact(ctx, refreshed); err != nil {
		t.Fatalf("SaveAPIArtifact() on the original instance after recovery error = %v, want the store usable", err)
	}
	if err := recovered.SupersedeAPIArtifact(ctx, predecessor.ArtifactID, successor.ArtifactID); err != nil {
		t.Fatalf("SupersedeAPIArtifact() after recovery error = %v, want the owner operation usable", err)
	}

	wantPredecessor := refreshed
	wantPredecessor.SupersededBy = successor.ArtifactID
	finalRecords := mustAPIArtifactRecords(t, recovered)
	if got := findAPIArtifactRecord(t, finalRecords, predecessor.ArtifactID); !reflect.DeepEqual(got, wantPredecessor) {
		t.Errorf("predecessor after the post-recovery operations = %#v, want %#v", got, wantPredecessor)
	}
	if got := findAPIArtifactRecord(t, finalRecords, successor.ArtifactID); !reflect.DeepEqual(got, successor) {
		t.Errorf("successor after the post-recovery operations = %#v, want the stored record %#v", got, successor)
	}
	if originalRows := mustAPIArtifactRecords(t, store); !reflect.DeepEqual(originalRows, finalRecords) {
		t.Errorf("the two instances disagree after recovery:\n original =%#v\n recovered=%#v", originalRows, finalRecords)
	}
	finalFile := snapshotAPIArtifactFile(t, root)
	if gained := strings.Count(finalFile, "\n") - strings.Count(beforeFile, "\n"); gained != 2 {
		t.Errorf("post-recovery operations appended %d records, want exactly the Save and the Supersede", gained)
	}
	if strings.Contains(finalFile, inFlight.ContentHash) {
		t.Errorf("the rolled back record surfaced later in %s", artifactFilename)
	}

	// A second reopen keeps the final state and leaves the artifact file alone: recovery
	// repaired the interrupted append, it did not rewrite the history behind it.
	reopened, reopenErr := NewJSONLStore(root)
	if reopenErr != nil {
		t.Fatalf("NewJSONLStore(reopen) error = %v", reopenErr)
	}
	if rows := mustAPIArtifactRecords(t, reopened); !reflect.DeepEqual(rows, finalRecords) {
		t.Errorf("records after the second reopen =\n%#v\nwant\n%#v", rows, finalRecords)
	}
	if afterReopen := snapshotAPIArtifactFile(t, root); afterReopen != finalFile {
		t.Errorf("the second reopen changed %s: before=%d bytes after=%d bytes", artifactFilename, len(finalFile), len(afterReopen))
	}
}
