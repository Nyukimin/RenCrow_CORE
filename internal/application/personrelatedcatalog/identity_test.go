package personrelatedcatalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpsertIdentityEvidenceConfirmsExactIDAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	evidence := IdentityEvidence{
		PersonID:       "p1",
		Authority:      "wikidata",
		ExternalID:     "Q123",
		CanonicalURL:   "https://www.wikidata.org/entity/Q123",
		State:          IdentityStatusConfirmed,
		EvidenceSource: "wikidata",
		EvidenceURL:    "https://www.wikidata.org/wiki/Q123",
		RetrievedAt:    "2026-08-12T00:00:00Z",
		MatchedFields:  []string{"birth_date", "known_work"},
	}
	first, err := UpsertIdentityEvidence(ctx, db, evidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := UpsertIdentityEvidence(ctx, db, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != IdentityStatusConfirmed || second.Status != IdentityStatusConfirmed || second.PersonID != "p1" {
		t.Fatalf("upsert results: first=%#v second=%#v", first, second)
	}
	resolved, err := ResolveIdentityMapping(ctx, db, "wikidata", "Q123")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != IdentityStatusConfirmed || resolved.PersonID != "p1" {
		t.Fatalf("resolved=%#v", resolved)
	}
	mappings, err := ListIdentityMappings(ctx, db, "p1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].ExternalID != "Q123" {
		t.Fatalf("mappings=%#v", mappings)
	}
	assertCount(t, db, "hobby_person_external_ids", 1)
	assertCount(t, db, "hobby_person_identity_evidence", 1)
	assertSearchPlan(t, db, `SELECT person_id FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_authority_external WHERE authority=? AND external_id=? LIMIT 1`, "wikidata", "Q123", "idx_hobby_person_external_ids_authority_external")
	assertSearchPlan(t, db, `SELECT external_id FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_person WHERE person_id=? AND authority=? AND state=? ORDER BY external_id LIMIT 20`, "p1", "wikidata", IdentityStatusConfirmed, "idx_hobby_person_external_ids_person")
}

func TestProviderIdentityAuthoritiesPreserveWireNames(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []IdentityEvidence{
		{
			PersonID:       "p-provider",
			Authority:      "wikidata_qid",
			ExternalID:     "Q42",
			CanonicalURL:   "https://www.wikidata.org/entity/Q42",
			State:          IdentityStatusConfirmed,
			EvidenceSource: "wikidata_award",
			EvidenceURL:    "https://www.wikidata.org/wiki/Q42",
			RetrievedAt:    "2026-08-12T00:00:00Z",
			MatchedFields:  []string{"validated_exact_id"},
		},
		{
			PersonID:       "p-provider",
			Authority:      "ndl_authority_uri",
			ExternalID:     "https://id.ndl.go.jp/auth/ndlna/00123456",
			CanonicalURL:   "https://id.ndl.go.jp/auth/ndlna/00123456",
			State:          IdentityStatusConfirmed,
			EvidenceSource: "ndl_national_bibliography",
			EvidenceURL:    "https://id.ndl.go.jp/auth/ndlna/00123456",
			RetrievedAt:    "2026-08-12T00:00:00Z",
			MatchedFields:  []string{"validated_exact_id"},
		},
	} {
		if _, err := UpsertIdentityEvidence(ctx, db, evidence); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := ConfirmedIdentityIDs(ctx, db, "p-provider", 20)
	if err != nil {
		t.Fatal(err)
	}
	if ids["wikidata_qid"] != "Q42" || ids["ndl_authority_uri"] == "" {
		t.Fatalf("confirmed provider IDs=%#v", ids)
	}
	if _, err := ResolveIdentityMapping(ctx, db, "wikidata_qid", "Q42"); err != nil {
		t.Fatal(err)
	}
}

func TestValidatedArtifactImportSeedsNormalizedIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	artifact := validArtifact(t)
	if _, err := Import(ctx, db, artifact, sha256Hex(artifact), int64(len(artifact))); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveIdentityMapping(ctx, db, "wikidata", "Q123")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != IdentityStatusConfirmed || resolved.PersonID != "p-known" {
		t.Fatalf("imported identity=%#v", resolved)
	}
	assertCount(t, db, "hobby_person_external_ids", 1)
	assertCount(t, db, "hobby_person_identity_evidence", 1)
}

func TestNameOnlyCandidateRemainsUnresolvedAndCannotScheduleCollection(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	result, err := UpsertIdentityEvidence(ctx, db, IdentityEvidence{
		PersonID:       "p-name-only",
		Authority:      "wikidata",
		CandidateName:  "Al Pacino",
		State:          IdentityStatusCandidate,
		EvidenceSource: "movie_catalog",
		EvidenceURL:    "https://example.test/person/p-name-only",
		RetrievedAt:    "2026-08-12T00:00:00Z",
		MatchedFields:  []string{"name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != IdentityStatusUnresolved {
		t.Fatalf("candidate result=%#v", result)
	}
	resolved, err := ResolvePersonIdentity(ctx, db, "p-name-only")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != IdentityStatusUnresolved || len(resolved.Candidates) != 1 {
		t.Fatalf("person resolution=%#v", resolved)
	}
	decision, err := IdentityScheduleDecision(ctx, db, "p-name-only")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.Status != IdentityStatusUnresolved {
		t.Fatalf("schedule decision=%#v", decision)
	}
	assertCount(t, db, "hobby_person_external_ids", 0)
}

func TestNameOnlyExactAssertionCannotPromoteMapping(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	result, err := UpsertIdentityEvidence(ctx, db, IdentityEvidence{
		PersonID:       "p-name-assertion",
		Authority:      "wikidata_qid",
		ExternalID:     "Q1234",
		CandidateName:  "Same Name",
		CanonicalURL:   "https://www.wikidata.org/entity/Q1234",
		State:          IdentityStatusConfirmed,
		EvidenceSource: "name-match",
		EvidenceURL:    "https://example.test/name-match",
		RetrievedAt:    "2026-08-12T00:00:00Z",
		MatchedFields:  []string{"name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != IdentityStatusUnresolved {
		t.Fatalf("name-only exact assertion result=%#v", result)
	}
	if _, err := ResolveIdentityMapping(ctx, db, "wikidata_qid", "Q1234"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "hobby_person_external_ids", 0)
}

func TestConflictingExactEvidenceBecomesAmbiguousAndBlocksBothPersons(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	base := IdentityEvidence{
		Authority:      "wikidata",
		ExternalID:     "Q999",
		CanonicalURL:   "https://www.wikidata.org/entity/Q999",
		State:          IdentityStatusConfirmed,
		EvidenceSource: "wikidata",
		EvidenceURL:    "https://www.wikidata.org/wiki/Q999",
		RetrievedAt:    "2026-08-12T00:00:00Z",
		MatchedFields:  []string{"birth_date"},
	}
	base.PersonID = "p1"
	if result, err := UpsertIdentityEvidence(ctx, db, base); err != nil || result.Status != IdentityStatusConfirmed {
		t.Fatalf("first evidence result=%#v err=%v", result, err)
	}
	base.PersonID = "p2"
	conflict, err := UpsertIdentityEvidence(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Status != IdentityStatusAmbiguous {
		t.Fatalf("conflict result=%#v", conflict)
	}
	resolved, err := ResolveIdentityMapping(ctx, db, "wikidata", "Q999")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != IdentityStatusAmbiguous || len(resolved.Candidates) < 2 {
		t.Fatalf("resolved conflict=%#v", resolved)
	}
	for _, personID := range []string{"p1", "p2"} {
		decision, err := IdentityScheduleDecision(ctx, db, personID)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Allowed || decision.Status != IdentityStatusAmbiguous {
			t.Fatalf("person=%s decision=%#v", personID, decision)
		}
	}
	assertSearchPlan(t, db, `SELECT person_id FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_authority_candidate WHERE authority=? AND candidate_id=? ORDER BY person_id`, "wikidata", "Q999", "idx_hobby_person_identity_evidence_authority_candidate")
}

func TestIdentityResolutionRejectsUnboundedListAndMalformedEvidence(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := EnsureHobbySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := ListIdentityMappings(ctx, db, "p1", 21); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("list limit error=%v", err)
	}
	if _, err := UpsertIdentityEvidence(ctx, db, IdentityEvidence{
		PersonID:       "p1",
		Authority:      "wikidata",
		ExternalID:     "Q1",
		State:          IdentityStatusConfirmed,
		EvidenceSource: "wikidata",
		EvidenceURL:    "not-a-url",
		CanonicalURL:   "https://www.wikidata.org/entity/Q1",
		RetrievedAt:    time.Now().UTC().Format(time.RFC3339),
	}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("malformed evidence error=%v", err)
	}
}
