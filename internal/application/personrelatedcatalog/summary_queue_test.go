package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummaryQueueClaimsAtMostTwentyAndReclaimsExpiredLease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 21; index++ {
		if err := EnqueueSummaryJob(ctx, db, SummaryJob{
			Category:    CategoryDrama,
			ItemID:      fmt.Sprintf("item-%02d", index),
			Source:      "jpsearch",
			AvailableAt: now,
		}); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	claimed, err := ClaimDueSummaryJobs(ctx, db, "worker-a", now, 20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 20 {
		t.Fatalf("claimed=%d, want 20", len(claimed))
	}
	if next, err := ClaimDueSummaryJobs(ctx, db, "worker-b", now, 20, time.Hour); err != nil {
		t.Fatal(err)
	} else if len(next) != 1 {
		t.Fatalf("unclaimed due jobs=%d, want 1", len(next))
	}
	if _, err := RetrySummaryJob(ctx, db, claimed[0].Category, claimed[0].ItemID, claimed[0].LeaseToken, now.Add(2*time.Hour), "temporary"); err != nil {
		t.Fatal(err)
	}
	if retry, err := GetSummaryJob(ctx, db, claimed[0].Category, claimed[0].ItemID); err != nil {
		t.Fatal(err)
	} else if retry.State != SummaryJobPending || !retry.AvailableAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("retry=%#v", retry)
	}
	if _, err := ClaimDueSummaryJobs(ctx, db, "worker-c", now.Add(2*time.Hour), 20, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimDueSummaryJobs(ctx, db, "worker-d", now.Add(3*time.Hour), 20, time.Hour); err != nil {
		t.Fatal(err)
	}
	assertSearchPlan(t, db, `SELECT category FROM hobby_summary_jobs INDEXED BY idx_hobby_summary_jobs_due WHERE state=? AND next_attempt_at<=? ORDER BY next_attempt_at,category,item_id LIMIT 20`, SummaryJobPending, now.Format(time.RFC3339), "idx_hobby_summary_jobs_due")
}

func TestSummaryQueueEnqueueIsIdempotentAndImportEnqueuesMissingSummary(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	job := SummaryJob{Category: CategoryDrama, ItemID: "drama-1", Source: "jpsearch", AvailableAt: now}
	if err := EnqueueSummaryJob(ctx, hobbyDB, job); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueSummaryJob(ctx, hobbyDB, job); err != nil {
		t.Fatal(err)
	}
	assertCount(t, hobbyDB, "hobby_summary_jobs", 1)

	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	queued, err := GetSummaryJob(ctx, hobbyDB, CategoryDrama, "drama-1")
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != SummaryJobPending || queued.SourceRecordID != "wikidata:Q1" {
		t.Fatalf("imported missing summary job=%#v", queued)
	}
}

func TestSummaryQueueReclaimsExpiredLeaseAndDueCompletedStates(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := EnqueueSummaryJob(ctx, db, SummaryJob{Category: CategoryDrama, ItemID: "lease-item", Source: "jpsearch", AvailableAt: now}); err != nil {
		t.Fatal(err)
	}
	first, err := ClaimDueSummaryJobs(ctx, db, "worker-a", now, 20, time.Hour)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	if second, err := ClaimDueSummaryJobs(ctx, db, "worker-b", now, 20, time.Hour); err != nil {
		t.Fatal(err)
	} else if len(second) != 0 {
		t.Fatalf("leased job claimed before expiry: %#v", second)
	}
	reclaimed, err := ClaimDueSummaryJobs(ctx, db, "worker-c", now.Add(time.Hour), 20, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].LeaseToken == first[0].LeaseToken {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	if _, err := RetrySummaryJob(ctx, db, CategoryDrama, "lease-item", reclaimed[0].LeaseToken, now.Add(365*24*time.Hour), "deferred"); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueSummaryJob(ctx, db, SummaryJob{Category: CategoryDrama, ItemID: "ready-item", Source: "jpsearch", AvailableAt: now}); err != nil {
		t.Fatal(err)
	}
	readyClaim, err := ClaimDueSummaryJobs(ctx, db, "worker-d", now, 20, time.Minute)
	if err != nil || len(readyClaim) != 1 {
		t.Fatalf("ready claim=%#v err=%v", readyClaim, err)
	}
	ready, err := CompleteSummaryJob(ctx, db, CategoryDrama, "ready-item", readyClaim[0].LeaseToken, SummaryJobReady, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if ready.NextAttemptAt != now.Add(SummaryReadyTTL) {
		t.Fatalf("ready next attempt=%s want=%s", ready.NextAttemptAt, now.Add(SummaryReadyTTL))
	}
	if due, err := ClaimDueSummaryJobs(ctx, db, "worker-e", now.Add(SummaryReadyTTL), 20, time.Minute); err != nil {
		t.Fatal(err)
	} else if len(due) != 1 || due[0].ItemID != "ready-item" {
		t.Fatalf("expired ready claim=%#v", due)
	} else if _, err := RetrySummaryJob(ctx, db, CategoryDrama, "ready-item", due[0].LeaseToken, now.Add(365*24*time.Hour), "deferred"); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueSummaryJob(ctx, db, SummaryJob{Category: CategoryDrama, ItemID: "rights-item", Source: "jpsearch", AvailableAt: now}); err != nil {
		t.Fatal(err)
	}
	rightsClaim, err := ClaimDueSummaryJobs(ctx, db, "worker-f", now, 20, time.Minute)
	if err != nil || len(rightsClaim) != 1 {
		t.Fatalf("rights claim=%#v err=%v", rightsClaim, err)
	}
	dead, err := CompleteSummaryJob(ctx, db, CategoryDrama, "rights-item", rightsClaim[0].LeaseToken, SummaryJobDead, "rights_unverified", now)
	if err != nil {
		t.Fatal(err)
	}
	if dead.NextAttemptAt != now.Add(SummaryRightsRejectedTTL) {
		t.Fatalf("dead next attempt=%s want=%s", dead.NextAttemptAt, now.Add(SummaryRightsRejectedTTL))
	}
	if due, err := ClaimDueSummaryJobs(ctx, db, "worker-g", now.Add(SummaryRightsRejectedTTL), 20, time.Minute); err != nil {
		t.Fatal(err)
	} else if len(due) != 1 || due[0].ItemID != "rights-item" {
		t.Fatalf("expired dead claim=%#v", due)
	}
}

func TestApplySummaryPatchCompletesExactJobAndPreservesRelationAnchor(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	patch := SummaryPatch{
		Category:            CategoryDrama,
		ItemID:              "drama-1",
		Source:              "jpsearch",
		DescriptionOriginal: "An English summary",
		DescriptionLanguage: "en",
		DescriptionJA:       "日本語サマリ",
		SourceStatus:        SummarySourceReady,
		TranslationStatus:   SummaryTranslationReady,
		SourceRecordID:      "jp:drama-1",
		CanonicalURL:        "https://example.test/jp/drama-1",
		EvidenceURL:         "https://example.test/jp/drama-1/evidence",
		RetrievedAt:         "2026-08-12T00:00:00Z",
	}
	if err := ApplySummaryPatch(ctx, hobbyDB, []SummaryPatch{patch}); err != nil {
		t.Fatal(err)
	}
	job, err := GetSummaryJob(ctx, hobbyDB, CategoryDrama, "drama-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != SummaryJobReady {
		t.Fatalf("job after ready patch=%#v", job)
	}
	if want := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Add(SummaryReadyTTL); !job.NextAttemptAt.Equal(want) {
		t.Fatalf("ready job next attempt=%s want=%s", job.NextAttemptAt, want)
	}
	var displayName, relationType, relationSource string
	if err := hobbyDB.QueryRow(`SELECT display_name FROM hobby_related_items WHERE category=? AND item_id=?`, CategoryDrama, "drama-1").Scan(&displayName); err != nil {
		t.Fatal(err)
	}
	if err := hobbyDB.QueryRow(`SELECT relation_type,source FROM hobby_person_relations WHERE target_item_id=?`, "drama-1").Scan(&relationType, &relationSource); err != nil {
		t.Fatal(err)
	}
	if displayName != "日本語作品" || relationType != "出演" || relationSource != "wikidata" {
		t.Fatalf("identity/name/relation mutated: name=%q relation=%q source=%q", displayName, relationType, relationSource)
	}
}

func TestApplyLeasedSummaryPatchesRejectsStaleLeaseAtomically(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	claimed, err := ClaimDueSummaryJobs(ctx, hobbyDB, "worker-new", now, 20, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	patch := SummaryPatch{
		Category: CategoryDrama, ItemID: "drama-1", Source: "jpsearch",
		DescriptionOriginal: "stale summary", DescriptionLanguage: "en",
		SourceStatus: SummarySourceReady, TranslationStatus: SummaryTranslationNotAttempted,
		SourceRecordID: "jp:drama-1", CanonicalURL: "https://example.test/jp/drama-1",
		EvidenceURL: "https://example.test/jp/drama-1/evidence", RetrievedAt: now.Format(time.RFC3339),
	}
	err = ApplyLeasedSummaryPatches(ctx, hobbyDB, []LeasedSummaryPatch{{Patch: patch, LeaseToken: "stale-token"}}, now)
	if !errors.Is(err, ErrSummaryJobLeaseLost) {
		t.Fatalf("stale lease err=%v", err)
	}
	var count int
	if err := hobbyDB.QueryRow(`SELECT COUNT(*) FROM hobby_item_summaries WHERE category=? AND item_id=? AND description_original=?`, CategoryDrama, "drama-1", "stale summary").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale patch was persisted: count=%d", count)
	}
	job, err := GetSummaryJob(ctx, hobbyDB, CategoryDrama, "drama-1")
	if err != nil || job.LeaseToken != claimed[0].LeaseToken || job.State != SummaryJobLeased {
		t.Fatalf("current lease mutated: job=%#v err=%v", job, err)
	}
}

func TestApplySummaryPatchUnavailableRequeuesExactJob(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := validArtifact(t)
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	patch := SummaryPatch{
		Category:          CategoryDrama,
		ItemID:            "drama-1",
		Source:            "jpsearch",
		SourceStatus:      SummarySourceUnavailable,
		TranslationStatus: SummaryTranslationNotAttempted,
		SourceRecordID:    "jp:drama-1",
		CanonicalURL:      "https://example.test/jp/drama-1",
		EvidenceURL:       "https://example.test/jp/drama-1/evidence",
		RetrievedAt:       "2026-08-12T00:00:00Z",
		ExpiresAt:         "2026-08-19T00:00:00Z",
		Reason:            "no_summary",
	}
	if err := ApplySummaryPatch(ctx, hobbyDB, []SummaryPatch{patch}); err != nil {
		t.Fatal(err)
	}
	job, err := GetSummaryJob(ctx, hobbyDB, CategoryDrama, "drama-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != SummaryJobUnavailable || !job.AvailableAt.Equal(time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("job after unavailable patch=%#v", job)
	}
}

func TestExpiredSummaryIsNotProjectedReady(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := []byte(strings.Replace(
		string(validArtifact(t)),
		`"description_translation_state":"not_attempted"`,
		`"description_original":"日本語の概要","description_language":"ja","description_ja":"日本語の概要","description_translation_state":"not_required"`,
		1,
	))
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	if _, err := hobbyDB.Exec(`UPDATE hobby_item_summaries SET expires_at=? WHERE category=? AND item_id=?`, "2026-08-11T23:59:59Z", CategoryDrama, "drama-1"); err != nil {
		t.Fatal(err)
	}
	result, err := LookupWithCoverage(ctx, hobbyDB, "p-known", CategoryDrama, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].SummaryState != "unavailable" || result.Items[0].SummaryJA != "" {
		t.Fatalf("expired summary projected as ready: %#v", result)
	}
	assertSearchPlan(t, hobbyDB, `SELECT item_id FROM hobby_item_summaries INDEXED BY idx_hobby_item_summaries_expiry WHERE source_status=? AND expires_at<=? ORDER BY expires_at,category,item_id LIMIT 20`, SummarySourceReady, "2026-08-12T00:00:00Z", "idx_hobby_item_summaries_expiry")
}

func TestReadyImportCreatesExpiryDueSummaryJob(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	artifact := []byte(strings.Replace(
		string(validArtifact(t)),
		`"description_translation_state":"not_attempted"`,
		`"description_original":"日本語の概要","description_language":"ja","description_ja":"日本語の概要","description_translation_state":"not_required"`,
		1,
	))
	hash := sha256.Sum256(artifact)
	if _, err := Import(ctx, hobbyDB, artifact, hex.EncodeToString(hash[:]), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	job, err := GetSummaryJob(ctx, hobbyDB, CategoryDrama, "drama-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != SummaryJobReady || !job.NextAttemptAt.Equal(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Add(SummaryReadyTTL)) {
		t.Fatalf("ready import job=%#v", job)
	}
}

func TestSummaryQueueRejectsInvalidState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	_, err := CompleteSummaryJob(ctx, db, CategoryDrama, "drama-1", "", SummaryJobState("invalid"), "", time.Now().UTC())
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("invalid state error=%v, want ErrInvalidArtifact", err)
	}
}

func TestEnsureHobbySchemaSummaryQueueMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	var tableSQL, dueSQL, expirySQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='hobby_summary_jobs'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "PRIMARY KEY(category, item_id)") {
		t.Fatalf("summary job primary key missing: %s", tableSQL)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_hobby_summary_jobs_due'`).Scan(&dueSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_hobby_item_summaries_expiry'`).Scan(&expirySQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dueSQL, "next_attempt_at") || !strings.Contains(expirySQL, "expires_at") {
		t.Fatalf("queue indexes malformed: due=%s expiry=%s", dueSQL, expirySQL)
	}
}
