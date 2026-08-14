package browsertrace

import (
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

func TestBuildValidationReviewPassesOnlyWhenAllChecksAreComplete(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 3, 4, 5, 0, time.UTC)
	item, err := BuildValidationReview(ValidationReviewInput{
		ValidationID:        "browser-validation/sha256:abc",
		CandidateID:         "candidate-1",
		TraceRunID:          "trace-1",
		Reviewer:            "shiro",
		ReviewNote:          "all checks complete",
		TermsReviewed:       true,
		OfficialAPIReviewed: true,
		PIIReviewed:         true,
		SchemaReviewed:      true,
		RiskReviewed:        true,
		CreatedAt:           createdAt,
	})
	if err != nil {
		t.Fatalf("BuildValidationReview() error = %v", err)
	}
	if !item.Passed || item.Status != "validated" || len(item.Issues) != 0 {
		t.Fatalf("validation = %#v, want validated without issues", item)
	}
	if item.Reviewer != "shiro" || item.ReviewNote != "all checks complete" || !item.CreatedAt.Equal(createdAt) {
		t.Fatalf("review metadata = %#v", item)
	}
}

func TestBuildValidationReviewRejectsIncompleteChecksWithStableIssues(t *testing.T) {
	item, err := BuildValidationReview(ValidationReviewInput{
		ValidationID: "browser-validation/sha256:def",
		CandidateID:  "candidate-1",
		TraceRunID:   "trace-1",
		Reviewer:     "shiro",
		CreatedAt:    time.Date(2026, 8, 14, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildValidationReview() error = %v", err)
	}
	if item.Passed || item.Status != "rejected" || len(item.Issues) != 5 {
		t.Fatalf("validation = %#v, want rejected with five issues", item)
	}
	wantCodes := []string{"terms_review_required", "official_api_review_required", "pii_review_required", "schema_review_required", "risk_review_required"}
	for i, issue := range item.Issues {
		if issue.Code != wantCodes[i] || issue.Message == "" {
			t.Fatalf("issue[%d] = %#v, want stable code %q", i, issue, wantCodes[i])
		}
	}
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		t.Fatalf("rejected review should satisfy domain validation: %v", err)
	}
}
