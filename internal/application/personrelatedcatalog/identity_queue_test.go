package personrelatedcatalog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIdentityJobSchemaAndDueClaimAreBoundedAndIndexed(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatalf("EnsureHobbySchema: %v", err)
	}
	for i := 0; i < 25; i++ {
		if err := EnqueueIdentityJob(ctx, db, IdentityJob{
			MovieCatalogPersonID: fmt.Sprintf("p-%02d", i),
			PersonName:           "人物",
			PersonURL:            "https://eiga.com/person/p",
			NextAttemptAt:        time.Date(2026, 8, 12, 0, 0, i, 0, time.UTC),
		}); err != nil {
			t.Fatalf("EnqueueIdentityJob(%d): %v", i, err)
		}
	}
	claimed, err := ClaimDueIdentityJobs(ctx, db, "worker-1", time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC), 20, time.Minute)
	if err != nil {
		t.Fatalf("ClaimDueIdentityJobs: %v", err)
	}
	if got, want := len(claimed), 20; got != want {
		t.Fatalf("claimed=%d want=%d", got, want)
	}
	var detail string
	planRows, queryErr := db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT movie_catalog_person_id FROM hobby_person_identity_jobs INDEXED BY idx_hobby_person_identity_jobs_due WHERE state=? AND next_attempt_at<=? ORDER BY next_attempt_at,movie_catalog_person_id LIMIT 20`, IdentityJobPending, "2026-08-12T01:00:00Z")
	if queryErr != nil {
		t.Fatalf("explain identity job claim: %v", queryErr)
	}
	defer planRows.Close()
	for planRows.Next() {
		var id, parent, notUsed int
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan explain identity job claim: %v", err)
		}
		if strings.Contains(strings.ToLower(detail), "idx_hobby_person_identity_jobs_due") {
			return
		}
	}
	t.Fatal("claim did not use due index")
}

func TestIdentityJobEnqueueIsIdempotentAndDoesNotResetLease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatalf("EnsureHobbySchema: %v", err)
	}
	job := IdentityJob{MovieCatalogPersonID: "p1", PersonName: "人物", PersonURL: "https://eiga.com/person/p1", NextAttemptAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)}
	if err := EnqueueIdentityJob(ctx, db, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := ClaimDueIdentityJobs(ctx, db, "worker-1", job.NextAttemptAt, 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	job.PersonName = "更新後の名前"
	job.NextAttemptAt = job.NextAttemptAt.Add(time.Hour)
	if err := EnqueueIdentityJob(ctx, db, job); err != nil {
		t.Fatal(err)
	}
	current, err := GetIdentityJob(ctx, db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if current.State != IdentityJobLeased || current.LeaseToken != claimed[0].LeaseToken || current.PersonName != "更新後の名前" {
		t.Fatalf("idempotent enqueue mutated lease or metadata: %#v", current)
	}
}

func TestIdentityJobLeaseExpiryAndStateTTLs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := EnqueueIdentityJob(ctx, db, IdentityJob{MovieCatalogPersonID: "p1", PersonName: "人物", PersonURL: "https://eiga.com/person/p1", NextAttemptAt: now}); err != nil {
		t.Fatal(err)
	}
	first, err := ClaimDueIdentityJobs(ctx, db, "worker-1", now, 1, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	second, err := ClaimDueIdentityJobs(ctx, db, "worker-2", now.Add(2*time.Minute), 1, time.Minute)
	if err != nil || len(second) != 1 || second[0].LeaseToken == first[0].LeaseToken {
		t.Fatalf("expired lease was not reclaimed: first=%#v second=%#v err=%v", first, second, err)
	}
	if got := IdentityJobNextAttempt(IdentityJobConfirmed, now); !got.Equal(now.Add(IdentityConfirmedTTL)) {
		t.Fatalf("confirmed TTL=%s", got)
	}
	if got := IdentityJobNextAttempt(IdentityJobAmbiguous, now); !got.Equal(now.Add(IdentityUnresolvedTTL)) {
		t.Fatalf("ambiguous TTL=%s", got)
	}
}

func TestIdentityMigrationIsCursorBoundedAndResumable(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	assertSearchPlan(t, movieDB, `SELECT p.person_id FROM movie_catalog_assessments AS a INDEXED BY idx_movie_catalog_assessments_eligible_target JOIN people AS p ON p.person_id=a.target_id WHERE a.kind='person' AND (a.familiarity='known' OR a.sentiment='like') AND p.person_id>? ORDER BY p.person_id LIMIT ?`, "", 200, "idx_movie_catalog_assessments_eligible_target")
	first, err := EnqueueInitialIdentityJobs(ctx, movieDB, hobbyDB, "", 1, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first.Queued != 1 || first.Done {
		t.Fatalf("first migration=%#v", first)
	}
	second, err := EnqueueInitialIdentityJobs(ctx, movieDB, hobbyDB, first.Cursor, 10, time.Date(2026, 8, 12, 0, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if second.Queued != 1 || !second.Done {
		t.Fatalf("second migration=%#v", second)
	}
	var count int
	if err := hobbyDB.QueryRow(`SELECT COUNT(*) FROM hobby_person_identity_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("identity jobs=%d want=2", count)
	}
}

func TestEnsureIdentityJobForPersonStopsForFreshConfirmedIdentity(t *testing.T) {
	ctx := context.Background()
	movieDB := openTestDB(t)
	hobbyDB := openTestDB(t)
	setupAssessmentFixture(t, movieDB)
	if err := EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if _, err := RecordIdentityEvidence(ctx, hobbyDB, IdentityEvidence{PersonID: "p-known", Authority: "wikidata_qid", ExternalID: "Q42", CanonicalURL: "https://www.wikidata.org/wiki/Q42", State: IdentityStatusConfirmed, EvidenceSource: "fixture", EvidenceURL: "https://www.wikidata.org/wiki/Q42", RetrievedAt: now.Add(-time.Hour).Format(time.RFC3339), MatchedFields: []string{"birth_date"}}); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureIdentityJobForPerson(ctx, movieDB, hobbyDB, "p-known", now)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("fresh confirmed identity unexpectedly queued")
	}
	var count int
	if err := hobbyDB.QueryRow(`SELECT COUNT(*) FROM hobby_person_identity_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("identity jobs=%d want=0", count)
	}
}
