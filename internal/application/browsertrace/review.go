package browsertrace

import (
	"fmt"
	"strings"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

// ValidationReviewInput contains the trusted identity and the explicit checks
// recorded by a manual validation review. The caller owns the ID and clock;
// this constructor owns the status and issue semantics.
type ValidationReviewInput struct {
	ValidationID        string
	CandidateID         string
	TraceRunID          string
	Reviewer            string
	ReviewNote          string
	TermsReviewed       bool
	OfficialAPIReviewed bool
	PIIReviewed         bool
	SchemaReviewed      bool
	RiskReviewed        bool
	CreatedAt           time.Time
}

// BuildValidationReview constructs a completed manual review. Incomplete
// checks are synchronously rejected; they never become a human-waiting state.
func BuildValidationReview(input ValidationReviewInput) (domaintrace.APICandidateValidationResult, error) {
	item := domaintrace.APICandidateValidationResult{
		ValidationID: strings.TrimSpace(input.ValidationID),
		CandidateID:  strings.TrimSpace(input.CandidateID),
		TraceRunID:   strings.TrimSpace(input.TraceRunID),
		Reviewer:     strings.TrimSpace(input.Reviewer),
		ReviewNote:   strings.TrimSpace(input.ReviewNote),
		Passed:       true,
		Status:       "validated",
		CreatedAt:    input.CreatedAt,
	}
	if item.Reviewer == "" {
		return domaintrace.APICandidateValidationResult{}, fmt.Errorf("reviewer is required")
	}
	item.Issues = validationReviewIssues(input)
	if len(item.Issues) > 0 {
		item.Passed = false
		item.Status = "rejected"
	}
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return domaintrace.APICandidateValidationResult{}, err
	}
	return item, nil
}

func validationReviewIssues(input ValidationReviewInput) []domaintrace.APIValidationIssue {
	var issues []domaintrace.APIValidationIssue
	add := func(code, message, severity string) {
		issues = append(issues, domaintrace.APIValidationIssue{Code: code, Message: message, Severity: severity})
	}
	if !input.TermsReviewed {
		add("terms_review_required", "terms, robots, API policy, and rate limit review must be recorded", "high")
	}
	if !input.OfficialAPIReviewed {
		add("official_api_review_required", "official API, RSS, Atom, or public feed alternative review must be recorded", "medium")
	}
	if !input.PIIReviewed {
		add("pii_review_required", "personal data safety review must be recorded", "high")
	}
	if !input.SchemaReviewed {
		add("schema_review_required", "schema and response sample review must be recorded", "medium")
	}
	if !input.RiskReviewed {
		add("risk_review_required", "risk review must be recorded before fetcher proposal", "medium")
	}
	return issues
}
