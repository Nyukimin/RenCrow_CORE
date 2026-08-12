package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildCollectionPlanBatchesFixedSourcesDeterministically(t *testing.T) {
	plan, err := BuildCollectionPlan(PlanRequest{
		PersonRefID:          "eiga:p1",
		MovieCatalogPersonID: "p1",
		Categories:           []string{CategoryManga, CategoryAnime, CategoryDrama},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlanRevision != CollectionPlanRevision {
		t.Fatalf("revision=%q", plan.PlanRevision)
	}
	got := make([]string, 0, len(plan.Batches))
	for _, batch := range plan.Batches {
		got = append(got, batch.Source+":"+strings.Join(batch.Categories, ","))
	}
	want := []string{
		"jpsearch:drama",
		"mediaarts_db:anime,manga",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("batches=%v want=%v", got, want)
	}
}

func TestBuildCollectionPlanSkipsOnlyFreshSourceAndContinues(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	plan, err := BuildCollectionPlan(PlanRequest{
		PersonRefID:          "eiga:p1",
		MovieCatalogPersonID: "p1",
		Categories:           []string{CategoryManga},
		FreshAttempts: []CollectionAttempt{{
			PersonRefID:          "eiga:p1",
			MovieCatalogPersonID: "p1",
			Category:             CategoryManga,
			Source:               "mediaarts_db",
			Status:               CollectionStatusUnavailable,
			ExpiresAt:            now.Add(time.Hour).Format(time.RFC3339),
		}},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 0 || plan.StopReason != StopReasonAllSourcesTerminal {
		t.Fatalf("fresh only approved source was not terminal: %#v", plan)
	}
}

func TestBuildCollectionPlanStopsWhenFreshReadyAttemptExists(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	ready := CollectionAttempt{
		RunID:                "run-ready",
		PersonRefID:          "eiga:p1",
		MovieCatalogPersonID: "p1",
		Category:             CategoryAnime,
		Source:               "mediaarts_db",
		Status:               CollectionStatusReady,
		ItemCount:            3,
		RetrievedAt:          now.Format(time.RFC3339),
		ExpiresAt:            now.Add(time.Hour).Format(time.RFC3339),
	}
	plan, err := BuildCollectionPlan(PlanRequest{
		PersonRefID:          "eiga:p1",
		MovieCatalogPersonID: "p1",
		Categories:           []string{CategoryAnime},
		FreshAttempts:        []CollectionAttempt{ready},
		Now:                  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 0 || plan.StopReason != StopReasonEnoughValidatedResults || len(plan.Attempts) != 1 {
		t.Fatalf("fresh ready attempt must stop later providers: %#v", plan)
	}
}

func TestCollectionAttemptTTLAndIndexedLookup(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	attempt := CollectionAttempt{
		RunID:                "run-1",
		PersonRefID:          "eiga:p1",
		MovieCatalogPersonID: "p1",
		Category:             CategoryManga,
		Source:               "mediaarts_db",
		Status:               CollectionStatusUnavailable,
		ReasonCode:           "no_match",
		RetrievedAt:          now.Format(time.RFC3339),
		PlanRevision:         CollectionPlanRevision,
	}
	if err := RecordCollectionAttempt(ctx, db, attempt); err != nil {
		t.Fatal(err)
	}
	fresh, ok, err := LookupFreshCollectionAttempt(ctx, db, "eiga:p1", "p1", CategoryManga, "mediaarts_db", now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("fresh=%#v ok=%t err=%v", fresh, ok, err)
	}
	if fresh.ExpiresAt == "" || fresh.Status != CollectionStatusUnavailable {
		t.Fatalf("fresh=%#v", fresh)
	}
	_, ok, err = LookupFreshCollectionAttempt(ctx, db, "eiga:p1", "p1", CategoryManga, "mediaarts_db", now.Add(NegativeCollectionAttemptTTL+time.Hour))
	if err != nil || ok {
		t.Fatalf("expired attempt ok=%t err=%v", ok, err)
	}
	assertSearchPlan(t, db, `SELECT run_id FROM hobby_collection_attempts INDEXED BY idx_hobby_collection_attempts_person_category_source WHERE person_ref_id=? AND movie_catalog_person_id=? AND category=? AND source=? AND expires_at>? ORDER BY retrieved_at DESC LIMIT 1`, "eiga:p1", "p1", CategoryManga, "mediaarts_db", now.Format(time.RFC3339), "idx_hobby_collection_attempts_person_category_source")
}

func TestApplySummaryPatchExactIDsAndLimits(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := validArtifact(t)
	if _, err := Import(ctx, hobbyDB, artifact, sha256Hex(artifact), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	patch := SummaryPatch{
		Category:            CategoryDrama,
		ItemID:              "drama-1",
		Source:              "jpsearch",
		DescriptionOriginal: "An English summary",
		DescriptionLanguage: "en",
		DescriptionJA:       "日本語サマリ",
		TranslationStatus:   SummaryTranslationReady,
		SourceStatus:        SummarySourceReady,
		SourceRecordID:      "jp:drama-1",
		CanonicalURL:        "https://example.test/jp/drama-1",
		EvidenceURL:         "https://example.test/jp/drama-1/evidence",
		RetrievedAt:         "2026-08-12T00:00:00Z",
	}
	if err := ApplySummaryPatch(ctx, hobbyDB, []SummaryPatch{patch}); err != nil {
		t.Fatal(err)
	}
	result, err := LookupWithCoverage(ctx, hobbyDB, "p-known", CategoryDrama, 50)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("lookup=%#v err=%v", result, err)
	}
	if result.Items[0].SummaryJA != "日本語サマリ" || result.Items[0].SummaryState != "translated_summary" {
		t.Fatalf("patched summary=%#v", result.Items[0])
	}
	if err := ApplySummaryPatch(ctx, hobbyDB, []SummaryPatch{{Category: CategoryDrama, ItemID: "missing", Source: "jpsearch"}}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("missing exact target err=%v", err)
	}
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// Keep sql imported in the Red test's package contract while the implementation
// is being introduced.
var _ *sql.DB
